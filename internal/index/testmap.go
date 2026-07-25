package index

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/djtouchette/recon/internal/scan"
)

// TestMap maps source files to their test files and vice versa.
//
// Mapping is a heuristic over names and directory layout — recon does not read
// what a test imports. That makes "no tests" a claim recon frequently cannot
// support, so every lookup also returns a TestMapStatus separating "there is
// no test" from "recon has no rules for this file type".
type TestMap struct {
	sourceToTest map[string][]string
	testToSource map[string]string
	unmapped     []string // test files no source could be found for
}

// TestMapStatus explains an empty lookup result.
type TestMapStatus string

const (
	// TestMapMapped means the lookup found something.
	TestMapMapped TestMapStatus = "mapped"
	// TestMapNoMatch means recon has naming rules for this file type and none
	// of them matched — the answer "no tests" is a real (if fallible) answer.
	TestMapNoMatch TestMapStatus = "no_match"
	// TestMapUnsupported means recon has no naming rules for this file type at
	// all, so it never even looked. Not the same as "there are no tests".
	TestMapUnsupported TestMapStatus = "unsupported"
)

// NewTestMap builds test mappings from the file index.
func NewTestMap(idx *FileIndex) *TestMap {
	tm := &TestMap{
		sourceToTest: make(map[string][]string),
		testToSource: make(map[string]string),
	}

	b := newTestMatcher(idx)
	for _, t := range idx.ByClass(scan.ClassTest) {
		src, status := b.find(t)
		if status == TestMapMapped {
			tm.sourceToTest[src] = append(tm.sourceToTest[src], t.RelPath)
			tm.testToSource[t.RelPath] = src
			continue
		}
		if status == TestMapNoMatch {
			tm.unmapped = append(tm.unmapped, t.RelPath)
		}
	}

	for src := range tm.sourceToTest {
		sort.Strings(tm.sourceToTest[src])
	}
	sort.Strings(tm.unmapped)
	return tm
}

// NewTestMapFromData creates a TestMap from pre-computed mappings (cache
// load). The unmapped-test list is not persisted, so UnmappedTests is empty
// for a map restored this way; per-path statuses still work because they are
// derived from the path itself.
func NewTestMapFromData(sourceToTest map[string][]string, testToSource map[string]string) *TestMap {
	return &TestMap{
		sourceToTest: sourceToTest,
		testToSource: testToSource,
	}
}

// TestsFor returns test files for a given source file path.
func (tm *TestMap) TestsFor(srcPath string) []string {
	return tm.sourceToTest[srcPath]
}

// LookupTests returns the tests for a source file plus why the list is empty.
func (tm *TestMap) LookupTests(srcPath string) ([]string, TestMapStatus) {
	if tests := tm.sourceToTest[srcPath]; len(tests) > 0 {
		return tests, TestMapMapped
	}
	if !SupportsTestMapping(srcPath) {
		return nil, TestMapUnsupported
	}
	return nil, TestMapNoMatch
}

// SourceFor returns the source file for a given test file.
func (tm *TestMap) SourceFor(testPath string) string {
	return tm.testToSource[testPath]
}

// LookupSource returns the source file for a test plus why it is empty.
func (tm *TestMap) LookupSource(testPath string) (string, TestMapStatus) {
	if src := tm.testToSource[testPath]; src != "" {
		return src, TestMapMapped
	}
	if !SupportsTestMapping(testPath) {
		return "", TestMapUnsupported
	}
	return "", TestMapNoMatch
}

// AllMappings returns the full source→test map.
func (tm *TestMap) AllMappings() map[string][]string {
	return tm.sourceToTest
}

// TestToSourceMap returns the full test→source map.
func (tm *TestMap) TestToSourceMap() map[string]string {
	return tm.testToSource
}

// UnmappedTests lists test files recon recognised as tests but could not tie
// to a source file. It is the honest measure of how much of the mapping is
// guesswork.
func (tm *TestMap) UnmappedTests() []string {
	return tm.unmapped
}

