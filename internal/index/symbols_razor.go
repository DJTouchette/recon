package index

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Razor (.cshtml, .razor) extraction.
//
// A Razor file is HTML markup with @-prefixed directives and interleaved C#
// fragments. It is not C#, and it used to be classified as such: every one of
// them was handed to the tree-sitter C# grammar, which failed on essentially
// all of them (173 of 173 .cshtml files "partial" on a real ASP.NET repo, 172
// of those with zero symbols).
//
// What a Razor file actually declares is its directives. `@model
// Leroy.Api.Pages.App.ContactsModel` is the single most useful fact in the file
// — it names the page-model class the view binds to — and `@using`, `@inject`,
// `@page` and `@attribute` are the rest of the file's contract. Deep parsing of
// the markup and of inline `@{ ... }` expressions buys nothing: those declare
// nothing. Only `@code` / `@functions` blocks hold real member declarations, and
// they are rare (6 of 173 files here).
//
// So this extractor is deliberately shallow: line-anchored directive patterns
// plus a member scan of `@code`/`@functions` bodies. Everything it produces is
// ExtractorRegex — approximate by construction, and labelled as such — which is
// the same contract Swift, Dart and Elixir extraction runs under.

// razorLang is the file-index language name for Razor markup. Code-behind
// (.cshtml.cs, .razor.cs) is keyed on its final extension and stays "csharp".
const razorLang = "razor"

// razorCodeLang keys the masking profile used for the body of an
// @code/@functions block. It is not a recon language and never appears on a
// file, a symbol or a parse record.
const razorCodeLang = "razor-code-block"

// Razor directive kinds. These are the `kind` field of razorDirective, and the
// symbol kinds are derived from them.
const (
	razorDirModel      = "model"
	razorDirPage       = "page"
	razorDirUsing      = "using"
	razorDirInject     = "inject"
	razorDirInherits   = "inherits"
	razorDirImplements = "implements"
	razorDirNamespace  = "namespace"
	razorDirAttribute  = "attribute"
)

var (
	// A directive occupies a whole line, so every pattern is anchored at both
	// ends. That anchoring is what keeps `@using (Html.BeginForm())` — a C#
	// *using statement* in markup, not an import — from being read as a
	// namespace import.
	razorModelRe      = regexp.MustCompile(`^\s*@model\s+([\w.<>,?\[\] ]+?)\s*$`)
	razorPageRe       = regexp.MustCompile(`^\s*@page(?:\s+"([^"]*)")?\s*$`)
	razorUsingRe      = regexp.MustCompile(`^\s*@using\s+(?:static\s+)?([A-Za-z_][\w.]*)\s*;?\s*$`)
	razorInjectRe     = regexp.MustCompile(`^\s*@inject\s+([\w.<>,?\[\] ]+?)\s+(\w+)\s*$`)
	razorInheritsRe   = regexp.MustCompile(`^\s*@inherits\s+([\w.<>,?\[\] ]+?)\s*$`)
	razorImplementsRe = regexp.MustCompile(`^\s*@implements\s+([\w.<>,?\[\] ]+?)\s*$`)
	razorNamespaceRe  = regexp.MustCompile(`^\s*@namespace\s+([\w.]+)\s*$`)
	razorAttributeRe  = regexp.MustCompile(`^\s*@attribute\s+\[\s*([\w.]+)`)

	// The opening of a code block. `@code` and `@functions` may put the brace
	// on the following line; `@{` may not.
	razorCodeOpenRe = regexp.MustCompile(`^\s*@(?:code|functions)\b`)

	// razorTypeNameRe pulls the type names out of a type expression, so
	// `IEnumerable<Leroy.Contacts.Contact>` yields both of them.
	razorTypeNameRe = regexp.MustCompile(`[A-Za-z_][\w.]*`)
)

