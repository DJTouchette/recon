package detect

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

var (
	// "package==1.0", "package>=1.0", "package[extra]~=1.0" or bare "package".
	pyReqRe = regexp.MustCompile(`^([a-zA-Z0-9][a-zA-Z0-9._-]*)`)
	// The canonical Python script guard.
	pyMainGuardRe = regexp.MustCompile(`(?m)^if\s+__name__\s*==\s*['"]__main__['"]`)
)

type PythonDetector struct{}

func (d *PythonDetector) Key() string         { return "python" }
func (d *PythonDetector) Languages() []string { return []string{"python"} }

func (d *PythonDetector) Detect(idx *index.FileIndex, root string) DetectorResult {
	var res DetectorResult
	if len(idx.ByLang("python")) == 0 {
		return res
	}

	addDep := func(name, version, manifest string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		res.Dependencies = append(res.Dependencies, Dependency{
			Name:     name,
			Version:  version,
			Language: "python",
			Manifest: manifest,
		})
		if fw, ok := pypiFrameworks.lookup(strings.ToLower(name)); ok {
			res.Frameworks = append(res.Frameworks, Framework{
				Name:     fw,
				Language: "python",
				Evidence: manifest + ": " + name,
			})
		}
	}

	mr := newManifestReader(root, "python")

	for _, rf := range []string{"requirements.txt", "requirements.in", "requirements-dev.txt", "dev-requirements.txt"} {
		content, ok := mr.read(rf)
		if !ok {
			continue
		}
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
				continue
			}
			if m := pyReqRe.FindStringSubmatch(line); m != nil {
				addDep(m[1], strings.TrimPrefix(line, m[1]), rf)
			}
		}
	}

	if content, ok := mr.read("pyproject.toml"); ok {
		parsePyprojectDeps(content, "pyproject.toml", addDep)
	}
	if content, ok := mr.read("Pipfile"); ok {
		parsePipfileDeps(content, "Pipfile", addDep)
	}

	if hasFile(idx, "manage.py") {
		res.Frameworks = append(res.Frameworks, Framework{
			Name: "Django", Language: "python", Evidence: "manage.py",
		})
	}

	fw, eps := d.scanSources(idx, root)
	res.Frameworks = append(res.Frameworks, fw...)
	res.Entrypoints = append(res.Entrypoints, eps...)
	res.ManifestIssues = mr.issues
	return res
}

// parsePyprojectDeps extracts dependency names from the three layouts in the
// wild: PEP 621 arrays, PEP 735 dependency-groups, and Poetry tables.
func parsePyprojectDeps(content, source string, addDep func(name, version, manifest string)) {
	section := ""
	inArray := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.Trim(trimmed, "[]")
			inArray = false
			continue
		}

		isArrayDepSection := section == "project" ||
			strings.HasPrefix(section, "project.optional-dependencies") ||
			section == "dependency-groups"

		if isArrayDepSection {
			if inArray {
				if strings.HasPrefix(trimmed, "]") {
					inArray = false
					continue
				}
				addPyArrayEntry(trimmed, source, addDep)
				continue
			}
			key, rest, ok := splitTOMLAssign(trimmed)
			if !ok {
				continue
			}
			if section == "project" && key != "dependencies" {
				continue
			}
			if strings.HasPrefix(rest, "[") {
				rest = strings.TrimPrefix(rest, "[")
				if strings.Contains(rest, "]") {
					// single-line array
					for _, entry := range strings.Split(rest[:strings.Index(rest, "]")], ",") {
						addPyArrayEntry(entry, source, addDep)
					}
					continue
				}
				inArray = true
				addPyArrayEntry(rest, source, addDep)
			}
			continue
		}

		// Poetry tables: name = "^1.0" / name = { version = "..." }
		if section == "tool.poetry.dependencies" ||
			strings.HasPrefix(section, "tool.poetry.group.") && strings.HasSuffix(section, ".dependencies") ||
			section == "tool.poetry.dev-dependencies" {
			key, rest, ok := splitTOMLAssign(trimmed)
			if !ok || key == "python" {
				continue
			}
			addDep(key, strings.Trim(rest, `"' `), source)
		}
	}
}

// addPyArrayEntry handles one element of a TOML dependency array.
func addPyArrayEntry(entry, source string, addDep func(name, version, manifest string)) {
	entry = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(entry), ","))
	entry = strings.Trim(entry, `"'`)
	if entry == "" {
		return
	}
	if m := pyReqRe.FindStringSubmatch(entry); m != nil {
		addDep(m[1], strings.TrimSpace(strings.TrimPrefix(entry, m[1])), source)
	}
}

// splitTOMLAssign splits "key = value" on the first '='.
func splitTOMLAssign(line string) (key, value string, ok bool) {
	i := strings.IndexByte(line, '=')
	if i <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(strings.Trim(strings.TrimSpace(line[:i]), `"'`))
	value = strings.TrimSpace(line[i+1:])
	return key, value, key != ""
}

// parsePipfileDeps extracts package names from Pipfile [packages] sections.
func parsePipfileDeps(content, source string, addDep func(name, version, manifest string)) {
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
		if !inPkgs || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if key, value, ok := splitTOMLAssign(trimmed); ok {
			addDep(key, strings.Trim(value, `"' `), source)
		}
	}
}

// pySourceMarkers prove a framework from an import or app construction. This
// is what makes a manifest-less Flask app in server.py detectable at all.
var pySourceMarkers = []struct {
	markers []string
	name    string
}{
	{[]string{"from flask import", "import flask", "Flask(__name__)"}, "Flask"},
	{[]string{"from fastapi import", "import fastapi", "FastAPI("}, "FastAPI"},
	{[]string{"from django", "import django"}, "Django"},
	{[]string{"from starlette", "import starlette"}, "Starlette"},
	{[]string{"import tornado", "from tornado"}, "Tornado"},
	{[]string{"from sanic import", "import sanic"}, "Sanic"},
	{[]string{"from aiohttp import", "import aiohttp"}, "aiohttp"},
	{[]string{"from celery import", "import celery"}, "Celery"},
	{[]string{"import pytest"}, "pytest"},
	{[]string{"import streamlit"}, "Streamlit"},
	{[]string{"import torch"}, "PyTorch"},
	{[]string{"import tensorflow"}, "TensorFlow"},
}

// pyServerApps mark a module as a server entrypoint.
var pyServerApps = []string{"Flask(__name__)", "FastAPI(", "Sanic(", "Starlette(", "Application()"}

func (d *PythonDetector) scanSources(idx *index.FileIndex, root string) ([]Framework, []Entrypoint) {
	var fws []Framework
	var eps []Entrypoint
	seen := make(map[string]bool)

	scanSource(idx, root, []string{"python"}, func(f *scan.FileEntry, content string) {
		for _, m := range pySourceMarkers {
			if seen[m.name] {
				continue
			}
			if containsAny(content, m.markers...) {
				seen[m.name] = true
				fws = append(fws, Framework{
					Name:     m.name,
					Language: "python",
					Evidence: f.RelPath + ": " + m.markers[0],
				})
			}
		}

		if f.Class != scan.ClassSource && f.Class != scan.ClassScript {
			return
		}
		base := filepath.Base(f.RelPath)
		switch {
		case base == "manage.py":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "cli"})
		case base == "wsgi.py" || base == "asgi.py":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "server"})
		case base == "__main__.py":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		case containsAny(content, pyServerApps...):
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "server"})
		case pyMainGuardRe.MatchString(content):
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		}
	})

	return fws, eps
}
