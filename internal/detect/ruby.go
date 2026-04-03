package detect

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/djtouchette/recon/internal/index"
)

type RubyDetector struct{}

func (d *RubyDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	if !hasFile(idx, "Gemfile") {
		return nil
	}

	var frameworks []Framework
	data, err := os.ReadFile(filepath.Join(root, "Gemfile"))
	if err != nil {
		return nil
	}
	content := strings.ToLower(string(data))

	fws := map[string]string{
		"rails":    "Ruby on Rails",
		"sinatra":  "Sinatra",
		"hanami":   "Hanami",
		"rspec":    "RSpec",
		"sidekiq":  "Sidekiq",
		"grape":    "Grape",
	}

	for dep, name := range fws {
		if strings.Contains(content, dep) {
			frameworks = append(frameworks, Framework{
				Name:     name,
				Language: "ruby",
				Evidence: "Gemfile: " + dep,
			})
		}
	}

	if hasFile(idx, "Rakefile") || hasFile(idx, "config/routes.rb") {
		found := false
		for _, f := range frameworks {
			if f.Name == "Ruby on Rails" {
				found = true
				break
			}
		}
		if !found {
			if hasFile(idx, "config/routes.rb") {
				frameworks = append(frameworks, Framework{
					Name:     "Ruby on Rails",
					Language: "ruby",
					Evidence: "config/routes.rb",
				})
			}
		}
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
