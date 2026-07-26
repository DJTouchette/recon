package index

import (
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Regex-based symbol extraction — the fallback for languages with no
// tree-sitter grammar (currently Swift, Dart and Elixir; the other pattern sets
// exist only as a safety net if a grammar's query stops compiling).
//
// This path is fundamentally approximate and every symbol it produces is
// tagged ExtractorRegex so consumers can say so. Within that limit it does
// three things a naive line scanner does not:
//
//  1. It masks comments and string literals before matching, so a declaration
//     inside a block comment, a heredoc or a string is not reported as code.
//  2. It tracks brace depth, so a member of a type is a "method" and a function
//     nested inside another function body is not reported at all.
//  3. It joins continuation lines, so a declaration whose parameter list spans
//     several lines gets a complete signature instead of a truncated one.

// --- Source reading and text decoding ---

var errBinarySource = errors.New("binary content")

// readSource reads a file and normalises it to UTF-8 text. limit caps how many
// bytes are read (0 means the whole file).
//
// Decoding matters more than it looks: Visual Studio writes C# as UTF-16 with a
// BOM, and passing those bytes to a parser that expects UTF-8 yields zero
// symbols with no error at all — a confident, wrong "this file declares
// nothing". Binary content is rejected outright rather than being scanned for
// declarations or stored as a preview.
func readSource(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var data []byte
	if limit > 0 {
		data, err = io.ReadAll(io.LimitReader(f, limit))
	} else {
		data, err = io.ReadAll(f)
	}
	if err != nil {
		return nil, err
	}

	decoded, ok := decodeText(data)
	if !ok {
		return nil, errBinarySource
	}
	return decoded, nil
}

const (
	bomUTF8    = "\xef\xbb\xbf"
	bomUTF16LE = "\xff\xfe"
	bomUTF16BE = "\xfe\xff"
)

// decodeText converts data to UTF-8, handling the byte-order marks editors
// actually emit. It reports false for content that is not text.
func decodeText(data []byte) ([]byte, bool) {
	switch {
	case strings.HasPrefix(string(data), bomUTF8):
		data = data[len(bomUTF8):]

	case strings.HasPrefix(string(data), bomUTF16LE):
		// A UTF-32LE BOM starts with the UTF-16LE BOM followed by two NULs.
		if len(data) >= 4 && data[2] == 0 && data[3] == 0 {
			return nil, false
		}
		return utf16ToUTF8(data[2:], true), true

	case strings.HasPrefix(string(data), bomUTF16BE):
		return utf16ToUTF8(data[2:], false), true

	default:
		if le, ok := sniffUTF16(data); ok {
			return utf16ToUTF8(data, le), true
		}
	}

	if !isTextual(data) {
		return nil, false
	}
	return data, true
}

// sniffUTF16 detects BOM-less UTF-16 holding ASCII text, which is what a
// BOM-stripped Windows-authored file looks like. It is deliberately strict —
// every byte in one parity class must be NUL — so arbitrary binary is never
// mistaken for text.
func sniffUTF16(data []byte) (littleEndian bool, ok bool) {
	probe := data
	if len(probe) > 512 {
		probe = probe[:512]
	}
	if len(probe) < 16 || len(probe)%2 != 0 {
		return false, false
	}
	evenNul, oddNul := 0, 0
	for i := 0; i < len(probe); i += 2 {
		if probe[i] == 0 {
			evenNul++
		}
		if probe[i+1] == 0 {
			oddNul++
		}
	}
	pairs := len(probe) / 2
	if oddNul == pairs && evenNul == 0 {
		return true, true // ASCII in the low byte: little endian
	}
	if evenNul == pairs && oddNul == 0 {
		return false, true
	}
	return false, false
}

// utf16ToUTF8 decodes UTF-16 code units to UTF-8, tolerating a trailing odd
// byte and unpaired surrogates rather than failing.
func utf16ToUTF8(data []byte, littleEndian bool) []byte {
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		if littleEndian {
			units = append(units, uint16(data[i])|uint16(data[i+1])<<8)
		} else {
			units = append(units, uint16(data[i])<<8|uint16(data[i+1]))
		}
	}
	return []byte(string(utf16.Decode(units)))
}

