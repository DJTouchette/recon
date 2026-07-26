package index

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/djtouchette/recon/internal/scan"
)

// ContextMarker flags a code comment as a context doc.
const ContextMarker = "rivet:context"

// maxDocBody caps stored doc bodies to keep the DB compact.
const maxDocBody = 8192

// ContextDoc is a context note attached to a file or symbol, extracted from a
// rivet:context code comment or a .context/ sidecar markdown file.
type ContextDoc struct {
	File   string `json:"file"`             // source file the doc attaches to
	Symbol string `json:"symbol,omitempty"` // attached symbol name ("" = file-level)
	Line   int    `json:"line"`             // marker line for comments, 0 for sidecars
	Source string `json:"source"`           // "comment" or "sidecar"
	Origin string `json:"origin"`           // file the doc text lives in (source file or .md)
	Body   string `json:"body"`
}

// ContextDocIndex holds all extracted context docs.
type ContextDocIndex struct {
	byFile   map[string][]ContextDoc
	bySymbol map[string][]ContextDoc
	all      []ContextDoc
}

// NewContextDocIndex extracts context docs from all eligible files:
// rivet:context comments in source/script/test files, and sidecar markdown
// in .context/ directories.
func NewContextDocIndex(root string, idx *FileIndex, symbols *SymbolIndex) *ContextDocIndex {
	var candidates []*scan.FileEntry
	for _, f := range idx.All() {
		if isContextDocCandidate(f) {
			candidates = append(candidates, f)
		}
	}

	var mu sync.Mutex
	var all []ContextDoc
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0)*2)

	for _, f := range candidates {
		f := f
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			docs := extractFileContextDocs(root, f, symbols, idx)
			if len(docs) == 0 {
				return
			}
			mu.Lock()
			all = append(all, docs...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Deterministic order regardless of goroutine scheduling.
	sort.Slice(all, func(i, j int) bool {
		if all[i].File != all[j].File {
			return all[i].File < all[j].File
		}
		if all[i].Line != all[j].Line {
			return all[i].Line < all[j].Line
		}
		return all[i].Origin < all[j].Origin
	})

	return NewContextDocIndexFromData(all)
}

// NewContextDocIndexFromData creates a ContextDocIndex from pre-loaded data.
func NewContextDocIndexFromData(docs []ContextDoc) *ContextDocIndex {
	ci := &ContextDocIndex{
		byFile:   make(map[string][]ContextDoc),
		bySymbol: make(map[string][]ContextDoc),
		all:      docs,
	}
	for i := range docs {
		d := &docs[i]
		ci.byFile[d.File] = append(ci.byFile[d.File], *d)
		if d.Symbol != "" {
			ci.bySymbol[d.Symbol] = append(ci.bySymbol[d.Symbol], *d)
		}
	}
	return ci
}

// ForFile returns docs attached to the given file (including its symbols' docs).
func (ci *ContextDocIndex) ForFile(path string) []ContextDoc {
	if ci == nil {
		return nil
	}
	return ci.byFile[path]
}

// ForSymbol returns docs attached to the given symbol name.
func (ci *ContextDocIndex) ForSymbol(name string) []ContextDoc {
	if ci == nil {
		return nil
	}
	return ci.bySymbol[name]
}

// All returns every extracted context doc.
func (ci *ContextDocIndex) All() []ContextDoc {
	if ci == nil {
		return nil
	}
	return ci.all
}

// ScanFileContextDocs extracts context docs for specific files (incremental).
func ScanFileContextDocs(root string, files []*scan.FileEntry, symbols *SymbolIndex, idx *FileIndex) []ContextDoc {
	var all []ContextDoc
	for _, f := range files {
		if !isContextDocCandidate(f) {
			continue
		}
		all = append(all, extractFileContextDocs(root, f, symbols, idx)...)
	}
	return all
}

// isContextDocCandidate reports whether a file can yield context docs: a code
// file in a language with known comment syntax, or a .context/ sidecar.
func isContextDocCandidate(f *scan.FileEntry) bool {
	if IsSidecarPath(f.RelPath) {
		return true
	}
	switch f.Class {
	case scan.ClassSource, scan.ClassScript, scan.ClassTest:
		_, ok := commentSyntaxFor(f.Lang)
		return ok
	}
	return false
}

// extractFileContextDocs dispatches to comment or sidecar extraction.
func extractFileContextDocs(root string, f *scan.FileEntry, symbols *SymbolIndex, idx *FileIndex) []ContextDoc {
	if IsSidecarPath(f.RelPath) {
		return extractSidecarDocs(root, f, idx)
	}
	return extractCommentDocs(root, f, symbols)
}

// --- Comment extraction ---

// blockPair is one block-comment delimiter pair.
type blockPair struct{ open, close string }

// commentSyntax describes how comments look in a language. A language can have
// more than one block form (a .vue file mixes HTML and JS comments).
type commentSyntax struct {
	line   []string // line-comment prefixes
	blocks []blockPair
}

// blockOpener returns the block pair that trimmed starts with.
func (cs commentSyntax) blockOpener(trimmed string) (blockPair, bool) {
	for _, b := range cs.blocks {
		if strings.HasPrefix(trimmed, b.open) {
			return b, true
		}
	}
	return blockPair{}, false
}

var (
	cBlock   = blockPair{"/*", "*/"}
	htmlPair = blockPair{"<!--", "-->"}
	cFamily  = commentSyntax{line: []string{"//"}, blocks: []blockPair{cBlock}}
	hashOnly = commentSyntax{line: []string{"#"}}
	// Single-file component formats interleave markup and script, so both
	// comment forms are valid in the same file.
	sfcFamily = commentSyntax{line: []string{"//"}, blocks: []blockPair{cBlock, htmlPair}}
)

var commentSyntaxes = map[string]commentSyntax{
	"go":         cFamily,
	"javascript": cFamily,
	"typescript": cFamily,
	"java":       cFamily,
	"kotlin":     cFamily,
	"csharp":     cFamily,
	// Razor markup takes HTML comments, C# comments inside @{ } and @code
	// blocks, and its own @* ... *@ form.
	"razor":      {line: []string{"//"}, blocks: []blockPair{cBlock, htmlPair, {"@*", "*@"}}},
	"fsharp":     {line: []string{"//"}, blocks: []blockPair{{"(*", "*)"}}},
	"swift":      cFamily,
	"c":          cFamily,
	"cpp":        cFamily,
	"rust":       cFamily,
	"scala":      cFamily,
	"dart":       cFamily,
	"zig":        {line: []string{"//"}},
	"php":        {line: []string{"//", "#"}, blocks: []blockPair{cBlock}},
	"python":     hashOnly,
	"ruby":       hashOnly,
	"elixir":     hashOnly,
	"shell":      hashOnly,
	"julia":      hashOnly,
	"r":          hashOnly,
	"powershell": {line: []string{"#"}, blocks: []blockPair{{"<#", "#>"}}},
	"lua":        {line: []string{"--"}, blocks: []blockPair{{"--[[", "]]"}}},
	"sql":        {line: []string{"--"}, blocks: []blockPair{cBlock}},
	"erlang":     {line: []string{"%"}},
	"clojure":    {line: []string{";"}},

	// Indexed source languages that had no comment syntax registered, so any
	// marker written in one was silently dropped.
	"vue":       sfcFamily,
	"svelte":    sfcFamily,
	"astro":     sfcFamily,
	"html":      {blocks: []blockPair{htmlPair}},
	"css":       {blocks: []blockPair{cBlock}},
	"scss":      cFamily,
	"less":      cFamily,
	"sass":      {line: []string{"//"}, blocks: []blockPair{cBlock}},
	"nim":       {line: []string{"#"}, blocks: []blockPair{{"#[", "]#"}}},
	"protobuf":  cFamily,
	"graphql":   hashOnly,
	"terraform": {line: []string{"#", "//"}, blocks: []blockPair{cBlock}},
	"hcl":       {line: []string{"#", "//"}, blocks: []blockPair{cBlock}},
}

func commentSyntaxFor(lang string) (commentSyntax, bool) {
	cs, ok := commentSyntaxes[lang]
	return cs, ok
}

// markerRe matches the marker and an optional explicit symbol:
// "rivet:context", "rivet:context(ProcessPayment)", optionally followed by
// ":" and inline text that becomes the first body line.
var markerRe = regexp.MustCompile(`^rivet:context\b(?:\(([^)\s]+)\))?[:\s]*(.*)$`)

// commentBlock is one contiguous run of comment text: a block comment, or a
// stack of line comments on consecutive lines. Text has the comment
// delimiters stripped; startLine is the 1-indexed line of text[0].
type commentBlock struct {
	startLine int
	text      []string
	// trailing marks a comment that follows code on the same line, which
	// documents the declaration it trails rather than whatever comes next.
	trailing bool
}

func (b commentBlock) endLine() int { return b.startLine + len(b.text) - 1 }

// extractCommentDocs scans a code file for rivet:context comment blocks.
//
// Where a tree-sitter grammar is available the comments come from the parse
// tree, so a marker inside a string literal is not a comment and cannot become
// a doc, and a marker in a comment trailing real code is still found. Other
// languages fall back to a line scanner, which cannot tell a comment inside a
// string from a real one.
func extractCommentDocs(root string, f *scan.FileEntry, symbols *SymbolIndex) []ContextDoc {
	cs, ok := commentSyntaxFor(f.Lang)
	if !ok {
		return nil
	}

	fullPath := filepath.Join(root, f.RelPath)
	info, err := os.Stat(fullPath)
	if err != nil || info.Size() == 0 {
		return nil
	}
	// Deliberately no size ceiling here: symbol extraction has none either,
	// and a cap only on docs means a big file's function is found while the
	// doc explaining it silently is not.
	data, err := os.ReadFile(fullPath)
	if err != nil || !strings.Contains(string(data), ContextMarker) {
		return nil
	}

	lines := strings.Split(string(data), "\n")
	blocks, parsed := tsCommentBlocks(data, f.Lang, cs)
	if !parsed {
		blocks = scanCommentBlocks(lines, cs)
	}

	fileSyms := symbols.ForFile(f.RelPath)

	var docs []ContextDoc
	for _, b := range blocks {
		docs = append(docs, docsFromBlock(f, b, lines, fileSyms, cs)...)
	}
	return docs
}

// docsFromBlock turns the markers inside one comment block into docs.
func docsFromBlock(f *scan.FileEntry, b commentBlock, lines []string, fileSyms []Symbol, cs commentSyntax) []ContextDoc {
	var docs []ContextDoc
	for i := 0; i < len(b.text); i++ {
		m := markerRe.FindStringSubmatch(b.text[i])
		if m == nil {
			continue
		}

		doc := ContextDoc{
			File:   f.RelPath,
			Symbol: m[1],
			Line:   b.startLine + i,
			Source: "comment",
			Origin: f.RelPath,
		}

		var body []string
		if first := strings.TrimSpace(m[2]); first != "" {
			body = append(body, first)
		}
		last := i
		for j := i + 1; j < len(b.text); j++ {
			// A new marker starts a new doc; stop this one.
			if markerRe.MatchString(b.text[j]) {
				break
			}
			body = append(body, b.text[j])
			last = j
		}

		doc.Body = trimDocBody(body)
		if doc.Body == "" {
			i = last
			continue
		}

		switch {
		case doc.Symbol != "":
			// An explicit rivet:context(Name) is a claim about a symbol, and
			// an unchecked claim lets `recon docs` and `recon symbols`
			// contradict each other. Keep the doc, drop the attachment.
			doc.Symbol = resolveNamedSymbol(doc.Symbol, fileSyms)
		case b.trailing:
			doc.Symbol = symbolOnLine(fileSyms, b.startLine)
		default:
			doc.Symbol = attachedSymbol(fileSyms, lines, b.startLine+last, cs)
		}

		docs = append(docs, doc)
		i = last
	}
	return docs
}

// resolveNamedSymbol keeps an explicit symbol name only when that symbol is
// actually declared in the same file. A qualified name (Service.Handle)
// matches on its last component. When the file has no indexed symbols at all
// (a language recon cannot parse) there is nothing to check against, so the
// name is taken at face value.
func resolveNamedSymbol(name string, fileSyms []Symbol) string {
	if len(fileSyms) == 0 {
		return name
	}
	short := name
	for _, sep := range []string{"::", ".", "#"} {
		if i := strings.LastIndex(short, sep); i >= 0 {
			short = short[i+len(sep):]
		}
	}
	for i := range fileSyms {
		if fileSyms[i].Name == name || fileSyms[i].Name == short {
			return fileSyms[i].Name
		}
	}
	return ""
}

// symbolOnLine returns the symbol declared on the given line, if any.
func symbolOnLine(syms []Symbol, line int) string {
	for i := range syms {
		if syms[i].Line == line {
			return syms[i].Name
		}
	}
	return ""
}

// maxAttachWindow is how far below a comment a declaration may sit and still
// be considered documented by it.
const maxAttachWindow = 10

// attachedSymbol returns the symbol a comment block directly precedes.
//
// "Directly" is the whole point: only blank lines, further comments, and
// decorators/attributes may sit between. A nearest-symbol-within-N-lines rule
// ignores what is in between, so a file header comment followed by
// `package x`, an import block and then the first function attaches the
// whole-file note to that function.
func attachedSymbol(syms []Symbol, lines []string, blockEnd int, cs commentSyntax) string {
	best := ""
	bestLine := 0
	for i := range syms {
		l := syms[i].Line
		if l <= blockEnd || l > blockEnd+maxAttachWindow {
			continue
		}
		if bestLine != 0 && l >= bestLine {
			continue
		}
		best = syms[i].Name
		bestLine = l
	}
	if best == "" {
		return ""
	}
	for n := blockEnd + 1; n < bestLine; n++ {
		if n-1 >= len(lines) {
			break
		}
		if !isAttachFiller(lines[n-1], cs) {
			return ""
		}
	}
	return best
}

// isAttachFiller reports whether a line may sit between a doc comment and the
// declaration it documents: blank, comment, or a decorator/attribute/modifier.
func isAttachFiller(line string, cs commentSyntax) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return true
	}
	if _, ok := lineCommentContent(t, cs); ok {
		return true
	}
	if _, ok := cs.blockOpener(t); ok {
		return true
	}
	switch t[0] {
	case '@', '[', '*': // Java/Python decorators, C#/Rust attributes, block continuation
		return true
	}
	return strings.HasPrefix(t, "#[") // Rust attribute
}

