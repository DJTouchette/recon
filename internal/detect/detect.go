// Package detect answers three questions about a repo: what languages it is
// written in, what frameworks it actually uses, and where execution starts.
//
// The three answers have very different evidence standards:
//
//   - Languages come from counting indexed files. Always knowable.
//   - Dependencies are whatever the package manifests declare. Also always
//     knowable, and deliberately NOT the same thing as frameworks.
//   - Frameworks are a *claim*, and a claim needs proof: a framework is only
//     reported when a curated rule recognises a dependency, a config file, or
//     a source-level marker. "It is in package.json" is not proof of a
//     framework — typescript, eslint and @types/node are all in package.json.
//   - Entrypoints are guessed from conventions. When no convention matches,
//     that is reported as an explicit status, never as an empty list that
//     reads like "this program has no entrypoint".
package detect

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

// Language represents a detected programming language.
type Language struct {
	Name       string
	FileCount  int
	Percentage float64
	Extensions []string
}

// Framework is an evidence-backed claim that the project uses a framework.
//
// Language names the ecosystem the evidence came from (the npm ecosystem is
// reported as "javascript" whether or not the repo is written in TypeScript),
// except for frameworks proven by a marker inside a source file, where it is
// that file's language.
//
// Evidence always names the concrete artifact the claim rests on: a manifest
// entry ("package.json: dependencies[express]"), a config file
// ("next.config.js"), or a source marker ("app/main.py: imports flask").
type Framework struct {
	Name     string
	Language string
	Evidence string
}

// Dependency is a package declared in a manifest. A dependency is a fact, not
// a claim: it says nothing about whether the project uses a framework.
type Dependency struct {
	Name     string
	Version  string // "" when the manifest format does not make it cheap to read
	Language string // ecosystem language, same convention as Framework.Language
	Manifest string // manifest file the entry came from
}

// Entrypoint represents a detected entry point.
type Entrypoint struct {
	Path string
	Kind string // "main", "cli", "server", "route", "handler"
}

// Status distinguishes "we looked and found nothing" from "we have no rules
// for this project", so an empty result is never silently read as "there are
// none".
type Status string

const (
	// StatusFound means detection produced at least one result.
	StatusFound Status = "found"
	// StatusNoneMatched means rules for this repo's languages ran and nothing
	// matched. The repo may still have one — recon just does not recognise it.
	StatusNoneMatched Status = "none_matched"
	// StatusUnsupported means no detector covers any language in this repo, so
	// recon never even had a rule to apply.
	StatusUnsupported Status = "unsupported"
)

// DetectorResult is one detector's contribution.
type DetectorResult struct {
	Frameworks   []Framework
	Dependencies []Dependency
	Entrypoints  []Entrypoint
}

// Detector detects frameworks, dependencies and entrypoints for one ecosystem.
type Detector interface {
	// Key is a stable ecosystem id ("go", "node", "python", ...).
	Key() string
	// Languages lists the file-index language names this detector covers.
	Languages() []string
	// Detect runs the ecosystem's rules against the index.
	Detect(idx *index.FileIndex, root string) DetectorResult
}

var detectors = []Detector{
	&GoDetector{},
	&NodeDetector{},
	&PythonDetector{},
	&RustDetector{},
	&RubyDetector{},
	&ElixirDetector{},
	&DotNetDetector{},
	&JavaDetector{},
	&DartDetector{},
	&ScalaDetector{},
}

// Result is the full detection answer, including the statuses that make an
// empty list interpretable.
type Result struct {
	Languages    []Language
	Frameworks   []Framework
	Dependencies []Dependency
	Entrypoints  []Entrypoint

	// FrameworkStatus / EntrypointStatus say why a list is empty.
	FrameworkStatus  Status
	EntrypointStatus Status
}

// Detect runs every detector and returns the complete result. Output is fully
// sorted, so two runs over the same tree produce byte-identical results.
func Detect(idx *index.FileIndex, root string) *Result {
	res := &Result{
		Languages: detectLanguages(idx),
	}

	covered := false
	for _, d := range detectors {
		if detectorApplies(d, idx) {
			covered = true
		}
		dr := d.Detect(idx, root)
		res.Frameworks = append(res.Frameworks, dr.Frameworks...)
		res.Dependencies = append(res.Dependencies, dr.Dependencies...)
		res.Entrypoints = append(res.Entrypoints, dr.Entrypoints...)
	}

	// Sort before dedupe so which duplicate survives is deterministic too.
	res.Frameworks = dedupeFrameworks(sortFrameworks(res.Frameworks))
	res.Dependencies = dedupeDependencies(sortDependencies(res.Dependencies))
	res.Entrypoints = dedupeEntrypoints(sortEntrypoints(res.Entrypoints))

	res.FrameworkStatus = statusFor(len(res.Frameworks), covered)
	res.EntrypointStatus = statusFor(len(res.Entrypoints), covered)
	return res
}

// DetectAll is the legacy three-value view of Detect, kept for existing
// callers.
//
// Deprecated: it cannot express FrameworkStatus/EntrypointStatus, so an empty
// slice is ambiguous. Use Detect.
func DetectAll(idx *index.FileIndex, root string) ([]Language, []Framework, []Entrypoint) {
	r := Detect(idx, root)
	return r.Languages, r.Frameworks, r.Entrypoints
}

// detectorApplies reports whether any language the detector covers is present
// in the repo, i.e. whether recon had rules that were relevant at all.
func detectorApplies(d Detector, idx *index.FileIndex) bool {
	for _, l := range d.Languages() {
		if len(idx.ByLang(l)) > 0 {
			return true
		}
	}
	return false
}

