package detect

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/djtouchette/recon/internal/index"
)

type JavaDetector struct{}

func (d *JavaDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	if len(idx.ByLang("java")) == 0 && len(idx.ByLang("kotlin")) == 0 {
		return nil
	}

	var frameworks []Framework
	seen := make(map[string]bool)

	// Check pom.xml
	if data, err := os.ReadFile(filepath.Join(root, "pom.xml")); err == nil {
		content := string(data)
		detectJavaDeps(content, "pom.xml", seen, &frameworks)
	}

	// Check build.gradle
	for _, name := range []string{"build.gradle", "build.gradle.kts"} {
		if data, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			content := string(data)
			detectJavaDeps(content, name, seen, &frameworks)
		}
	}

	return frameworks
}

func detectJavaDeps(content, source string, seen map[string]bool, frameworks *[]Framework) {
	fws := map[string]string{
		"spring-boot":   "Spring Boot",
		"spring-web":    "Spring Web",
		"spring-data":   "Spring Data",
		"micronaut":     "Micronaut",
		"quarkus":       "Quarkus",
		"vert.x":        "Vert.x",
		"hibernate":     "Hibernate",
		"mybatis":       "MyBatis",
		"junit":         "JUnit",
		"mockito":       "Mockito",
		"lombok":        "Lombok",
		"jackson":       "Jackson",
		"grpc":          "gRPC",
		"kafka":         "Kafka",
	}

	lang := "java"
	if strings.Contains(source, ".kts") {
		lang = "kotlin"
	}

	for dep, name := range fws {
		if strings.Contains(content, dep) && !seen[name] {
			seen[name] = true
			*frameworks = append(*frameworks, Framework{
				Name:     name,
				Language: lang,
				Evidence: source + ": " + dep,
			})
		}
	}
}

func (d *JavaDetector) DetectEntrypoints(idx *index.FileIndex) []Entrypoint {
	var eps []Entrypoint

	for _, lang := range []string{"java", "kotlin"} {
		for _, f := range idx.ByLang(lang) {
			base := filepath.Base(f.RelPath)
			lbase := strings.ToLower(base)
			if lbase == "application.java" || lbase == "application.kt" ||
				lbase == "app.java" || lbase == "app.kt" ||
				lbase == "main.java" || lbase == "main.kt" {
				eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
			}
		}
	}

	return eps
}
