package detect

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djtouchette/recon/internal/index"
)

// Matches "  dep_name: ^1.0.0" or "  dep_name:" style lines in pubspec.yaml
var pubspecDep = regexp.MustCompile(`^\s{2,4}(\w[\w_-]*):\s*`)

type DartDetector struct{}

func (d *DartDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	if !hasFile(idx, "pubspec.yaml") {
		return nil
	}

	data, err := os.ReadFile(filepath.Join(root, "pubspec.yaml"))
	if err != nil {
		return nil
	}

	var frameworks []Framework
	seen := make(map[string]bool)

	// Parse dependencies and dev_dependencies sections from pubspec.yaml
	inDeps := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)

		// Detect section headers
		if trimmed == "dependencies:" || trimmed == "dev_dependencies:" || trimmed == "dependency_overrides:" {
			inDeps = true
			continue
		}
		// Any other top-level key ends the deps section
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' && strings.Contains(line, ":") {
			inDeps = false
			continue
		}

		if inDeps {
			if m := pubspecDep.FindStringSubmatch(line); m != nil {
				dep := m[1]
				// Skip the flutter SDK pseudo-dependency
				if dep == "sdk" || dep == "flutter" {
					// "flutter" as a dep is actually the Flutter SDK — still worth reporting
					if dep == "flutter" && !seen[dep] {
						seen[dep] = true
						frameworks = append(frameworks, Framework{
							Name:     "flutter",
							Language: "dart",
							Evidence: "pubspec.yaml",
						})
					}
					continue
				}
				if !seen[dep] {
					seen[dep] = true
					frameworks = append(frameworks, Framework{
						Name:     dep,
						Language: "dart",
						Evidence: "pubspec.yaml",
					})
				}
			}
		}
	}

	return frameworks
}

func (d *DartDetector) DetectEntrypoints(idx *index.FileIndex) []Entrypoint {
	var eps []Entrypoint

	entryFiles := []struct {
		path string
		kind string
	}{
		{"lib/main.dart", "main"},
		{"bin/main.dart", "main"},
		{"bin/server.dart", "server"},
		{"web/main.dart", "main"},
		{"lib/app.dart", "main"},
	}

	for _, ef := range entryFiles {
		if hasFile(idx, ef.path) {
			eps = append(eps, Entrypoint{Path: ef.path, Kind: ef.kind})
		}
	}

	for _, f := range idx.All() {
		base := filepath.Base(f.RelPath)
		if base == "router.dart" || base == "routes.dart" || base == "app_router.dart" {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "route"})
		}
	}

	return eps
}
