package detect

import (
	"os"
	"path/filepath"
	"regexp"

	"github.com/djtouchette/recon/internal/index"
)

// Matches "org" %% "artifact" or "org" % "artifact" in build.sbt
var sbtDep = regexp.MustCompile(`"[^"]+"\s+%%?\s+"([^"]+)"`)

type ScalaDetector struct{}

func (d *ScalaDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	hasSBT := hasFile(idx, "build.sbt")
	hasMill := hasFile(idx, "build.sc")

	if !hasSBT && !hasMill {
		return nil
	}

	var frameworks []Framework
	seen := make(map[string]bool)

	if hasSBT {
		data, err := os.ReadFile(filepath.Join(root, "build.sbt"))
		if err == nil {
			for _, m := range sbtDep.FindAllStringSubmatch(string(data), -1) {
				dep := m[1]
				if !seen[dep] {
					seen[dep] = true
					frameworks = append(frameworks, Framework{
						Name:     dep,
						Language: "scala",
						Evidence: "build.sbt",
					})
				}
			}
		}
	}

	if hasMill {
		data, err := os.ReadFile(filepath.Join(root, "build.sc"))
		if err == nil {
			// Mill uses ivy"org::artifact:version" syntax
			for _, m := range sbtDep.FindAllStringSubmatch(string(data), -1) {
				dep := m[1]
				if !seen[dep] {
					seen[dep] = true
					frameworks = append(frameworks, Framework{
						Name:     dep,
						Language: "scala",
						Evidence: "build.sc",
					})
				}
			}
		}
	}

	// Check for Play Framework routes file
	if hasFile(idx, "conf/routes") && !seen["play"] {
		seen["play"] = true
		frameworks = append(frameworks, Framework{
			Name:     "play",
			Language: "scala",
			Evidence: "conf/routes",
		})
	}

	return frameworks
}

func (d *ScalaDetector) DetectEntrypoints(idx *index.FileIndex) []Entrypoint {
	var eps []Entrypoint

	for _, f := range idx.All() {
		if f.Lang != "scala" {
			continue
		}
		base := filepath.Base(f.RelPath)
		switch base {
		case "Main.scala", "App.scala", "Application.scala", "Server.scala":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		case "Routes.scala":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "route"})
		}
	}

	if hasFile(idx, "conf/routes") {
		eps = append(eps, Entrypoint{Path: "conf/routes", Kind: "route"})
	}

	return eps
}