// razorDirectivePatterns is the directive table, tried in order. Every pattern
// captures its payload in group 1; @inject captures the member name in group 2.
var razorDirectivePatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	{razorDirPage, razorPageRe},
	{razorDirUsing, razorUsingRe},
	{razorDirInject, razorInjectRe},
	{razorDirModel, razorModelRe},
	{razorDirInherits, razorInheritsRe},
	{razorDirImplements, razorImplementsRe},
	{razorDirNamespace, razorNamespaceRe},
	{razorDirAttribute, razorAttributeRe},
}

// razorTypeNames returns the type names referenced by a type expression, with
// primitives dropped. `IEnumerable<Contact>` names two types and either could
// be the one a reader is looking for.
func razorTypeNames(expr string) []string {
	var out []string
	for _, name := range razorTypeNameRe.FindAllString(expr, -1) {
		if razorPrimitiveTypes[shortTypeName(name)] {
			continue
		}
		out = append(out, name)
	}
	return out
}

// razorPrimitiveTypes are the C# type names that name no declaration in the
// repo, so `@model string?` must not report a symbol called "string" or resolve
// to a file.
var razorPrimitiveTypes = map[string]bool{
	"string": true, "int": true, "long": true, "short": true, "byte": true,
	"sbyte": true, "uint": true, "ulong": true, "ushort": true, "bool": true,
	"char": true, "decimal": true, "double": true, "float": true,
	"object": true, "dynamic": true, "var": true, "void": true,
	"String": true, "Int32": true, "Int64": true, "Boolean": true,
	"Object": true, "Decimal": true, "Double": true, "Guid": true,
	"DateTime": true, "DateTimeOffset": true, "TimeSpan": true,
}

// razorDirective is one directive line.
type razorDirective struct {
	Kind  string // one of the razorDir* constants
	Value string // the payload: a type name, namespace, route, attribute name
	Name  string // the injected member's name, for @inject only
	Line  int    // 1-based
	Raw   string // the trimmed source line
}

// parseRazorDirectives returns every directive in the file, in source order.
//
// Razor comments are stripped first: `@* ... *@` spans lines and commenting a
// page's directives out is exactly how a directive stops applying, so a
// directive inside one is not there.
func parseRazorDirectives(data []byte) []razorDirective {
	lines := razorStripComments(splitLines(data))

	var out []razorDirective
	for i, line := range lines {
		if !strings.Contains(line, "@") {
			continue
		}
		for _, p := range razorDirectivePatterns {
			m := p.re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			d := razorDirective{
				Kind:  p.kind,
				Value: m[1],
				Line:  i + 1,
				Raw:   trimSig(strings.TrimSpace(line)),
			}
			if p.kind == razorDirInject {
				d.Name = m[2]
			}
			out = append(out, d)
			break
		}
	}
	return out
}

// razorStripComments returns a copy of lines with every `@* ... *@` region
// blanked. Line count and length are preserved so positions stay correct.
func razorStripComments(lines []string) []string {
	out := make([]string, len(lines))
	copy(out, lines)

	inComment := false
	for i, line := range out {
		var b []byte
		j := 0
		for j < len(line) {
			if inComment {
				if k := strings.Index(line[j:], "*@"); k >= 0 {
					b = append(b, blanks(k+2)...)
					j += k + 2
					inComment = false
					continue
				}
				b = append(b, blanks(len(line)-j)...)
				j = len(line)
				continue
			}
			if strings.HasPrefix(line[j:], "@*") {
				inComment = true
				b = append(b, blanks(2)...)
				j += 2
				continue
			}
			b = append(b, line[j])
			j++
		}
		out[i] = string(b)
	}
	return out
}

func blanks(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
	}
	return b
}

