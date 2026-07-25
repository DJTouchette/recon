package detect

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

type NodeDetector struct{}

func (d *NodeDetector) Key() string { return "node" }
func (d *NodeDetector) Languages() []string {
	return []string{"javascript", "typescript", "vue", "svelte", "astro"}
}

// nodeLang is the ecosystem language for npm evidence. It is deliberately not
// the repo's dominant language: express is a JavaScript package whether or not
// the repo that depends on it is written in TypeScript.
const nodeLang = "javascript"

type packageJSON struct {
	Name            string            `json:"name"`
	Main            string            `json:"main"`
	Module          string            `json:"module"`
	Bin             json.RawMessage   `json:"bin"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	PeerDeps        map[string]string `json:"peerDependencies"`
}

func (d *NodeDetector) Detect(idx *index.FileIndex, root string) DetectorResult {
	var res DetectorResult

	// package.json is the one manifest here that also declares entrypoints
	// ("main", "bin"), so losing it costs all three answers.
	mr := newManifestReader(root, nodeLang,
		FeatureFrameworks, FeatureDependencies, FeatureEntrypoints)

	pkg, hasPkg := readPackageJSON(mr)
	if hasPkg {
		for _, group := range []struct {
			field string
			deps  map[string]string
		}{
			{"dependencies", pkg.Dependencies},
			{"devDependencies", pkg.DevDependencies},
			{"peerDependencies", pkg.PeerDeps},
		} {
			for name, version := range group.deps {
				res.Dependencies = append(res.Dependencies, Dependency{
					Name:     name,
					Version:  version,
					Language: nodeLang,
					Manifest: "package.json",
				})
				if fw, ok := npmFrameworks.lookup(name); ok {
					res.Frameworks = append(res.Frameworks, Framework{
						Name:     fw,
						Language: nodeLang,
						Evidence: "package.json: " + group.field + "[" + name + "]",
					})
				}
			}
		}
	}

	// Config-file markers prove a framework even when the manifest does not
	// (monorepo child, generated lockfile-only checkout, ...).
	for _, file := range sortedKeys(nodeConfigMarkers) {
		if hasFile(idx, file) {
			res.Frameworks = append(res.Frameworks, Framework{
				Name:     nodeConfigMarkers[file],
				Language: nodeLang,
				Evidence: file,
			})
		}
	}

	// With no manifest at all, source imports are the only evidence available.
	if !hasPkg {
		res.Frameworks = append(res.Frameworks, d.sourceFrameworks(idx, root)...)
	}

	res.Entrypoints = d.entrypoints(idx, pkg, hasPkg)
	res.ManifestIssues = mr.issues
	return res
}

func readPackageJSON(mr *manifestReader) (packageJSON, bool) {
	var pkg packageJSON
	content, ok := mr.read("package.json")
	if !ok {
		return pkg, false
	}
	if err := json.Unmarshal([]byte(content), &pkg); err != nil {
		// A package.json that will not parse is not the same thing as a repo
		// without one: its dependencies and its declared entrypoints are
		// unknown, so record it rather than falling through to "none".
		mr.unusable("package.json", err)
		return pkg, false
	}
	return pkg, true
}

var nodeConfigMarkers = map[string]string{
	"next.config.js":       "Next.js",
	"next.config.mjs":      "Next.js",
	"next.config.ts":       "Next.js",
	"nuxt.config.js":       "Nuxt",
	"nuxt.config.ts":       "Nuxt",
	"svelte.config.js":     "Svelte",
	"astro.config.mjs":     "Astro",
	"astro.config.ts":      "Astro",
	"gatsby-config.js":     "Gatsby",
	"remix.config.js":      "Remix",
	"angular.json":         "Angular",
	"nest-cli.json":        "NestJS",
	"vite.config.ts":       "Vite",
	"vite.config.js":       "Vite",
	"jest.config.js":       "Jest",
	"jest.config.ts":       "Jest",
	"vitest.config.ts":     "Vitest",
	"playwright.config.ts": "Playwright",
	"playwright.config.js": "Playwright",
	"cypress.config.js":    "Cypress",
	"cypress.config.ts":    "Cypress",
	"tailwind.config.js":   "Tailwind CSS",
	"tailwind.config.ts":   "Tailwind CSS",
}

// nodeImportMarkers proves a framework from an import in source. Only used
// when there is no package.json to read.
var nodeImportMarkers = []struct {
	module string
	name   string
}{
	{"express", "Express"},
	{"fastify", "Fastify"},
	{"koa", "Koa"},
	{"react", "React"},
	{"vue", "Vue"},
	{"svelte", "Svelte"},
	{"next", "Next.js"},
	{"@nestjs/core", "NestJS"},
	{"socket.io", "Socket.IO"},
}

func (d *NodeDetector) sourceFrameworks(idx *index.FileIndex, root string) []Framework {
	var out []Framework
	seen := make(map[string]bool)
	scanSource(idx, root, []string{"javascript", "typescript"}, func(f *scan.FileEntry, content string) {
		for _, m := range nodeImportMarkers {
			if seen[m.name] {
				continue
			}
			if containsAny(content,
				`require("`+m.module+`"`, `require('`+m.module+`'`,
				`from "`+m.module+`"`, `from '`+m.module+`'`,
				`import "`+m.module+`"`, `import '`+m.module+`'`) {
				seen[m.name] = true
				out = append(out, Framework{
					Name:     m.name,
					Language: f.Lang,
					Evidence: f.RelPath + ": imports " + m.module,
				})
			}
		}
	})
	return out
}

var nodeEntryFiles = []struct {
	path string
	kind string
}{
	{"src/index.ts", "main"},
	{"src/index.js", "main"},
	{"src/main.ts", "main"},
	{"src/main.js", "main"},
	{"src/server.ts", "server"},
	{"src/server.js", "server"},
	{"src/app.ts", "server"},
	{"src/app.js", "server"},
	{"index.ts", "main"},
	{"index.js", "main"},
	{"index.mjs", "main"},
	{"server.ts", "server"},
	{"server.js", "server"},
	{"app.ts", "server"},
	{"app.js", "server"},
	{"main.ts", "main"},
	{"main.js", "main"},
}

func (d *NodeDetector) entrypoints(idx *index.FileIndex, pkg packageJSON, hasPkg bool) []Entrypoint {
	var eps []Entrypoint

	// The manifest is the authoritative entrypoint declaration; conventions
	// are only the fallback.
	if hasPkg {
		for _, p := range []string{pkg.Main, pkg.Module} {
			if p = cleanRelPath(p); p != "" && hasFile(idx, p) {
				eps = append(eps, Entrypoint{Path: p, Kind: "main"})
			}
		}
		for _, p := range binPaths(pkg.Bin) {
			if p = cleanRelPath(p); p != "" && hasFile(idx, p) {
				eps = append(eps, Entrypoint{Path: p, Kind: "cli"})
			}
		}
	}

	for _, ef := range nodeEntryFiles {
		if hasFile(idx, ef.path) {
			eps = append(eps, Entrypoint{Path: ef.path, Kind: ef.kind})
		}
	}

	for _, f := range idx.All() {
		if f.Class != scan.ClassSource {
			continue
		}
		switch strings.ToLower(filepath.Base(f.RelPath)) {
		case "routes.ts", "routes.js", "router.ts", "router.js", "routes.tsx", "router.tsx":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "route"})
		}
	}

	return eps
}

// binPaths reads npm's "bin" field, which is either a path or a name→path map.
func binPaths(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return []string{single}
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) == nil {
		out := make([]string, 0, len(m))
		for _, k := range sortedKeys(m) {
			out = append(out, m[k])
		}
		return out
	}
	return nil
}

// cleanRelPath normalises a manifest-declared path ("./dist/index.js") to the
// index's relpath form.
func cleanRelPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.TrimPrefix(p, "./")
	return filepath.ToSlash(filepath.Clean(p))
}
