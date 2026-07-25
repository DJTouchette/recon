package relate

import (
	"math"
	"path/filepath"
	"sort"
	"strings"

	gitpkg "github.com/djtouchette/recon/internal/git"
	"github.com/djtouchette/recon/internal/index"
)

// RelatedFile is a file related to a query path with a relevance score.
type RelatedFile struct {
	Path    string
	Score   float64
	Signals []string
}

// Scores are squashed into [0,1) rather than clamped at it.
//
// A hard clamp collapsed every well-connected file onto exactly 1.0, and since
// ties fall through to the alphabetical path tie-break, the ranking became a
// directory listing: `recon related internal/index/depgraph.go` returned twenty
// results all scoring 1.00 in a-z order, with genuinely related files pushed out
// of the result set by the alphabet. The signals were computed correctly and
// then thrown away at the last step.
//
// The knee keeps ordinary scores at face value; above it the curve bends toward
// 1.0 without reaching it, so more evidence always ranks higher and no two
// distinct sums ever collide. relateDecay is set for the range this scorer
// actually produces — signals here sum to ~4 at the top end, far higher than a
// [0,1] scorer, so a tighter curve would re-saturate everything above ~1.5.
const (
	relateKnee  = 0.8
	relateDecay = 1.5
)

func softCap(score float64) float64 {
	if score <= relateKnee {
		return score
	}
	return relateKnee + (1-relateKnee)*(1-math.Exp(-(score-relateKnee)/relateDecay))
}

// FindRelated returns files related to the given path, ranked by score.
func FindRelated(path string, idx *index.FileIndex, deps *index.DepGraph, tests *index.TestMap, cochange *gitpkg.CoChange, metrics *index.MetricsIndex, ownership *index.Ownership, maxResults int) []RelatedFile {
	if maxResults <= 0 {
		maxResults = 20
	}

	scores := make(map[string]*RelatedFile)

	addSignal := func(filePath string, weight float64, signal string) {
		if filePath == path {
			return
		}
		if !idx.Exists(filePath) {
			return
		}
		rf, ok := scores[filePath]
		if !ok {
			rf = &RelatedFile{Path: filePath}
			scores[filePath] = rf
		}
		rf.Score += weight
		rf.Signals = append(rf.Signals, signal)
	}

	// Signal 1: Same directory (weight 0.3)
	dir := filepath.Dir(path)
	for _, f := range idx.ByDir(dir) {
		addSignal(f.RelPath, 0.3, "same-directory")
	}

	// Signal 2: Test pair (weight 0.9)
	if tests != nil {
		for _, t := range tests.TestsFor(path) {
			addSignal(t, 0.9, "test-pair")
		}
		if src := tests.SourceFor(path); src != "" {
			addSignal(src, 0.9, "test-pair")
		}
	}

	// Signal 3: Import edges (weight 0.7)
	if deps != nil {
		for _, imp := range deps.ImportsOf(path) {
			addSignal(imp, 0.7, "imports")
		}
		for _, imp := range deps.ImportedBy(path) {
			addSignal(imp, 0.7, "imported-by")
		}
	}

	// Signal 4: Co-change (weight 0.5, scaled by frequency)
	if cochange != nil {
		pairs := cochange.CoChangedWith(path, 2)
		for _, p := range pairs {
			weight := 0.5
			if p.Count >= 10 {
				weight = 0.8
			} else if p.Count >= 5 {
				weight = 0.7
			}
			addSignal(p.File, weight, "co-change")
		}
	}

	// Signal 5: Adjacent package (weight 0.2)
	//
	// Only *immediate sibling* directories count. This used to call
	// idx.FilesInDir(parentDir), which is prefix-recursive, so for a target in
	// internal/index it matched every file under internal/ — in a repo where
	// everything lives under one top-level directory that is not a signal at
	// all, it is a constant added to every candidate, and it was the main thing
	// driving every score to the saturation ceiling.
	parentDir := filepath.Dir(dir)
	if parentDir != "." && parentDir != "" {
		// FilesUnderDir is the explicitly-named recursive form; the filter below
		// is what narrows it to immediate siblings. FilesDirectlyIn(parentDir)
		// would return the wrong set — files sitting *in* the parent, not files
		// in the parent's other child directories.
		for _, f := range idx.FilesUnderDir(parentDir) {
			fDir := filepath.Dir(f.RelPath)
			if fDir != dir && filepath.Dir(fDir) == parentDir {
				addSignal(f.RelPath, 0.2, "adjacent-package")
			}
		}
	}

	// Signal 6: Naming convention (weight 0.6)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	nameNoExt := strings.TrimSuffix(base, ext)
	for _, f := range idx.All() {
		if f.RelPath == path {
			continue
		}
		fBase := filepath.Base(f.RelPath)
		fExt := filepath.Ext(fBase)
		fNameNoExt := strings.TrimSuffix(fBase, fExt)
		if fNameNoExt == nameNoExt && filepath.Dir(f.RelPath) != dir {
			addSignal(f.RelPath, 0.6, "same-name")
		}
	}

	// Signal 7: High fan-in importers/imports get a boost (weight 0.3)
	// If a file that imports/is-imported-by target is itself a hotspot,
	// it's more likely to be meaningfully related.
	if metrics != nil {
		targetMetrics := metrics.Get(path)
		if deps != nil {
			for _, imp := range deps.ImportsOf(path) {
				if m := metrics.Get(imp); m != nil && m.HotspotScore > 0.1 {
					addSignal(imp, 0.3, "hotspot-dep")
				}
			}
			for _, imp := range deps.ImportedBy(path) {
				if m := metrics.Get(imp); m != nil && m.HotspotScore > 0.1 {
					addSignal(imp, 0.3, "hotspot-dep")
				}
			}
		}
		// If target itself is a hotspot, boost its co-change partners
		if targetMetrics != nil && targetMetrics.FanIn > 10 && cochange != nil {
			pairs := cochange.CoChangedWith(path, 1)
			for _, p := range pairs {
				addSignal(p.File, 0.2, "hotspot-cochange")
			}
		}
	}

	// Signal 8: Shared ownership (weight 0.15)
	if ownership != nil && ownership.HasRules() {
		targetOwners := ownership.OwnersOf(path)
		if len(targetOwners) > 0 {
			ownerSet := make(map[string]bool, len(targetOwners))
			for _, o := range targetOwners {
				ownerSet[o] = true
			}
			// Only check files already in the candidate set to avoid scanning all files
			for filePath := range scores {
				fileOwners := ownership.OwnersOf(filePath)
				for _, o := range fileOwners {
					if ownerSet[o] {
						addSignal(filePath, 0.15, "same-owner")
						break
					}
				}
			}
		}
	}

	// Convert to sorted slice
	result := make([]RelatedFile, 0, len(scores))
	for _, rf := range scores {
		rf.Score = softCap(rf.Score)
		result = append(result, *rf)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Score != result[j].Score {
			return result[i].Score > result[j].Score
		}
		return result[i].Path < result[j].Path
	})

	if len(result) > maxResults {
		result = result[:maxResults]
	}

	return result
}