// SupportsTestMapping reports whether recon has test-naming rules for a path's
// language. When this is false, an empty TestsFor result means "no rules",
// not "no tests".
func SupportsTestMapping(path string) bool {
	_, ok := conventionFor(path)
	return ok
}

// ClassifyTestKind guesses the test kind from the path.
func ClassifyTestKind(relPath string) string {
	lpath := strings.ToLower(relPath)
	if strings.Contains(lpath, "e2e") || strings.Contains(lpath, "playwright") ||
		strings.Contains(lpath, "cypress") || strings.Contains(lpath, "selenium") {
		return "e2e"
	}
	if strings.Contains(lpath, "integration") || strings.Contains(lpath, "integ") {
		return "integration"
	}
	return "unit"
}

// --- Naming conventions ---

// testConvention describes how a language spells a test file and which source
// extensions it can correspond to. srcExts is ordered by preference.
type testConvention struct {
	prefixes []string
	suffixes []string
	srcExts  []string
}

var jsConvention = testConvention{
	suffixes: []string{".test", ".spec", "-test", "-spec", "_test", "_spec"},
	srcExts:  []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".mts", ".cjs", ".vue", ".svelte"},
}

var cConvention = testConvention{
	prefixes: []string{"test_", "test-"},
	suffixes: []string{"_test", "_tests", "_unittest", "-test", "Test", "Tests"},
	srcExts:  []string{".c", ".h", ".cpp", ".cc", ".cxx", ".hpp"},
}

// testConventions is keyed by lowercased file extension. A language absent
// here is reported as TestMapUnsupported rather than "no tests found" —
// silence and ignorance should not look the same.
var testConventions = map[string]testConvention{
	".go": {suffixes: []string{"_test"}, srcExts: []string{".go"}},

	".js":  jsConvention,
	".jsx": jsConvention,
	".ts":  jsConvention,
	".tsx": jsConvention,
	".mjs": jsConvention,
	".mts": jsConvention,
	".cjs": jsConvention,

	".py": {
		prefixes: []string{"test_"},
		suffixes: []string{"_test"},
		srcExts:  []string{".py", ".pyw"},
	},
	".rb": {suffixes: []string{"_spec", "_test"}, srcExts: []string{".rb"}},
	".ex": {suffixes: []string{"_test"}, srcExts: []string{".ex", ".exs"}},
	".exs": {
		suffixes: []string{"_test"},
		srcExts:  []string{".ex", ".exs"},
	},
	".erl": {suffixes: []string{"_SUITE", "_tests", "_test"}, srcExts: []string{".erl"}},
	".cs": {
		suffixes: []string{"Tests", "Test", "Spec", "Specs", "Fixture"},
		srcExts:  []string{".cs"},
	},
	".fs":   {suffixes: []string{"Tests", "Test"}, srcExts: []string{".fs"}},
	".java": {suffixes: []string{"ITCase", "Tests", "Test", "IT"}, srcExts: []string{".java", ".kt"}},
	".kt":   {suffixes: []string{"Tests", "Test", "Spec"}, srcExts: []string{".kt", ".java"}},
	".kts":  {suffixes: []string{"Tests", "Test", "Spec"}, srcExts: []string{".kt", ".kts"}},
	".rs":   {suffixes: []string{"_test", "_tests"}, srcExts: []string{".rs"}},
	".php":  {suffixes: []string{"Test", "Spec"}, srcExts: []string{".php"}},
	".swift": {
		suffixes: []string{"Tests", "Test", "Spec"},
		srcExts:  []string{".swift"},
	},
	".dart":  {suffixes: []string{"_test"}, srcExts: []string{".dart"}},
	".scala": {suffixes: []string{"Spec", "Suite", "Tests", "Test"}, srcExts: []string{".scala", ".java"}},
	".clj":   {suffixes: []string{"_test", "-test"}, srcExts: []string{".clj"}},

	// Tree-sitter languages recon parses but the test map used to ignore.
	".c":   cConvention,
	".h":   cConvention,
	".cpp": cConvention,
	".cc":  cConvention,
	".cxx": cConvention,
	".hpp": cConvention,
	".lua": {
		prefixes: []string{"test_"},
		suffixes: []string{"_test", "_spec"},
		srcExts:  []string{".lua"},
	},
	".jl": {
		prefixes: []string{"test_", "test-"},
		suffixes: []string{"_test", "_tests"},
		srcExts:  []string{".jl"},
	},
	".zig": {suffixes: []string{"_test", "_tests"}, srcExts: []string{".zig"}},
	".sh": {
		prefixes: []string{"test_", "test-"},
		suffixes: []string{"_test", "_tests", ".test", "-test"},
		srcExts:  []string{".sh", ".bash"},
	},
	".bash": {
		prefixes: []string{"test_", "test-"},
		suffixes: []string{"_test", "_tests", ".test", "-test"},
		srcExts:  []string{".sh", ".bash"},
	},
	".ps1": {suffixes: []string{".Tests", ".Test"}, srcExts: []string{".ps1", ".psm1"}},
	".r": {
		prefixes: []string{"test_", "test-"},
		suffixes: []string{"_test"},
		srcExts:  []string{".r"},
	},
}

