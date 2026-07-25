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

// stemIndex looks source files up by a name key, case-sensitively first and
// case-insensitively as a fallback.
type stemIndex struct {
	exact   map[string][]*scan.FileEntry
	lowered map[string][]*scan.FileEntry
}

func newStemIndex() *stemIndex {
	return &stemIndex{
		exact:   make(map[string][]*scan.FileEntry),
		lowered: make(map[string][]*scan.FileEntry),
	}
}

func (si *stemIndex) add(key string, e *scan.FileEntry) {
	si.exact[key] = append(si.exact[key], e)
	l := strings.ToLower(key)
	if l != key {
		si.lowered[l] = append(si.lowered[l], e)
	}
}

func (si *stemIndex) get(key string) []*scan.FileEntry {
	if hits := si.exact[key]; len(hits) > 0 {
		return hits
	}
	l := strings.ToLower(key)
	if hits := si.exact[l]; len(hits) > 0 {
		return hits
	}
	return si.lowered[l]
}

// testMatcher resolves a test file to its source by looking source files up by
// stem, then scoring candidates on directory affinity. The old implementation
// built a fixed list of literal paths per language, so anything that did not
// live in the test's own directory or its immediate parent was invisible.
type testMatcher struct {
	idx *FileIndex
	// stems keys whole source stems ("AprysePdfGenerationService").
	stems *stemIndex
	// prefixes keys every proper leading run of a source stem's name parts
	// ("Apryse", "AprysePdf", "AprysePdfGeneration"). Used only by the
	// prefix tier, which is heavily fenced — see best().
	prefixes *stemIndex
	// dirsByName maps a directory's base name to every directory with that
	// name that holds source files, itself or below. This is what lets
	// tests/Leroy.Platform.Tests find src/Domains/Leroy.Platform, which no
	// amount of segment rewriting on the test path would ever produce.
	dirsByName map[string][]string
}

func newTestMatcher(idx *FileIndex) *testMatcher {
	m := &testMatcher{
		idx:        idx,
		stems:      newStemIndex(),
		prefixes:   newStemIndex(),
		dirsByName: make(map[string][]string),
	}
	// Scripts count as sources here: a shell test's subject is a shell script,
	// which the scanner classifies as ClassScript, not ClassSource.
	sources := append(append([]*scan.FileEntry{}, idx.ByClass(scan.ClassSource)...), idx.ByClass(scan.ClassScript)...)
	seenDir := map[string]bool{}
	for _, s := range sources {
		stem := stemOf(s.RelPath)
		m.stems.add(stem, s)
		if parts := splitNameParts(stem); len(parts) > 1 {
			for n := 1; n < len(parts); n++ {
				m.prefixes.add(joinNameParts(parts[:n], stem), s)
			}
		}
		for d := dirOf(s.RelPath); d != ""; d = dirOf(d) {
			if seenDir[d] {
				break // ancestors were recorded the first time we saw this dir
			}
			seenDir[d] = true
			name := d
			if i := strings.LastIndex(d, "/"); i >= 0 {
				name = d[i+1:]
			}
			m.dirsByName[name] = append(m.dirsByName[name], d)
		}
	}
	for name := range m.dirsByName {
		sort.Strings(m.dirsByName[name])
	}
	return m
}

