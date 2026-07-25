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
//
// The same standard applies to reading failures. A manifest that is absent is
// evidence of absence; a manifest that is present and unreadable is evidence of
// nothing at all, and the two are kept apart all the way from the read
// (readManifest, manifestReader) to the status a caller sees (StatusIncomplete
// plus Result.ManifestIssues, which name the file and the reason). An empty list
// that asserts "there are none" when the truth is "I could not tell" is the one
// answer worse than no answer.
package detect

import (
	"errors"
	"io/fs"
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
// for this project" from "we could not look properly", so an empty result is
// never silently read as "there are none".
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
	// StatusIncomplete means a manifest this answer depends on was present but
	// unusable, so the list may be short: the honest answer is "could not
	// tell", not "there are none". Result.ManifestIssues names which manifest
	// and what went wrong.
	//
	// It takes precedence over the other three, StatusFound included: a list
	// assembled from a partial reading of the repo is still not the whole
	// answer. The other three keep exactly the meanings they always had, so a
	// consumer that has not been taught about this value never sees it lie —
	// it just sees a status it does not recognise.
	StatusIncomplete Status = "incomplete"
)

// Feature names one of the lists a detector produces, so a manifest that could
// not be read can say which answers it would have contributed to. An
// unreadable pom.xml costs dependencies and framework claims; it costs no
// entrypoints, because JVM entrypoints come from source. Saying so keeps
// StatusIncomplete off the lists that really are complete.
type Feature string

const (
	FeatureFrameworks   Feature = "frameworks"
	FeatureDependencies Feature = "dependencies"
	FeatureEntrypoints  Feature = "entrypoints"
)

// ManifestIssue is a manifest that recon found and could not use: permissions,
// a dangling symlink, an I/O error, a directory in its place, or content that
// would not parse at all.
//
// It names the concrete artifact and the concrete reason on purpose. A bare
// "there was an error" status is only marginally better than the silence it
// replaces — the reader needs to know that it was pom.xml, and that it was
// permission denied, to do anything about it.
type ManifestIssue struct {
	// Manifest is the repo-relative, slash-separated path recon tried to read:
	// "pom.xml", "src/Api/Api.csproj".
	Manifest string
	// Language is the ecosystem language of the detector that tried, same
	// convention as Framework.Language.
	Language string
	// Reason is the short cause: "permission denied", "is a directory",
	// "unexpected end of JSON input". Deliberately not the full error string,
	// which embeds an absolute path and would make output machine-specific.
	Reason string
	// Affects lists the answers that are therefore incomplete.
	Affects []Feature
}

// DetectorResult is one detector's contribution.
type DetectorResult struct {
	Frameworks   []Framework
	Dependencies []Dependency
	Entrypoints  []Entrypoint

	// ManifestIssues are the manifests this detector tried and could not use.
	// They originate at the read (see manifestReader), never from a central
	// list of manifest filenames: such a list would immediately drift out of
	// sync with the detectors, which are the only code that knows what it
	// actually looked for.
	ManifestIssues []ManifestIssue
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

	// FrameworkStatus / EntrypointStatus / DependencyStatus say why a list is
	// empty — or, when StatusIncomplete, why it may be short.
	FrameworkStatus  Status
	EntrypointStatus Status
	DependencyStatus Status

	// ManifestIssues lists every manifest that was present but unusable, with
	// the reason. This is the evidence behind StatusIncomplete: without it the
	// status would say "something went wrong" and leave the reader no better
	// off than the empty list did.
	ManifestIssues []ManifestIssue
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
		res.ManifestIssues = append(res.ManifestIssues, dr.ManifestIssues...)
	}

	// Sort before dedupe so which duplicate survives is deterministic too.
	res.Frameworks = dedupeFrameworks(sortFrameworks(res.Frameworks))
	res.Dependencies = dedupeDependencies(sortDependencies(res.Dependencies))
	res.Entrypoints = dedupeEntrypoints(sortEntrypoints(res.Entrypoints))
	res.ManifestIssues = dedupeIssues(sortIssues(res.ManifestIssues))

	res.FrameworkStatus = statusFor(len(res.Frameworks), covered,
		anyAffects(res.ManifestIssues, FeatureFrameworks))
	res.EntrypointStatus = statusFor(len(res.Entrypoints), covered,
		anyAffects(res.ManifestIssues, FeatureEntrypoints))
	res.DependencyStatus = statusFor(len(res.Dependencies), covered,
		anyAffects(res.ManifestIssues, FeatureDependencies))
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

