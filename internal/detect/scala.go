package detect

import (
	"regexp"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

var (
	// "org" %% "artifact" % "version" in build.sbt, ivy"org::artifact:version"
	// in Mill.
	sbtDep = regexp.MustCompile(`"[^"]+"\s+%%?\s+"([^"]+)"(?:\s+%\s+"([^"]+)")?`)
	// object Foo extends App / def main(args: Array[String])
	scalaMainRe = regexp.MustCompile(`(?m)(extends\s+App\b|def\s+main\s*\(\s*args)`)
)

type ScalaDetector struct{}

func (d *ScalaDetector) Key() string         { return "scala" }
func (d *ScalaDetector) Languages() []string { return []string{"scala"} }

func (d *ScalaDetector) Detect(idx *index.FileIndex, root string) DetectorResult {
	var res DetectorResult

	mr := newManifestReader(root, "scala")

	for _, manifest := range []string{"build.sbt", "build.sc"} {
		if !hasFile(idx, manifest) {
			continue
		}
		content, ok := mr.read(manifest)
		if !ok {
			continue
		}
		for _, m := range sbtDep.FindAllStringSubmatch(content, -1) {
			res.Dependencies = append(res.Dependencies, Dependency{
				Name:     m[1],
				Version:  m[2],
				Language: "scala",
				Manifest: manifest,
			})
			if fw, ok := sbtFrameworks.lookup(m[1]); ok {
				res.Frameworks = append(res.Frameworks, Framework{
					Name:     fw,
					Language: "scala",
					Evidence: manifest + ": " + m[1],
				})
			}
		}
	}

	if hasFile(idx, "conf/routes") {
		res.Frameworks = append(res.Frameworks, Framework{
			Name: "Play Framework", Language: "scala", Evidence: "conf/routes",
		})
		res.Entrypoints = append(res.Entrypoints, Entrypoint{Path: "conf/routes", Kind: "route"})
	}

	res.Entrypoints = append(res.Entrypoints, d.entrypoints(idx, root)...)
	res.ManifestIssues = mr.issues
	return res
}

func (d *ScalaDetector) entrypoints(idx *index.FileIndex, root string) []Entrypoint {
	var eps []Entrypoint
	scanSource(idx, root, []string{"scala"}, func(f *scan.FileEntry, content string) {
		if f.Class != scan.ClassSource {
			return
		}
		if strings.HasSuffix(f.RelPath, "Routes.scala") {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "route"})
			return
		}
		if scalaMainRe.MatchString(content) {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		}
	})
	return eps
}