// tsCommentBlocks extracts comment blocks from the parse tree. The bool is
// false when no grammar is registered for lang (or the parse fails), meaning
// the caller must fall back to the line scanner.
//
// This is what makes the doc scanner agree with the symbol scanner about what
// a comment is: `# rivet:context:` inside a Python triple-quoted template is
// a string, not a comment, and the parser knows the difference.
func tsCommentBlocks(source []byte, lang string, cs commentSyntax) ([]commentBlock, bool) {
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

	type rawComment struct {
		start, end int // 0-indexed rows
		lines      []string
		ownLine    bool
	}
	var raw []rawComment

	stack := []*tree_sitter.Node{tree.RootNode()}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == nil {
			continue
		}
		if strings.Contains(n.Kind(), "comment") {
			start := int(n.StartPosition().Row)
			text := strings.Split(n.Utf8Text(source), "\n")
			raw = append(raw, rawComment{
				start:   start,
				end:     int(n.EndPosition().Row),
				lines:   text,
				ownLine: startsItsLine(source, n.StartByte()),
			})
			continue
		}
		for i := n.ChildCount(); i > 0; i-- {
			stack = append(stack, n.Child(i-1))
		}
	}

	sort.Slice(raw, func(i, j int) bool { return raw[i].start < raw[j].start })

	var blocks []commentBlock
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		b := commentBlock{
			startLine: c.start + 1,
			text:      stripCommentLines(c.lines, cs),
			trailing:  !c.ownLine,
		}
		// Consecutive own-line comments read as one note.
		for c.ownLine && i+1 < len(raw) && raw[i+1].ownLine && raw[i+1].start == c.end+1 {
			i++
			c = raw[i]
			b.text = append(b.text, stripCommentLines(c.lines, cs)...)
		}
		blocks = append(blocks, b)
	}
	return blocks, true
}

