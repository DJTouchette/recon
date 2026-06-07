package index

import (
	"embed"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// Tree-sitter based import extraction.
//
// Only the *extraction* of raw import specifiers moves to tree-sitter; the
// per-language resolution of those specifiers to local files stays in
// depgraph.go. Real parsing fixes the things a per-line regex can't: multi-line
// import statements, `export … from` re-exports (barrel files), and import-like
// text inside strings or comments.
//
// Each language with an import query has queries/imports/<lang>.scm capturing
// the specifier string as @path. Languages without one stay on the regex path.

//go:embed queries/imports/*.scm
var importQueryFS embed.FS

var tsImportRegistry = map[string]*tsLang{}

func init() {
	for _, g := range tsGrammars {
		src, err := importQueryFS.ReadFile("queries/imports/" + g.lang + ".scm")
		if err != nil {
			continue // no import query for this language → regex fallback
		}
		l := tree_sitter.NewLanguage(g.langPtr)
		q, qerr := tree_sitter.NewQuery(l, string(src))
		if qerr != nil {
			continue
		}
		tsImportRegistry[g.lang] = &tsLang{lang: l, query: q}
	}
}

// hasTSImports reports whether tree-sitter import extraction is available for lang.
func hasTSImports(lang string) bool {
	_, ok := tsImportRegistry[lang]
	return ok
}

// tsImportSpecs parses source and returns the de-duplicated raw import
// specifiers (module strings / relative paths) captured as @path. The bool is
// false when no import query is registered or the parse fails.
func tsImportSpecs(source []byte, lang string) ([]string, bool) {
	tl := tsImportRegistry[lang]
	if tl == nil {
		return nil, false
	}

	p := tsParserPool.Get().(*tree_sitter.Parser)
	defer tsParserPool.Put(p)
	if err := p.SetLanguage(tl.lang); err != nil {
		return nil, false
	}

	tree := p.Parse(source, nil)
	if tree == nil {
		return nil, false
	}
	defer tree.Close()

	qc := tree_sitter.NewQueryCursor()
	defer qc.Close()

	names := tl.query.CaptureNames()
	matches := qc.Matches(tl.query, tree.RootNode(), source)

	var specs []string
	seen := make(map[string]bool)
	for {
		m := matches.Next()
		if m == nil {
			break
		}
		for i := range m.Captures {
			c := &m.Captures[i]
			if names[c.Index] != "path" {
				continue
			}
			s := c.Node.Utf8Text(source)
			if s != "" && !seen[s] {
				seen[s] = true
				specs = append(specs, s)
			}
		}
	}
	return specs, true
}

// tsImportEachMatch runs the import query for lang and invokes fn once per match
// with a map of capture-name → captured text. Used by languages (Ruby, Rust)
// whose specifiers carry a directive that a flat @path list can't express.
// Returns false when no query is registered or the parse fails.
func tsImportEachMatch(source []byte, lang string, fn func(caps map[string]string)) bool {
	tl := tsImportRegistry[lang]
	if tl == nil {
		return false
	}

	p := tsParserPool.Get().(*tree_sitter.Parser)
	defer tsParserPool.Put(p)
	if err := p.SetLanguage(tl.lang); err != nil {
		return false
	}

	tree := p.Parse(source, nil)
	if tree == nil {
		return false
	}
	defer tree.Close()

	qc := tree_sitter.NewQueryCursor()
	defer qc.Close()

	names := tl.query.CaptureNames()
	matches := qc.Matches(tl.query, tree.RootNode(), source)
	for {
		m := matches.Next()
		if m == nil {
			break
		}
		caps := make(map[string]string, len(m.Captures))
		for i := range m.Captures {
			c := &m.Captures[i]
			caps[names[c.Index]] = c.Node.Utf8Text(source)
		}
		fn(caps)
	}
	return true
}
