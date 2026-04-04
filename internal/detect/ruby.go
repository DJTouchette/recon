package detect

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djtouchette/recon/internal/index"
)

// Matches gem "name" or gem 'name' in Gemfile
var gemRe = regexp.MustCompile(`^\s*gem\s+['"]([^'"]+)['"]`)

type RubyDetector struct{}

func (d *RubyDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	if !hasFile(idx, "Gemfile") {
		return nil
	}

	data, err := os.ReadFile(filepath.Join(root, "Gemfile"))
	if err != nil {
		return nil
	}

	var frameworks []Framework
	seen := make(map[string]bool)

	for _, line := range strings.Split(string(data), "\n") {
		if m := gemRe.FindStringSubmatch(line); m != nil {
			dep := m[1]
			if !seen[dep] {
				seen[dep] = true
				frameworks = append(frameworks, Framework{
					Name:     dep,
					Language: "ruby",
					Evidence: "Gemfile",
				})
			}
		}
	}

	// Config file markers
	if hasFile(idx, "config/routes.rb") && !seen["rails"] {
		seen["rails"] = true
		frameworks = append(frameworks, Framework{
			Name:     "rails",
			Language: "ruby",
			Evidence: "config/routes.rb",
		})
	}

	return frameworks
}

func (d *RubyDetector) DetectEntrypoints(idx *index.FileIndex) []Entrypoint {
	var eps []Entrypoint

	entryFiles := []struct {
		path string
		kind string
	}{
		{"config.ru", "server"},
		{"config/routes.rb", "route"},
		{"Rakefile", "cli"},
		{"bin/rails", "cli"},
	}

	for _, ef := range entryFiles {
		if hasFile(idx, ef.path) {
			eps = append(eps, Entrypoint{Path: ef.path, Kind: ef.kind})
		}
	}

	return eps
}
