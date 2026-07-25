package index

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/djtouchette/recon/internal/scan"
)

// DepGraph holds the import/require dependency graph between files.
type DepGraph struct {
	imports    map[string][]string    // file → files it imports
	importedBy map[string][]string    // file → files that import it
	stats      map[string]ImportStats // file → import-resolution telemetry
}

// ImportStats records what happened to one file's import specifiers.
//
// A bare `fan_in: 0` is ambiguous: it can mean "nothing imports this" or "recon
// does not understand your import style". These counts disambiguate the two.
// Every specifier an extractor produced lands in exactly one bucket:
//
//	Resolved   — produced at least one edge to a local file
//	External   — deliberately not a local file (stdlib, third-party, unknown
//	             module path) — expected to produce no edge
//	Unresolved — looked like it should name a local file but nothing matched;
//	             this is the bucket that means "recon dropped a real edge"
type ImportStats struct {
	Lang            string   `json:"lang"`
	Extracted       int      `json:"extracted"`
	Resolved        int      `json:"resolved"`
	External        int      `json:"external"`
	Unresolved      int      `json:"unresolved"`
	UnresolvedSpecs []string `json:"unresolved_specs,omitempty"`
}

// LangImportCoverage aggregates ImportStats over the files of one language.
type LangImportCoverage struct {
	Lang       string `json:"lang"`
	Files      int    `json:"files"`
	Extracted  int    `json:"extracted"`
	Resolved   int    `json:"resolved"`
	External   int    `json:"external"`
	Unresolved int    `json:"unresolved"`
}

// maxUnresolvedSamples bounds the specifier samples kept per file.
const maxUnresolvedSamples = 20

// importTally accumulates ImportStats while a file's specifiers are resolved.
// A nil *importTally is a no-op so resolvers stay callable without one.
type importTally struct {
	lang       string
	extracted  int
	resolved   int
	external   int
	unresolved int
	samples    []string
	seen       map[string]bool
}

func (t *importTally) extract(n int) {
	if t == nil {
		return
	}
	t.extracted += n
}

// hit records a specifier that resolved to at least one local file.
func (t *importTally) hit() {
	if t == nil {
		return
	}
	t.resolved++
}

// skip records a specifier deliberately treated as non-local.
func (t *importTally) skip() {
	if t == nil {
		return
	}
	t.external++
}

// miss records a specifier that should have named a local file but did not.
func (t *importTally) miss(spec string) {
	if t == nil {
		return
	}
	t.unresolved++
	if spec == "" || len(t.samples) >= maxUnresolvedSamples {
		return
	}
	if t.seen == nil {
		t.seen = make(map[string]bool)
	}
	if t.seen[spec] {
		return
	}
	t.seen[spec] = true
	t.samples = append(t.samples, spec)
}

func (t *importTally) stats() ImportStats {
	if t == nil {
		return ImportStats{}
	}
	return ImportStats{
		Lang:            t.lang,
		Extracted:       t.extracted,
		Resolved:        t.resolved,
		External:        t.external,
		Unresolved:      t.unresolved,
		UnresolvedSpecs: t.samples,
	}
}

// NewDepGraph builds a dependency graph by scanning source files for import statements.
func NewDepGraph(root string, idx *FileIndex) *DepGraph {
	dg := &DepGraph{
		imports:    make(map[string][]string),
		importedBy: make(map[string][]string),
		stats:      make(map[string]ImportStats),
	}

	rc := newResolveCtx(root, idx)

	// Scan both source and test files for imports.
	// Test files need import resolution for languages like C# where
	// test projects have non-matching names and the import graph is
	// the only way to connect tests to source files.
	sources := idx.ByClass(scan.ClassSource)
	tests := idx.ByClass(scan.ClassTest)
	scripts := idx.ByClass(scan.ClassScript)
	allFiles := make([]*scan.FileEntry, 0, len(sources)+len(tests)+len(scripts))
	allFiles = append(allFiles, sources...)
	allFiles = append(allFiles, tests...)
	allFiles = append(allFiles, scripts...)

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0)*2)

	for _, f := range allFiles {
		f := f
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			imports, st := extractImports(f, rc)
			if len(imports) == 0 && st.Extracted == 0 {
				return
			}

			mu.Lock()
			if st.Extracted > 0 {
				dg.stats[f.RelPath] = st
			}
			if len(imports) > 0 {
				dg.imports[f.RelPath] = imports
				for _, imp := range imports {
					dg.importedBy[imp] = append(dg.importedBy[imp], f.RelPath)
				}
			}
			mu.Unlock()
		}()
	}

	wg.Wait()
	return dg
}

// NewDepGraphFromCache rebuilds a graph from persisted imports AND their
// resolution telemetry.
//
// NewDepGraphFromData exists for callers that only have edges, but it returns an
// empty stats map — and since every run serves from cache after the first, using
// it on the load path made the telemetry invisible in practice. That is the same
// "signal survives only the first build" failure as symbol parse status: the
// whole point of recording that recon dropped N imports is that the next reader
// finds out, and the next reader is almost always reading the cache.
func NewDepGraphFromCache(imports map[string][]string, stats map[string]ImportStats) *DepGraph {
	dg := NewDepGraphFromData(imports)
	if stats != nil {
		dg.stats = stats
	}
	return dg
}

// NewDepGraphFromData creates a DepGraph from pre-computed import edges.
// Import telemetry is not part of the edge data, so a graph restored this way
// reports no ImportStats until a scan recomputes them. Prefer
// NewDepGraphFromCache on any path that has persisted stats available.
func NewDepGraphFromData(imports map[string][]string) *DepGraph {
	dg := &DepGraph{
		imports:    imports,
		importedBy: make(map[string][]string),
		stats:      make(map[string]ImportStats),
	}
	for src, targets := range imports {
		for _, t := range targets {
			dg.importedBy[t] = append(dg.importedBy[t], src)
		}
	}
	return dg
}

// ImportsOf returns files imported by the given file.
func (dg *DepGraph) ImportsOf(path string) []string {
	return dg.imports[path]
}

// ImportedBy returns files that import the given file.
func (dg *DepGraph) ImportedBy(path string) []string {
	return dg.importedBy[path]
}

// AllImports returns the full import map (source → targets).
func (dg *DepGraph) AllImports() map[string][]string {
	return dg.imports
}

// ImportStatsOf returns the import-resolution telemetry for a file.
func (dg *DepGraph) ImportStatsOf(path string) (ImportStats, bool) {
	st, ok := dg.stats[path]
	return st, ok
}

// AllImportStats returns per-file import-resolution telemetry.
func (dg *DepGraph) AllImportStats() map[string]ImportStats {
	return dg.stats
}

// ImportCoverage aggregates the per-file telemetry by language, sorted by
// unresolved count (worst first) so a caller can surface the languages whose
// import styles recon is silently dropping.
func (dg *DepGraph) ImportCoverage() []LangImportCoverage {
	byLang := make(map[string]*LangImportCoverage)
	for _, st := range dg.stats {
		c := byLang[st.Lang]
		if c == nil {
			c = &LangImportCoverage{Lang: st.Lang}
			byLang[st.Lang] = c
		}
		c.Files++
		c.Extracted += st.Extracted
		c.Resolved += st.Resolved
		c.External += st.External
		c.Unresolved += st.Unresolved
	}
	out := make([]LangImportCoverage, 0, len(byLang))
	for _, c := range byLang {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Unresolved != out[j].Unresolved {
			return out[i].Unresolved > out[j].Unresolved
		}
		return out[i].Lang < out[j].Lang
	})
	return out
}

// ScanFileImports extracts imports for specific files. Used during incremental refresh.
func ScanFileImports(root string, files []*scan.FileEntry, idx *FileIndex) map[string][]string {
	imports, _ := ScanFileImportsWithStats(root, files, idx)
	return imports
}

// ScanFileImportsWithStats is ScanFileImports plus the per-file resolution
// telemetry.
//
// The incremental path needs both: ScanFileImports discarded the stats, so a
// refresh wrote new edges while leaving the old unresolved counts in place, and
// a file whose imports had started resolving cleanly kept reporting a stale
// caveat. Stats are returned for every file scanned, including files that
// produced no edges at all — that is the case the telemetry exists for, since
// "no edges" and "could not resolve any edges" are otherwise identical.
func ScanFileImportsWithStats(root string, files []*scan.FileEntry, idx *FileIndex) (map[string][]string, map[string]ImportStats) {
	rc := newResolveCtx(root, idx)
	imports := make(map[string][]string)
	stats := make(map[string]ImportStats)
	for _, f := range files {
		found, st := extractImports(f, rc)
		if len(found) > 0 {
			imports[f.RelPath] = found
		}
		if st.Extracted > 0 {
			stats[f.RelPath] = st
		}
	}
	return imports, stats
}

// ─── Resolution context ───────────────────────────────────────────────────────

// resolveCtx carries the repo-wide facts import resolution needs (module paths,
// source roots, namespace maps). It is built once per scan and shared by every
// file's resolver; the expensive maps are built lazily and only for languages
// the repo actually contains.
//
// Before this existed, per-repo maps (the Elixir module map in particular) were
// rebuilt for every file — O(files²) reads — and file lookups were resolved
// against the process working directory rather than the scan root, so the same
// repo produced different graphs depending on where recon was invoked from.
type resolveCtx struct {
	root string
	idx  *FileIndex

	goMods []goModule

	pyOnce  sync.Once
	pyRoots []string

	jsOnce    sync.Once
	jsAliases []jsAlias

	csOnce sync.Once
	csNS   map[string][]string

	jvmOnce sync.Once
	jvmPkg  map[string][]string
	jvmDecl map[string][]string

	exOnce sync.Once
	exMods map[string]string

	swiftOnce    sync.Once
	swiftTargets map[string][]string

	phpOnce sync.Once
	phpPSR4 map[string][]string
}

func newResolveCtx(root string, idx *FileIndex) *resolveCtx {
	rc := &resolveCtx{root: root, idx: idx}
	rc.goMods = detectGoModules(root, idx)
	return rc
}

// langFileScan is one file's contribution to a lazily-built language map.
type langFileScan[T any] struct {
	file *scan.FileEntry
	val  T
}

