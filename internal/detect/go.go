package detect

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

type GoDetector struct{}

func (d *GoDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	if !hasFile(idx, "go.mod") {
		return nil
	}

	var frameworks []Framework

	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil
	}
	content := string(data)

	fws := map[string]string{
		"github.com/gin-gonic/gin":    "Gin",
		"github.com/labstack/echo":    "Echo",
		"github.com/gofiber/fiber":    "Fiber",
		"github.com/gorilla/mux":      "Gorilla Mux",
		"github.com/go-chi/chi":       "Chi",
		"github.com/spf13/cobra":      "Cobra",
		"github.com/urfave/cli":       "urfave/cli",
		"google.golang.org/grpc":      "gRPC",
		"github.com/graphql-go/graphql": "GraphQL",
		"gorm.io/gorm":                "GORM",
		"entgo.io/ent":                "Ent",
		"github.com/jmoiron/sqlx":     "sqlx",
	}

	for dep, name := range fws {
		if strings.Contains(content, dep) {
			frameworks = append(frameworks, Framework{
				Name:     name,
				Language: "go",
				Evidence: "go.mod: " + dep,
			})
		}
	}

	return frameworks
}

func (d *GoDetector) DetectEntrypoints(idx *index.FileIndex) []Entrypoint {
	var eps []Entrypoint

	for _, f := range idx.ByLang("go") {
		if f.Class != scan.ClassSource {
			continue
		}
		base := filepath.Base(f.RelPath)
		dir := filepath.Dir(f.RelPath)

		if base == "main.go" {
			kind := "main"
			if strings.HasPrefix(dir, "cmd/") || dir == "cmd" {
				kind = "cli"
			}
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: kind})
		}
	}

	return eps
}
