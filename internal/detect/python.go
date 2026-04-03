package detect

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

type PythonDetector struct{}

func (d *PythonDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	if len(idx.ByLang("python")) == 0 {
		return nil
	}

	var frameworks []Framework
	seen := make(map[string]bool)

	// Check requirements files
	reqFiles := []string{"requirements.txt", "requirements.in", "requirements-dev.txt"}
	for _, rf := range reqFiles {
		data, err := os.ReadFile(filepath.Join(root, rf))
		if err != nil {
			continue
		}
		content := strings.ToLower(string(data))
		detectPythonDeps(content, rf, seen, &frameworks)
	}

	// Check pyproject.toml
	if data, err := os.ReadFile(filepath.Join(root, "pyproject.toml")); err == nil {
		content := strings.ToLower(string(data))
		detectPythonDeps(content, "pyproject.toml", seen, &frameworks)
	}

	// Check Pipfile
	if data, err := os.ReadFile(filepath.Join(root, "Pipfile")); err == nil {
		content := strings.ToLower(string(data))
		detectPythonDeps(content, "Pipfile", seen, &frameworks)
	}

	// Config file markers
	if hasFile(idx, "manage.py") && !seen["Django"] {
		frameworks = append(frameworks, Framework{Name: "Django", Language: "python", Evidence: "manage.py"})
	}

	return frameworks
}

func detectPythonDeps(content, source string, seen map[string]bool, frameworks *[]Framework) {
	fws := map[string]string{
		"django":       "Django",
		"flask":        "Flask",
		"fastapi":      "FastAPI",
		"starlette":    "Starlette",
		"tornado":      "Tornado",
		"celery":       "Celery",
		"sqlalchemy":   "SQLAlchemy",
		"pytest":       "pytest",
		"pandas":       "pandas",
		"numpy":        "NumPy",
		"tensorflow":   "TensorFlow",
		"torch":        "PyTorch",
		"scikit-learn": "scikit-learn",
		"pydantic":     "Pydantic",
	}

	for dep, name := range fws {
		if strings.Contains(content, dep) && !seen[name] {
			seen[name] = true
			*frameworks = append(*frameworks, Framework{
				Name:     name,
				Language: "python",
				Evidence: source + ": " + dep,
			})
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

	// Look for __main__.py in packages
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