// scanLangFilesParallel reads every file of the given language and maps it with
// fn, concurrently, returning the results in index order so the maps built from
// them stay deterministic.
//
// The language maps touch every file of a language exactly once. Doing that
// serially inside the sync.Once would stall every resolver goroutine behind a
// single thread of I/O and parsing.
func scanLangFilesParallel[T any](rc *resolveCtx, langs []string, fn func(*scan.FileEntry, []byte) T) []langFileScan[T] {
	wanted := make(map[string]bool, len(langs))
	for _, l := range langs {
		wanted[l] = true
	}

	var files []*scan.FileEntry
	for _, f := range rc.idx.All() {
		if wanted[f.Lang] {
			files = append(files, f)
		}
	}

	out := make([]langFileScan[T], len(files))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0)*2)
	for i, f := range files {
		i, f := i, f
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			data, err := os.ReadFile(filepath.Join(rc.root, f.RelPath))
			if err != nil {
				return
			}
			out[i] = langFileScan[T]{file: f, val: fn(f, data)}
		}()
	}
	wg.Wait()
	return out
}

// ─── Import extraction regexes ────────────────────────────────────────────────

var (
	goImportSingle = regexp.MustCompile(`import\s+"([^"]+)"`)
	goImportBlock  = regexp.MustCompile(`import\s*\(([^)]+)\)`)
	goImportLine   = regexp.MustCompile(`"([^"]+)"`)

	jsImportFrom = regexp.MustCompile(`(?:import\s+.*?from\s+|require\s*\(\s*)['"]([^'"]+)['"]`)

	pyImportFrom = regexp.MustCompile(`^from\s+(\.*[\w.]*)\s+import\s+(.*)$`)
	pyImport     = regexp.MustCompile(`^import\s+([\w.,\s]+?)\s*$`)

	csUsing = regexp.MustCompile(`^using\s+(?:static\s+)?([A-Za-z][\w.]*)\s*;`)
	// csNamespaceRe matches a C# namespace declaration in either the
	// file-scoped (`namespace A.B;`) or block (`namespace A.B {`) form.
	csNamespaceRe = regexp.MustCompile(`^\s*namespace\s+([A-Za-z_][\w.]*)\s*[;{]?\s*$`)

	// The trailing (\.\*)? group is what makes on-demand ("wildcard") imports
	// visible: `import com.example.models.*;`.
	javaImportRe   = regexp.MustCompile(`^import\s+(?:static\s+)?([A-Za-z][\w.]*?)(\.\*)?\s*;`)
	kotlinImportRe = regexp.MustCompile(`^import\s+([A-Za-z][\w.]*?)(\.\*)?\s*$`)

	// jvmPackageRe matches the package declaration of a Java/Kotlin file.
	jvmPackageRe = regexp.MustCompile(`(?m)^\s*package\s+([\w.]+)`)
	// jvmDeclRe matches a *top-level* Java/Kotlin declaration — anchored at
	// column 0, so members of a class (which are indented) cannot match. This
	// is what lets a Kotlin file declaring several types be found by an import
	// of any one of them.
	jvmDeclRe = regexp.MustCompile(`(?m)^(?:@\w+(?:\([^)\n]*\))?\s+)*` +
		`(?:(?:public|private|protected|internal|open|final|abstract|sealed|non-sealed|static|strictfp|` +
		`data|value|inner|annotation|enum|expect|actual|external|const|suspend|operator|infix|inline|` +
		`lateinit|tailrec|companion)\s+)*` +
		`(?:@interface|class|interface|object|typealias|record|enum|fun|val|var)\b\s*` +
		`(?:<[^>\n]*>\s*)?([A-Za-z_]\w*)`)

	rbRequire         = regexp.MustCompile(`^\s*require\s+['"]([^'"]+)['"]`)
	rbRequireRelative = regexp.MustCompile(`^\s*require_relative\s+['"]([^'"]+)['"]`)

	// Elixir module reference patterns
	exModuleRef = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*(?:\.[A-Z][A-Za-z0-9_]*)+)`)

	// Rust import patterns
	rsUseStmt = regexp.MustCompile(`^use\s+((?:crate|self|super)(?:::\w+)+)`)
	rsModDecl = regexp.MustCompile(`^(?:pub\s+)?mod\s+(\w+)\s*[;{]`)

	// PHP use statement pattern
	phpUseStmt = regexp.MustCompile(`^use\s+(?:function\s+|const\s+)?([A-Z][\w\\]+)`)

	// Swift import pattern
	swiftImportRe = regexp.MustCompile(`^import\s+([A-Za-z_]\w*)`)

	// Dart import/export pattern
	dartImportRe = regexp.MustCompile(`^(?:import|export)\s+['"]([^'"]+)['"]`)

	// Scala import pattern
	scalaImportRe = regexp.MustCompile(`^import\s+([A-Za-z_][\w.]*)(?:\.\{|\.[\w*]+)`)

	// Julia: string literals inside an include(...) argument list.
	juliaStringRe = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
)

func extractImports(f *scan.FileEntry, rc *resolveCtx) ([]string, ImportStats) {
	fullPath := filepath.Join(rc.root, f.RelPath)
	t := &importTally{lang: f.Lang}

	var out []string
	switch f.Lang {
	case "go":
		out = resolveGoSpecs(importSpecs(fullPath, f.Lang, 150, goRegexSpecs), f.RelPath, rc.goMods, rc.idx, t)
	case "javascript", "typescript":
		out = resolveJSSpecs(importSpecs(fullPath, f.Lang, 150, jsRegexSpecs), f.RelPath, rc, t)
	case "python":
		out = resolvePySpecs(pythonImportSpecs(fullPath), f.RelPath, rc, t)
	case "zig":
		out = resolveZigSpecs(importSpecs(fullPath, f.Lang, 300, noRegexSpecs), f.RelPath, rc.idx, t)
	case "lua":
		out = resolveLuaSpecs(importSpecs(fullPath, f.Lang, 150, noRegexSpecs), f.RelPath, rc.idx, t)
	case "julia":
		out = resolveJuliaSpecs(importSpecs(fullPath, f.Lang, 300, noRegexSpecs), f.RelPath, rc.idx, t)
	case "shell":
		out = resolveShellSpecs(importSpecs(fullPath, f.Lang, 200, noRegexSpecs), f.RelPath, rc.idx, t)
	case "java", "kotlin":
		out = resolveJavaSpecs(jvmImportSpecs(fullPath, f.Lang), f.RelPath, f.Lang, rc, t)
	case "csharp":
		out = resolveCSharpSpecs(csharpImportSpecs(fullPath), f.RelPath, rc, t)
	case "ruby":
		out = resolveRubySpecs(rubyImportSpecs(fullPath), f.RelPath, rc.idx, t)
	case "rust":
		out = resolveRustSpecs(rustImportSpecs(fullPath), f.RelPath, rc.idx, t)
	case "swift":
		lines, err := readHeadLines(fullPath, 50)
		if err != nil {
			return nil, ImportStats{}
		}
		out = resolveSwiftImports(lines, f.RelPath, rc, t)
	case "php":
		out = resolvePHPSpecs(importSpecs(fullPath, f.Lang, 100, phpRegexSpecs), f.RelPath, rc, t)
	case "dart":
		lines, err := readHeadLines(fullPath, 100)
		if err != nil {
			return nil, ImportStats{}
		}
		out = resolveDartImports(lines, f.RelPath, rc.root, rc.idx, t)
	case "scala":
		out = resolveScalaSpecs(importSpecs(fullPath, f.Lang, 100, scalaRegexSpecs), f.RelPath, rc.idx, t)
	case "elixir":
		// Elixir needs a full file scan — module refs appear anywhere, not just
		// at the top.
		lines, err := readAllLines(fullPath)
		if err != nil {
			return nil, ImportStats{}
		}
		out = resolveElixirImports(lines, f.RelPath, rc, t)
	default:
		return nil, ImportStats{}
	}
	return out, t.stats()
}

func readHeadLines(path string, maxLines int) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() && len(lines) < maxLines {
		lines = append(lines, scanner.Text())
	}
	return lines, nil
}

func readAllLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

// importSpecs returns the raw import specifiers for a file, preferring
// tree-sitter extraction and falling back to a per-language regex extractor
// (over the first maxLines lines) when no grammar/import-query is available or
// the parse fails.
func importSpecs(fullPath, lang string, maxLines int, regexFallback func([]string) []string) []string {
	if hasTSImports(lang) {
		if data, err := os.ReadFile(fullPath); err == nil {
			if specs, ok := tsImportSpecs(data, lang); ok {
				return specs
			}
		}
	}
	lines, err := readHeadLines(fullPath, maxLines)
	if err != nil {
		return nil
	}
	return regexFallback(lines)
}

// noRegexSpecs is the no-op fallback for languages whose import extraction is
// tree-sitter-only (no regex extractor).
func noRegexSpecs([]string) []string { return nil }

// splitSpec splits a "kind:value" tagged specifier. Untagged specifiers (which
// is what the resolver unit tests pass) fall back to defKind.
func splitSpec(spec, defKind string) (kind, value string) {
	k, v, ok := strings.Cut(spec, ":")
	if !ok {
		return defKind, spec
	}
	return k, v
}

// ─── Go ───────────────────────────────────────────────────────────────────────

// goModule is one go.mod found in the repo: its declared module path and the
// repo-relative directory it lives in ("" for the repo root).
type goModule struct {
	Path string
	Dir  string
}

// detectGoModules finds every go.mod in the repo, not just the one at the root.
// Multi-module repos (go.work workspaces, services/*/go.mod layouts) are common
// and a root-only lookup silently produces an empty Go import graph for them.
func detectGoModules(root string, idx *FileIndex) []goModule {
	seen := make(map[string]bool)
	var mods []goModule

	add := func(rel string) {
		if seen[rel] {
			return
		}
		seen[rel] = true
		modPath := readGoModulePath(filepath.Join(root, rel))
		if modPath == "" {
			return
		}
		dir := filepath.Dir(rel)
		if dir == "." {
			dir = ""
		}
		mods = append(mods, goModule{Path: modPath, Dir: dir})
	}

	add("go.mod")
	if idx != nil {
		for _, f := range idx.All() {
			if filepath.Base(f.RelPath) == "go.mod" {
				add(f.RelPath)
			}
		}
	}

	// Longest module path first so nested modules win over their parents.
	sort.Slice(mods, func(i, j int) bool {
		if len(mods[i].Path) != len(mods[j].Path) {
			return len(mods[i].Path) > len(mods[j].Path)
		}
		return mods[i].Path < mods[j].Path
	})
	return mods
}

func readGoModulePath(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// matchGoModule returns the module owning an import path and the package path
// within it ("" when the import names the module's own root package).
func matchGoModule(imp string, mods []goModule) (goModule, string, bool) {
	for _, m := range mods {
		if imp == m.Path {
			return m, "", true
		}
		if strings.HasPrefix(imp, m.Path+"/") {
			return m, imp[len(m.Path)+1:], true
		}
	}
	return goModule{}, "", false
}

// goRegexSpecs is the regex fallback extractor for Go import specifiers. It
// returns the bare module paths (no quotes), matching what tree-sitter captures.
func goRegexSpecs(lines []string) []string {
	content := strings.Join(lines, "\n")
	var specs []string

	// Single imports
	for _, m := range goImportSingle.FindAllStringSubmatch(content, -1) {
		specs = append(specs, m[1])
	}

	// Block imports
	for _, block := range goImportBlock.FindAllStringSubmatch(content, -1) {
		for _, m := range goImportLine.FindAllStringSubmatch(block[1], -1) {
			specs = append(specs, m[1])
		}
	}
	return specs
}

// resolveGoSpecs resolves Go import paths belonging to any module in the repo.
func resolveGoSpecs(specs []string, filePath string, mods []goModule, idx *FileIndex, t *importTally) []string {
	t.extract(len(specs))

	seen := make(map[string]bool)
	var resolved []string
	for _, imp := range specs {
		mod, pkgPath, ok := matchGoModule(imp, mods)
		if !ok {
			t.skip()
			continue
		}
		// pkgPath == "" is the module's own root package — a library whose API
		// lives at its module root is imported exactly this way.
		localDir := mod.Dir
		if pkgPath != "" {
			localDir = filepath.Join(mod.Dir, pkgPath)
		}

		found := false
		for _, f := range idx.ByDir(localDir) {
			if f.Lang != "go" || f.Class != scan.ClassSource {
				continue
			}
			found = true
			if f.RelPath == filePath || seen[f.RelPath] {
				continue
			}
			seen[f.RelPath] = true
			resolved = append(resolved, f.RelPath)
		}
		if found {
			t.hit()
		} else {
			t.miss(imp)
		}
	}
	return resolved
}

// ─── JavaScript / TypeScript ──────────────────────────────────────────────────

// jsRegexSpecs is the regex fallback extractor for JS/TS import specifiers.
func jsRegexSpecs(lines []string) []string {
	var specs []string
	for _, line := range lines {
		for _, m := range jsImportFrom.FindAllStringSubmatch(line, -1) {
			specs = append(specs, m[1])
		}
	}
	return specs
}

// jsAlias is one tsconfig `paths` entry. Patterns contain at most one "*".
type jsAlias struct {
	prefix   string
	suffix   string
	wildcard bool
	targets  []string // baseUrl-joined, repo-relative, "*" kept as placeholder
}

// resolveJSSpecs resolves JS/TS module specifiers to local files: relative
// paths, and bare specifiers matched by a tsconfig/jsconfig `paths` alias.
func resolveJSSpecs(specs []string, filePath string, rc *resolveCtx, t *importTally) []string {
	t.extract(len(specs))

	dir := filepath.Dir(filePath)
	seen := make(map[string]bool)
	var resolved []string

	add := func(found string) {
		if found == "" || found == filePath || seen[found] {
			return
		}
		seen[found] = true
		resolved = append(resolved, found)
	}

	for _, imp := range specs {
		if strings.HasPrefix(imp, ".") {
			target := filepath.Clean(filepath.Join(dir, imp))
			found := resolveJSPath(target, rc.idx)
			if found == "" {
				t.miss(imp)
				continue
			}
			t.hit()
			add(found)
			continue
		}

		matched, found := resolveJSAlias(imp, rc)
		switch {
		case found != "":
			t.hit()
			add(found)
		case matched:
			// An alias claimed this specifier but no file backs it.
			t.miss(imp)
		default:
			t.skip() // third-party package
		}
	}
	return resolved
}

// resolveJSAlias maps a bare specifier through the tsconfig `paths` aliases.
// The bool reports whether an alias pattern matched at all, which is what
// separates "unresolved local alias" from "third-party package".
func resolveJSAlias(imp string, rc *resolveCtx) (bool, string) {
	matched := false
	for _, a := range rc.jsPathAliases() {
		var star string
		if a.wildcard {
			if !strings.HasPrefix(imp, a.prefix) || !strings.HasSuffix(imp, a.suffix) ||
				len(imp) < len(a.prefix)+len(a.suffix) {
				continue
			}
			star = imp[len(a.prefix) : len(imp)-len(a.suffix)]
		} else if imp != a.prefix {
			continue
		}
		matched = true
		for _, target := range a.targets {
			cand := filepath.Clean(strings.Replace(target, "*", star, 1))
			if found := resolveJSPath(cand, rc.idx); found != "" {
				return true, found
			}
		}
	}
	return matched, ""
}

// jsExtCandidates maps the extension written in a specifier to the extensions
// that may back it on disk. Under moduleResolution NodeNext/Node16, TypeScript
// *requires* ESM specifiers to say ".js" while the file on disk is ".ts", so a
// literal extension match finds nothing in a modern TS ESM repo.
var jsExtCandidates = map[string][]string{
	".js":  {".ts", ".tsx", ".js", ".jsx"},
	".jsx": {".tsx", ".jsx"},
	".mjs": {".mts", ".mjs"},
	".cjs": {".cts", ".cjs"},
}

func resolveJSPath(target string, idx *FileIndex) string {
	// Try exact path
	if idx.Exists(target) {
		return target
	}
	// A written extension may name the compiler *output*; try the sources that
	// could produce it.
	if ext := filepath.Ext(target); ext != "" {
		if cands, ok := jsExtCandidates[ext]; ok {
			base := strings.TrimSuffix(target, ext)
			for _, c := range cands {
				if idx.Exists(base + c) {
					return base + c
				}
			}
			// ./dir/index.js written for ./dir/index.ts is covered above; a
			// ".js" specifier naming a directory is not legal ESM, so stop here.
			return ""
		}
	}
	// Extensionless specifier
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".mts"} {
		if idx.Exists(target + ext) {
			return target + ext
		}
	}
	// Try index files
	for _, name := range []string{"/index.ts", "/index.tsx", "/index.js", "/index.jsx"} {
		if idx.Exists(target + name) {
			return target + name
		}
	}
	return ""
}

// jsPathAliases parses `compilerOptions.paths` from the repo-root tsconfig.json
// (or jsconfig.json).
//
// Scope: the root config only — `extends` chains, per-package configs and
// workspace/monorepo package resolution are deliberately not followed. Those
// need the package.json graph and produce ambiguous many-to-one mappings; a
// wrong guess there fabricates edges, which is worse than dropping them. Bare
// specifiers no alias claims stay classified External, so the telemetry shows
// how much is being left on the table.
func (rc *resolveCtx) jsPathAliases() []jsAlias {
	rc.jsOnce.Do(func() {
		for _, name := range []string{"tsconfig.json", "jsconfig.json"} {
			data, err := os.ReadFile(filepath.Join(rc.root, name))
			if err != nil {
				continue
			}
			var cfg struct {
				CompilerOptions struct {
					BaseURL string              `json:"baseUrl"`
					Paths   map[string][]string `json:"paths"`
				} `json:"compilerOptions"`
			}
			if err := json.Unmarshal(stripJSONComments(data), &cfg); err != nil {
				continue
			}
			base := cfg.CompilerOptions.BaseURL
			if base == "." {
				base = ""
			}
			for pattern, targets := range cfg.CompilerOptions.Paths {
				a := jsAlias{}
				if i := strings.IndexByte(pattern, '*'); i >= 0 {
					a.wildcard = true
					a.prefix = pattern[:i]
					a.suffix = pattern[i+1:]
				} else {
					a.prefix = pattern
				}
				for _, tgt := range targets {
					a.targets = append(a.targets, filepath.Join(base, tgt))
				}
				sort.Strings(a.targets)
				rc.jsAliases = append(rc.jsAliases, a)
			}
			// Longest prefix first so a more specific alias wins.
			sort.Slice(rc.jsAliases, func(i, j int) bool {
				return len(rc.jsAliases[i].prefix) > len(rc.jsAliases[j].prefix)
			})
			return
		}
	})
	return rc.jsAliases
}

// stripJSONComments removes // and /* */ comments and trailing commas so a
// JSONC file (which is what tsconfig.json really is) parses as JSON.
func stripJSONComments(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inStr, esc := false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
				i++
			}
			i++
		default:
			out = append(out, c)
		}
	}
	// Drop trailing commas before } or ].
	res := make([]byte, 0, len(out))
	for i := 0; i < len(out); i++ {
		if out[i] == ',' {
			j := i + 1
			for j < len(out) && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
				j++
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				continue
			}
		}
		res = append(res, out[i])
	}
	return res
}

// ─── Ruby ─────────────────────────────────────────────────────────────────────

// rubyImportSpecs returns tagged Ruby import specifiers, preferring tree-sitter
// and falling back to regex.
func rubyImportSpecs(fullPath string) []string {
	if hasTSImports("ruby") {
		if data, err := os.ReadFile(fullPath); err == nil {
			var specs []string
			if tsImportEachMatch(data, "ruby", func(caps map[string]string) {
				path := caps["path"]
				if path == "" {
					return
				}
				if caps["_m"] == "require_relative" {
					specs = append(specs, "rel:"+path)
				} else {
					specs = append(specs, "abs:"+path)
				}
			}) {
				return specs
			}
		}
	}
	lines, err := readHeadLines(fullPath, 100)
	if err != nil {
		return nil
	}
	return rubyRegexSpecs(lines)
}

// resolveRubyImports is the line-based entry point kept for tests; it extracts
// specifiers with regex and resolves them.
func resolveRubyImports(lines []string, filePath string, idx *FileIndex) []string {
	return resolveRubySpecs(rubyRegexSpecs(lines), filePath, idx, nil)
}

// rubyRegexSpecs is the regex fallback extractor for Ruby. Specifiers are tagged
// with their directive: "rel:<path>" for require_relative, "abs:<path>" for
// require — the resolver needs the distinction.
func rubyRegexSpecs(lines []string) []string {
	var specs []string
	for _, line := range lines {
		if m := rbRequireRelative.FindStringSubmatch(line); m != nil {
			specs = append(specs, "rel:"+m[1])
			continue
		}
		if m := rbRequire.FindStringSubmatch(line); m != nil {
			specs = append(specs, "abs:"+m[1])
		}
	}
	return specs
}

// resolveRubySpecs resolves tagged Ruby require/require_relative specifiers.
func resolveRubySpecs(specs []string, filePath string, idx *FileIndex, t *importTally) []string {
	t.extract(len(specs))

	dir := filepath.Dir(filePath)
	seen := make(map[string]bool)
	var resolved []string

	for _, spec := range specs {
		kind, imp := splitSpec(spec, "abs")

		if kind == "rel" {
			// require_relative — resolve relative to the current file's directory
			target := filepath.Clean(filepath.Join(dir, imp))
			if !strings.HasSuffix(target, ".rb") {
				target += ".rb"
			}
			if !idx.Exists(target) {
				t.miss(imp)
				continue
			}
			t.hit()
			if !seen[target] {
				seen[target] = true
				resolved = append(resolved, target)
			}
			continue
		}

		// require — try common Ruby source roots.
		// Skip gem-like requires: no "/" or "." and no local file match.
		if !strings.Contains(imp, "/") && !strings.Contains(imp, ".") {
			found := false
			for _, root := range []string{"lib/", "app/", "src/"} {
				if idx.Exists(root + imp + ".rb") {
					found = true
					break
				}
			}
			if !found {
				t.skip()
				continue
			}
		}

		hit := false
		for _, root := range []string{"lib/", "app/", "src/", ""} {
			candidate := filepath.Clean(root + imp)
			if !strings.HasSuffix(candidate, ".rb") {
				candidate += ".rb"
			}
			if !idx.Exists(candidate) {
				continue
			}
			hit = true
			if !seen[candidate] {
				seen[candidate] = true
				resolved = append(resolved, candidate)
			}
			break
		}
		if hit {
			t.hit()
		} else {
			t.skip() // a gem, not a local file
		}
	}
	return resolved
}

// ─── Rust ─────────────────────────────────────────────────────────────────────

// rustImportSpecs returns tagged Rust import specifiers, preferring tree-sitter
// and falling back to regex.
func rustImportSpecs(fullPath string) []string {
	if hasTSImports("rust") {
		if data, err := os.ReadFile(fullPath); err == nil {
			var specs []string
			if tsImportEachMatch(data, "rust", func(caps map[string]string) {
				if u := caps["use"]; u != "" {
					specs = append(specs, "use:"+u)
				}
				if md := caps["mod"]; md != "" {
					specs = append(specs, "mod:"+md)
				}
			}) {
				return specs
			}
		}
	}
	lines, err := readHeadLines(fullPath, 150)
	if err != nil {
		return nil
	}
	return rustRegexSpecs(lines)
}

// resolveRustImports is the line-based entry point kept for tests.
func resolveRustImports(lines []string, filePath string, idx *FileIndex) []string {
	return resolveRustSpecs(rustRegexSpecs(lines), filePath, idx, nil)
}

// rustRegexSpecs is the regex fallback extractor for Rust. Specifiers are tagged
// "use:<path>" (use crate::/self::/super:: paths) or "mod:<name>" (mod decls).
func rustRegexSpecs(lines []string) []string {
	var specs []string
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if m := rsModDecl.FindStringSubmatch(t); m != nil {
			specs = append(specs, "mod:"+m[1])
			continue
		}
		if m := rsUseStmt.FindStringSubmatch(t); m != nil {
			specs = append(specs, "use:"+m[1])
		}
	}
	return specs
}

// rustModuleDirs returns the directory that holds the submodules of the module
// defined by filePath, and the directory that holds its siblings (its parent
// module's submodules).
//
// Only mod.rs (and the crate roots lib.rs/main.rs) stand for the *directory*
// they live in. Any other file src/models/user.rs defines module models::user,
// whose parent (models) owns src/models — so `super::` there must resolve in
// src/models, not one level above it.
func rustModuleDirs(filePath string) (self, parent string) {
	dir := filepath.Dir(filePath)
	switch filepath.Base(filePath) {
	case "mod.rs", "lib.rs", "main.rs":
		return dir, filepath.Dir(dir)
	default:
		stem := strings.TrimSuffix(filepath.Base(filePath), ".rs")
		return filepath.Join(dir, stem), dir
	}
}

// resolveRustSpecs resolves tagged Rust use-paths and mod declarations.
func resolveRustSpecs(specs []string, filePath string, idx *FileIndex, t *importTally) []string {
	t.extract(len(specs))

	dir := filepath.Dir(filePath)
	selfDir, parentDir := rustModuleDirs(filePath)
	seen := make(map[string]bool)
	var resolved []string

	// Find the crate root (the enclosing src/ directory).
	crateRoot := ""
	parts := strings.Split(filepath.ToSlash(filePath), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "src" {
			crateRoot = strings.Join(parts[:i+1], "/")
			break
		}
	}

	// resolveUnder tries {base}/{modPath}.rs, {base}/{modPath}/mod.rs, and the
	// parent module file (the imported item may be defined in the parent module).
	resolveUnder := func(base string, modParts []string) bool {
		modPath := strings.Join(modParts, "/")
		candidates := []string{
			filepath.Join(base, modPath+".rs"),
			filepath.Join(base, modPath, "mod.rs"),
		}
		if len(modParts) > 1 {
			parentPath := strings.Join(modParts[:len(modParts)-1], "/")
			candidates = append(candidates, filepath.Join(base, parentPath+".rs"))
		}
		for _, candidate := range candidates {
			if !idx.Exists(candidate) || candidate == filePath {
				continue
			}
			if !seen[candidate] {
				seen[candidate] = true
				resolved = append(resolved, candidate)
			}
			return true
		}
		return false
	}

	for _, spec := range specs {
		kind, imp := splitSpec(spec, "use")

		if kind == "mod" {
			// mod child; -> {dir}/child.rs or {dir}/child/mod.rs
			if imp == "tests" || imp == "test" {
				t.skip()
				continue
			}
			hit := false
			for _, candidate := range []string{
				filepath.Join(dir, imp+".rs"),
				filepath.Join(dir, imp, "mod.rs"),
			} {
				if !idx.Exists(candidate) {
					continue
				}
				hit = true
				if !seen[candidate] {
					seen[candidate] = true
					resolved = append(resolved, candidate)
				}
				break
			}
			if hit {
				t.hit()
			} else {
				// An inline `mod name { ... }` block has no file of its own.
				t.skip()
			}
			continue
		}

		// use crate::a::b / use self::x / use super::x
		segments := strings.Split(imp, "::")
		if len(segments) < 2 {
			t.skip()
			continue
		}
		modParts := segments[1:]
		hit := false
		switch segments[0] {
		case "crate":
			if crateRoot != "" {
				hit = resolveUnder(crateRoot, modParts)
			}
		case "super":
			hit = resolveUnder(parentDir, modParts)
		case "self":
			hit = resolveUnder(selfDir, modParts)
		default:
			t.skip()
			continue
		}
		if hit {
			t.hit()
		} else {
			// The item may live inline in a module file we already link, so this
			// is a genuine drop worth reporting rather than an external crate.
			t.miss(imp)
		}
	}
	return resolved
}

// ─── Python ───────────────────────────────────────────────────────────────────

// pythonImportSpecs returns tagged Python import specifiers.
//
//	mod:<module>          the module of a from-import (relative or absolute)
//	from:<module>|<name>  an imported name, which may itself be a submodule
//	imp:<module>          a plain `import a.b.c`
//
// The names matter: `from . import mod_a` names no module at all in its
// module_name, so without the name there is nothing to resolve.
func pythonImportSpecs(fullPath string) []string {
	if data, err := os.ReadFile(fullPath); err == nil {
		if specs, ok := pythonSpecsFromSource(data); ok {
			return specs
		}
	}
	lines, err := readHeadLines(fullPath, 150)
	if err != nil {
		return nil
	}
	return pyRegexSpecs(lines)
}

// pythonSpecsFromSource is the tree-sitter half of pythonImportSpecs.
func pythonSpecsFromSource(data []byte) ([]string, bool) {
	if !hasTSImports("python") {
		return nil, false
	}
	var specs []string
	seen := make(map[string]bool)
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			specs = append(specs, s)
		}
	}
	ok := tsImportEachMatch(data, "python", func(caps map[string]string) {
		if p := caps["plain"]; p != "" {
			add("imp:" + p)
			return
		}
		mod := caps["mod"]
		if mod == "" {
			return
		}
		add("mod:" + mod)
		if n := caps["name"]; n != "" {
			add("from:" + mod + "|" + n)
		}
	})
	return specs, ok
}

// pyRegexSpecs is the regex fallback extractor for Python imports.
func pyRegexSpecs(lines []string) []string {
	var specs []string
	seen := make(map[string]bool)
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			specs = append(specs, s)
		}
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if m := pyImportFrom.FindStringSubmatch(line); m != nil {
			mod := m[1]
			add("mod:" + mod)
			for _, name := range pySplitNames(m[2]) {
				add("from:" + mod + "|" + name)
			}
			continue
		}
		if m := pyImport.FindStringSubmatch(line); m != nil {
			for _, name := range pySplitNames(m[1]) {
				add("imp:" + name)
			}
		}
	}
	return specs
}

// pySplitNames splits an import name list ("a, b as c, (d, e)") into names,
// dropping aliases and wildcards.
func pySplitNames(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimSuffix(s, ")")
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if i := strings.Index(part, " as "); i >= 0 {
			part = strings.TrimSpace(part[:i])
		}
		if part == "" || part == "*" || part == "\\" {
			continue
		}
		out = append(out, part)
	}
	return out
}

// pythonRoots returns the directories absolute Python imports resolve against:
// the repo root plus every detected package root (the first directory above a
// package that is not itself a package — which is exactly the src/ of a
// src-layout project).
func (rc *resolveCtx) pythonRoots() []string {
	rc.pyOnce.Do(func() {
		pkgDirs := make(map[string]bool)
		hasSrc := false
		for _, f := range rc.idx.All() {
			if filepath.Base(f.RelPath) == "__init__.py" {
				d := filepath.Dir(f.RelPath)
				if d == "." {
					d = ""
				}
				pkgDirs[d] = true
			}
			if f.Lang == "python" && strings.HasPrefix(f.RelPath, "src/") {
				hasSrc = true
			}
		}

		roots := map[string]bool{"": true}
		if hasSrc {
			// PEP 420 namespace packages have no __init__.py to walk up from.
			roots["src"] = true
		}
		for d := range pkgDirs {
			if d == "" {
				continue
			}
			parent := filepath.Dir(d)
			if parent == "." {
				parent = ""
			}
			if !pkgDirs[parent] {
				roots[parent] = true
			}
		}
		for r := range roots {
			rc.pyRoots = append(rc.pyRoots, r)
		}
		// Deepest root first: a more specific source root wins.
		sort.Slice(rc.pyRoots, func(i, j int) bool {
			if len(rc.pyRoots[i]) != len(rc.pyRoots[j]) {
				return len(rc.pyRoots[i]) > len(rc.pyRoots[j])
			}
			return rc.pyRoots[i] < rc.pyRoots[j]
		})
	})
	return rc.pyRoots
}

// pyModuleFile maps an absolute dotted module to a local file, or "" when no
// file backs it. It only ever returns a path that actually exists, so a
// third-party import cannot be turned into an edge by guessing.
func (rc *resolveCtx) pyModuleFile(module string) string {
	rel := strings.ReplaceAll(module, ".", "/")
	for _, root := range rc.pythonRoots() {
		base := filepath.Join(root, rel)
		if rc.idx.Exists(base + ".py") {
			return base + ".py"
		}
		if p := filepath.Join(base, "__init__.py"); rc.idx.Exists(p) {
			return p
		}
	}
	return ""
}

// pyTopLevelIsLocal reports whether the first segment of an absolute import
// names a package that exists in this repo. It is what separates "we should
// have resolved this" (unresolved) from "that's a third-party package"
// (external) without needing a hardcoded stdlib list.
func (rc *resolveCtx) pyTopLevelIsLocal(module string) bool {
	top, _, _ := strings.Cut(module, ".")
	if top == "" {
		return false
	}
	for _, root := range rc.pythonRoots() {
		base := filepath.Join(root, top)
		if rc.idx.Exists(base+".py") || rc.idx.Exists(filepath.Join(base, "__init__.py")) {
			return true
		}
		if len(rc.idx.ByDir(base)) > 0 {
			return true
		}
	}
	return false
}

// pyResolveModule resolves a relative or absolute Python module to a file.
func (rc *resolveCtx) pyResolveModule(dir, module string) string {
	if !strings.HasPrefix(module, ".") {
		return rc.pyModuleFile(module)
	}

	dots := 0
	for dots < len(module) && module[dots] == '.' {
		dots++
	}
	rest := module[dots:]

	targetDir := dir
	for i := 1; i < dots; i++ {
		targetDir = filepath.Dir(targetDir)
	}
	if targetDir == "." {
		targetDir = ""
	}

	if rest == "" {
		// `from . import x` — the package itself is the module.
		if p := filepath.Join(targetDir, "__init__.py"); rc.idx.Exists(p) {
			return p
		}
		return ""
	}

	base := filepath.Join(targetDir, strings.ReplaceAll(rest, ".", "/"))
	if rc.idx.Exists(base + ".py") {
		return base + ".py"
	}
	if p := filepath.Join(base, "__init__.py"); rc.idx.Exists(p) {
		return p
	}
	return ""
}

// resolvePySpecs resolves Python import specifiers to local files. Both
// relative and absolute imports are resolved; absolute imports only ever match
// a file that exists under a detected source root, so third-party packages
// cannot be fabricated into edges.
func resolvePySpecs(specs []string, filePath string, rc *resolveCtx, t *importTally) []string {
	dir := filepath.Dir(filePath)
	seen := make(map[string]bool)
	var resolved []string
	modules := 0

	add := func(p string) {
		if p == "" || p == filePath || seen[p] {
			return
		}
		seen[p] = true
		resolved = append(resolved, p)
	}

	for _, spec := range specs {
		kind, val := splitSpec(spec, "mod")
		switch kind {
		case "mod", "imp":
			modules++
			target := rc.pyResolveModule(dir, val)
			if target == "" {
				if strings.HasPrefix(val, ".") || rc.pyTopLevelIsLocal(val) {
					t.miss(val)
				} else {
					t.skip()
				}
				continue
			}
			t.hit()
			add(target)
		case "from":
			// An imported name may itself be a submodule; if it is not, the
			// module edge already covers it, so a miss here is not counted.
			mod, name, _ := strings.Cut(val, "|")
			combined := mod + "." + name
			if strings.HasSuffix(mod, ".") {
				combined = mod + name
			}
			add(rc.pyResolveModule(dir, combined))
		}
	}
	t.extract(modules)
	return resolved
}

// resolvePyRelative maps a relative from-import to a module file path. Kept as
// the narrow, directly testable core of relative resolution.
func resolvePyRelative(dir, imp string) string {
	dots := 0
	for _, c := range imp {
		if c == '.' {
			dots++
		} else {
			break
		}
	}
	module := imp[dots:]

	targetDir := dir
	for i := 1; i < dots; i++ {
		targetDir = filepath.Dir(targetDir)
	}

	if module == "" {
		return ""
	}

	relPath := strings.ReplaceAll(module, ".", "/")
	return filepath.Join(targetDir, relPath) + ".py"
}

// ─── Java / Kotlin ────────────────────────────────────────────────────────────

// jvmImportSpecs returns tagged Java/Kotlin import specifiers:
// "imp:<dotted>" for a normal import, "star:<package>" for an on-demand
// (wildcard) import.
func jvmImportSpecs(fullPath, lang string) []string {
	if data, err := os.ReadFile(fullPath); err == nil {
		if specs, ok := jvmSpecsFromSource(data, lang); ok {
			return specs
		}
	}
	lines, err := readHeadLines(fullPath, 100)
	if err != nil {
		return nil
	}
	if lang == "kotlin" {
		return kotlinRegexSpecs(lines)
	}
	return javaRegexSpecs(lines)
}

// jvmSpecsFromSource is the tree-sitter half of jvmImportSpecs.
func jvmSpecsFromSource(data []byte, lang string) ([]string, bool) {
	if !hasTSImports(lang) {
		return nil, false
	}
	var plain, stars []string
	ok := tsImportEachMatch(data, lang, func(caps map[string]string) {
		p := caps["path"]
		if p == "" {
			return
		}
		// Java marks the wildcard with an (asterisk) node; the Kotlin grammar
		// drops it, so the statement text is the only signal.
		if caps["star"] != "" || strings.HasSuffix(strings.TrimSpace(caps["stmt"]), ".*") {
			stars = append(stars, p)
			return
		}
		plain = append(plain, p)
	})
	if !ok {
		return nil, false
	}
	return mergeJVMSpecs(plain, stars), true
}

// mergeJVMSpecs tags the two specifier kinds and drops the plain form of any
// import that also matched as a wildcard (both query patterns match a
// wildcard import).
func mergeJVMSpecs(plain, stars []string) []string {
	isStar := make(map[string]bool, len(stars))
	var specs []string
	seen := make(map[string]bool)
	for _, s := range stars {
		isStar[s] = true
		if !seen["star:"+s] {
			seen["star:"+s] = true
			specs = append(specs, "star:"+s)
		}
	}
	for _, p := range plain {
		if isStar[p] || seen["imp:"+p] {
			continue
		}
		seen["imp:"+p] = true
		specs = append(specs, "imp:"+p)
	}
	return specs
}

// javaRegexSpecs is the regex fallback extractor for Java import specifiers.
func javaRegexSpecs(lines []string) []string {
	return javaLikeRegexSpecs(lines, javaImportRe)
}

// kotlinRegexSpecs is the regex fallback extractor for Kotlin import specifiers.
func kotlinRegexSpecs(lines []string) []string {
	return javaLikeRegexSpecs(lines, kotlinImportRe)
}

func javaLikeRegexSpecs(lines []string, re *regexp.Regexp) []string {
	var specs []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if len(m) > 2 && m[2] != "" {
			specs = append(specs, "star:"+m[1])
			continue
		}
		specs = append(specs, "imp:"+m[1])
	}
	return specs
}

// jvmIndex maps Java/Kotlin packages and fully-qualified top-level declarations
// to the files that declare them, read from the files themselves.
//
// The path convention (com.example.User → .../com/example/User.java) only holds
// for Java's one-public-type-per-file rule. Kotlin routinely declares several
// types (and top-level functions) in one file, so convention alone drops those
// imports entirely.
func (rc *resolveCtx) jvmIndex() (pkgFiles, declFiles map[string][]string) {
	rc.jvmOnce.Do(func() {
		rc.jvmPkg = make(map[string][]string)
		rc.jvmDecl = make(map[string][]string)

		type jvmFacts struct {
			pkg   string
			decls []string
		}
		scanned := scanLangFilesParallel(rc, []string{"java", "kotlin"}, func(_ *scan.FileEntry, data []byte) jvmFacts {
			src := string(data)
			var facts jvmFacts
			if m := jvmPackageRe.FindStringSubmatch(src); m != nil {
				facts.pkg = m[1]
			}
			for _, m := range jvmDeclRe.FindAllStringSubmatch(src, -1) {
				facts.decls = append(facts.decls, m[1])
			}
			return facts
		})

		for _, s := range scanned {
			if s.file == nil {
				continue
			}
			rc.jvmPkg[s.val.pkg] = append(rc.jvmPkg[s.val.pkg], s.file.RelPath)
			for _, name := range s.val.decls {
				key := name
				if s.val.pkg != "" {
					key = s.val.pkg + "." + name
				}
				rc.jvmDecl[key] = append(rc.jvmDecl[key], s.file.RelPath)
			}
		}
	})
	return rc.jvmPkg, rc.jvmDecl
}

// jvmSourceRoots are the conventional source-root prefixes a dotted name may
// live under (plus "" for a repo whose packages start at the root).
var jvmSourceRoots = []string{
	"src/main/java/",
	"src/main/kotlin/",
	"src/",
	"app/src/main/java/",
	"app/src/main/kotlin/",
	"",
}

// resolveJavaSpecs resolves Java/Kotlin imports to local files.
func resolveJavaSpecs(specs []string, filePath string, lang string, rc *resolveCtx, t *importTally) []string {
	t.extract(len(specs))

	seen := make(map[string]bool)
	var resolved []string
	add := func(p string) bool {
		if p == "" || p == filePath {
			return false
		}
		if !seen[p] {
			seen[p] = true
			resolved = append(resolved, p)
		}
		return true
	}

	for _, spec := range specs {
		kind, imp := splitSpec(spec, "imp")

		// Skip standard library imports
		if strings.HasPrefix(imp, "java.") || strings.HasPrefix(imp, "javax.") ||
			strings.HasPrefix(imp, "kotlin.") || strings.HasPrefix(imp, "kotlinx.") ||
			strings.HasPrefix(imp, "android.") {
			t.skip()
			continue
		}

		if kind == "star" {
			if resolveJVMPackage(imp, rc, add) {
				t.hit()
			} else {
				t.miss(imp)
			}
			continue
		}

		if resolveJVMType(imp, rc, add) {
			t.hit()
			continue
		}
		// A static / member import names a member of the type before it.
		parts := strings.Split(imp, ".")
		if len(parts) > 1 {
			last := parts[len(parts)-1]
			if len(last) > 0 && last[0] >= 'a' && last[0] <= 'z' {
				if resolveJVMType(strings.Join(parts[:len(parts)-1], "."), rc, add) {
					t.hit()
					continue
				}
			}
		}
		t.miss(imp)
	}
	return resolved
}

// resolveJVMType resolves one fully-qualified type name, first by the file-path
// convention and then by the declarations actually found in the sources.
func resolveJVMType(imp string, rc *resolveCtx, add func(string) bool) bool {
	classPath := strings.ReplaceAll(imp, ".", "/")
	found := false
	for _, root := range jvmSourceRoots {
		for _, ext := range []string{".java", ".kt"} {
			candidate := root + classPath + ext
			if rc.idx.Exists(candidate) {
				found = add(candidate) || found
			}
		}
	}
	if found {
		return true
	}
	_, declFiles := rc.jvmIndex()
	for _, p := range declFiles[imp] {
		found = add(p) || found
	}
	return found
}

// resolveJVMPackage resolves an on-demand import to every file in the package.
func resolveJVMPackage(pkg string, rc *resolveCtx, add func(string) bool) bool {
	found := false
	pkgFiles, _ := rc.jvmIndex()
	for _, p := range pkgFiles[pkg] {
		found = add(p) || found
	}
	if found {
		return true
	}
	// Fall back to the directory convention for files we could not read.
	dirPath := strings.ReplaceAll(pkg, ".", "/")
	for _, root := range jvmSourceRoots {
		for _, f := range rc.idx.ByDir(root + dirPath) {
			if f.Lang == "java" || f.Lang == "kotlin" {
				found = add(f.RelPath) || found
			}
		}
	}
	return found
}

// ─── C# ───────────────────────────────────────────────────────────────────────

// csharpImportSpecs returns tagged C# specifiers: "using:<namespace>" for a
// using directive and "ns:<namespace>" for a namespace the file itself declares.
func csharpImportSpecs(fullPath string) []string {
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil
	}
	return csharpSpecsFromData(data)
}

// csharpSpecsFromData extracts C# specifiers from source bytes, falling back to
// the line regex when the grammar is unavailable or the parse fails.
func csharpSpecsFromData(data []byte) []string {
	if specs, ok := csharpSpecsFromSource(data); ok {
		return specs
	}
	return csharpRegexSpecs(strings.Split(string(data), "\n"))
}

// csharpSpecsFromSource is the tree-sitter half of csharpImportSpecs.
func csharpSpecsFromSource(data []byte) ([]string, bool) {
	if !hasTSImports("csharp") {
		return nil, false
	}
	var specs []string
	seen := make(map[string]bool)
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			specs = append(specs, s)
		}
	}
	ok := tsImportEachMatch(data, "csharp", func(caps map[string]string) {
		if ns := caps["ns"]; ns != "" {
			add("ns:" + ns)
		}
		if p := caps["path"]; p != "" {
			add("using:" + p)
		}
	})
	return specs, ok
}

// csharpRegexSpecs is the regex fallback extractor for C#.
func csharpRegexSpecs(lines []string) []string {
	var specs []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if m := csUsing.FindStringSubmatch(trimmed); m != nil {
			specs = append(specs, "using:"+m[1])
			continue
		}
		if m := csNamespaceRe.FindStringSubmatch(line); m != nil {
			specs = append(specs, "ns:"+m[1])
		}
	}
	return specs
}

// csharpNamespaces maps each declared namespace to the files declaring it.
func (rc *resolveCtx) csharpNamespaces() map[string][]string {
	rc.csOnce.Do(func() {
		rc.csNS = make(map[string][]string)
		scanned := scanLangFilesParallel(rc, []string{"csharp"}, func(_ *scan.FileEntry, data []byte) []string {
			var namespaces []string
			for _, spec := range csharpSpecsFromData(data) {
				if kind, ns := splitSpec(spec, ""); kind == "ns" && ns != "" {
					namespaces = append(namespaces, ns)
				}
			}
			return namespaces
		})
		for _, s := range scanned {
			if s.file == nil {
				continue
			}
			for _, ns := range s.val {
				rc.csNS[ns] = append(rc.csNS[ns], s.file.RelPath)
			}
		}
	})
	return rc.csNS
}

// resolveCSharpSpecs resolves C# using directives against the namespaces the
// source files actually declare.
//
// The previous heuristic matched the last 1–3 lowercased namespace segments
// against *directory names* and never read a namespace declaration, so
// `using MyApp.Models;` linked every file in any directory ending in "models".
// A fabricated edge is worse than a missing one — it inflates fan-in and
// corrupts hotspot ranking — so anything we cannot pin to a declared namespace
// is now reported unresolved instead of guessed.
func resolveCSharpSpecs(specs []string, filePath string, rc *resolveCtx, t *importTally) []string {
	nsMap := rc.csharpNamespaces()
	seen := make(map[string]bool)
	var resolved []string
	usings := 0

	add := func(p string) bool {
		if p == "" || p == filePath {
			return false
		}
		if !seen[p] {
			seen[p] = true
			resolved = append(resolved, p)
		}
		return true
	}

	for _, spec := range specs {
		kind, ns := splitSpec(spec, "using")
		if kind != "using" {
			continue // the file's own namespace declaration
		}
		usings++

		// Skip system/framework namespaces
		if strings.HasPrefix(ns, "System") || strings.HasPrefix(ns, "Microsoft") ||
			strings.HasPrefix(ns, "NuGet") {
			t.skip()
			continue
		}

		found := false
		for _, p := range nsMap[ns] {
			found = add(p) || found
		}
		if found {
			t.hit()
			continue
		}

		// `using static Some.Namespace.Type;` names a type, not a namespace.
		// Accept it only on an exact file-name match inside the parent
		// namespace — never on a fuzzy directory suffix.
		if i := strings.LastIndexByte(ns, '.'); i > 0 {
			parent, typeName := ns[:i], ns[i+1:]
			for _, p := range nsMap[parent] {
				if strings.TrimSuffix(filepath.Base(p), ".cs") == typeName {
					found = add(p) || found
				}
			}
		}
		if found {
			t.hit()
		} else {
			t.miss(ns)
		}
	}
	t.extract(usings)
	return resolved
}

// ─── Zig / Lua / Julia / Shell ────────────────────────────────────────────────

// resolveZigSpecs resolves Zig @import("path.zig") specifiers (relative to the
// importing file). Non-file imports like "std"/"builtin" are skipped.
func resolveZigSpecs(specs []string, filePath string, idx *FileIndex, t *importTally) []string {
	t.extract(len(specs))
	dir := filepath.Dir(filePath)
	seen := make(map[string]bool)
	var resolved []string
	for _, imp := range specs {
		if !strings.HasSuffix(imp, ".zig") {
			t.skip()
			continue
		}
		target := filepath.Clean(filepath.Join(dir, imp))
		if !idx.Exists(target) {
			t.miss(imp)
			continue
		}
		t.hit()
		if target != filePath && !seen[target] {
			seen[target] = true
			resolved = append(resolved, target)
		}
	}
	return resolved
}

// resolveLuaSpecs resolves Lua require("a.b.c") specifiers. Lua module names map
// to paths with dots as separators; we try common source roots and relative.
func resolveLuaSpecs(specs []string, filePath string, idx *FileIndex, t *importTally) []string {
	t.extract(len(specs))
	dir := filepath.Dir(filePath)
	seen := make(map[string]bool)
	var resolved []string
	for _, imp := range specs {
		rel := strings.ReplaceAll(imp, ".", "/") + ".lua"
		candidates := []string{filepath.Clean(filepath.Join(dir, rel))}
		for _, root := range []string{"", "src/", "lua/", "lib/"} {
			candidates = append(candidates, filepath.Clean(root+rel))
		}
		found := false
		for _, p := range candidates {
			if !idx.Exists(p) {
				continue
			}
			found = true
			if p != filePath && !seen[p] {
				seen[p] = true
				resolved = append(resolved, p)
			}
		}
		if found {
			t.hit()
		} else {
			t.skip() // a rock / stdlib module
		}
	}
	return resolved
}

// parseJuliaInclude turns the raw text of an include(...) argument list into a
// path relative to the including file.
//
// A bare literal and the joinpath(@__DIR__, "…") idiom are both extremely
// common; anything else (a variable, an interpolated string) is reported
// unresolvable rather than guessed at.
func parseJuliaInclude(arg string) (string, bool) {
	s := strings.TrimSpace(arg)
	if !strings.HasPrefix(s, "(") {
		return s, s != "" // already a plain path (regex/legacy callers)
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "("), ")")

	lits := juliaStringRe.FindAllStringSubmatch(s, -1)
	if len(lits) == 0 {
		return "", false
	}
	rest := juliaStringRe.ReplaceAllString(s, "")
	for _, tok := range []string{"joinpath", "dirname", "@__DIR__", "@__FILE__", "(", ")", ",", " ", "\t", "\n", "\r"} {
		rest = strings.ReplaceAll(rest, tok, "")
	}
	if rest != "" {
		return "", false
	}

	parts := make([]string, 0, len(lits))
	for _, m := range lits {
		if strings.Contains(m[1], "$") {
			return "", false
		}
		parts = append(parts, m[1])
	}
	return filepath.Join(parts...), true
}

// resolveJuliaSpecs resolves Julia include(...) paths (relative to the including
// file). using/import target packages, not local files.
func resolveJuliaSpecs(specs []string, filePath string, idx *FileIndex, t *importTally) []string {
	t.extract(len(specs))
	dir := filepath.Dir(filePath)
	seen := make(map[string]bool)
	var resolved []string
	for _, spec := range specs {
		imp, ok := parseJuliaInclude(spec)
		if !ok {
			t.miss(spec)
			continue
		}
		target := filepath.Clean(filepath.Join(dir, imp))
		if !idx.Exists(target) {
			t.miss(imp)
			continue
		}
		t.hit()
		if target != filePath && !seen[target] {
			seen[target] = true
			resolved = append(resolved, target)
		}
	}
	return resolved
}

// shellSpecPath strips the quoting from a captured `source` argument. The
// argument is captured whole (not just its string_content) precisely so the
// variable expansions stay visible: tree-sitter splits `"$LIB/util.sh"` into an
// expansion plus the literal "/util.sh", and resolving that remainder relative
// to the script invents an edge to a same-named file in the wrong directory.
func shellSpecPath(spec string) string {
	s := strings.TrimSpace(spec)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		s = s[1 : len(s)-1]
	}
	return s
}

// resolveShellSpecs resolves shell `source path` / `. path` includes (relative
// to the sourcing script). Specifiers containing shell expansions are skipped.
func resolveShellSpecs(specs []string, filePath string, idx *FileIndex, t *importTally) []string {
	t.extract(len(specs))
	dir := filepath.Dir(filePath)
	seen := make(map[string]bool)
	var resolved []string
	for _, spec := range specs {
		imp := shellSpecPath(spec)
		if imp == "" {
			continue
		}
		if strings.ContainsAny(imp, "$*?`") {
			// The path is computed at run time; we cannot know it.
			t.miss(imp)
			continue
		}
		target := filepath.Clean(filepath.Join(dir, imp))
		if !idx.Exists(target) {
			t.miss(imp)
			continue
		}
		t.hit()
		if target != filePath && !seen[target] {
			seen[target] = true
			resolved = append(resolved, target)
		}
	}
	return resolved
}

