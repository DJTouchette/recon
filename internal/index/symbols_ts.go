package index

import (
	"embed"
	"strings"
	"sync"
	"unsafe"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	ts_csharp "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"
	ts_c "github.com/tree-sitter/tree-sitter-c/bindings/go"
	ts_cpp "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
	ts_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
	ts_java "github.com/tree-sitter/tree-sitter-java/bindings/go"
	ts_js "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	ts_php "github.com/tree-sitter/tree-sitter-php/bindings/go"
	ts_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	ts_ruby "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
	ts_rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	ts_scala "github.com/tree-sitter/tree-sitter-scala/bindings/go"
	ts_ts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	ts_kotlin "github.com/tree-sitter-grammars/tree-sitter-kotlin/bindings/go"
	ts_lua "github.com/tree-sitter-grammars/tree-sitter-lua/bindings/go"

	ts_zig "github.com/tree-sitter-grammars/tree-sitter-zig/bindings/go"
	ts_bash "github.com/tree-sitter/tree-sitter-bash/bindings/go"
	ts_julia "github.com/tree-sitter/tree-sitter-julia/bindings/go"
)

// Tree-sitter based symbol extraction.
//
// This is the preferred path: it parses the real grammar instead of matching
// regexes line-by-line, so it gets accurate names, correct kinds, multi-line
// signatures, and never mistakes a match inside a string or comment for a
// declaration. Languages without a registered grammar fall back to the regex
// patterns in symbols.go.
//
// Each grammar has a query in queries/<lang>.scm. By convention the capture
// name on the identifier IS the symbol kind (e.g. @function, @method, @struct),
// and a sibling @def capture marks the whole declaration node so we can build a
// clean signature. Captures whose name starts with "_" are query-internal
// helpers (used only by #match?/#eq? predicates) and are ignored. Adding a
// language is registering a grammar + writing its query — no Go code changes.

//go:embed queries/*.scm
var queryFS embed.FS

type tsLang struct {
	lang  *tree_sitter.Language
	query *tree_sitter.Query
}

var tsRegistry = map[string]*tsLang{}

// tsParserPool reuses parsers across the concurrent extraction goroutines.
// Parsers are not safe for concurrent use, but Query values are immutable and
// shared freely; each goroutine gets its own parser and query cursor.
var tsParserPool = sync.Pool{New: func() any { return tree_sitter.NewParser() }}

// tsxLangKey is the registry key for the TSX grammar. It is not a recon
// language of its own — scan.LangFromExt reports "typescript" for both .ts and
// .tsx — but the two need different grammars, so TSX gets its own registry
// entry that grammarCandidates selects by file extension.
const tsxLangKey = "tsx"

// tsGrammar pairs a registry key with a grammar pointer and its query file.
// Some grammars expose more than one language (TypeScript/TSX, PHP). Where the
// variants are interchangeable we pick the broadest (the full PHP grammar
// handles inline HTML); where they are not, each variant gets its own key.
//
// TypeScript and TSX are the "not interchangeable" case. TSX reinterprets `<`
// as the start of a JSX element, so it cannot parse the angle-bracket type
// assertion `<number>x` that is legal in .ts. Registering TSX for all of
// "typescript" meant every .ts file using a legacy cast parsed into a tree full
// of ERROR nodes and lost most of its declarations.
type tsGrammar struct {
	lang    string
	langPtr unsafe.Pointer
	query   string // filename under queries/
}

// refQueryFile is the refs query for this grammar, under queries/refs/. TSX
// shares TypeScript's queries; every other grammar uses its own key.
func (g tsGrammar) refQueryFile() string {
	if g.lang == tsxLangKey {
		return "typescript.scm"
	}
	return g.lang + ".scm"
}

