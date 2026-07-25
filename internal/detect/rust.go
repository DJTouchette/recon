package detect

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

// Matches "name = ..." inside a [dependencies] table.
var cargoDep = regexp.MustCompile(`^([a-zA-Z0-9_-]+)\s*=`)

type RustDetector struct{}

func (d *RustDetector) Key() string         { return "rust" }
func (d *RustDetector) Languages() []string { return []string{"rust"} }

func (d *RustDetector) Detect(idx *index.FileIndex, root string) DetectorResult {
	var res DetectorResult

	if content, ok := readManifest(root, "Cargo.toml"); ok && hasFile(idx, "Cargo.toml") {
		for _, dep := range parseCargoDeps(content) {
			res.Dependencies = append(res.Dependencies, Dependency{
				Name:     dep.name,
				Version:  dep.version,
				Language: "rust",
				Manifest: "Cargo.toml",
			})
			if fw, ok := cargoFrameworks.lookup(dep.name); ok {
				res.Frameworks = append(res.Frameworks, Framework{
					Name:     fw,
					Language: "rust",
					Evidence: "Cargo.toml: " + dep.name,
				})
			}
		}
	}

	res.Entrypoints = d.entrypoints(idx)
	return res
}

type cargoDependency struct{ name, version string }

func parseCargoDeps(content string) []cargoDependency {
	var deps []cargoDependency
	inDeps := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			section := strings.Trim(trimmed, "[]")
			inDeps = section == "dependencies" || section == "dev-dependencies" ||
				section == "build-dependencies" ||
				strings.HasSuffix(section, ".dependencies")
			// [dependencies.foo] declares foo itself.
			if i := strings.Index(section, "dependencies."); i >= 0 {
				deps = append(deps, cargoDependency{name: section[i+len("dependencies."):]})
				inDeps = false
			}
			continue
		}
		if !inDeps {
			continue
		}
		m := cargoDep.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		name := m[1]
		switch name {
		case "version", "features", "optional", "path", "git", "branch", "rev", "tag", "default-features", "package":
			// keys inside an inline dependency table, not dependency names
			continue
		}
		_, value, _ := splitTOMLAssign(trimmed)
		deps = append(deps, cargoDependency{name: name, version: cargoVersion(value)})
	}
	return deps
}

// cargoVersion pulls the version out of `"1.0"` or `{ version = "1.0", ... }`.
func cargoVersion(value string) string {
	if strings.HasPrefix(value, "{") {
		if i := strings.Index(value, "version"); i >= 0 {
			if _, v, ok := splitTOMLAssign(value[i:]); ok {
				v = strings.TrimSpace(v)
				if j := strings.Index(v[1:], `"`); strings.HasPrefix(v, `"`) && j >= 0 {
					return v[1 : j+1]
				}
			}
		}
		return ""
	}
	return strings.Trim(value, `"' `)
}

func (d *RustDetector) entrypoints(idx *index.FileIndex) []Entrypoint {
	var eps []Entrypoint
	for _, f := range idx.ByLang("rust") {
		if f.Class != scan.ClassSource {
			continue
		}
		switch filepath.Base(f.RelPath) {
		case "main.rs":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		case "lib.rs":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		}
	}
	return eps
}
