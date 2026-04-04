package detect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

type NodeDetector struct{}

func (d *NodeDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	var frameworks []Framework
	seen := make(map[string]bool)

	lang := "javascript"
	if len(idx.ByLang("typescript")) > 0 {
		lang = "typescript"
	}

	// Parse package.json — report all dependencies
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return nil
	}

	for dep := range pkg.Dependencies {
		if !seen[dep] {
			seen[dep] = true
			frameworks = append(frameworks, Framework{
				Name:     dep,
				Language: lang,
				Evidence: "package.json: dependencies",
			})
		}
	}
	for dep := range pkg.DevDependencies {
		if !seen[dep] {
			seen[dep] = true
			frameworks = append(frameworks, Framework{
				Name:     dep,
				Language: lang,
				Evidence: "package.json: devDependencies",
			})
		}
	}

	// Config file markers — detect frameworks even without explicit deps
	configMarkers := map[string]string{
		"next.config.js":       "Next.js",
		"next.config.mjs":      "Next.js",
		"next.config.ts":       "Next.js",
		"nuxt.config.js":       "Nuxt",
		"nuxt.config.ts":       "Nuxt",
		"svelte.config.js":     "Svelte",
		"astro.config.mjs":     "Astro",
		"gatsby-config.js":     "Gatsby",
		"remix.config.js":      "Remix",
		"angular.json":         "Angular",
		"playwright.config.ts": "Playwright",
		"playwright.config.js": "Playwright",
		"cypress.config.js":    "Cypress",
		"cypress.config.ts":    "Cypress",
	}

	for file, name := range configMarkers {
		if hasFile(idx, file) && !seen[name] {
			seen[name] = true
			frameworks = append(frameworks, Framework{
				Name:     name,
				Language: lang,
				Evidence: file,
			})
		}
	}

	return frameworks
}

func (d *NodeDetector) DetectEntrypoints(idx *index.FileIndex) []Entrypoint {
	var eps []Entrypoint

	entryFiles := []struct {
		path string
		kind string
	}{
		{"src/index.ts", "main"},
		{"src/index.js", "main"},
		{"src/main.ts", "main"},
		{"src/main.js", "main"},
		{"src/app.ts", "server"},
		{"src/app.js", "server"},
		{"index.ts", "main"},
		{"index.js", "main"},
		{"server.ts", "server"},
		{"server.js", "server"},
		{"app.ts", "server"},
		{"app.js", "server"},
	}

	for _, ef := range entryFiles {
		if hasFile(idx, ef.path) {
			eps = append(eps, Entrypoint{Path: ef.path, Kind: ef.kind})
		}
	}

	for _, f := range idx.All() {
		if f.Class != scan.ClassSource {
			continue
		}
		base := filepath.Base(f.RelPath)
		lbase := strings.ToLower(base)
		if lbase == "routes.ts" || lbase == "routes.js" || lbase == "router.ts" || lbase == "router.js" {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "route"})
		}
	}

	return eps
}