// extractRazorSymbols is the razor branch of extractSymbolsRegex: directives
// plus the members of any @code/@functions block.
//
// `@using` produces no symbol on purpose. It is an import, not a declaration —
// exactly as in C#, where a `using` is a dependency-graph edge and never a
// symbol. It is wired into the dep graph instead (see razorImportSpecs).
func extractRazorSymbols(data []byte, relPath string) []Symbol {
	var syms []Symbol
	add := func(name, kind string, d razorDirective) {
		if name == "" || !validSymbolName(name) {
			return
		}
		syms = append(syms, Symbol{
			File:      relPath,
			Name:      name,
			Kind:      kind,
			Line:      d.Line,
			Signature: d.Raw,
			Extractor: ExtractorRegex,
		})
	}

	for _, d := range parseRazorDirectives(data) {
		switch d.Kind {
		case razorDirModel:
			// The short name is what the class is called and what a reader
			// searches for; the fully-qualified name stays in the signature.
			for _, ref := range razorTypeNames(d.Value) {
				add(shortTypeName(ref), "model", d)
			}
		case razorDirPage:
			// A bare `@page` still declares a page — the route is then derived
			// from the file name, so that is the name to report. A route
			// literal is the more specific declaration and is reported as
			// well, because "which file serves /tenant/{id}/contacts" is the
			// question a route is asked.
			add(razorPageName(relPath), "page", d)
			if d.Value != "" {
				add(d.Value, "route", d)
			}
		case razorDirInject:
			// @inject really does declare a property on the generated view
			// class, named by the second token.
			add(d.Name, "property", d)
		case razorDirInherits:
			for _, ref := range razorTypeNames(d.Value) {
				add(shortTypeName(ref), "inherits", d)
			}
		case razorDirImplements:
			for _, ref := range razorTypeNames(d.Value) {
				add(shortTypeName(ref), "implements", d)
			}
		case razorDirNamespace:
			// Matches the "module" kind the C# grammar gives a namespace.
			add(d.Value, "module", d)
		case razorDirAttribute:
			add(shortTypeName(d.Value), "attribute", d)
		}
	}

	syms = append(syms, razorCodeBlockSymbols(data, relPath)...)
	sortSymbols(syms)
	return syms
}