// startsItsLine reports whether only whitespace precedes off on its line, i.e.
// the comment is a standalone note rather than a trailing remark.
func startsItsLine(source []byte, off uint) bool {
	for i := int(off) - 1; i >= 0; i-- {
		switch source[i] {
		case '\n':
			return true
		case ' ', '\t', '\r':
		default:
			return false
		}
	}
	return true
}

// stripCommentLines removes the comment delimiters from a comment node's raw
// text, one output line per source line.
func stripCommentLines(lines []string, cs commentSyntax) []string {
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if i == 0 {
			if c, ok := lineCommentContent(t, cs); ok {
				out = append(out, c)
				continue
			}
			if bp, ok := cs.blockOpener(t); ok {
				t = strings.TrimSpace(strings.TrimPrefix(t, bp.open))
				t = strings.TrimSpace(strings.TrimSuffix(t, bp.close))
				out = append(out, t)
				continue
			}
			out = append(out, t)
			continue
		}
		for _, bp := range cs.blocks {
			if k := strings.Index(t, bp.close); k >= 0 {
				t = strings.TrimSpace(t[:k])
			}
		}
		t = strings.TrimSpace(strings.TrimPrefix(t, "*"))
		if c, ok := lineCommentContent(t, cs); ok {
			t = c
		}
		out = append(out, t)
	}
	return out
}