func conventionFor(path string) (testConvention, bool) {
	c, ok := testConventions[strings.ToLower(filepath.Ext(path))]
	return c, ok
}

// --- Matching ---

// testMatcher resolves a test file to its source by looking source files up by
// stem, then scoring candidates on directory affinity. The old implementation
// built a fixed list of literal paths per language, so anything that did not
// live in the test's own directory or its immediate parent was invisible.
type testMatcher struct {
	idx     *FileIndex
	byStem  map[string][]*scan.FileEntry
	lowered map[string][]*scan.FileEntry
}

func newTestMatcher(idx *FileIndex) *testMatcher {
	m := &testMatcher{
		idx:     idx,
		byStem:  make(map[string][]*scan.FileEntry),
		lowered: make(map[string][]*scan.FileEntry),
	}
	// Scripts count as sources here: a shell test's subject is a shell script,
	// which the scanner classifies as ClassScript, not ClassSource.
	sources := append(append([]*scan.FileEntry{}, idx.ByClass(scan.ClassSource)...), idx.ByClass(scan.ClassScript)...)
	for _, s := range sources {
		stem := stemOf(s.RelPath)
		m.byStem[stem] = append(m.byStem[stem], s)
		if l := strings.ToLower(stem); l != stem {
			m.lowered[l] = append(m.lowered[l], s)
		} else {
			m.lowered[l] = m.byStem[stem]
		}
	}
	return m
}