// razorPageName is the name a page answers to: its file stem, minus the markup
// extension. Pages/App/Contacts.cshtml is "Contacts".
func razorPageName(relPath string) string {
	base := filepath.Base(relPath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// shortTypeName drops the namespace and any generic arguments from a type
// reference: "Leroy.Api.Pages.App.ContactsModel" → "ContactsModel".
func shortTypeName(s string) string {
	if i := strings.IndexAny(s, "<["); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSuffix(s, "?")
}

// razorCodeBlockSymbols extracts the members declared in @code / @functions
// blocks — the only part of a Razor file that holds real C# declarations.
//
// The block is handed to the ordinary line-pattern engine rather than to the C#
// grammar. A block body is a bare list of class members, which is not a
// compilation unit: making the grammar accept it means wrapping it in a
// synthetic class, and a synthetic wrapper is exactly the kind of thing that
// reports a symbol nothing in the file declares. The pattern engine costs
// nothing, keeps every symbol honestly labelled ExtractorRegex, and these
// blocks hold a handful of members at most.
//
// The block is passed starting at its opening line, so its members sit at brace
// depth 1 and the engine's depth rule applies: a member of the block is a
// declaration, and anything inside a member's body is not.
func razorCodeBlockSymbols(data []byte, relPath string) []Symbol {
	lines := razorStripComments(splitLines(data))

	var syms []Symbol
	for _, r := range razorCodeRegions(lines) {
		if r.kind != razorRegionCode {
			continue
		}
		block := strings.Join(lines[r.start:r.end+1], "\n")
		for _, s := range extractSymbolsRegex([]byte(block), relPath, razorCodeLang, razorMemberPatterns) {
			s.Line += r.start // block-relative line 1 is file line r.start+1
			syms = append(syms, s)
		}
	}
	return syms
}

// razorMemberPatterns match C# member declarations inside a @code/@functions
// block. Unlike csharpPatterns they do not require an access modifier: Razor
// blocks are conventionally written without one (`bool IsEnabled(string t) =>
// ...`), so requiring one found nothing at all.
//
// Every kind is repeated as the nested kind because these patterns are only
// ever applied inside a block, where members sit at depth 1.
var razorMemberPatterns = compilePatterns([]rawPattern{
	{pattern: `^\s*(?:\[[^\]]*\]\s*)*(?:(?:public|private|protected|internal|static|abstract|virtual|override|sealed|partial|new|readonly|record)\s+)*class\s+(\w+)`, kind: "class", nested: "class"},
	{pattern: `^\s*(?:\[[^\]]*\]\s*)*(?:(?:public|private|protected|internal|static|abstract|sealed|partial|readonly)\s+)*record\s+(?:class\s+|struct\s+)?(\w+)`, kind: "class", nested: "class"},
	{pattern: `^\s*(?:\[[^\]]*\]\s*)*(?:(?:public|private|protected|internal|static|partial|readonly|ref)\s+)*struct\s+(\w+)`, kind: "struct", nested: "struct"},
	{pattern: `^\s*(?:\[[^\]]*\]\s*)*(?:(?:public|private|protected|internal|partial)\s+)*interface\s+(\w+)`, kind: "interface", nested: "interface"},
	{pattern: `^\s*(?:\[[^\]]*\]\s*)*(?:(?:public|private|protected|internal)\s+)*enum\s+(\w+)`, kind: "enum", nested: "enum"},
	// Property: type, name, then a `{ get`.
	{pattern: `^\s*(?:\[[^\]]*\]\s*)*(?:(?:public|private|protected|internal|static|virtual|override|abstract|required|new)\s+)*[\w.<>\[\],?]+\s+(\w+)\s*\{\s*(?:get|set|init)`, kind: "property", nested: "property"},
	// Method: type, name, then a parameter list. A leading statement keyword
	// cannot reach this: a statement lives inside a member body, which is a
	// deeper brace level than the patterns are applied at.
	{pattern: `^\s*(?:\[[^\]]*\]\s*)*(?:(?:public|private|protected|internal|static|virtual|override|abstract|async|sealed|extern|partial|new)\s+)*[\w.<>\[\],?]+\s+(\w+)\s*\(`, kind: "method", nested: "method"},
	// Field / const.
	{pattern: `^\s*(?:\[[^\]]*\]\s*)*(?:(?:public|private|protected|internal|static|readonly|const|new)\s+)+[\w.<>\[\],?]+\s+(\w+)\s*[=;]`, kind: "constant", nested: "property"},
})

// ─── Code regions ────────────────────────────────────────────────────────────

const (
	razorRegionCode   = "code"   // @code { } / @functions { }
	razorRegionInline = "inline" // @{ }
)

// razorRegion is one brace-delimited C# region, as 0-based inclusive line
// bounds plus the columns of its opening and closing braces.
type razorRegion struct {
	kind      string
	start     int
	end       int
	openCol   int
	closeCol  int
	bodyStart int // line holding the opening brace (>= start for @code)
}

// razorCodeRegions finds the file's C# regions: `@{ ... }` blocks and
// `@code`/`@functions` blocks. lines must already have Razor comments stripped.
//
// A region whose braces do not balance is skipped rather than being taken to
// end of file: an unbalanced guess would drag the whole rest of the markup into
// the region and undo the point of finding it.
func razorCodeRegions(lines []string) []razorRegion {
	var out []razorRegion
	for i := 0; i < len(lines); i++ {
		kind, braceLine, braceCol, ok := razorRegionOpen(lines, i)
		if !ok {
			continue
		}
		endLine, endCol, ok := razorMatchBrace(lines, braceLine, braceCol)
		if !ok {
			continue
		}
		out = append(out, razorRegion{
			kind: kind, start: i, end: endLine,
			openCol: braceCol, closeCol: endCol, bodyStart: braceLine,
		})
		i = endLine
	}
	return out
}

// razorRegionOpen reports whether a region starts on line i, and where its
// opening brace is. @code/@functions may put the brace on a following line.
func razorRegionOpen(lines []string, i int) (kind string, braceLine, braceCol int, ok bool) {
	line := lines[i]
	if c := strings.Index(line, "@{"); c >= 0 {
		return razorRegionInline, i, c + 1, true
	}
	if !razorCodeOpenRe.MatchString(line) {
		return "", 0, 0, false
	}
	for j := i; j < len(lines) && j <= i+2; j++ {
		from := 0
		if j == i {
			from = strings.Index(lines[j], "@")
		}
		if c := strings.IndexByte(lines[j][from:], '{'); c >= 0 {
			return razorRegionCode, j, from + c, true
		}
		if j > i && strings.TrimSpace(lines[j]) != "" {
			break // something other than a brace follows: not a block
		}
	}
	return "", 0, 0, false
}

// razorMatchBrace finds the brace closing the one at (line, col), masking C#
// string and character literals and // comments so a brace inside one does not
// count.
func razorMatchBrace(lines []string, line, col int) (endLine, endCol int, ok bool) {
	depth := 0
	for i := line; i < len(lines); i++ {
		masked := maskCSharpLine(lines[i])
		start := 0
		if i == line {
			start = col
		}
		for c := start; c < len(masked); c++ {
			switch masked[c] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return i, c, true
				}
			}
		}
	}
	return 0, 0, false
}