func statusFor(n int, covered bool) Status {
	switch {
	case n > 0:
		return StatusFound
	case covered:
		return StatusNoneMatched
	default:
		return StatusUnsupported
	}
}

// detectLanguages builds the language breakdown from the file index.
func detectLanguages(idx *index.FileIndex) []Language {
	langCounts := idx.Languages()
	languages := make([]Language, 0, len(langCounts))

	extMap := make(map[string]map[string]bool)
	for _, f := range idx.All() {
		if f.Lang == "" {
			continue
		}
		if f.Class != scan.ClassSource && f.Class != scan.ClassTest {
			continue
		}
		ext := extFromPath(f.RelPath)
		if ext == "" {
			continue
		}
		if extMap[f.Lang] == nil {
			extMap[f.Lang] = make(map[string]bool)
		}
		extMap[f.Lang][ext] = true
	}

	for _, lc := range langCounts {
		exts := make([]string, 0, len(extMap[lc.Name]))
		for ext := range extMap[lc.Name] {
			exts = append(exts, ext)
		}
		sort.Strings(exts)
		languages = append(languages, Language{
			Name:       lc.Name,
			FileCount:  lc.Count,
			Percentage: lc.Percentage,
			Extensions: exts,
		})
	}
	return languages
}

// --- Deterministic ordering ---
//
// Every collection here is built by ranging maps somewhere upstream, so it is
// sorted on the way out. Without this, two runs over an unchanged tree return
// the same set in a different order.

func sortFrameworks(fw []Framework) []Framework {
	sort.SliceStable(fw, func(i, j int) bool {
		if fw[i].Name != fw[j].Name {
			return fw[i].Name < fw[j].Name
		}
		if fw[i].Language != fw[j].Language {
			return fw[i].Language < fw[j].Language
		}
		return fw[i].Evidence < fw[j].Evidence
	})
	return fw
}

func sortDependencies(deps []Dependency) []Dependency {
	sort.SliceStable(deps, func(i, j int) bool {
		if deps[i].Name != deps[j].Name {
			return deps[i].Name < deps[j].Name
		}
		return deps[i].Manifest < deps[j].Manifest
	})
	return deps
}

func sortEntrypoints(eps []Entrypoint) []Entrypoint {
	sort.SliceStable(eps, func(i, j int) bool {
		if eps[i].Path != eps[j].Path {
			return eps[i].Path < eps[j].Path
		}
		return eps[i].Kind < eps[j].Kind
	})
	return eps
}

// dedupeFrameworks keys on the framework name alone: "React" proven twice is
// one claim, and listing it once per language reads like two frameworks.
func dedupeFrameworks(fw []Framework) []Framework {
	seen := make(map[string]bool, len(fw))
	out := fw[:0]
	for _, f := range fw {
		if seen[f.Name] {
			continue
		}
		seen[f.Name] = true
		out = append(out, f)
	}
	return out
}

func dedupeDependencies(deps []Dependency) []Dependency {
	seen := make(map[string]bool, len(deps))
	out := deps[:0]
	for _, d := range deps {
		k := d.Name + "\x00" + d.Manifest
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, d)
	}
	return out
}

func dedupeEntrypoints(eps []Entrypoint) []Entrypoint {
	seen := make(map[string]bool, len(eps))
	out := eps[:0]
	for _, e := range eps {
		k := e.Path + "\x00" + e.Kind
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

// --- Shared helpers ---

func extFromPath(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return path[i:]
		}
		if path[i] == '/' {
			return ""
		}
	}
	return ""
}

// hasFile checks if a specific file exists in the index.
func hasFile(idx *index.FileIndex, path string) bool {
	return idx.Exists(path)
}

// readManifest reads a manifest file relative to root. Manifests are small; a
// missing or unreadable one is simply "no evidence".
// readManifest reads a manifest file.
//
// KNOWN LIMITATION: a manifest that exists but cannot be read — permissions, a
// broken symlink, a truncated file — is reported identically to one that is
// absent, so the caller reports zero dependencies and a status of
// none_matched when the honest answer is "could not tell". Fixing it properly
// means threading the error through every detector into a fourth status (or a
// per-feature reason model); returning an error that nothing reads would only
// move the silence one layer down. Deliberately left until there is a consumer
// for the distinction.
func readManifest(root, rel string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "", false
	}
	return string(data), true
}

// Content scans back the source-marker rules (a Flask app with no manifest is
// still a Flask app). They are bounded so `recon overview` stays fast on a
// large repo: at most maxScanBytes per file and maxScanFiles files per scan.
const (
	maxScanBytes = 256 << 10
	maxScanFiles = 5000
)

// scanSource calls fn with the (bounded) content of every source-class file in
// one of langs. Iteration order follows the file index, which is walk order,
// so it is stable across runs.
func scanSource(idx *index.FileIndex, root string, langs []string, fn func(f *scan.FileEntry, content string)) {
	want := make(map[string]bool, len(langs))
	for _, l := range langs {
		want[l] = true
	}
	n := 0
	for _, f := range idx.All() {
		if !want[f.Lang] {
			continue
		}
		if f.Class != scan.ClassSource && f.Class != scan.ClassScript {
			continue
		}
		if n >= maxScanFiles {
			return
		}
		n++
		content, ok := readBounded(root, f.RelPath)
		if !ok {
			continue
		}
		fn(f, content)
	}
}

// readBounded reads at most maxScanBytes from a file.
func readBounded(root, rel string) (string, bool) {
	fh, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "", false
	}
	defer fh.Close()
	buf := make([]byte, maxScanBytes)
	n, err := fh.Read(buf)
	if n == 0 && err != nil {
		return "", false
	}
	return string(buf[:n]), true
}

// sortedKeys returns a map's keys in sorted order, so map iteration never
// leaks into detection output.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// containsAny reports whether s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