// ─── PHP ──────────────────────────────────────────────────────────────────────

// phpRegexSpecs is the regex fallback extractor for PHP use statements. It
// returns FQCNs (backslash-separated, e.g. App\Models\User).
func phpRegexSpecs(lines []string) []string {
	var specs []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if m := phpUseStmt.FindStringSubmatch(line); m != nil {
			specs = append(specs, m[1])
		}
	}
	return specs
}

// resolvePHPSpecs resolves PHP use FQCNs to local file paths.
// It parses PSR-4 namespace imports and maps them to files using composer.json
// autoload config when available, falling back to common directory conventions.
func resolvePHPSpecs(specs []string, filePath string, rc *resolveCtx, t *importTally) []string {
	t.extract(len(specs))

	seen := make(map[string]bool)
	var resolved []string
	psr4Map := rc.phpPSR4Map()

	add := func(p string) bool {
		if p == "" || p == filePath {
			return false
		}
		if !seen[p] {
			seen[p] = true
			resolved = append(resolved, p)
		}
		return true
	}

	for _, fqcn := range specs {
		// Skip PHP built-in namespaces that won't resolve to local files
		if isPHPBuiltinNamespace(fqcn) {
			t.skip()
			continue
		}

		// Convert backslashes to forward slashes for path resolution
		classPath := strings.ReplaceAll(fqcn, "\\", "/") + ".php"
		found := false

		// Strategy 1: Try composer.json PSR-4 mappings
		for prefix, dirs := range psr4Map {
			nsPrefix := strings.ReplaceAll(prefix, "\\", "/")
			if !strings.HasPrefix(classPath, nsPrefix) {
				continue
			}
			remainder := strings.TrimPrefix(classPath, nsPrefix)
			for _, dir := range dirs {
				candidate := filepath.Clean(filepath.Join(dir, remainder))
				if rc.idx.Exists(candidate) {
					found = add(candidate) || found
				}
			}
		}

		// Strategy 2: Try direct path (namespace mirrors directory structure)
		if rc.idx.Exists(classPath) {
			found = add(classPath) || found
		}

		// Strategy 3: Strip first namespace segment and try common root prefixes
		// e.g., App\Models\User → Models/User.php under src/, app/, lib/
		parts := strings.SplitN(fqcn, "\\", 2)
		if len(parts) == 2 {
			remainder := strings.ReplaceAll(parts[1], "\\", "/") + ".php"
			for _, prefix := range []string{"src/", "app/", "lib/", ""} {
				candidate := filepath.Clean(prefix + remainder)
				if rc.idx.Exists(candidate) {
					found = add(candidate) || found
				}
			}
		}

		if found {
			t.hit()
		} else {
			t.miss(fqcn)
		}
	}
	return resolved
}