func stemOf(relPath string) string {
	base := filepath.Base(relPath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// scope is everywhere a given test file's subject could plausibly live.
type scope struct {
	dir     string          // the test's own directory
	mirrors map[string]bool // directories the test directory could be mirroring
	roots   []string        // source project roots, matched as whole subtrees
}

// Directory-affinity scores. Anything below scoreProject is "not nearby", and
// only an exact, repo-unique stem is allowed to map from there.
const (
	scoreSameDir = 4
	scoreMirror  = 3
	scoreProject = 2
	scoreNone    = 0
)

func (s scope) score(dir string) int {
	switch {
	case dir == s.dir:
		return scoreSameDir
	case s.mirrors[dir]:
		return scoreMirror
	}
	for _, r := range s.roots {
		if dir == r || strings.HasPrefix(dir, r+"/") {
			return scoreProject
		}
	}
	return scoreNone
}

// matchMode records how much of the test's name the candidate stem actually
// accounts for; each step down buys a stricter acceptance rule.
type matchMode int

const (
	modeExact  matchMode = iota // stem minus the language's test affixes
	modeFuzzy                   // stem with trailing name parts dropped
	modePrefix                  // stem is a leading run of the source's name
)

// find resolves one test file.
func (m *testMatcher) find(test *scan.FileEntry) (string, TestMapStatus) {
	conv, ok := conventionFor(test.RelPath)
	if !ok {
		return "", TestMapUnsupported
	}

	testDir := dirOf(test.RelPath)
	sc := scope{
		dir:     testDir,
		mirrors: mirrorDirs(testDir),
		roots:   m.projectRoots(testDir),
	}
	testExt := strings.ToLower(filepath.Ext(test.RelPath))

	exact, fuzzy := candidateStems(stemOf(test.RelPath), conv)

	for _, stem := range exact {
		if hit := m.best(m.stems.get(stem), conv, testExt, sc, modeExact); hit != "" {
			return hit, TestMapMapped
		}
	}
	// A test named for the behaviour it exercises (checkout-flow.test.ts) can
	// still be tied to checkout.ts, but only when the shortened name lands
	// somewhere plausible or is unique in the repo.
	for _, stem := range fuzzy {
		if hit := m.best(m.stems.get(stem), conv, testExt, sc, modeFuzzy); hit != "" {
			return hit, TestMapMapped
		}
	}
	// Last resort: the test's name is a *prefix* of the source's rather than a
	// truncation of it — AprysePdfGoldenTests.cs against
	// AprysePdfGenerationService.cs. Shortening the test stem can never reach
	// that name, so the lookup has to run the other way.
	raw := stemOf(test.RelPath)
	for _, stem := range append(append([]string{}, exact...), fuzzy...) {
		if !usableAsPrefix(stem, raw) {
			continue
		}
		if hit := m.best(m.prefixes.get(stem), conv, testExt, sc, modePrefix); hit != "" {
			return hit, TestMapMapped
		}
	}
	return "", TestMapNoMatch
}

// usableAsPrefix is half the precision line on prefix matching.
//
// The stem must be at least two name components: "AprysePdf" names a subject,
// "Pdf" or "Certificate" would hand a whole source project to a whole test
// project. And it must not be the test file's own untouched stem — that stem
// carries no evidence the file is about anything but itself, so "X.cs is a
// prefix of XData.cs" would map any file onto its longer-named neighbour. Only
// a stem the affix-stripping or shortening rules produced counts.
func usableAsPrefix(stem, rawTestStem string) bool {
	return stem != rawTestStem && nameComponentCount(stem) >= minPrefixComponents
}

const minPrefixComponents = 2

// best picks the strongest source candidate from cands, or "" for none.
func (m *testMatcher) best(cands []*scan.FileEntry, conv testConvention, testExt string, sc scope, mode matchMode) string {
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
		pool = append(pool, scored{
			path:  c.RelPath,
			score: sc.score(dirOf(c.RelPath)),
			ext:   rank,
		})
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
	if mode == modePrefix {
		// The other half of the precision line. A prefix match is the weakest
		// evidence recon has, so the prefix must single one source out: nearby,
		// and with nothing else equally close wearing the same prefix. Picking
		// the shortest or the alphabetically first of
		// DocumentFieldSources/Types/Validator would be a coin toss dressed up
		// as an answer, so recon declines and says no_match.
		if top.score < scoreProject {
			return ""
		}
		if len(pool) > 1 && pool[1].score == top.score && pool[1].ext == top.ext {
			return ""
		}
		return top.path
	}
	if top.score >= scoreProject {
		return top.path
	}
	// Nothing nearby. A stem that occurs exactly once in the whole repo is
	// still an unambiguous answer; anything else is a guess, so decline.
	if len(pool) == 1 && mode == modeExact {
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

// dotNetTestDir maps a .NET test project directory to its production project:
// "MyApp.Tests" → "MyApp", "MyApp.IntegrationTests" → "MyApp". Any final
// dotted component ending in Test/Tests/Spec/Specs counts, which covers the
// suffixes teams invent (.UnitTests, .FunctionalTests, .ApiTests) without
// keeping a list of them.
func dotNetTestDir(seg string) string {
	i := strings.LastIndex(seg, ".")
	if i <= 0 {
		return ""
	}
	for _, suffix := range []string{"Tests", "Test", "Specs", "Spec"} {
		if strings.HasSuffix(seg[i+1:], suffix) {
			return seg[:i]
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

// projectTestDir returns the production-project name a test-project directory
// segment pairs with, or "".
func projectTestDir(seg string) string {
	if base := dotNetTestDir(seg); base != "" {
		return base
	}
	return swiftTestDir(seg)
}

// maxProjectRoots caps how many same-named directories a test project is
// allowed to claim. A name shared by more than a couple of directories is a
// generic word, not a project.
const maxProjectRoots = 3

// projectRoots returns the source project directories a test directory pairs
// with, as whole subtrees.
//
// This is the rule the .NET solution layout needs and plain directory
// mirroring cannot express: tests/Leroy.Platform.Tests pairs with the
// *project* src/Leroy.Platform, not with a directory at the same relative
// depth, so a source at src/Leroy.Platform/Pdf/X.cs is in scope even though
// the test tree has no Pdf/ directory. Rewriting path segments cannot find it
// either when the projects are nested unevenly (src/Domains/Leroy.Certificates
// against tests/Leroy.Certificates.Tests), so the project is looked up by name
// among directories that actually contain sources.
//
// The subtree is a *scope*, not a match: being in it never maps a test on its
// own, it only makes a name match count as nearby.
func (m *testMatcher) projectRoots(testDir string) []string {
	if testDir == "" {
		return nil
	}
	segs := strings.Split(testDir, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		base := projectTestDir(segs[i])
		if base == "" {
			continue
		}
		cands := m.dirsByName[base]
		if len(cands) == 0 || len(cands) > maxProjectRoots {
			continue
		}
		var out []string
		for _, c := range cands {
			if c != testDir && !underTestTree(c) {
				out = append(out, c)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// underTestTree reports whether a directory is itself part of a test tree, so
// it can never be the source side of a pairing.
func underTestTree(dir string) bool {
	for _, seg := range strings.Split(dir, "/") {
		if testDirSegments[strings.ToLower(seg)] || projectTestDir(seg) != "" {
			return true
		}
	}
	return false
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
	return splitCamel(stem)
}

// nameComponentCount counts the words in a stem, splitting on separators *and*
// camel-case humps. splitNameParts deliberately stops at the first kind it
// finds (so "Service.Cvi" is two parts, not five); this is the other question
// — how specific is this name — and both splits count towards it.
func nameComponentCount(stem string) int {
	n := 0
	for _, part := range strings.FieldsFunc(stem, func(r rune) bool {
		return r == '_' || r == '-' || r == '.'
	}) {
		n += len(splitCamel(part))
	}
	return n
}

// splitCamel splits "AprysePdf" into ["Apryse", "Pdf"].
func splitCamel(s string) []string {
	var parts []string
	start := 0
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			parts = append(parts, s[start:i])
			start = i
		}
	}
	return append(parts, s[start:])
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
