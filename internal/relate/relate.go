package relate

import (
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
	for _, t := range tests.TestsFor(path) {
		addSignal(t, 0.9, "test-pair")
	}
	if src := tests.SourceFor(path); src != "" {
		addSignal(src, 0.9, "test-pair")
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

	// Signal 5: Same package/module (weight 0.2)
	parentDir := filepath.Dir(dir)
	if parentDir != "." && parentDir != "" {
		for _, f := range idx.FilesInDir(parentDir) {
			if filepath.Dir(f.RelPath) != dir {
				addSignal(f.RelPath, 0.2, "same-package")
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
		if rf.Score > 1.0 {
			rf.Score = 1.0
		}
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