// phpPSR4Map reads composer.json and extracts PSR-4 autoload mappings once.
func (rc *resolveCtx) phpPSR4Map() map[string][]string {
	rc.phpOnce.Do(func() {
		rc.phpPSR4 = parsePHPComposerPSR4(rc.root)
	})
	return rc.phpPSR4
}

// parsePHPComposerPSR4 reads composer.json and extracts PSR-4 autoload mappings.
// Returns a map from namespace prefix to directory paths.
func parsePHPComposerPSR4(root string) map[string][]string {
	data, err := os.ReadFile(filepath.Join(root, "composer.json"))
	if err != nil {
		return nil
	}

	var composer struct {
		Autoload struct {
			PSR4 map[string]json.RawMessage `json:"psr-4"`
		} `json:"autoload"`
	}
	if err := json.Unmarshal(data, &composer); err != nil {
		return nil
	}

	result := make(map[string][]string)
	for ns, raw := range composer.Autoload.PSR4 {
		// PSR-4 values can be a string or array of strings
		var single string
		if err := json.Unmarshal(raw, &single); err == nil {
			result[ns] = []string{single}
			continue
		}
		var multiple []string
		if err := json.Unmarshal(raw, &multiple); err == nil {
			result[ns] = multiple
		}
	}
	return result
}

