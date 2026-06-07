package index

import (
	"fmt"
	"strings"
	"sync"
	"unsafe"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
)

// Tree-sitter based symbol extraction.
//
// This is the preferred path: it parses the real grammar instead of matching
// regexes line-by-line, so it gets accurate names, correct kinds, multi-line
// signatures, and never mistakes a match inside a string or comment for a
// declaration. Languages without a registered grammar fall back to the regex
// patterns in symbols.go.
//
// Each grammar registers a query whose capture names are the symbol kind
// (e.g. @function, @method, @struct). A separate @def capture marks the whole
// declaration node so we can build a clean signature. Adding a language is just
// registering its grammar + a query; no Go code changes.

type tsLang struct {
	lang  *tree_sitter.Language
	query *tree_sitter.Query
}

var tsRegistry = map[string]*tsLang{}

// tsParserPool reuses parsers across the concurrent extraction goroutines.
// Parsers are not safe for concurrent use, but Query values are immutable and
// shared freely; each goroutine gets its own parser and query cursor.
var tsParserPool = sync.Pool{New: func() any { return tree_sitter.NewParser() }}

func registerTSLang(name string, langPtr unsafe.Pointer, query string) {
	l := tree_sitter.NewLanguage(langPtr)
	q, qerr := tree_sitter.NewQuery(l, query)
	if qerr != nil {
		// A malformed query is a programming error in this package; fail loud
		// so it's caught by tests rather than silently disabling a language.
		panic(fmt.Sprintf("recon: invalid tree-sitter query for %q: %v", name, qerr))
	}
	tsRegistry[name] = &tsLang{lang: l, query: q}
}

// hasTSLang reports whether a tree-sitter grammar is registered for lang.
func hasTSLang(lang string) bool {
	_, ok := tsRegistry[lang]
	return ok
}

// extractSymbolsTS parses source with the registered grammar for lang and
// returns its symbols. The bool is false when no grammar is registered (the
// caller should fall back to regex) or the parse fails outright.
func extractSymbolsTS(source []byte, relPath, lang string) ([]Symbol, bool) {
	tl := tsRegistry[lang]
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

	root := tree.RootNode()
	names := tl.query.CaptureNames()
	matches := qc.Matches(tl.query, root, source)

	var syms []Symbol
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
			if capName == "def" {
				defNode = &c.Node
				continue
			}
			kind = capName
			nameNode = &c.Node
		}
		if nameNode == nil {
			continue
		}

		name := nameNode.Utf8Text(source)
		if name == "" || name == "_" {
			continue
		}

		syms = append(syms, Symbol{
			File:      relPath,
			Name:      name,
			Kind:      kind,
			Line:      int(nameNode.StartPosition().Row) + 1,
			Signature: trimSig(tsSignature(defNode, nameNode, source)),
		})
	}

	return syms, true
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

func init() {
	registerTSLang("go", tree_sitter_go.Language(), goTSQuery)
}

// goTSQuery captures top-level and method declarations. The capture name on the
// identifier is the symbol kind; @def marks the whole declaration for the
// signature. The three type patterns are mutually exclusive by body kind so a
// struct/interface is never also reported as a plain "type".
const goTSQuery = `
(function_declaration name: (identifier) @function) @def
(method_declaration name: (field_identifier) @method) @def

(source_file (type_declaration
  (type_spec name: (type_identifier) @struct type: (struct_type))) @def)
(source_file (type_declaration
  (type_spec name: (type_identifier) @interface type: (interface_type))) @def)
(source_file (type_declaration
  (type_spec name: (type_identifier) @type
    type: [
      (type_identifier)
      (qualified_type)
      (pointer_type)
      (map_type)
      (slice_type)
      (array_type)
      (channel_type)
      (function_type)
      (generic_type)
    ])) @def)
(source_file (type_declaration
  (type_alias name: (type_identifier) @type)) @def)

(source_file (const_declaration (const_spec name: (identifier) @constant)))
(source_file (var_declaration (var_spec name: (identifier) @var)))
`