var tsGrammars = []tsGrammar{
	{"go", ts_go.Language(), "go.scm"},
	{"python", ts_python.Language(), "python.scm"},
	{"javascript", ts_js.Language(), "javascript.scm"},
	{"typescript", ts_ts.LanguageTypescript(), "typescript.scm"},
	{tsxLangKey, ts_ts.LanguageTSX(), "typescript.scm"},
	{"rust", ts_rust.Language(), "rust.scm"},
	{"ruby", ts_ruby.Language(), "ruby.scm"},
	{"java", ts_java.Language(), "java.scm"},
	{"csharp", ts_csharp.Language(), "csharp.scm"},
	{"php", ts_php.LanguagePHP(), "php.scm"},
	{"scala", ts_scala.Language(), "scala.scm"},
	{"kotlin", ts_kotlin.Language(), "kotlin.scm"},
	{"c", ts_c.Language(), "c.scm"},
	{"cpp", ts_cpp.Language(), "cpp.scm"},
	{"lua", ts_lua.Language(), "lua.scm"},
	{"shell", ts_bash.Language(), "shell.scm"},
	{"julia", ts_julia.Language(), "julia.scm"},
	{"zig", ts_zig.Language(), "zig.scm"},
}

func init() {
	for _, g := range tsGrammars {
		// A query that fails to compile (e.g. a grammar bumped a node name)
		// simply leaves that language on the regex fallback rather than
		// crashing recon. TestQueriesCompile guards against this in CI.
		_ = registerTSLang(g.lang, g.langPtr, g.query)
	}
}

func registerTSLang(name string, langPtr unsafe.Pointer, queryFile string) error {
	src, err := queryFS.ReadFile("queries/" + queryFile)
	if err != nil {
		return err
	}
	l := tree_sitter.NewLanguage(langPtr)
	q, qerr := tree_sitter.NewQuery(l, string(src))
	if qerr != nil {
		return qerr
	}
	tsRegistry[name] = &tsLang{lang: l, query: q}
	return nil
}

// hasTSLang reports whether a tree-sitter grammar is registered for lang.
func hasTSLang(lang string) bool {
	_, ok := tsRegistry[lang]
	return ok
}

// tsParseResult describes the parse behind a symbol list: which grammar was
// used and whether the resulting tree was free of syntax errors.
type tsParseResult struct {
	key   string
	clean bool
}

// extractWithGrammars runs each candidate grammar in turn and returns the best
// result. A clean parse wins immediately; when no candidate parses cleanly the
// one with the most symbols is kept, and the result is flagged partial so the
// caller can say so rather than presenting a truncated list as complete.
//
// Candidates after the first only run when the earlier ones did not parse
// cleanly, except for genuinely ambiguous extensions (.h) where every candidate
// is tried and compared. That keeps the common case at exactly one parse.
func extractWithGrammars(source []byte, relPath string, candidates []string) ([]Symbol, tsParseResult, bool) {
	var (
		best     []Symbol
		bestRes  tsParseResult
		haveBest bool
		compare  = len(candidates) > 1 && ambiguousGrammar(relPath)
	)

	for _, key := range candidates {
		syms, res, ok := extractSymbolsTS(source, relPath, key)
		if !ok {
			continue
		}
		if !haveBest || betterParse(syms, res, best, bestRes) {
			best, bestRes, haveBest = syms, res, true
		}
		// Stop at the first clean parse unless the extension is ambiguous, in
		// which case a later grammar may still find strictly more.
		if res.clean && !compare {
			break
		}
	}
	return best, bestRes, haveBest
}

// betterParse reports whether candidate (a) should replace the incumbent (b):
// a clean parse always beats a partial one, otherwise more symbols wins.
func betterParse(a []Symbol, ares tsParseResult, b []Symbol, bres tsParseResult) bool {
	if ares.clean != bres.clean {
		return ares.clean
	}
	return len(a) > len(b)
}

// ambiguousGrammar reports whether a path's extension is claimed by more than
// one language, so every candidate grammar must be tried and compared instead
// of stopping at the first one that happens to parse.
func ambiguousGrammar(relPath string) bool {
	return strings.HasSuffix(strings.ToLower(relPath), ".h")
}