// isPHPBuiltinNamespace returns true for PHP standard/extension namespaces
// that won't resolve to local project files.
func isPHPBuiltinNamespace(fqcn string) bool {
	builtins := []string{
		"Psr\\",
		"Symfony\\",
		"Illuminate\\",
		"Doctrine\\",
		"PHPUnit\\",
		"GuzzleHttp\\",
		"Monolog\\",
		"Carbon\\",
		"Ramsey\\",
		"Faker\\",
		"League\\",
		"Composer\\",
	}
	for _, prefix := range builtins {
		if strings.HasPrefix(fqcn, prefix) {
			return true
		}
	}
	return false
}

// ─── Dart ─────────────────────────────────────────────────────────────────────

// resolveDartImports resolves Dart import/export statements to local file paths.
// Dart imports use either:
//   - 'package:myapp/models/user.dart' → maps to lib/models/user.dart
//   - 'relative/path.dart' → relative to current file
//   - 'dart:core' → SDK, skipped
func resolveDartImports(lines []string, filePath string, root string, idx *FileIndex, t *importTally) []string {
	dir := filepath.Dir(filePath)
	pkgName := detectDartPackageName(root)

	seen := make(map[string]bool)
	var resolved []string
	count := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		m := dartImportRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		imp := m[1]
		count++

		// Skip SDK imports
		if strings.HasPrefix(imp, "dart:") {
			t.skip()
			continue
		}

		var target string
		if strings.HasPrefix(imp, "package:") {
			// package:myapp/models/user.dart → lib/models/user.dart
			pkgPath := strings.TrimPrefix(imp, "package:")
			slash := strings.IndexByte(pkgPath, '/')
			if slash < 0 {
				t.skip()
				continue
			}
			pkg := pkgPath[:slash]
			rest := pkgPath[slash+1:]
			// Only resolve imports from our own package
			if pkgName != "" && pkg != pkgName {
				t.skip()
				continue
			}
			target = filepath.Join("lib", rest)
		} else {
			// Relative import
			target = filepath.Clean(filepath.Join(dir, imp))
		}

		if target == "" || !idx.Exists(target) {
			t.miss(imp)
			continue
		}
		t.hit()
		if target != filePath && !seen[target] {
			seen[target] = true
			resolved = append(resolved, target)
		}
	}
	t.extract(count)
	return resolved
}

