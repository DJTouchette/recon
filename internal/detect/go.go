package detect

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

var goRequireRe = regexp.MustCompile(`^\s+([\w./-]+)\s+v`)

type GoDetector struct{}

func (d *GoDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	if !hasFile(idx, "go.mod") {
		return nil
	}

	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil
	}

	var frameworks []Framework
	seen := make(map[string]bool)

	// Parse require blocks from go.mod
	inRequire := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "require (" {
			inRequire = true
			continue
		}
		if trimmed == ")" {
			inRequire = false
			continue
		}
		if !inRequire {
			continue
		}
		// Skip indirect dependencies
		if strings.Contains(trimmed, "// indirect") {
			continue
		}
		if m := goRequireRe.FindStringSubmatch(line); m != nil {
			dep := m[1]
			if !seen[dep] {
				seen[dep] = true
				frameworks = append(frameworks, Framework{
					Name:     dep,
					Language: "go",
					Evidence: "go.mod",
				})
			}
		}
	}

	return frameworks
}

func (d *GoDetector) DetectEntrypoints(idx *index.FileIndex) []Entrypoint {
	var eps []Entrypoint

	for _, f := range idx.ByLang("go") {
		if f.Class != scan.ClassSource {
			continue
		}
		base := filepath.Base(f.RelPath)
		dir := filepath.Dir(f.RelPath)

		if base == "main.go" {
			kind := "main"
			if strings.HasPrefix(dir, "cmd/") || dir == "cmd" {
				kind = "cli"
			}
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: kind})
		}
	}

	return eps
}
