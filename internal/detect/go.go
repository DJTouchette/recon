package detect

import (
	"regexp"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

var (
	// A require line inside a require ( ... ) block, or after "require ".
	goRequireRe = regexp.MustCompile(`^([\w.~-]+(?:/[\w.~-]+)*)\s+(v[\w.+-]+)`)
	// Go declares its entrypoint in the code, not the filename: package main
	// plus a func main() in the same file.
	goPackageMainRe = regexp.MustCompile(`(?m)^package\s+main\b`)
	goFuncMainRe    = regexp.MustCompile(`(?m)^func\s+main\s*\(\s*\)`)
)

type GoDetector struct{}

func (d *GoDetector) Key() string         { return "go" }
func (d *GoDetector) Languages() []string { return []string{"go"} }

func (d *GoDetector) Detect(idx *index.FileIndex, root string) DetectorResult {
	var res DetectorResult
	if !hasFile(idx, "go.mod") {
		// Still worth looking for entrypoints: a repo can vendor Go code or
		// live inside a parent module.
		res.Entrypoints = d.entrypoints(idx, root)
		return res
	}

	content, ok := readManifest(root, "go.mod")
	if ok {
		for _, dep := range parseGoMod(content) {
			res.Dependencies = append(res.Dependencies, Dependency{
				Name:     dep.name,
				Version:  dep.version,
				Language: "go",
				Manifest: "go.mod",
			})
			if name, isFramework := goFrameworks.lookup(normalizeGoModule(dep.name)); isFramework {
				res.Frameworks = append(res.Frameworks, Framework{
					Name:     name,
					Language: "go",
					Evidence: "go.mod: require " + dep.name,
				})
			}
		}
	}

	res.Entrypoints = d.entrypoints(idx, root)
	return res
}

type goDep struct{ name, version string }

// parseGoMod extracts direct (non-indirect) requirements from go.mod, handling
// both the block and single-line forms.
func parseGoMod(content string) []goDep {
	var deps []goDep
	inRequire := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if i := strings.Index(trimmed, "//"); i >= 0 {
			if strings.Contains(trimmed[i:], "indirect") {
				continue
			}
			trimmed = strings.TrimSpace(trimmed[:i])
		}
		switch {
		case trimmed == "require (":
			inRequire = true
			continue
		case trimmed == ")":
			inRequire = false
			continue
		case strings.HasPrefix(trimmed, "require "):
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "require "))
		case !inRequire:
			continue
		}
		if m := goRequireRe.FindStringSubmatch(trimmed); m != nil {
			deps = append(deps, goDep{name: m[1], version: m[2]})
		}
	}
	return deps
}

// entrypoints finds every file that declares package main and defines func
// main(). Filename equality (the old "is it called main.go?" rule) misses
// cmd/serve.go and friends entirely.
func (d *GoDetector) entrypoints(idx *index.FileIndex, root string) []Entrypoint {
	var eps []Entrypoint
	scanSource(idx, root, []string{"go"}, func(f *scan.FileEntry, content string) {
		if f.Class != scan.ClassSource {
			return
		}
		if !goPackageMainRe.MatchString(content) || !goFuncMainRe.MatchString(content) {
			return
		}
		eps = append(eps, Entrypoint{Path: f.RelPath, Kind: goEntryKind(f.RelPath)})
	})
	return eps
}

// goEntryKind labels a main package under a cmd/ directory as a CLI, matching
// the standard Go project layout.
func goEntryKind(relPath string) string {
	for _, seg := range strings.Split(relPath, "/") {
		if seg == "cmd" {
			return "cli"
		}
	}
	return "main"
}