// lineCommentContent returns the comment text if trimmed is a line comment.
func lineCommentContent(trimmed string, cs commentSyntax) (string, bool) {
	for _, p := range cs.line {
		if strings.HasPrefix(trimmed, p) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, p)), true
		}
	}
	return "", false
}

// scanCommentBlocks is the parser-less fallback: group consecutive line
// comments, and read block comments to their terminator. It only recognises a
// comment that starts its line, and cannot tell a comment inside a string
// literal from a real one — that is what the tree-sitter path is for.
func scanCommentBlocks(lines []string, cs commentSyntax) []commentBlock {
	var blocks []commentBlock
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])

		if content, ok := lineCommentContent(trimmed, cs); ok {
			b := commentBlock{startLine: i + 1, text: []string{content}}
			for j := i + 1; j < len(lines); j++ {
				next, ok := lineCommentContent(strings.TrimSpace(lines[j]), cs)
				if !ok {
					break
				}
				b.text = append(b.text, next)
				i = j
			}
			blocks = append(blocks, b)
			continue
		}

		bp, ok := cs.blockOpener(trimmed)
		if !ok {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, bp.open))
		b := commentBlock{startLine: i + 1}
		if k := strings.Index(rest, bp.close); k >= 0 {
			b.text = append(b.text, strings.TrimSpace(rest[:k]))
			blocks = append(blocks, b)
			continue
		}
		b.text = append(b.text, rest)
		for j := i + 1; j < len(lines); j++ {
			text := strings.TrimSpace(lines[j])
			closed := false
			if k := strings.Index(text, bp.close); k >= 0 {
				text = strings.TrimSpace(text[:k])
				closed = true
			}
			b.text = append(b.text, strings.TrimSpace(strings.TrimPrefix(text, "*")))
			i = j
			if closed {
				break
			}
		}
		blocks = append(blocks, b)
	}
	return blocks
}