// statusFor grades one list. incomplete is checked first: whether the list came
// out empty or not, a manifest recon could not read means the list is not
// something to assert completeness about.
func statusFor(n int, covered, incomplete bool) Status {
	switch {
	case incomplete:
		return StatusIncomplete
	case n > 0:
		return StatusFound
	case covered:
		return StatusNoneMatched
	default:
		return StatusUnsupported
	}
}

// anyAffects reports whether any unusable manifest would have contributed to f.
func anyAffects(issues []ManifestIssue, f Feature) bool {
	for _, is := range issues {
		for _, a := range is.Affects {
			if a == f {
				return true
			}
		}
	}
	return false
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

func sortIssues(is []ManifestIssue) []ManifestIssue {
	sort.SliceStable(is, func(i, j int) bool {
		if is[i].Manifest != is[j].Manifest {
			return is[i].Manifest < is[j].Manifest
		}
		if is[i].Language != is[j].Language {
			return is[i].Language < is[j].Language
		}
		return is[i].Reason < is[j].Reason
	})
	return is
}

func dedupeIssues(is []ManifestIssue) []ManifestIssue {
	seen := make(map[string]bool, len(is))
	out := is[:0]
	for _, i := range is {
		k := i.Manifest + "\x00" + i.Language + "\x00" + i.Reason
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, i)
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

// manifestReader reads the manifests for one detector and remembers the ones
// that were present but unusable, so the failure reaches the result instead of
// being flattened into "no evidence".
//
// Call sites keep the shape they had — `content, ok := mr.read("pom.xml")` — and
// the recorded fact originates where the read happens. That is the only place
// that knows the file was tried at all, which is why there is no central list
// of known manifest filenames anywhere in this package: such a list would drift
// out of sync with the detectors the moment one of them learned a new file.
type manifestReader struct {
	root    string
	lang    string
	affects []Feature
	issues  []ManifestIssue
}

// newManifestReader builds a reader for one detector. affects defaults to
// frameworks and dependencies, which is what a manifest normally proves; pass
// it explicitly when a manifest also declares entrypoints (npm's "main"/"bin").
func newManifestReader(root, lang string, affects ...Feature) *manifestReader {
	if len(affects) == 0 {
		affects = []Feature{FeatureFrameworks, FeatureDependencies}
	}
	return &manifestReader{root: root, lang: lang, affects: affects}
}

// read returns the manifest's content. ok is false both when the manifest is
// absent and when it could not be read, so a caller's existing "then there is
// no evidence here" control flow stays correct — but the unreadable case is
// recorded as an issue on the way past, and so is no longer silent.
func (m *manifestReader) read(rel string) (string, bool) {
	content, ok, err := readManifest(m.root, rel)
	if err != nil {
		m.unusable(rel, err)
	}
	return content, ok
}

// unusable records a manifest that was found but could not be used. Parse
// failures are reported through here too: a pom.xml that is not valid XML
// yields no dependencies for exactly the same reason an unreadable one does,
// and calling that "none" is the same wrong answer.
func (m *manifestReader) unusable(rel string, err error) {
	m.issues = append(m.issues, ManifestIssue{
		Manifest: filepath.ToSlash(rel),
		Language: m.lang,
		Reason:   failureReason(err),
		Affects:  append([]Feature(nil), m.affects...),
	})
}

// readManifest reads a manifest file relative to root, keeping apart the two
// cases the old version collapsed:
//
//	ok=true             — read; content holds the bytes
//	ok=false, err==nil  — absent, which is real evidence of absence: a repo with
//	                      no pom.xml genuinely has no Maven dependencies
//	ok=false, err!=nil  — present but unusable, so everything derived from it is
//	                      unknown rather than empty
//
// A dangling symlink fails with ErrNotExist even though the path itself exists,
// which would land it in the "absent" bucket; Lstat settles that case, because
// something is there — recon just cannot see through it.
func readManifest(root, rel string) (string, bool, error) {
	path := filepath.Join(root, filepath.FromSlash(rel))
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		return string(data), true, nil
	case errors.Is(err, fs.ErrNotExist):
		if _, lerr := os.Lstat(path); lerr == nil {
			return "", false, err // dangling symlink: present, unreadable
		}
		return "", false, nil // genuinely absent
	default:
		return "", false, err
	}
}

// failureReason renders an error as a short, stable cause.
//
// os.ReadFile wraps its cause in a *fs.PathError whose message embeds the
// absolute path, which would put a machine-specific temp dir into recon's
// output and break the guarantee that two runs over one tree are
// byte-identical. ManifestIssue.Manifest already names the file, so only the
// cause is kept.
func failureReason(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) && pe.Err != nil {
		return pe.Err.Error()
	}
	return err.Error()
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