// detectDartPackageName reads the package name from pubspec.yaml.
func detectDartPackageName(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "pubspec.yaml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		}
	}
	return ""
}

// ─── Scala ────────────────────────────────────────────────────────────────────

// scalaRegexSpecs is the regex fallback extractor for Scala import statements.
// It returns the dotted import prefix (e.g. "com.example" for
// "import com.example.User", "com.example.models" for a selector/wildcard
// import), matching the historical resolver input.
func scalaRegexSpecs(lines []string) []string {
	var specs []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if m := scalaImportRe.FindStringSubmatch(line); m != nil {
			specs = append(specs, m[1])
		}
	}
	return specs
}

// scalaNormalizeSpec converts a tree-sitter-captured import statement (the full
// "import …" text) into the dotted prefix the resolver consumes, using the same
// regex as the fallback. Specs that are already normalized (no "import" prefix)
// pass through unchanged.
func scalaNormalizeSpec(spec string) string {
	spec = strings.TrimSpace(spec)
	if m := scalaImportRe.FindStringSubmatch(spec); m != nil {
		return m[1]
	}
	// A full "import …" statement that the regex couldn't normalize (an exotic
	// form) is dropped rather than fed verbatim to the resolver.
	if strings.HasPrefix(spec, "import") {
		return ""
	}
	return spec
}

