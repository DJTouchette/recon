package detect

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/djtouchette/recon/internal/index"
)

type DartDetector struct{}

func (d *DartDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	if !hasFile(idx, "pubspec.yaml") {
		return nil
	}

	var frameworks []Framework
	data, err := os.ReadFile(filepath.Join(root, "pubspec.yaml"))
	if err != nil {
		return nil
	}
	content := string(data)

	fws := map[string]string{
		"flutter:":         "Flutter",
		"flutter_bloc:":    "BLoC",
		"riverpod:":        "Riverpod",
		"provider:":        "Provider",
		"get:":             "GetX",
		"dio:":             "Dio",
		"freezed:":         "Freezed",
		"drift:":           "Drift",
		"isar:":            "Isar",
		"hive:":            "Hive",
		"go_router:":       "GoRouter",
		"auto_route:":      "AutoRoute",
		"flutter_test:":    "Flutter Test",
		"mockito:":         "Mockito",
		"bloc_test:":       "BLoC Test",
		"shelf:":           "Shelf",
		"dart_frog:":       "Dart Frog",
		"serverpod:":       "Serverpod",
	}

	for dep, name := range fws {
		if strings.Contains(content, dep) {
			frameworks = append(frameworks, Framework{
				Name:     name,
				Language: "dart",
				Evidence: "pubspec.yaml: " + strings.TrimSuffix(dep, ":"),
			})
		}
	}

	return frameworks
}

func (d *DartDetector) DetectEntrypoints(idx *index.FileIndex) []Entrypoint {
	var eps []Entrypoint

	entryFiles := []struct {
		path string
		kind string
	}{
		{"lib/main.dart", "main"},
		{"bin/main.dart", "main"},
		{"bin/server.dart", "server"},
		{"web/main.dart", "main"},
		{"lib/app.dart", "main"},
	}

	for _, ef := range entryFiles {
		if hasFile(idx, ef.path) {
			eps = append(eps, Entrypoint{Path: ef.path, Kind: ef.kind})
		}
	}

	// Look for routes in common Flutter routing locations
	for _, f := range idx.All() {
		base := filepath.Base(f.RelPath)
		if base == "router.dart" || base == "routes.dart" || base == "app_router.dart" {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "route"})
		}
	}

	return eps
}
