package detect

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/djtouchette/recon/internal/index"
)

type ElixirDetector struct{}

func (d *ElixirDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	if !hasFile(idx, "mix.exs") {
		return nil
	}

	var frameworks []Framework
	data, err := os.ReadFile(filepath.Join(root, "mix.exs"))
	if err != nil {
		return nil
	}
	content := string(data)

	fws := map[string]string{
		":phoenix":     "Phoenix",
		":ecto":        "Ecto",
		":absinthe":    "Absinthe (GraphQL)",
		":oban":        "Oban",
		":tesla":       "Tesla",
		":broadway":    "Broadway",
		":ash":         "Ash",
		":live_view":   "Phoenix LiveView",
		":phoenix_live_view": "Phoenix LiveView",
	}

	for dep, name := range fws {
		if strings.Contains(content, dep) {
			frameworks = append(frameworks, Framework{
				Name:     name,
				Language: "elixir",
				Evidence: "mix.exs: " + dep,
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

	// Look for router
	for _, f := range idx.All() {
		base := filepath.Base(f.RelPath)
		if base == "router.ex" {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "route"})
		}
	}

	return eps
}
