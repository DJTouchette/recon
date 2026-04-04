package detect

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djtouchette/recon/internal/index"
)

// Matches {:dep_name, "~> 1.0"} or {:dep_name, ">= 0"} etc. in mix.exs
var mixDep = regexp.MustCompile(`\{:(\w+),`)

type ElixirDetector struct{}

func (d *ElixirDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	if !hasFile(idx, "mix.exs") {
		return nil
	}

	data, err := os.ReadFile(filepath.Join(root, "mix.exs"))
	if err != nil {
		return nil
	}

	var frameworks []Framework
	seen := make(map[string]bool)

	for _, m := range mixDep.FindAllStringSubmatch(string(data), -1) {
		dep := m[1]
		if !seen[dep] {
			seen[dep] = true
			frameworks = append(frameworks, Framework{
				Name:     dep,
				Language: "elixir",
				Evidence: "mix.exs",
			})
		}
	}

	return frameworks
}

func (d *ElixirDetector) DetectEntrypoints(idx *index.FileIndex) []Entrypoint {
	var eps []Entrypoint

	if hasFile(idx, "lib/application.ex") {
		eps = append(eps, Entrypoint{Path: "lib/application.ex", Kind: "main"})
	}

	for _, f := range idx.All() {
		base := filepath.Base(f.RelPath)
		if base == "router.ex" {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "route"})
		}
	}

	// Look for Application modules in lib/<app>/ directories
	for _, f := range idx.All() {
		if f.Lang != "elixir" || !strings.HasPrefix(f.RelPath, "lib/") {
			continue
		}
		if filepath.Base(f.RelPath) == "application.ex" && f.RelPath != "lib/application.ex" {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		}
	}

	return eps
}
