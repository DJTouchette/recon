package detect

import (
	"encoding/xml"
	"regexp"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

var (
	// implementation("group:artifact:version") / api 'group:artifact' etc.
	gradleDep = regexp.MustCompile(`(?:implementation|api|compileOnly|runtimeOnly|testImplementation|testRuntimeOnly|annotationProcessor|kapt|ksp)\s*(?:\(?\s*(?:platform\(\s*)?["'])([^"']+)["']`)
	// plugins { id("org.springframework.boot") version "3.2.0" }
	gradlePlugin = regexp.MustCompile(`id\s*(?:\(\s*)?["']([^"']+)["']`)
	// Java's real entrypoint signature, in any of its spellings.
	javaMainRe = regexp.MustCompile(`(?m)^\s*(?:public\s+)?static\s+(?:final\s+)?void\s+main\s*\(`)
	// Kotlin's top-level fun main(...)
	kotlinMainRe = regexp.MustCompile(`(?m)^\s*(?:public\s+)?fun\s+main\s*\(`)
)

type JavaDetector struct{}

func (d *JavaDetector) Key() string         { return "jvm" }
func (d *JavaDetector) Languages() []string { return []string{"java", "kotlin"} }

func (d *JavaDetector) Detect(idx *index.FileIndex, root string) DetectorResult {
	var res DetectorResult
	if len(idx.ByLang("java")) == 0 && len(idx.ByLang("kotlin")) == 0 {
		return res
	}

	lang := jvmLang(idx)
	mr := newManifestReader(root, lang)

	// pom.xml: parse the XML rather than grepping <artifactId>. A regex over
	// artifactIds also matches the project's own coordinates, its parent POM,
	// and every build plugin — none of which the project depends on.
	if content, ok := mr.read("pom.xml"); ok {
		deps, err := parsePomDependencies(content)
		if err != nil {
			// Malformed XML loses the whole dependency list, so it is an
			// unusable manifest, not a manifest that declares nothing.
			mr.unusable("pom.xml", err)
		}
		for _, dep := range deps {
			res.Dependencies = append(res.Dependencies, Dependency{
				Name:     dep.ArtifactID,
				Version:  dep.Version,
				Language: lang,
				Manifest: "pom.xml",
			})
			if name, ok := jvmFrameworks.lookup(dep.ArtifactID); ok {
				res.Frameworks = append(res.Frameworks, Framework{
					Name:     name,
					Language: lang,
					Evidence: "pom.xml: dependency " + dep.ArtifactID,
				})
			}
		}
	}

	for _, manifest := range []string{"build.gradle", "build.gradle.kts"} {
		content, ok := mr.read(manifest)
		if !ok {
			continue
		}
		for _, m := range gradleDep.FindAllStringSubmatch(content, -1) {
			artifact, version := splitGradleCoord(m[1])
			if artifact == "" {
				continue
			}
			res.Dependencies = append(res.Dependencies, Dependency{
				Name:     artifact,
				Version:  version,
				Language: lang,
				Manifest: manifest,
			})
			if name, ok := jvmFrameworks.lookup(artifact); ok {
				res.Frameworks = append(res.Frameworks, Framework{
					Name:     name,
					Language: lang,
					Evidence: manifest + ": " + m[1],
				})
			}
		}
		for _, m := range gradlePlugin.FindAllStringSubmatch(content, -1) {
			if name, ok := gradlePluginFrameworks[m[1]]; ok {
				res.Frameworks = append(res.Frameworks, Framework{
					Name:     name,
					Language: lang,
					Evidence: manifest + ": plugin " + m[1],
				})
			}
		}
	}

	fw, eps := d.scanSources(idx, root, lang)
	res.Frameworks = dropSubsumed(append(res.Frameworks, fw...))
	res.Entrypoints = eps
	res.ManifestIssues = mr.issues
	return res
}

// dropSubsumed removes framework claims that a narrower claim already covers.
func dropSubsumed(fws []Framework) []Framework {
	present := make(map[string]bool, len(fws))
	for _, f := range fws {
		present[f.Name] = true
	}
	out := fws[:0]
	for _, f := range fws {
		if by, ok := jvmSubsumed[f.Name]; ok && present[by] {
			continue
		}
		out = append(out, f)
	}
	return out
}

// jvmLang picks the ecosystem language label for a JVM repo.
func jvmLang(idx *index.FileIndex) string {
	if len(idx.ByLang("kotlin")) > len(idx.ByLang("java")) {
		return "kotlin"
	}
	return "java"
}

var gradlePluginFrameworks = map[string]string{
	"org.springframework.boot": "Spring Boot",
	"io.quarkus":               "Quarkus",
	"io.micronaut.application": "Micronaut",
	"com.android.application":  "Android",
	"com.android.library":      "Android",
	"io.ktor.plugin":           "Ktor",
}

// mavenDependency is one <dependency> entry.
type mavenDependency struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
	Scope      string `xml:"scope"`
}