// trimDocBody joins body lines, dropping leading/trailing blanks and capping size.
func trimDocBody(lines []string) string {
	start, end := 0, len(lines)
	for start < end && lines[start] == "" {
		start++
	}
	for end > start && lines[end-1] == "" {
		end--
	}
	s := strings.Join(lines[start:end], "\n")
	if len(s) > maxDocBody {
		s = s[:maxDocBody] + "\n[truncated]"
	}
	return s
}

// --- Sidecar extraction ---

const sidecarDir = ".context"

// IsSidecarPath reports whether relPath is a markdown file inside a .context/
// directory (e.g. "src/orders/.context/handler.md").
func IsSidecarPath(relPath string) bool {
	if !strings.HasSuffix(relPath, ".md") {
		return false
	}
	dir := filepath.Dir(relPath)
	return filepath.Base(dir) == sidecarDir
}

// extractSidecarDocs attaches a .context/<name>.md file to the matching source
// file(s) in the parent directory. "<stem>.md" matches any code file with that
// stem ("handler.md" → "handler.go"); "<full name>.md" matches exactly
// ("handler.go.md" → "handler.go").
func extractSidecarDocs(root string, f *scan.FileEntry, idx *FileIndex) []ContextDoc {
	parent := filepath.Dir(filepath.Dir(f.RelPath)) // strip ".context"
	if parent == "." {
		parent = ""
	}

	base := strings.TrimSuffix(filepath.Base(f.RelPath), ".md")

	var targets []string
	for _, sib := range idx.ByDir(parent) {
		switch sib.Class {
		case scan.ClassSource, scan.ClassScript, scan.ClassTest:
		default:
			continue
		}
		name := filepath.Base(sib.RelPath)
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		if name == base || stem == base {
			targets = append(targets, sib.RelPath)
		}
	}
	if len(targets) == 0 {
		return nil
	}

	fullPath := filepath.Join(root, f.RelPath)
	info, err := os.Stat(fullPath)
	if err != nil || info.Size() > maxFileSize || info.Size() == 0 {
		return nil
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil
	}
	body := strings.TrimSpace(string(data))
	if body == "" {
		return nil
	}
	if len(body) > maxDocBody {
		body = body[:maxDocBody] + "\n[truncated]"
	}

	sort.Strings(targets)
	docs := make([]ContextDoc, 0, len(targets))
	for _, t := range targets {
		docs = append(docs, ContextDoc{
			File:   t,
			Source: "sidecar",
			Origin: f.RelPath,
			Body:   body,
		})
	}
	return docs
}