// resolveScalaSpecs resolves Scala dotted import prefixes to local file paths.
// Resolution uses the same source root conventions as Java.
func resolveScalaSpecs(specs []string, filePath string, idx *FileIndex, t *importTally) []string {
	t.extract(len(specs))

	seen := make(map[string]bool)
	var resolved []string

	// Standard library prefixes to skip
	skipPrefixes := []string{"scala.", "java.", "javax.", "akka.", "cats.", "zio."}

	for _, raw := range specs {
		imp := scalaNormalizeSpec(raw)
		if imp == "" {
			t.skip()
			continue
		}

		// Skip standard library / common external packages
		skip := false
		for _, prefix := range skipPrefixes {
			if strings.HasPrefix(imp, prefix) {
				skip = true
				break
			}
		}
		if skip {
			t.skip()
			continue
		}

		// Convert dots to path separators
		classPath := strings.ReplaceAll(imp, ".", "/")

		// Try source roots with both .scala and .java extensions
		roots := []string{"src/main/scala/", "src/main/java/", "src/", "app/", ""}
		exts := []string{".scala", ".java"}

		found := false
		for _, root := range roots {
			for _, ext := range exts {
				target := root + classPath + ext
				if !idx.Exists(target) {
					continue
				}
				found = true
				if target != filePath && !seen[target] {
					seen[target] = true
					resolved = append(resolved, target)
				}
			}
			// Also try as a directory (wildcard import: import com.example.models._)
			// Find all source files in the directory
			dirTarget := root + classPath
			for _, f := range idx.ByDir(dirTarget) {
				if (f.Lang != "scala" && f.Lang != "java") || f.Class != scan.ClassSource {
					continue
				}
				found = true
				if f.RelPath != filePath && !seen[f.RelPath] {
					seen[f.RelPath] = true
					resolved = append(resolved, f.RelPath)
				}
			}
		}
		if found {
			t.hit()
		} else {
			t.miss(imp)
		}
	}

	return resolved
}

