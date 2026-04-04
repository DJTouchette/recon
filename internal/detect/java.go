package detect

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djtouchette/recon/internal/index"
)

var (
	// Matches <artifactId>name</artifactId> in pom.xml
	pomArtifact = regexp.MustCompile(`<artifactId>([^<]+)</artifactId>`)
	// Matches implementation("group:artifact:version") or api("group:artifact") in Gradle
	gradleDep = regexp.MustCompile(`(?:implementation|api|compileOnly|runtimeOnly|testImplementation|kapt|ksp)\s*(?:\(?\s*["'])([^"']+)["']`)
)

type JavaDetector struct{}

func (d *JavaDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	if len(idx.ByLang("java")) == 0 && len(idx.ByLang("kotlin")) == 0 {
		return nil
	}

	var frameworks []Framework
	seen := make(map[string]bool)

	lang := "java"
	if len(idx.ByLang("kotlin")) > len(idx.ByLang("java")) {
		lang = "kotlin"
	}

	addDep := func(name, evidence string) {
		if !seen[name] {
			seen[name] = true
			frameworks = append(frameworks, Framework{
				Name:     name,
				Language: lang,
				Evidence: evidence,
			})
		}
	}

	// Parse pom.xml — extract artifactIds
	if data, err := os.ReadFile(filepath.Join(root, "pom.xml")); err == nil {
		for _, m := range pomArtifact.FindAllStringSubmatch(string(data), -1) {
			addDep(m[1], "pom.xml")
		}
	}

	// Parse build.gradle / build.gradle.kts — extract dependency coordinates
	for _, name := range []string{"build.gradle", "build.gradle.kts"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		for _, m := range gradleDep.FindAllStringSubmatch(string(data), -1) {
			coord := m[1]
			// Parse "group:artifact:version" → use artifact name
			parts := strings.Split(coord, ":")
			if len(parts) >= 2 {
				addDep(parts[1], name)
			} else {
				addDep(coord, name)
			}
		}
	}

	return frameworks
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
