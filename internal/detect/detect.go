package detect

import (
	"sort"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

// Language represents a detected programming language.
type Language struct {
	Name       string
	FileCount  int
	Percentage float64
	Extensions []string
}

// Framework represents a detected framework.
type Framework struct {
	Name     string
	Language string
	Evidence string
}

// Entrypoint represents a detected entry point.
type Entrypoint struct {
	Path string
	Kind string // "main", "cli", "server", "route", "handler"
}

// Detector can detect languages, frameworks, and entrypoints.
type Detector interface {
	DetectFrameworks(idx *index.FileIndex, root string) []Framework
	DetectEntrypoints(idx *index.FileIndex) []Entrypoint
}

var detectors = []Detector{
	&GoDetector{},
	&NodeDetector{},
	&PythonDetector{},
	&RustDetector{},
	&RubyDetector{},
	&ElixirDetector{},
	&DotNetDetector{},
	&JavaDetector{},
}

// DetectAll runs all detectors and returns aggregated results.
func DetectAll(idx *index.FileIndex, root string) ([]Language, []Framework, []Entrypoint) {
	// Languages come from the file index
	langCounts := idx.Languages()
	languages := make([]Language, 0, len(langCounts))

	// Build extension map per language
	extMap := make(map[string]map[string]bool)
	for _, f := range idx.All() {
		if f.Lang == "" {
			continue
		}
		if f.Class != scan.ClassSource && f.Class != scan.ClassTest {
			continue
		}
		ext := extFromPath(f.RelPath)
		if ext == "" {
			continue
		}
		if extMap[f.Lang] == nil {
			extMap[f.Lang] = make(map[string]bool)
		}
		extMap[f.Lang][ext] = true
	}

	for _, lc := range langCounts {
		var exts []string
		for ext := range extMap[lc.Name] {
			exts = append(exts, ext)
		}
		sort.Strings(exts)
		languages = append(languages, Language{
			Name:       lc.Name,
			FileCount:  lc.Count,
			Percentage: lc.Percentage,
			Extensions: exts,
		})
	}

	// Frameworks and entrypoints from detectors
	var frameworks []Framework
	var entrypoints []Entrypoint

	for _, d := range detectors {
		frameworks = append(frameworks, d.DetectFrameworks(idx, root)...)
		entrypoints = append(entrypoints, d.DetectEntrypoints(idx)...)
	}

	return languages, frameworks, entrypoints
}

func extFromPath(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return path[i:]
		}
		if path[i] == '/' {
			return ""
		}
	}
	return ""
}

// hasFile checks if a specific file exists in the index.
func hasFile(idx *index.FileIndex, path string) bool {
	return idx.Exists(path)
}

// hasAnyFile checks if any of the given files exist.
func hasAnyFile(idx *index.FileIndex, paths ...string) (string, bool) {
	for _, p := range paths {
		if idx.Exists(p) {
			return p, true
		}
	}
	return "", false
}
