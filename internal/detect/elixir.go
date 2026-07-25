package detect

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

// {:dep_name, "~> 1.0"} in mix.exs
var mixDep = regexp.MustCompile(`\{:(\w+),\s*(?:"([^"]+)")?`)

type ElixirDetector struct{}

func (d *ElixirDetector) Key() string         { return "elixir" }
func (d *ElixirDetector) Languages() []string { return []string{"elixir"} }

func (d *ElixirDetector) Detect(idx *index.FileIndex, root string) DetectorResult {
	var res DetectorResult

	if content, ok := readManifest(root, "mix.exs"); ok && hasFile(idx, "mix.exs") {
		for _, m := range mixDep.FindAllStringSubmatch(content, -1) {
			res.Dependencies = append(res.Dependencies, Dependency{
				Name:     m[1],
				Version:  m[2],
				Language: "elixir",
				Manifest: "mix.exs",
			})
			if fw, ok := hexFrameworks.lookup(m[1]); ok {
				res.Frameworks = append(res.Frameworks, Framework{
					Name:     fw,
					Language: "elixir",
					Evidence: "mix.exs: :" + m[1],
				})
			}
		}
	}

	res.Entrypoints = d.entrypoints(idx, root)
	return res
}

func (d *ElixirDetector) entrypoints(idx *index.FileIndex, root string) []Entrypoint {
	var eps []Entrypoint

	for _, f := range idx.ByLang("elixir") {
		base := filepath.Base(f.RelPath)
		switch {
		case base == "router.ex":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "route"})
		case base == "application.ex":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		}
	}

	// `use Application` is the real marker; the filename is only a convention.
	scanSource(idx, root, []string{"elixir"}, func(f *scan.FileEntry, content string) {
		if strings.Contains(content, "use Application") {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		}
	})

	return eps
}
