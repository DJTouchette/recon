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

	// Check root and nested package.json files
	pkgPaths := []string{"package.json"}
	for _, f := range idx.ByClass(scan.ClassConfig) {
		if filepath.Base(f.RelPath) == "package.json" && f.RelPath != "package.json" {
			pkgPaths = append(pkgPaths, f.RelPath)
		}
	}

	seen := make(map[string]bool)
	for _, pkgPath := range pkgPaths {
		data, err := os.ReadFile(filepath.Join(root, pkgPath))
		if err != nil {
			continue
		}
		var pkg struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(data, &pkg) != nil {
			continue
		}

		fws := map[string]struct{ name, lang string }{
			"next":        {"Next.js", "typescript"},
			"react":       {"React", "javascript"},
			"vue":         {"Vue.js", "javascript"},
			"@angular/core": {"Angular", "typescript"},
			"svelte":      {"Svelte", "javascript"},
			"express":     {"Express", "javascript"},
			"fastify":     {"Fastify", "javascript"},
			"koa":         {"Koa", "javascript"},
			"nestjs":      {"NestJS", "typescript"},
			"@nestjs/core": {"NestJS", "typescript"},
			"nuxt":        {"Nuxt", "javascript"},
			"gatsby":      {"Gatsby", "javascript"},
			"astro":       {"Astro", "javascript"},
			"remix":       {"Remix", "typescript"},
			"@remix-run/node": {"Remix", "typescript"},
			"playwright":  {"Playwright", "typescript"},
			"@playwright/test": {"Playwright", "typescript"},
			"cypress":     {"Cypress", "javascript"},
			"jest":        {"Jest", "javascript"},
			"vitest":      {"Vitest", "typescript"},
			"prisma":      {"Prisma", "typescript"},
			"@prisma/client": {"Prisma", "typescript"},
			"drizzle-orm": {"Drizzle", "typescript"},
		}

		allDeps := make(map[string]bool)
		for dep := range pkg.Dependencies {
			allDeps[dep] = true
		}
		for dep := range pkg.DevDependencies {
			allDeps[dep] = true
		}

		for dep, fw := range fws {
			if allDeps[dep] && !seen[fw.name] {
				seen[fw.name] = true
				lang := fw.lang
				if idx.ByLang("typescript") != nil && len(idx.ByLang("typescript")) > 0 {
					lang = "typescript"
				}
				frameworks = append(frameworks, Framework{
					Name:     fw.name,
					Language: lang,
					Evidence: pkgPath + ": " + dep,
				})
			}
		}
	}

	// Config file markers
	configMarkers := map[string]struct{ name, lang string }{
		"next.config.js":      {"Next.js", "javascript"},
		"next.config.mjs":     {"Next.js", "javascript"},
		"next.config.ts":      {"Next.js", "typescript"},
		"nuxt.config.js":      {"Nuxt", "javascript"},
		"nuxt.config.ts":      {"Nuxt", "typescript"},
		"svelte.config.js":    {"Svelte", "javascript"},
		"astro.config.mjs":    {"Astro", "javascript"},
		"gatsby-config.js":    {"Gatsby", "javascript"},
		"remix.config.js":     {"Remix", "javascript"},
		"angular.json":        {"Angular", "typescript"},
		"playwright.config.ts": {"Playwright", "typescript"},
		"playwright.config.js": {"Playwright", "javascript"},
		"cypress.config.js":   {"Cypress", "javascript"},
		"cypress.config.ts":   {"Cypress", "typescript"},
	}

	for file, fw := range configMarkers {
		if hasFile(idx, file) && !seen[fw.name] {
			seen[fw.name] = true
			frameworks = append(frameworks, Framework{
				Name:     fw.name,
				Language: fw.lang,
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

	// Look for route files
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