func stemOf(relPath string) string {
	base := filepath.Base(relPath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// find resolves one test file.
func (m *testMatcher) find(test *scan.FileEntry) (string, TestMapStatus) {
	conv, ok := conventionFor(test.RelPath)
	if !ok {
		return "", TestMapUnsupported
	}

	testDir := dirOf(test.RelPath)
	mirrors := mirrorDirs(testDir)
	testExt := strings.ToLower(filepath.Ext(test.RelPath))

	exact, fuzzy := candidateStems(stemOf(test.RelPath), conv)

	for _, stem := range exact {
		if hit := m.best(stem, conv, testExt, testDir, mirrors, true); hit != "" {
			return hit, TestMapMapped
		}
	}
	// A test named for the behaviour it exercises (checkout-flow.test.ts) can
	// still be tied to checkout.ts, but only when the shortened name lands
	// somewhere plausible or is unique in the repo.
	for _, stem := range fuzzy {
		if hit := m.best(stem, conv, testExt, testDir, mirrors, false); hit != "" {
			return hit, TestMapMapped
		}
	}
	return "", TestMapNoMatch
}

// best picks the strongest source candidate for a stem, or "" for none.
func (m *testMatcher) best(stem string, conv testConvention, testExt, testDir string, mirrors map[string]bool, exactStem bool) string {
	cands := m.byStem[stem]
	if len(cands) == 0 {
		cands = m.lowered[strings.ToLower(stem)]
	}
	if len(cands) == 0 {
		return ""
	}

	type scored struct {
		path  string
		score int
		ext   int
	}
	var pool []scored
	for _, c := range cands {
		ext := strings.ToLower(filepath.Ext(c.RelPath))
		rank := extRank(conv.srcExts, ext)
		if rank < 0 {
			continue
		}
		if ext == testExt {
			rank = -1 // same extension as the test wins ties
		}
		score := 0
		switch {
		case dirOf(c.RelPath) == testDir:
			score = 3
		case mirrors[dirOf(c.RelPath)]:
			score = 2
		}
		pool = append(pool, scored{path: c.RelPath, score: score, ext: rank})
	}
	if len(pool) == 0 {
		return ""
	}

	sort.Slice(pool, func(i, j int) bool {
		if pool[i].score != pool[j].score {
			return pool[i].score > pool[j].score
		}
		if pool[i].ext != pool[j].ext {
			return pool[i].ext < pool[j].ext
		}
		return pool[i].path < pool[j].path
	})

	top := pool[0]
	if top.score >= 2 {
		return top.path
	}
	// Nothing nearby. A stem that occurs exactly once in the whole repo is
	// still an unambiguous answer; anything else is a guess, so decline.
	if len(pool) == 1 && exactStem {
		return top.path
	}
	return ""
}

func extRank(order []string, ext string) int {
	for i, e := range order {
		if e == ext {
			return i
		}
	}
	return -1
}

func dirOf(relPath string) string {
	d := filepath.ToSlash(filepath.Dir(relPath))
	if d == "." {
		return ""
	}
	return d
}

// --- Directory mirroring ---

// testDirSegments are path segments that mark a directory as holding tests.
// Replacing or dropping them is how a test path is mirrored onto a source path
// (test/src/cart.test.ts → src/cart.ts, src/test/java/... → src/main/java/...).
var testDirSegments = map[string]bool{
	"test": true, "tests": true, "spec": true, "specs": true,
	"__tests__": true, "__test__": true, "testing": true,
	"e2e": true, "it": true, "integration-tests": true,
}

// dropOnlySegments are qualifiers inside a test tree (tests/Unit/...) that
// have no source-side counterpart; they are only ever removed.
var dropOnlySegments = map[string]bool{
	"unit": true, "integration": true, "functional": true, "feature": true,
	"features": true, "acceptance": true, "e2e": true, "smoke": true,
}

// sourceRoots are the directory names a test tree commonly mirrors.
var sourceRoots = []string{"src", "lib", "app", "main", "internal", "pkg", "source", "sources"}

// mirrorDirs returns the set of directories a test in testDir could be
// mirroring: the directory itself, every ancestor, and every variant produced
// by rewriting or dropping the test-marking segments.
func mirrorDirs(testDir string) map[string]bool {
	out := map[string]bool{testDir: true, "": true}

	for d := testDir; d != ""; d = dirOf(d) {
		out[d] = true
	}

	segs := strings.Split(testDir, "/")
	if testDir == "" {
		return out
	}

	// Collect the indices worth rewriting (bounded so deeply nested test trees
	// cannot blow up the combination count).
	var idxs []int
	for i, s := range segs {
		l := strings.ToLower(s)
		if testDirSegments[l] || dropOnlySegments[l] || dotNetTestDir(s) != "" || swiftTestDir(s) != "" {
			idxs = append(idxs, i)
		}
		if len(idxs) == 3 {
			break
		}
	}

	variants := [][]string{segs}
	for _, i := range idxs {
		var next [][]string
		for _, v := range variants {
			next = append(next, v)
			for _, alt := range segmentAlternatives(v[i]) {
				cp := make([]string, len(v))
				copy(cp, v)
				cp[i] = alt
				next = append(next, cp)
			}
		}
		variants = next
	}

	for _, v := range variants {
		parts := v[:0:0]
		for _, s := range v {
			if s != "" {
				parts = append(parts, s)
			}
		}
		out[strings.Join(parts, "/")] = true
		// Also treat every ancestor of a rewritten path as plausible.
		for d := strings.Join(parts, "/"); d != ""; d = dirOf(d) {
			out[d] = true
		}
	}
	return out
}

// segmentAlternatives lists the source-side spellings of one test-side path
// segment. "" means "drop this segment".
func segmentAlternatives(seg string) []string {
	l := strings.ToLower(seg)
	var alts []string
	if testDirSegments[l] {
		alts = append(alts, "")
		alts = append(alts, sourceRoots...)
	}
	if dropOnlySegments[l] {
		alts = append(alts, "")
	}
	if base := dotNetTestDir(seg); base != "" {
		alts = append(alts, base)
	}
	if base := swiftTestDir(seg); base != "" {
		alts = append(alts, base)
	}
	return alts
}

// dotNetTestDir maps "MyApp.Tests" → "MyApp".
func dotNetTestDir(seg string) string {
	for _, suffix := range []string{".IntegrationTests", ".UnitTests", ".Tests", ".Test", ".Specs"} {
		if strings.HasSuffix(seg, suffix) {
			return strings.TrimSuffix(seg, suffix)
		}
	}
	return ""
}

// swiftTestDir maps "OrdersTests" → "Orders" (Tests/OrdersTests → Sources/Orders).
func swiftTestDir(seg string) string {
	for _, suffix := range []string{"Tests", "Test"} {
		if seg != suffix && strings.HasSuffix(seg, suffix) {
			return strings.TrimSuffix(seg, suffix)
		}
	}
	return ""
}

// --- Stem derivation ---

// candidateStems turns a test file's stem into the source stems it could
// correspond to: first the exact ones (affixes stripped), then progressively
// shortened ones for tests named after a behaviour rather than a file.
func candidateStems(name string, conv testConvention) (exact, fuzzy []string) {
	seen := map[string]bool{}
	add := func(dst *[]string, s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		*dst = append(*dst, s)
	}

	stripped := stripAffixes(name, conv)
	for _, s := range stripped {
		add(&exact, s)
	}
	// A file in a tests/ directory need not carry any affix at all.
	add(&exact, name)

	for _, s := range append([]string{}, exact...) {
		for _, short := range shortenings(s) {
			add(&fuzzy, short)
		}
	}
	return exact, fuzzy
}

// stripAffixes removes the language's test prefixes/suffixes, longest first.
func stripAffixes(name string, conv testConvention) []string {
	var out []string
	bases := []string{name}

	for _, p := range sortByLenDesc(conv.prefixes) {
		if strings.HasPrefix(name, p) && len(name) > len(p) {
			bases = append(bases, strings.TrimPrefix(name, p))
		}
	}
	for _, b := range bases {
		for _, s := range sortByLenDesc(conv.suffixes) {
			if strings.HasSuffix(b, s) && len(b) > len(s) {
				out = append(out, strings.TrimSuffix(b, s))
			}
		}
	}
	// A prefix-only match (test_foo.py) is itself a candidate.
	for _, b := range bases[1:] {
		out = append(out, b)
	}
	return out
}

func sortByLenDesc(in []string) []string {
	out := append([]string{}, in...)
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}

// shortenings drops trailing name components, longest first:
// "depgraph_resolve" → "depgraph"; "CheckoutFlow" → "Checkout".
func shortenings(stem string) []string {
	var out []string
	if parts := splitNameParts(stem); len(parts) > 1 {
		for n := len(parts) - 1; n >= 1; n-- {
			out = append(out, joinNameParts(parts[:n], stem))
		}
	}
	return out
}

// splitNameParts splits on separators, or on camel-case humps when there are
// no separators.
func splitNameParts(stem string) []string {
	if strings.ContainsAny(stem, "_-.") {
		return strings.FieldsFunc(stem, func(r rune) bool {
			return r == '_' || r == '-' || r == '.'
		})
	}
	var parts []string
	start := 0
	for i, r := range stem {
		if i > 0 && r >= 'A' && r <= 'Z' {
			parts = append(parts, stem[start:i])
			start = i
		}
	}
	parts = append(parts, stem[start:])
	return parts
}

// joinNameParts rebuilds a shortened stem using the original separator.
func joinNameParts(parts []string, original string) string {
	sep := ""
	switch {
	case strings.Contains(original, "_"):
		sep = "_"
	case strings.Contains(original, "-"):
		sep = "-"
	case strings.Contains(original, "."):
		sep = "."
	}
	return strings.Join(parts, sep)
}