// isTextual reports whether data looks like text: no NUL bytes in the leading
// probe, and valid UTF-8. This is the same null-byte heuristic grep uses.
func isTextual(data []byte) bool {
	probe := data
	if len(probe) > 512 {
		probe = probe[:512]
	}
	for _, b := range probe {
		if b == 0 {
			return false
		}
	}
	// Latin-1 and other 8-bit encodings are not valid UTF-8 but are still
	// perfectly scannable text, so only reject content that is both invalid
	// UTF-8 and full of control bytes.
	if utf8.Valid(probe) {
		return true
	}
	ctrl := 0
	for _, b := range probe {
		if b < 0x09 || (b > 0x0d && b < 0x20) {
			ctrl++
		}
	}
	return ctrl*10 < len(probe)
}

// splitLines splits data into lines, dropping the line terminators and
// tolerating CRLF. Unlike bufio.Scanner it has no maximum line length, so a
// minified bundle with one 80KB line does not silently truncate the rest of the
// file.
func splitLines(data []byte) []string {
	s := string(data)
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	// A trailing newline produces a final empty element; drop it.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// --- Comment and string masking ---

// maskConfig describes a language's comment and string syntax well enough to
// blank out everything that is not code.
type maskConfig struct {
	lineComment []string
	blockOpen   string
	blockClose  string
	blockNests  bool     // Swift and Dart allow nested /* /* */ */
	quotes      []byte   // single-line string delimiters
	triples     []string // multi-line string / heredoc delimiters
	escape      bool     // backslash escapes inside strings
	braces      bool     // whether {} nesting is meaningful for scoping
}

var maskConfigs = map[string]maskConfig{
	"swift": {
		lineComment: []string{"//"},
		blockOpen:   "/*", blockClose: "*/", blockNests: true,
		quotes: []byte{'"'}, triples: []string{`"""`},
		escape: true, braces: true,
	},
	"dart": {
		lineComment: []string{"//"},
		blockOpen:   "/*", blockClose: "*/", blockNests: true,
		quotes: []byte{'"', '\''}, triples: []string{`"""`, "'''"},
		escape: true, braces: true,
	},
	"elixir": {
		lineComment: []string{"#"},
		quotes:      []byte{'"'}, triples: []string{`"""`},
		escape: true,
	},
	// Razor code blocks. This key is deliberately not a recon language: it is
	// the masking profile for the body of an @code/@functions block, which is
	// plain C#. Brace depth is what separates a member of the block from a
	// statement inside one of its bodies.
	razorCodeLang: {
		lineComment: []string{"//"},
		blockOpen:   "/*", blockClose: "*/",
		quotes: []byte{'"', '\''},
		escape: true, braces: true,
	},
}

// litMasker walks a file line by line, returning each line with its comment and
// string-literal spans replaced by spaces. Positions are preserved so
// column-anchored patterns still work, and the caller keeps the raw line for
// signatures.
type litMasker struct {
	cfg        maskConfig
	blockDepth int
	heredoc    string // open multi-line string delimiter, "" when none
}

func newMasker(lang string) *litMasker {
	return &litMasker{cfg: maskConfigs[lang]}
}

// inLiteral reports whether the masker is currently inside a multi-line
// construct (block comment or heredoc), meaning the next line starts in
// non-code.
func (m *litMasker) inLiteral() bool { return m.blockDepth > 0 || m.heredoc != "" }

func (m *litMasker) mask(line string) string {
	out := []byte(line)
	blank := func(a, b int) {
		if a < 0 {
			a = 0
		}
		if b > len(out) {
			b = len(out)
		}
		for k := a; k < b; k++ {
			if out[k] != '\t' {
				out[k] = ' '
			}
		}
	}

	i := 0
	for i < len(line) {
		if m.heredoc != "" {
			j := strings.Index(line[i:], m.heredoc)
			if j < 0 {
				blank(i, len(line))
				return string(out)
			}
			end := i + j + len(m.heredoc)
			blank(i, end)
			m.heredoc = ""
			i = end
			continue
		}

		if m.blockDepth > 0 {
			cl := strings.Index(line[i:], m.cfg.blockClose)
			op := -1
			if m.cfg.blockNests {
				op = strings.Index(line[i:], m.cfg.blockOpen)
			}
			if cl < 0 && op < 0 {
				blank(i, len(line))
				return string(out)
			}
			if op >= 0 && (cl < 0 || op < cl) {
				m.blockDepth++
				end := i + op + len(m.cfg.blockOpen)
				blank(i, end)
				i = end
				continue
			}
			m.blockDepth--
			end := i + cl + len(m.cfg.blockClose)
			blank(i, end)
			i = end
			continue
		}

		rest := line[i:]

		if hasAnyPrefix(rest, m.cfg.lineComment) {
			blank(i, len(line))
			return string(out)
		}

		if m.cfg.blockOpen != "" && strings.HasPrefix(rest, m.cfg.blockOpen) {
			m.blockDepth = 1
			end := i + len(m.cfg.blockOpen)
			blank(i, end)
			i = end
			continue
		}

		if t := matchPrefix(rest, m.cfg.triples); t != "" {
			after := i + len(t)
			if j := strings.Index(line[after:], t); j >= 0 {
				end := after + j + len(t)
				blank(i, end)
				i = end
			} else {
				m.heredoc = t
				blank(i, len(line))
				return string(out)
			}
			continue
		}

		if containsByte(m.cfg.quotes, rest[0]) {
			q := rest[0]
			j := i + 1
			for j < len(line) {
				if m.cfg.escape && line[j] == '\\' {
					j += 2
					continue
				}
				if line[j] == q {
					j++
					break
				}
				j++
			}
			blank(i, j)
			i = j
			continue
		}

		i++
	}
	return string(out)
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func matchPrefix(s string, options []string) string {
	for _, p := range options {
		if p != "" && strings.HasPrefix(s, p) {
			return p
		}
	}
	return ""
}

func containsByte(set []byte, b byte) bool {
	for _, c := range set {
		if c == b {
			return true
		}
	}
	return false
}

// --- The regex extractor ---

// maxContinuationLines caps how far a declaration's signature may be joined
// across physical lines.
const maxContinuationLines = 8

func extractSymbolsRegex(data []byte, relPath, lang string, patterns []symbolPattern) []Symbol {
	// Razor is markup, not a language whose declarations sit on lines of their
	// own; what it declares is directives plus the members of a @code block. It
	// has its own extractor, which calls back into this one under the
	// razorCodeLang masking profile for the block bodies.
	if lang == razorLang {
		return extractRazorSymbols(data, relPath)
	}

	lines := splitLines(data)
	if len(lines) == 0 {
		return nil
	}

	cfg := maskConfigs[lang]
	m := newMasker(lang)

	// Mask every line up front; depth tracking needs the masked text too.
	masked := make([]string, len(lines))
	startsInLiteral := make([]bool, len(lines))
	for i, l := range lines {
		startsInLiteral[i] = m.inLiteral()
		masked[i] = m.mask(l)
	}

	var symbols []Symbol
	depth := 0
	elixirModule := ""

	for i := 0; i < len(lines); i++ {
		lineDepth := depth
		if cfg.braces {
			depth += braceDelta(masked[i])
		}

		if strings.TrimSpace(masked[i]) == "" {
			continue
		}

		// Join continuation lines so a multi-line declaration is matched and
		// signed as a whole rather than being missed or truncated.
		span := 1
		joinedMasked, joinedRaw := masked[i], strings.TrimSpace(lines[i])
		if openDelims(masked[i]) > 0 {
			for span < maxContinuationLines && i+span < len(lines) {
				joinedMasked += " " + strings.TrimSpace(masked[i+span])
				joinedRaw += " " + strings.TrimSpace(lines[i+span])
				span++
				if openDelims(joinedMasked) <= 0 {
					break
				}
			}
		}

		if lang == "elixir" {
			if mod := elixirModuleName(joinedMasked); mod != "" {
				elixirModule = mod
			}
			if s, ok := elixirStruct(joinedMasked, relPath, i+1, joinedRaw, elixirModule); ok {
				symbols = append(symbols, s)
				continue
			}
		}

		for _, p := range patterns {
			sub := p.regex.FindStringSubmatch(joinedMasked)
			if sub == nil || len(sub) < 2 {
				continue
			}
			name := sub[1]
			if !validSymbolName(name) {
				continue
			}

			kind := p.kind
			if cfg.braces {
				// depth 0 = top level, 1 = member of a type, 2+ = a local
				// declaration inside a function body, which is not part of the
				// file's public shape and used to leak out as a bogus method.
				if lineDepth >= 2 {
					break
				}
				if lineDepth >= 1 && p.nested != "" {
					kind = p.nested
				}
			}

			symbols = append(symbols, Symbol{
				File:      relPath,
				Name:      name,
				Kind:      kind,
				Line:      i + 1,
				Signature: trimSig(joinedRaw),
				Extractor: ExtractorRegex,
			})
			break // one match per declaration
		}

		// Skip the continuation lines that were folded into this declaration.
		if span > 1 {
			for k := 1; k < span; k++ {
				if cfg.braces {
					depth += braceDelta(masked[i+k])
				}
			}
			i += span - 1
		}
	}

	return symbols
}

// validSymbolName filters out names that are never real declarations.
func validSymbolName(name string) bool {
	switch name {
	case "", "_":
		return false
	case "unquote":
		// `def unquote(:name)(args)` is Elixir metaprogramming: the declared
		// name is computed, and reporting "unquote" as a function invents a
		// symbol that does not exist.
		return false
	}
	return true
}

// braceDelta returns the net change in brace nesting for a masked line.
func braceDelta(masked string) int {
	d := 0
	for i := 0; i < len(masked); i++ {
		switch masked[i] {
		case '{':
			d++
		case '}':
			d--
		}
	}
	return d
}

// openDelims returns the count of unclosed ( and [ on a masked line — the
// signal that a declaration continues onto the next line.
func openDelims(masked string) int {
	d := 0
	for i := 0; i < len(masked); i++ {
		switch masked[i] {
		case '(', '[':
			d++
		case ')', ']':
			d--
		}
	}
	return d
}

var (
	elixirModuleRe = regexp.MustCompile(`^\s*defmodule\s+([\w.]+)`)
	elixirStructRe = regexp.MustCompile(`^\s*defstruct\b`)
)

func elixirModuleName(masked string) string {
	if m := elixirModuleRe.FindStringSubmatch(masked); m != nil {
		return m[1]
	}
	return ""
}

// elixirStruct turns a `defstruct` into a symbol named after its enclosing
// module, which is how the struct is actually referred to (`%MyModule{}`). The
// old pattern had no capture group, so it could never emit anything at all.
func elixirStruct(masked, relPath string, line int, raw, module string) (Symbol, bool) {
	if module == "" || !elixirStructRe.MatchString(masked) {
		return Symbol{}, false
	}
	return Symbol{
		File:      relPath,
		Name:      module,
		Kind:      "struct",
		Line:      line,
		Signature: trimSig(raw),
		Extractor: ExtractorRegex,
	}, true
}

// --- Compiled patterns per language ---

type symbolPattern struct {
	regex *regexp.Regexp
	kind  string
	// nested is the kind to use when the declaration sits inside a type body
	// (brace depth 1). Empty means the kind does not change with nesting.
	nested string
}

type rawPattern struct {
	pattern string
	kind    string
	nested  string
}

func compilePatterns(raw []rawPattern) []symbolPattern {
	compiled := make([]symbolPattern, len(raw))
	for i, r := range raw {
		compiled[i] = symbolPattern{
			regex:  regexp.MustCompile(r.pattern),
			kind:   r.kind,
			nested: r.nested,
		}
	}
	return compiled
}

var langPatterns = map[string][]symbolPattern{
	"go":         goPatterns,
	"typescript": tsPatterns,
	"javascript": tsPatterns, // shares TS patterns
	"csharp":     csharpPatterns,
	razorLang:    razorMemberPatterns,
	"java":       javaPatterns,
	"kotlin":     javaPatterns, // close enough
	"python":     pythonPatterns,
	"rust":       rustPatterns,
	"ruby":       rubyPatterns,
	"elixir":     elixirPatterns,
	"php":        phpPatterns,
	"swift":      swiftPatterns,
	"dart":       dartPatterns,
	"scala":      scalaPatterns,
}

func patternsForLang(lang string) []symbolPattern {
	return langPatterns[lang]
}

var goPatterns = compilePatterns([]rawPattern{
	{pattern: `^func\s+(\w+)\s*\(`, kind: "function"},
	{pattern: `^func\s+\([^)]+\)\s+(\w+)\s*\(`, kind: "method"},
	{pattern: `^type\s+(\w+)\s+struct\b`, kind: "struct"},
	{pattern: `^type\s+(\w+)\s+interface\b`, kind: "interface"},
	{pattern: `^type\s+(\w+)\s+`, kind: "type"},
	{pattern: `^var\s+(\w+)\s+`, kind: "var"},
	{pattern: `^const\s+(\w+)\s+`, kind: "constant"},
})

var tsPatterns = compilePatterns([]rawPattern{
	{pattern: `^export\s+(?:async\s+)?function\s+(\w+)`, kind: "function"},
	{pattern: `^export\s+class\s+(\w+)`, kind: "class"},
	{pattern: `^export\s+abstract\s+class\s+(\w+)`, kind: "class"},
	{pattern: `^export\s+interface\s+(\w+)`, kind: "interface"},
	{pattern: `^export\s+type\s+(\w+)`, kind: "type"},
	{pattern: `^export\s+const\s+(\w+)`, kind: "constant"},
	{pattern: `^export\s+enum\s+(\w+)`, kind: "enum"},
	{pattern: `^export\s+default\s+(?:class|function)\s+(\w+)`, kind: "function"},
	{pattern: `^\s*(?:async\s+)?function\s+(\w+)`, kind: "function"},
	{pattern: `^\s*class\s+(\w+)`, kind: "class"},
	{pattern: `^\s*interface\s+(\w+)`, kind: "interface"},
	{pattern: `^\s*(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?\(`, kind: "function"},       // arrow fn
	{pattern: `^\s*(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?function`, kind: "function"}, // fn expression
})

var csharpPatterns = compilePatterns([]rawPattern{
	{pattern: `(?:public|internal|protected|private)\s+(?:static\s+)?(?:partial\s+)?(?:abstract\s+)?class\s+(\w+)`, kind: "class"},
	{pattern: `(?:public|internal|protected|private)\s+(?:static\s+)?interface\s+(\w+)`, kind: "interface"},
	{pattern: `(?:public|internal|protected|private)\s+(?:static\s+)?enum\s+(\w+)`, kind: "enum"},
	{pattern: `(?:public|internal|protected|private)\s+(?:static\s+)?struct\s+(\w+)`, kind: "struct"},
	{pattern: `(?:public|internal|protected|private)\s+(?:static\s+)?record\s+(\w+)`, kind: "class"},
	// Methods — match return-type + name + paren
	{pattern: `(?:public|internal|protected|private)\s+(?:static\s+)?(?:virtual\s+)?(?:override\s+)?(?:async\s+)?(?:[\w<>\[\],\s]+?)\s+(\w+)\s*\(`, kind: "method"},
	// Properties
	{pattern: `(?:public|internal|protected|private)\s+(?:static\s+)?(?:[\w<>\[\]?]+)\s+(\w+)\s*\{\s*get`, kind: "property"},
	// Delegate
	{pattern: `(?:public|internal|protected|private)\s+delegate\s+\S+\s+(\w+)\s*\(`, kind: "delegate"},
})

var javaPatterns = compilePatterns([]rawPattern{
	{pattern: `(?:public|protected|private)\s+(?:static\s+)?(?:abstract\s+)?(?:final\s+)?class\s+(\w+)`, kind: "class"},
	{pattern: `(?:public|protected|private)\s+interface\s+(\w+)`, kind: "interface"},
	{pattern: `(?:public|protected|private)\s+enum\s+(\w+)`, kind: "enum"},
	{pattern: `(?:public|protected|private)\s+(?:static\s+)?(?:final\s+)?(?:synchronized\s+)?(?:[\w<>\[\]]+)\s+(\w+)\s*\(`, kind: "method"},
	{pattern: `@interface\s+(\w+)`, kind: "annotation"},
})

var pythonPatterns = compilePatterns([]rawPattern{
	{pattern: `^class\s+(\w+)`, kind: "class"},
	{pattern: `^def\s+(\w+)`, kind: "function"},
	{pattern: `^async\s+def\s+(\w+)`, kind: "function"},
	{pattern: `^\s{4}def\s+(\w+)`, kind: "method"},
	{pattern: `^\s{4}async\s+def\s+(\w+)`, kind: "method"},
	{pattern: `^([A-Z][A-Z_0-9]+)\s*=`, kind: "constant"}, // UPPER_SNAKE constants
})

var rustPatterns = compilePatterns([]rawPattern{
	{pattern: `^pub(?:\(crate\))?\s+(?:async\s+)?fn\s+(\w+)`, kind: "function"},
	{pattern: `^pub(?:\(crate\))?\s+struct\s+(\w+)`, kind: "struct"},
	{pattern: `^pub(?:\(crate\))?\s+enum\s+(\w+)`, kind: "enum"},
	{pattern: `^pub(?:\(crate\))?\s+trait\s+(\w+)`, kind: "trait"},
	{pattern: `^pub(?:\(crate\))?\s+type\s+(\w+)`, kind: "type"},
	{pattern: `^pub(?:\(crate\))?\s+const\s+(\w+)`, kind: "constant"},
	{pattern: `^pub(?:\(crate\))?\s+static\s+(\w+)`, kind: "constant"},
	{pattern: `^\s+pub(?:\(crate\))?\s+(?:async\s+)?fn\s+(\w+)`, kind: "method"},
	{pattern: `^\s+fn\s+(\w+)`, kind: "method"}, // impl methods
})

var rubyPatterns = compilePatterns([]rawPattern{
	{pattern: `^class\s+(\w+)`, kind: "class"},
	{pattern: `^module\s+(\w+)`, kind: "module"},
	{pattern: `^\s+def\s+self\.(\w+[?!]?)`, kind: "function"},
	{pattern: `^\s+def\s+(\w+[?!]?)`, kind: "method"},
	{pattern: `^\s+attr_(?:reader|writer|accessor)\s+:(\w+)`, kind: "property"},
	{pattern: `([A-Z][A-Z_0-9]+)\s*=`, kind: "constant"},
})

// Elixir: `def` may sit at any indentation (inside `defmodule`, inside a
// `quote`, or at the top of a script), function names may end in ? or !, and
// the full family of def* forms declares something worth indexing. `defstruct`
// is handled separately because the name it declares is the enclosing module's.
var elixirPatterns = compilePatterns([]rawPattern{
	{pattern: `^\s*defmodule\s+([\w.]+)`, kind: "module"},
	{pattern: `^\s*defprotocol\s+([\w.]+)`, kind: "interface"},
	{pattern: `^\s*defimpl\s+([\w.]+)`, kind: "module"},
	{pattern: `^\s*defexception\b.*?:(\w+)`, kind: "struct"},
	{pattern: `^\s*defmacrop?\s+(\w+[?!]?)`, kind: "macro"},
	{pattern: `^\s*defguardp?\s+(\w+[?!]?)`, kind: "macro"},
	{pattern: `^\s*defdelegate\s+(\w+[?!]?)`, kind: "function"},
	{pattern: `^\s*defp?\s+(\w+[?!]?)`, kind: "function"},
})

var phpPatterns = compilePatterns([]rawPattern{
	{pattern: `(?:abstract\s+)?class\s+(\w+)`, kind: "class"},
	{pattern: `interface\s+(\w+)`, kind: "interface"},
	{pattern: `trait\s+(\w+)`, kind: "trait"},
	{pattern: `(?:public|protected|private)\s+(?:static\s+)?function\s+(\w+)`, kind: "method"},
	{pattern: `^function\s+(\w+)`, kind: "function"},
})

// Dart types, constructors, accessors and fields. Order matters: the more
// specific forms (constructor, getter, setter) must be tried before the general
// "type name(" method pattern, which would otherwise claim them.
var dartPatterns = compilePatterns([]rawPattern{
	{pattern: `^\s*abstract\s+class\s+(\w+)`, kind: "class"},
	{pattern: `^\s*class\s+(\w+)`, kind: "class"},
	{pattern: `^\s*mixin\s+(\w+)`, kind: "class"},
	{pattern: `^\s*enum\s+(\w+)`, kind: "enum"},
	{pattern: `^\s*extension\s+(\w+)`, kind: "type"},
	{pattern: `^\s*typedef\s+(\w+)`, kind: "type"},
	// Constructors: `Foo(`, `const Foo(`, `factory Foo.named(`. The leading
	// capital distinguishes them from method calls.
	{pattern: `^\s+(?:const\s+|factory\s+)?([A-Z]\w*(?:\.\w+)?)\s*\(`, kind: "constructor"},
	// Getters and setters.
	{pattern: `^\s*(?:static\s+)?(?:[\w<>?,\[\] ]+\s+)?get\s+(\w+)`, kind: "property"},
	{pattern: `^\s*(?:static\s+)?(?:void\s+)?set\s+(\w+)\s*\(`, kind: "property"},
	// Arrow-bodied and block-bodied functions/methods.
	{pattern: `^\s*(?:static\s+|external\s+|@\w+\s+)*(?:Future|Stream|void|int|double|num|bool|String|dynamic|var|[\w<>?,\[\]]+)\s+(\w+)\s*\(`, kind: "function", nested: "method"},
	// Fields: `final int count = 0;`, `static const foo = 1;`, `String? name;`.
	{pattern: `^\s*(?:static\s+)?(?:final|const|late|var)\s+(?:[\w<>?,\[\]]+\s+)?(\w+)\s*[=;]`, kind: "constant", nested: "property"},
	{pattern: `^\s+(?:[\w<>?,\[\]]+)\s+(\w+)\s*[=;]`, kind: "property"},
})

// Swift declarations. The old patterns anchored on `public|open|internal` at
// line start or a leading-whitespace `func`, so every top-level unmodified
// `func` and every `private func` in the language was invisible. These accept
// any run of modifiers and attributes instead, and rely on brace depth rather
// than indentation to tell a method from a function.
var swiftPatterns = compilePatterns([]rawPattern{
	{pattern: `^\s*(?:(?:@\w+(?:\([^)]*\))?|public|open|internal|fileprivate|private|final|static|class|override|mutating|nonmutating|convenience|required|dynamic|nonisolated|indirect|lazy|weak|unowned|optional)\s+)*func\s+(\w+)`, kind: "function", nested: "method"},
	{pattern: `^\s*(?:(?:@\w+(?:\([^)]*\))?|public|open|internal|fileprivate|private|final)\s+)*class\s+(\w+)`, kind: "class"},
	{pattern: `^\s*(?:(?:@\w+(?:\([^)]*\))?|public|open|internal|fileprivate|private|final)\s+)*actor\s+(\w+)`, kind: "class"},
	{pattern: `^\s*(?:(?:@\w+(?:\([^)]*\))?|public|open|internal|fileprivate|private|final)\s+)*struct\s+(\w+)`, kind: "struct"},
	{pattern: `^\s*(?:(?:@\w+(?:\([^)]*\))?|public|open|internal|fileprivate|private|indirect)\s+)*enum\s+(\w+)`, kind: "enum"},
	{pattern: `^\s*(?:(?:@\w+(?:\([^)]*\))?|public|open|internal|fileprivate|private)\s+)*protocol\s+(\w+)`, kind: "interface"},
	{pattern: `^\s*(?:(?:@\w+(?:\([^)]*\))?|public|open|internal|fileprivate|private)\s+)*extension\s+(\w+)`, kind: "type"},
	{pattern: `^\s*(?:(?:@\w+(?:\([^)]*\))?|public|open|internal|fileprivate|private)\s+)*typealias\s+(\w+)`, kind: "type"},
	{pattern: `^\s*(?:(?:@\w+(?:\([^)]*\))?|public|open|internal|fileprivate|private|static|class|final|lazy|weak|unowned)\s+)*(?:var|let)\s+(\w+)`, kind: "constant", nested: "property"},
})

var scalaPatterns = compilePatterns([]rawPattern{
	{pattern: `(?:abstract\s+)?class\s+(\w+)`, kind: "class"},
	{pattern: `(?:sealed\s+)?trait\s+(\w+)`, kind: "trait"},
	{pattern: `object\s+(\w+)`, kind: "module"},
	{pattern: `case\s+class\s+(\w+)`, kind: "class"},
	{pattern: `case\s+object\s+(\w+)`, kind: "module"},
	{pattern: `def\s+(\w+)`, kind: "function"},
	{pattern: `type\s+(\w+)`, kind: "type"},
	{pattern: `val\s+(\w+)`, kind: "constant"},
	{pattern: `lazy\s+val\s+(\w+)`, kind: "constant"},
})