// mavenProject reads only the dependency elements. The project's own
// <artifactId>, its <parent>, and <build><plugins> are intentionally not
// dependencies and are never reported as such.
type mavenProject struct {
	XMLName      xml.Name          `xml:"project"`
	Dependencies []mavenDependency `xml:"dependencies>dependency"`
	Managed      []mavenDependency `xml:"dependencyManagement>dependencies>dependency"`
	Profiles     []struct {
		Dependencies []mavenDependency `xml:"dependencies>dependency"`
	} `xml:"profiles>profile"`
}

// parsePomDependencies returns the declared dependencies, or the parse error:
// "no dependencies" and "this file could not be understood" are different
// answers and the caller reports them differently.
func parsePomDependencies(content string) ([]mavenDependency, error) {
	var p mavenProject
	if err := xml.Unmarshal([]byte(content), &p); err != nil {
		return nil, err
	}
	deps := make([]mavenDependency, 0, len(p.Dependencies)+len(p.Managed))
	deps = append(deps, p.Dependencies...)
	deps = append(deps, p.Managed...)
	for _, prof := range p.Profiles {
		deps = append(deps, prof.Dependencies...)
	}
	out := deps[:0]
	for _, d := range deps {
		if strings.TrimSpace(d.ArtifactID) != "" {
			d.ArtifactID = strings.TrimSpace(d.ArtifactID)
			d.Version = strings.TrimSpace(d.Version)
			out = append(out, d)
		}
	}
	return out, nil
}

// splitGradleCoord turns "group:artifact:version" into its artifact and
// version. Bare "group:artifact" yields an empty version.
func splitGradleCoord(coord string) (artifact, version string) {
	parts := strings.Split(coord, ":")
	switch len(parts) {
	case 0:
		return "", ""
	case 1:
		return parts[0], ""
	case 2:
		return parts[1], ""
	default:
		return parts[1], parts[2]
	}
}

// jvmSourceMarkers prove a framework from an annotation or import in source.
var jvmSourceMarkers = []struct {
	markers []string
	name    string
}{
	{[]string{"@SpringBootApplication", "org.springframework.boot"}, "Spring Boot"},
	{[]string{"org.springframework"}, "Spring"},
	{[]string{"io.quarkus"}, "Quarkus"},
	{[]string{"io.micronaut"}, "Micronaut"},
	{[]string{"io.ktor"}, "Ktor"},
	{[]string{"io.vertx"}, "Vert.x"},
	{[]string{"javax.ws.rs", "jakarta.ws.rs"}, "JAX-RS"},
	{[]string{"android.app.Activity"}, "Android"},
}

// jvmSubsumed drops a broader claim when the narrower one is already proven:
// every Spring Boot app also imports org.springframework, and reporting both
// "Spring" and "Spring Boot" reads as two frameworks.
var jvmSubsumed = map[string]string{
	"Spring": "Spring Boot",
}

// scanSources finds JVM entrypoints and source-proven frameworks in one pass.
//
// Entrypoints cannot be found by filename: Spring's own convention is
// <Name>Application.java, so an Application.java/App.java/Main.java filename
// list misses OrderServiceApplication.java — the single most common JVM
// entrypoint there is.
func (d *JavaDetector) scanSources(idx *index.FileIndex, root, lang string) ([]Framework, []Entrypoint) {
	var fws []Framework
	var eps []Entrypoint
	seen := make(map[string]bool)

	scanSource(idx, root, []string{"java", "kotlin"}, func(f *scan.FileEntry, content string) {
		for _, m := range jvmSourceMarkers {
			if seen[m.name] {
				continue
			}
			for _, marker := range m.markers {
				if strings.Contains(content, marker) {
					seen[m.name] = true
					fws = append(fws, Framework{
						Name:     m.name,
						Language: lang,
						Evidence: f.RelPath + ": " + marker,
					})
					break
				}
			}
		}

		if f.Class != scan.ClassSource {
			return
		}
		switch {
		case strings.Contains(content, "@SpringBootApplication"):
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		case f.Lang == "java" && javaMainRe.MatchString(content):
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		case f.Lang == "kotlin" && (kotlinMainRe.MatchString(content) || javaMainRe.MatchString(content)):
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		}
	})

	return fws, eps
}