// ─── Elixir ───────────────────────────────────────────────────────────────────

// stripElixirNonCode blanks out comments, string literals and heredocs so that
// module names mentioned in @moduledoc/@doc text or in string data cannot be
// mistaken for references. Elixir has no tree-sitter grammar here, so this is
// the cheapest way to get the "not inside a string" guarantee the other
// languages get from parsing.
func stripElixirNonCode(lines []string) []string {
	out := make([]string, len(lines))
	inHeredoc := false
	heredocDelim := ""

	for i, line := range lines {
		if inHeredoc {
			if strings.Contains(line, heredocDelim) {
				inHeredoc = false
			}
			out[i] = ""
			continue
		}

		var b strings.Builder
		var quote byte
		for j := 0; j < len(line); j++ {
			c := line[j]
			if quote != 0 {
				if c == '\\' {
					j++
					continue
				}
				if c == quote {
					quote = 0
				}
				continue
			}
			if c == '"' || c == '\'' {
				delim := strings.Repeat(string(c), 3)
				if strings.HasPrefix(line[j:], delim) {
					rest := line[j+3:]
					if k := strings.Index(rest, delim); k >= 0 {
						j += 3 + k + 2
						continue
					}
					heredocDelim = delim
					inHeredoc = true
					break
				}
				quote = c
				continue
			}
			if c == '#' {
				break
			}
			b.WriteByte(c)
		}
		out[i] = b.String()
	}
	return out
}

// resolveElixirImports finds module references in an Elixir file and resolves
// them to file paths. Elixir modules are referenced by name (e.g.,
// QuotePilot.Notifications.Providers.Twilio) and map to file paths by reading
// the defmodule of every source file.
func resolveElixirImports(lines []string, filePath string, rc *resolveCtx, t *importTally) []string {
	modToFile := rc.elixirModules()

	// Find the module defined in this file so we don't self-reference.
	selfModule := ""
	code := stripElixirNonCode(lines)
	for _, line := range code {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "defmodule ") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				selfModule = strings.TrimSuffix(parts[1], ",")
				break
			}
		}
	}

	seen := make(map[string]bool)
	seenMod := make(map[string]bool)
	var resolved []string
	refs := 0

	for _, line := range code {
		for _, m := range exModuleRef.FindAllString(line, -1) {
			if m == selfModule || seenMod[m] {
				continue
			}
			seenMod[m] = true
			refs++

			target, ok := modToFile[m]
			if !ok {
				t.skip() // a dependency or stdlib module (Ecto.Query, …)
				continue
			}
			t.hit()
			if target == filePath || seen[target] {
				continue
			}
			seen[target] = true
			resolved = append(resolved, target)
		}
	}
	t.extract(refs)
	return resolved
}

// elixirModules maps Elixir module names to file paths, built once per repo by
// reading the defmodule of every Elixir source file.
func (rc *resolveCtx) elixirModules() map[string]string {
	rc.exOnce.Do(func() {
		rc.exMods = make(map[string]string)
		// Files are read relative to the scan root, not the process working
		// directory — otherwise the same repo yields a different graph
		// depending on where recon was invoked from.
		scanned := scanLangFilesParallel(rc, []string{"elixir"}, func(_ *scan.FileEntry, data []byte) string {
			return parseDefmodule(data)
		})
		for _, s := range scanned {
			if s.file == nil || s.val == "" {
				continue
			}
			if _, exists := rc.exMods[s.val]; !exists {
				rc.exMods[s.val] = s.file.RelPath
			}
		}
	})
	return rc.exMods
}

// parseDefmodule returns the first defmodule declaration in an Elixir source
// (e.g. "QuotePilot.Notifications.SMS"), or "".
func parseDefmodule(data []byte) string {
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "defmodule ") {
			continue
		}
		// "defmodule QuotePilot.Notifications.SMS do" → the name ends at the
		// first space or comma.
		rest := strings.TrimPrefix(line, "defmodule ")
		if i := strings.IndexAny(rest, " ,"); i > 0 {
			return rest[:i]
		}
		return rest
	}
	return ""
}

// ─── Swift ────────────────────────────────────────────────────────────────────

// swiftSystemFrameworks lists common Apple/system frameworks to skip during import resolution.
var swiftSystemFrameworks = map[string]bool{
	"Foundation": true, "UIKit": true, "SwiftUI": true, "Combine": true,
	"CoreData": true, "CoreGraphics": true, "CoreLocation": true, "CoreImage": true,
	"CoreText": true, "CoreFoundation": true, "CoreML": true, "CoreMotion": true,
	"CoreBluetooth": true, "CoreMedia": true, "CoreVideo": true, "CoreAudio": true,
	"AVFoundation": true, "ARKit": true, "AppKit": true, "Accelerate": true,
	"AuthenticationServices": true, "BackgroundTasks": true, "CallKit": true,
	"CarPlay": true, "CloudKit": true, "Contacts": true, "ContactsUI": true,
	"CryptoKit": true, "Darwin": true, "Dispatch": true, "EventKit": true,
	"GameKit": true, "GameplayKit": true, "HealthKit": true, "HomeKit": true,
	"MapKit": true, "MediaPlayer": true, "MessageUI": true, "Metal": true,
	"MetalKit": true, "MultipeerConnectivity": true, "NaturalLanguage": true,
	"Network": true, "NotificationCenter": true, "ObjectiveC": true,
	"PassKit": true, "PhotosUI": true, "Photos": true, "PushKit": true,
	"QuartzCore": true, "RealityKit": true, "ReplayKit": true, "SafariServices": true,
	"SceneKit": true, "Security": true, "SpriteKit": true, "StoreKit": true,
	"SystemConfiguration": true, "UserNotifications": true, "Vision": true,
	"WatchKit": true, "WebKit": true, "WidgetKit": true, "XCTest": true,
	"os": true, "Swift": true, "SwiftData": true, "Observation": true,
}

// resolveSwiftImports resolves Swift import statements to local source files.
// It maps cross-module dependencies in Swift Package Manager projects.
func resolveSwiftImports(lines []string, filePath string, rc *resolveCtx, t *importTally) []string {
	// Collect imported module names
	var moduleNames []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		m := swiftImportRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		moduleNames = append(moduleNames, m[1])
	}
	t.extract(len(moduleNames))
	if len(moduleNames) == 0 {
		return nil
	}

	localTargets := rc.swiftTargetMap()

	seen := make(map[string]bool)
	var resolved []string
	for _, modName := range moduleNames {
		if swiftSystemFrameworks[modName] {
			t.skip()
			continue
		}
		dirs, ok := localTargets[modName]
		if !ok {
			t.skip() // an external package product
			continue
		}
		found := false
		for _, dir := range dirs {
			for _, f := range rc.idx.FilesInDir(dir) {
				if f.Lang != "swift" || f.Class != scan.ClassSource {
					continue
				}
				found = true
				if f.RelPath != filePath && !seen[f.RelPath] {
					seen[f.RelPath] = true
					resolved = append(resolved, f.RelPath)
				}
			}
		}
		if found {
			t.hit()
		} else {
			t.miss(modName)
		}
	}
	return resolved
}

// swiftTargetMap discovers local Swift package targets and maps their names to
// source directories, once per repo.
func (rc *resolveCtx) swiftTargetMap() map[string][]string {
	rc.swiftOnce.Do(func() {
		targets := make(map[string][]string)

		if rc.idx.Exists("Package.swift") {
			targets = parseSwiftPackageTargets(rc.root)
		}

		// Fallback/supplement: look for directories under Sources/
		for _, f := range rc.idx.All() {
			if f.Lang != "swift" || !strings.HasPrefix(f.RelPath, "Sources/") {
				continue
			}
			rest := strings.TrimPrefix(f.RelPath, "Sources/")
			parts := strings.SplitN(rest, "/", 2)
			if len(parts) < 2 {
				continue
			}
			modName := parts[0]
			if _, ok := targets[modName]; !ok {
				targets[modName] = []string{"Sources/" + modName}
			}
		}
		rc.swiftTargets = targets
	})
	return rc.swiftTargets
}

// swiftTargetNameRe matches .target(name: "Foo" or .executableTarget(name: "Foo" patterns.
var swiftTargetNameRe = regexp.MustCompile(`\.(?:target|executableTarget|testTarget)\s*\(\s*name:\s*"([^"]+)"`)

// swiftTargetPathRe matches path: "some/path" within a target definition.
var swiftTargetPathRe = regexp.MustCompile(`path:\s*"([^"]+)"`)

// parseSwiftPackageTargets reads Package.swift and extracts target name →
// directory mappings. The manifest is read relative to the scan root.
func parseSwiftPackageTargets(root string) map[string][]string {
	targets := make(map[string][]string)

	lines, err := readAllLines(filepath.Join(root, "Package.swift"))
	if err != nil {
		return targets
	}

	for i, line := range lines {
		m := swiftTargetNameRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		targetName := m[1]
		// Skip test targets
		if strings.Contains(line, ".testTarget") {
			continue
		}

		// Look for a path: override in the next few lines
		customPath := ""
		for j := i; j < len(lines) && j < i+5; j++ {
			if pm := swiftTargetPathRe.FindStringSubmatch(lines[j]); pm != nil {
				customPath = pm[1]
				break
			}
		}

		if customPath != "" {
			targets[targetName] = []string{customPath}
		} else {
			// Default SPM convention: Sources/<TargetName>
			targets[targetName] = []string{"Sources/" + targetName}
		}
	}
	return targets
}
