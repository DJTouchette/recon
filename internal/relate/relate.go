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
func FindRelated(path string, idx *index.FileIndex, deps *index.DepGraph, tests *index.TestMap, cochange *gitpkg.CoChange, maxResults int) []RelatedFile {
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
			if p.Count >= 5 {
				weight = 0.7
			} else if p.Count >= 10 {
				weight = 0.8
			}
			addSignal(p.File, weight, "co-change")
		}
	}

	// Signal 5: Same package/module (weight 0.2)
	// Files in sibling directories under the same parent
	parentDir := filepath.Dir(dir)
	if parentDir != "." && parentDir != "" {
		for _, f := range idx.FilesInDir(parentDir) {
			if filepath.Dir(f.RelPath) != dir {
				addSignal(f.RelPath, 0.2, "same-package")
			}
		}
	}

	// Signal 6: Naming convention (weight 0.6)
	// Files with similar base names in different directories
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

	// Convert to sorted slice
	result := make([]RelatedFile, 0, len(scores))
	for _, rf := range scores {
		// Cap score at 1.0
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
