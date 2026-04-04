package detect

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/djtouchette/recon/internal/index"
)

type ScalaDetector struct{}

func (d *ScalaDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	// Check for build.sbt (SBT projects)
	hasSBT := hasFile(idx, "build.sbt")
	// Check for build.sc (Mill projects)
	hasMill := hasFile(idx, "build.sc")

	if !hasSBT && !hasMill {
		return nil
	}

	var frameworks []Framework

	// Read build definition for dependency detection
	var content string
	if hasSBT {
		data, err := os.ReadFile(filepath.Join(root, "build.sbt"))
		if err == nil {
			content = string(data)
		}
	}
	if hasMill {
		data, err := os.ReadFile(filepath.Join(root, "build.sc"))
		if err == nil {
			content += "\n" + string(data)
		}
	}

	fws := map[string]string{
		"play":          "Play Framework",
		"akka-http":     "Akka HTTP",
		"akka-actor":    "Akka",
		"akka-stream":   "Akka Streams",
		"http4s":        "http4s",
		"zio":           "ZIO",
		"cats-effect":   "Cats Effect",
		"fs2":           "FS2",
		"slick":         "Slick",
		"doobie":        "Doobie",
		"quill":         "Quill",
		"circe":         "Circe",
		"tapir":         "Tapir",
		"scalatest":     "ScalaTest",
		"specs2":        "Specs2",
		"munit":         "MUnit",
		"spark":         "Apache Spark",
		"flink":         "Apache Flink",
		"lagom":         "Lagom",
		"finatra":       "Finatra",
		"scalatra":      "Scalatra",
		"pekko":         "Pekko",
	}

	for dep, name := range fws {
		if strings.Contains(content, dep) {
			frameworks = append(frameworks, Framework{
				Name:     name,
				Language: "scala",
				Evidence: "build definition: " + dep,
			})
		}
	}

	return frameworks
}

func (d *ScalaDetector) DetectEntrypoints(idx *index.FileIndex) []Entrypoint {
	var eps []Entrypoint

	// Look for common entrypoint patterns
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

	// Check for Play Framework routes file
	if hasFile(idx, "conf/routes") {
		eps = append(eps, Entrypoint{Path: "conf/routes", Kind: "route"})
	}

	return eps
}