// maskCSharpLine blanks string literals, character literals and line comments
// in one line of C#, preserving length.
func maskCSharpLine(line string) string {
	out := []byte(line)
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '/' && i+1 < len(line) && line[i+1] == '/':
			for j := i; j < len(out); j++ {
				out[j] = ' '
			}
			return string(out)
		case line[i] == '"' || line[i] == '\'':
			q := line[i]
			verbatim := i > 0 && line[i-1] == '@'
			j := i + 1
			for j < len(line) {
				if !verbatim && line[j] == '\\' {
					j += 2
					continue
				}
				if line[j] == q {
					break
				}
				j++
			}
			for k := i; k < j && k < len(out); k++ {
				out[k] = ' '
			}
			if j >= len(line) {
				return string(out)
			}
			out[j] = ' '
			i = j
		}
	}
	return string(out)
}

// razorCSharpProjection returns the file with everything that is not C# blanked
// out, keeping every line in place so line numbers still refer to the original.
//
// It exists for reference extraction. The C# refs grammar over a whole Razor
// file produced 4064 "references" on a real repo of which 96% were markup, HTML
// attribute names and script-tag JavaScript — including 668 calls to a function
// named "@if". Over the projection it sees only the code the file really
// contains.
//
// The projection is best-effort by nature: a code block that interleaves markup
// (`@{ ... <text>hi</text> ... }`) does not survive as valid C#. That costs
// recall inside such a block and nothing else, since the refs extractor already
// tolerates a partial parse.
func razorCSharpProjection(data []byte) []byte {
	lines := razorStripComments(splitLines(data))
	out := make([]string, len(lines))

	// Markup lines keep only their embedded call expressions. A view calls
	// helpers straight from the markup —
	// `@Leroy.Certificates.Models.LabTestData.FormatPhone(data.VetPhone)` — and
	// those are ordinary call sites that `recon callers` should find.
	for i, line := range lines {
		out[i] = razorMarkupCalls(line)
	}

	for _, r := range razorCodeRegions(lines) {
		for i := r.start; i <= r.end; i++ {
			line := lines[i]
			// A code region owns its lines outright; whatever the markup pass
			// put there is replaced.
			switch {
			case i < r.bodyStart:
				// The `@code` / `@functions` keyword line: drop it, the brace
				// is further down.
				out[i] = ""
			case i == r.bodyStart && i == r.end:
				out[i] = string(blanks(r.openCol)) + line[r.openCol:r.closeCol+1]
			case i == r.bodyStart:
				// Blank the markup before the brace. A bare `{ ... }` at top
				// level is a legal C# block statement, so the region parses on
				// its own.
				out[i] = string(blanks(r.openCol)) + line[r.openCol:]
			case i == r.end:
				out[i] = line[:r.closeCol+1]
			default:
				out[i] = line
			}
		}
	}
	return []byte(strings.Join(out, "\n"))
}

