package detect

import (
	"regexp"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

// gem "name", "~> 1.0"
var gemRe = regexp.MustCompile(`(?m)^\s*gem\s+['"]([^'"]+)['"](?:\s*,\s*['"]([^'"]+)['"])?`)

type RubyDetector struct{}

func (d *RubyDetector) Key() string         { return "ruby" }
func (d *RubyDetector) Languages() []string { return []string{"ruby"} }

func (d *RubyDetector) Detect(idx *index.FileIndex, root string) DetectorResult {
	var res DetectorResult

	if content, ok := readManifest(root, "Gemfile"); ok && hasFile(idx, "Gemfile") {
		for _, m := range gemRe.FindAllStringSubmatch(content, -1) {
			res.Dependencies = append(res.Dependencies, Dependency{
				Name:     m[1],
				Version:  m[2],
				Language: "ruby",
				Manifest: "Gemfile",
			})
			if fw, ok := rubyGemFrameworks.lookup(m[1]); ok {
				res.Frameworks = append(res.Frameworks, Framework{
					Name:     fw,
					Language: "ruby",
					Evidence: "Gemfile: gem " + m[1],
				})
			}
		}
	}

	for _, marker := range []struct{ file, name string }{
		{"config/routes.rb", "Rails"},
		{"config/application.rb", "Rails"},
		{"spec/spec_helper.rb", "RSpec"},
		{"_config.yml", "Jekyll"},
	} {
		if hasFile(idx, marker.file) {
			res.Frameworks = append(res.Frameworks, Framework{
				Name: marker.name, Language: "ruby", Evidence: marker.file,
			})
		}
	}

	res.Entrypoints = d.entrypoints(idx, root)
	return res
}

func (d *RubyDetector) entrypoints(idx *index.FileIndex, root string) []Entrypoint {
	var eps []Entrypoint
	for _, ef := range []struct{ path, kind string }{
		{"config.ru", "server"},
		{"config/routes.rb", "route"},
		{"Rakefile", "cli"},
		{"bin/rails", "cli"},
	} {
		if hasFile(idx, ef.path) {
			eps = append(eps, Entrypoint{Path: ef.path, Kind: ef.kind})
		}
	}

	// A plain ruby script with the __FILE__ == $0 guard is an entrypoint too.
	scanSource(idx, root, []string{"ruby"}, func(f *scan.FileEntry, content string) {
		if f.Class != scan.ClassSource && f.Class != scan.ClassScript {
			return
		}
		if strings.Contains(content, "__FILE__ == $0") || strings.Contains(content, "__FILE__ == $PROGRAM_NAME") {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		}
	})

	return eps
}
