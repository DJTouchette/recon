package detect

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

var (
	// "  dep_name: ^1.0.0" style lines in pubspec.yaml
	pubspecDep = regexp.MustCompile(`^\s{2,4}(\w[\w_-]*):\s*(.*)$`)
	// Dart's entrypoint is `void main()` / `Future<void> main()`.
	dartMainRe = regexp.MustCompile(`(?m)^\s*(?:void|Future<void>|Future)\s+main\s*\(`)
)

type DartDetector struct{}

func (d *DartDetector) Key() string         { return "dart" }
func (d *DartDetector) Languages() []string { return []string{"dart"} }

func (d *DartDetector) Detect(idx *index.FileIndex, root string) DetectorResult {
	var res DetectorResult

	mr := newManifestReader(root, "dart")

	if content, ok := mr.read("pubspec.yaml"); ok && hasFile(idx, "pubspec.yaml") {
		for _, dep := range parsePubspecDeps(content) {
			res.Dependencies = append(res.Dependencies, Dependency{
				Name:     dep.name,
				Version:  dep.version,
				Language: "dart",
				Manifest: "pubspec.yaml",
			})
			if fw, ok := pubFrameworks.lookup(dep.name); ok {
				res.Frameworks = append(res.Frameworks, Framework{
					Name:     fw,
					Language: "dart",
					Evidence: "pubspec.yaml: " + dep.name,
				})
			}
		}
	}

	res.Entrypoints = d.entrypoints(idx, root)
	res.ManifestIssues = mr.issues
	return res
}

type pubDependency struct{ name, version string }

func parsePubspecDeps(content string) []pubDependency {
	var deps []pubDependency
	inDeps := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "dependencies:" || trimmed == "dev_dependencies:" || trimmed == "dependency_overrides:" {
			inDeps = true
			continue
		}
		// Any other top-level key ends the section.
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' && strings.Contains(line, ":") {
			inDeps = false
			continue
		}
		if !inDeps {
			continue
		}
		m := pubspecDep.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[1] == "sdk" {
			continue
		}
		deps = append(deps, pubDependency{name: m[1], version: strings.TrimSpace(m[2])})
	}
	return deps
}

func (d *DartDetector) entrypoints(idx *index.FileIndex, root string) []Entrypoint {
	var eps []Entrypoint

	for _, f := range idx.ByLang("dart") {
		switch filepath.Base(f.RelPath) {
		case "router.dart", "routes.dart", "app_router.dart":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "route"})
		}
	}

	scanSource(idx, root, []string{"dart"}, func(f *scan.FileEntry, content string) {
		if f.Class != scan.ClassSource {
			return
		}
		if !dartMainRe.MatchString(content) {
			return
		}
		kind := "main"
		if strings.HasPrefix(f.RelPath, "bin/") && strings.Contains(content, "serve") {
			kind = "server"
		}
		eps = append(eps, Entrypoint{Path: f.RelPath, Kind: kind})
	})

	return eps
}
