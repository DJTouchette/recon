package detect

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

// Matches [dependencies.foo] or foo = "version" or foo = { version = "..." }
var cargoDep = regexp.MustCompile(`^([a-zA-Z0-9_-]+)\s*=`)

type RustDetector struct{}

func (d *RustDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	if !hasFile(idx, "Cargo.toml") {
		return nil
	}

	data, err := os.ReadFile(filepath.Join(root, "Cargo.toml"))
	if err != nil {
		return nil
	}

	var frameworks []Framework
	seen := make(map[string]bool)

	// Parse [dependencies], [dev-dependencies], [build-dependencies] sections
	inDeps := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			section := strings.Trim(trimmed, "[]")
			inDeps = section == "dependencies" || section == "dev-dependencies" || section == "build-dependencies"
			// Also handle [dependencies.foo] inline tables
			if strings.HasPrefix(section, "dependencies.") {
				dep := strings.TrimPrefix(section, "dependencies.")
				if !seen[dep] {
					seen[dep] = true
					frameworks = append(frameworks, Framework{
						Name:     dep,
						Language: "rust",
						Evidence: "Cargo.toml",
					})
				}
			}
			continue
		}
		if inDeps {
			if m := cargoDep.FindStringSubmatch(trimmed); m != nil {
				dep := m[1]
				if dep == "version" || dep == "features" || dep == "optional" || dep == "path" || dep == "git" {
					continue // these are keys inside a dependency block, not dep names
				}
				if !seen[dep] {
					seen[dep] = true
					frameworks = append(frameworks, Framework{
						Name:     dep,
						Language: "rust",
						Evidence: "Cargo.toml",
					})
				}
			}
		}
	}

	return frameworks
}

func (d *RustDetector) DetectEntrypoints(idx *index.FileIndex) []Entrypoint {
	var eps []Entrypoint

	for _, f := range idx.ByLang("rust") {
		if f.Class != scan.ClassSource {
			continue
		}
		base := filepath.Base(f.RelPath)
		if base == "main.rs" {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		} else if base == "lib.rs" {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		}
	}

	return eps
}
