package detect

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

// Matches "package==1.0" or "package>=1.0" or "package~=1.0" or just "package"
var pyReqRe = regexp.MustCompile(`^([a-zA-Z0-9][a-zA-Z0-9._-]*)`)

type PythonDetector struct{}

func (d *PythonDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	if len(idx.ByLang("python")) == 0 {
		return nil
	}

	var frameworks []Framework
	seen := make(map[string]bool)

	addDep := func(name, evidence string) {
		if !seen[name] {
			seen[name] = true
			frameworks = append(frameworks, Framework{
				Name:     name,
				Language: "python",
				Evidence: evidence,
			})
		}
	}

	// Parse requirements*.txt files
	for _, rf := range []string{"requirements.txt", "requirements.in", "requirements-dev.txt"} {
		data, err := os.ReadFile(filepath.Join(root, rf))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
				continue
			}
			if m := pyReqRe.FindStringSubmatch(line); m != nil {
				addDep(m[1], rf)
			}
		}
	}

	// Parse pyproject.toml — extract dependency names from [project.dependencies]
	if data, err := os.ReadFile(filepath.Join(root, "pyproject.toml")); err == nil {
		parsePyprojectDeps(string(data), "pyproject.toml", addDep)
	}

	// Parse Pipfile — extract package names from [packages] and [dev-packages]
	if data, err := os.ReadFile(filepath.Join(root, "Pipfile")); err == nil {
		parsePipfileDeps(string(data), "Pipfile", addDep)
	}

	// Config file markers
	if hasFile(idx, "manage.py") {
		addDep("django", "manage.py")
	}

	return frameworks
}

// parsePyprojectDeps extracts dependencies from pyproject.toml.
// Looks for lines in [project.dependencies] or [project.optional-dependencies.*].
func parsePyprojectDeps(content, source string, addDep func(string, string)) {
	inDeps := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[project]" || strings.HasPrefix(trimmed, "[project.optional-dependencies") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			inDeps = false
			continue
		}
		if trimmed == "dependencies = [" || strings.HasSuffix(trimmed, "= [") {
			inDeps = true
			continue
		}
		if trimmed == "]" {
			inDeps = false
			continue
		}
		if inDeps {
			// Lines look like: "django>=4.0",
			dep := strings.Trim(trimmed, `",' `)
			if m := pyReqRe.FindStringSubmatch(dep); m != nil {
				addDep(m[1], source)
			}
		}
	}
}

// parsePipfileDeps extracts package names from Pipfile [packages] and [dev-packages].
func parsePipfileDeps(content, source string, addDep func(string, string)) {
	inPkgs := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[packages]" || trimmed == "[dev-packages]" {
			inPkgs = true
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			inPkgs = false
			continue
		}
		if inPkgs {
			// Lines look like: django = "*" or requests = {version = ">=2.0"}
			eq := strings.IndexByte(trimmed, '=')
			if eq > 0 {
				name := strings.TrimSpace(trimmed[:eq])
				if name != "" && !strings.HasPrefix(name, "#") {
					addDep(name, source)
				}
			}
		}
	}
}

func (d *PythonDetector) DetectEntrypoints(idx *index.FileIndex) []Entrypoint {
	var eps []Entrypoint

	entryFiles := []struct {
		path string
		kind string
	}{
		{"manage.py", "cli"},
		{"app.py", "server"},
		{"main.py", "main"},
		{"__main__.py", "main"},
		{"src/__main__.py", "main"},
		{"wsgi.py", "server"},
		{"asgi.py", "server"},
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
		if filepath.Base(f.RelPath) == "__main__.py" && f.RelPath != "__main__.py" && f.RelPath != "src/__main__.py" {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		}
	}

	return eps
}