// extractSymbolsTS parses source with the grammar registered under key and
// returns its symbols. The bool is false when no grammar is registered (the
// caller should fall back to regex) or the parse fails outright. The
// tsParseResult reports whether the tree was free of ERROR/MISSING nodes — a
// tree with errors still yields whatever the query could match, but that list
// is incomplete and must not be presented as authoritative.
func extractSymbolsTS(source []byte, relPath, key string) ([]Symbol, tsParseResult, bool) {
	res := tsParseResult{key: key}
	tl := tsRegistry[key]
	if tl == nil {
		return nil, res, false
	}

	p := tsParserPool.Get().(*tree_sitter.Parser)
	defer tsParserPool.Put(p)
	if err := p.SetLanguage(tl.lang); err != nil {
		return nil, res, false
	}

	tree := p.Parse(source, nil)
	if tree == nil {
		return nil, res, false
	}
	defer tree.Close()

	qc := tree_sitter.NewQueryCursor()
	defer qc.Close()

	root := tree.RootNode()
	// HasError covers ERROR nodes and MISSING nodes anywhere in the tree.
	res.clean = !root.HasError()
	names := tl.query.CaptureNames()
	matches := qc.Matches(tl.query, root, source)

	var syms []Symbol
	at := make(map[string]int) // name+line -> index in syms
	for {
		m := matches.Next()
		if m == nil {
			break
		}

		var nameNode, defNode *tree_sitter.Node
		var kind string
		for i := range m.Captures {
			c := &m.Captures[i]
			capName := names[c.Index]
			switch {
			case capName == "def":
				defNode = &c.Node
			case strings.HasPrefix(capName, "_"):
				// query-internal helper capture (predicate target); ignore
			default:
				kind = capName
				nameNode = &c.Node
			}
		}
		if nameNode == nil {
			continue
		}

		name := nameNode.Utf8Text(source)
		if name == "" || name == "_" {
			continue
		}
		line := int(nameNode.StartPosition().Row) + 1

		// Several patterns can match the same declaration — e.g. a top-level
		// arrow-function `const` matches both the @function rule and the broad
		// @constant rule. Keep the most specific kind for a given name+line.
		dedupKey := name + ":" + itoa(line)
		sym := Symbol{
			File:      relPath,
			Name:      name,
			Kind:      kind,
			Line:      line,
			Signature: trimSig(tsSignature(defNode, nameNode, source)),
			Extractor: ExtractorTreeSitter,
		}
		if idx, ok := at[dedupKey]; ok {
			if kindRank(kind) < kindRank(syms[idx].Kind) {
				syms[idx] = sym
			}
			continue
		}
		at[dedupKey] = len(syms)
		syms = append(syms, sym)
	}

	return syms, res, true
}

// kindRank orders symbol kinds by specificity so the de-dup keeps the most
// meaningful label when several patterns hit the same name+line. Lower is more
// specific; "constant"/"var" are the generic fallbacks that lose to anything.
func kindRank(kind string) int {
	switch kind {
	case "var":
		return 4
	case "constant":
		return 3
	case "type", "property":
		return 2
	default:
		return 1
	}
}

// tsSignature builds a one-line signature from the declaration node, cutting
// off the body block when there is one (so a multi-line function signature
// collapses to just its header). Falls back to the line holding the name.
func tsSignature(def, name *tree_sitter.Node, source []byte) string {
	if def != nil {
		start := def.StartByte()
		end := def.EndByte()
		if body := def.ChildByFieldName("body"); body != nil {
			end = body.StartByte()
		}
		if end > start && int(end) <= len(source) {
			return collapseWS(string(source[start:end]))
		}
	}
	if name != nil {
		return collapseWS(lineAtByte(source, name.StartByte()))
	}
	return ""
}

// collapseWS replaces any run of whitespace (including newlines) with a single
// space and trims the result.
func collapseWS(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// lineAtByte returns the source line containing the given byte offset.
func lineAtByte(source []byte, off uint) string {
	if int(off) > len(source) {
		return ""
	}
	start := strings.LastIndexByte(string(source[:off]), '\n') + 1
	end := strings.IndexByte(string(source[off:]), '\n')
	if end < 0 {
		return string(source[start:])
	}
	return string(source[start : int(off)+end])
}