// razorMarkupCalls renders the call expressions embedded in one line of markup
// as C# statements: `<td>@Fmt.Phone(x)</td>` becomes `_=Fmt.Phone(x);`.
//
// Only call-shaped expressions are kept, because only an invocation is a
// reference — `@Model.Name` names no callee and emitting it would just be more
// text for the parser to trip over. The output is a statement per expression,
// on the line the expression came from, which is all reference extraction needs
// (it reports rows, not columns).
func razorMarkupCalls(line string) string {
	var sb strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] != '@' {
			continue
		}
		if i+1 >= len(line) {
			break
		}
		if line[i+1] == '@' {
			i++ // `@@` is an escaped literal at-sign
			continue
		}
		// An @ preceded by a word character is an email address or part of an
		// identifier, not the start of an expression.
		if i > 0 && isRazorWordByte(line[i-1]) {
			continue
		}

		rest := line[i+1:]
		if rest[0] == '(' {
			// Explicit expression: @(...).
			end, ok := razorBalanced(rest, 0)
			if !ok {
				continue
			}
			if inner := rest[1:end]; strings.Contains(inner, "(") {
				sb.WriteString("_=(" + inner + ");")
			}
			i += end + 1
			continue
		}

		n := razorImplicitExprLen(rest)
		if n == 0 {
			continue
		}
		expr := rest[:n]
		i += n
		// A control-flow or directive keyword is not an expression, and
		// `@media (...)` in an inline stylesheet is not C# at all.
		if !strings.Contains(expr, "(") || razorNonExprWords[razorFirstIdent(expr)] {
			continue
		}
		sb.WriteString("_=" + expr + ";")
	}
	return sb.String()
}

// razorNonExprWords are the words that can follow an @ without starting an
// expression: C# control flow, and the Razor directives.
var razorNonExprWords = map[string]bool{
	"if": true, "else": true, "for": true, "foreach": true, "while": true,
	"do": true, "switch": true, "case": true, "try": true, "catch": true,
	"finally": true, "lock": true, "using": true, "await": true, "return": true,
	"new": true, "checked": true, "unchecked": true, "media": true,
	"code": true, "functions": true, "section": true, "model": true,
	"page": true, "inject": true, "inherits": true, "implements": true,
	"namespace": true, "attribute": true, "addTagHelper": true,
	"removeTagHelper": true, "tagHelperPrefix": true, "typeparam": true,
	"layout": true, "rendermode": true, "preservewhitespace": true,
	"bind": true, "ref": true, "key": true, "attributes": true,
}

// razorImplicitExprLen returns the length of the implicit Razor expression at
// the start of s: an identifier followed by any run of `.member`, `(...)` and
// `[...]`. The parts must be adjacent — Razor ends the expression at the first
// character that cannot continue it, which is why `@media (min-width: 1px)`
// stops at "media".
func razorImplicitExprLen(s string) int {
	if len(s) == 0 || !isRazorIdentStart(s[0]) {
		return 0
	}
	j := 1
	for j < len(s) && isRazorWordByte(s[j]) {
		j++
	}
	for j < len(s) {
		switch s[j] {
		case '.':
			if j+1 >= len(s) || !isRazorIdentStart(s[j+1]) {
				return j
			}
			j += 2
			for j < len(s) && isRazorWordByte(s[j]) {
				j++
			}
		case '(', '[':
			end, ok := razorBalanced(s, j)
			if !ok {
				return j
			}
			j = end + 1
		default:
			return j
		}
	}
	return j
}

// razorBalanced returns the index of the bracket closing the one at s[start],
// ignoring brackets inside string and character literals.
func razorBalanced(s string, start int) (int, bool) {
	open := s[start]
	var close byte
	switch open {
	case '(':
		close = ')'
	case '[':
		close = ']'
	default:
		return 0, false
	}
	depth := 0
	for i := start; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\'':
			for i++; i < len(s); i++ {
				if s[i] == '\\' {
					i++
					continue
				}
				if s[i] == c {
					break
				}
			}
			if i >= len(s) {
				return 0, false
			}
		case c == open:
			depth++
		case c == close:
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

func razorFirstIdent(s string) string {
	j := 0
	for j < len(s) && isRazorWordByte(s[j]) {
		j++
	}
	return s[:j]
}

func isRazorIdentStart(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isRazorWordByte(b byte) bool {
	return isRazorIdentStart(b) || (b >= '0' && b <= '9')
}
