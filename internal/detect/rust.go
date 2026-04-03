package detect

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

type RustDetector struct{}

func (d *RustDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	if !hasFile(idx, "Cargo.toml") {
		return nil
	}

	var frameworks []Framework
	data, err := os.ReadFile(filepath.Join(root, "Cargo.toml"))
	if err != nil {
		return nil
	}
	content := string(data)

	fws := map[string]string{
		"actix-web":  "Actix Web",
		"axum":       "Axum",
		"rocket":     "Rocket",
		"warp":       "Warp",
		"tokio":      "Tokio",
		"diesel":     "Diesel",
		"sqlx":       "SQLx",
		"clap":       "Clap",
		"serde":      "Serde",
		"tonic":      "Tonic (gRPC)",
	}

	for dep, name := range fws {
		if strings.Contains(content, dep) {
			frameworks = append(frameworks, Framework{
				Name:     name,
				Language: "rust",
				Evidence: "Cargo.toml: " + dep,
			})
		}
	}

	return frameworks
}

func (d *RustDetector) DetectEntrypoints(idx *index.FileIndex) []Entrypoint {
	var eps []Entrypoint

	for _, f := range idx.ByLang("rust") {
		if f.Class != scan.ClassSource {
			continue
		}
		base := filepath.Base(f.RelPath)
		if base == "main.rs" {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		} else if base == "lib.rs" {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		}
	}

	return eps
}
