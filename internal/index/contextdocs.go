package index

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

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

// commentSyntax describes how comments look in a language.
type commentSyntax struct {
	line       []string // line-comment prefixes
	blockOpen  string   // block-comment opener ("" = none)
	blockClose string
}

var cFamily = commentSyntax{line: []string{"//"}, blockOpen: "/*", blockClose: "*/"}
var hashOnly = commentSyntax{line: []string{"#"}}

var commentSyntaxes = map[string]commentSyntax{
	"go":         cFamily,
	"javascript": cFamily,
	"typescript": cFamily,
	"java":       cFamily,
	"kotlin":     cFamily,
	"csharp":     cFamily,
	"fsharp":     {line: []string{"//"}, blockOpen: "(*", blockClose: "*)"},
	"swift":      cFamily,
	"c":          cFamily,
	"cpp":        cFamily,
	"rust":       cFamily,
	"scala":      cFamily,
	"dart":       cFamily,
	"zig":        {line: []string{"//"}},
	"php":        {line: []string{"//", "#"}, blockOpen: "/*", blockClose: "*/"},
	"python":     hashOnly,
	"ruby":       hashOnly,
	"elixir":     hashOnly,
	"shell":      hashOnly,
	"julia":      hashOnly,
	"r":          hashOnly,
	"powershell": {line: []string{"#"}, blockOpen: "<#", blockClose: "#>"},
	"lua":        {line: []string{"--"}, blockOpen: "--[[", blockClose: "]]"},
	"sql":        {line: []string{"--"}, blockOpen: "/*", blockClose: "*/"},
	"erlang":     {line: []string{"%"}},
	"clojure":    {line: []string{";"}},
}

func commentSyntaxFor(lang string) (commentSyntax, bool) {
	cs, ok := commentSyntaxes[lang]
	return cs, ok
}

// markerRe matches the marker and an optional explicit symbol:
// "rivet:context", "rivet:context(ProcessPayment)", optionally followed by
// ":" and inline text that becomes the first body line.
var markerRe = regexp.MustCompile(`^rivet:context\b(?:\(([^)\s]+)\))?[:\s]*(.*)$`)

// extractCommentDocs scans a code file for rivet:context comment blocks.
func extractCommentDocs(root string, f *scan.FileEntry, symbols *SymbolIndex) []ContextDoc {
	cs, ok := commentSyntaxFor(f.Lang)
	if !ok {
		return nil
	}

	fullPath := filepath.Join(root, f.RelPath)
	info, err := os.Stat(fullPath)
	if err != nil || info.Size() > maxFileSize || info.Size() == 0 {
		return nil
	}
	data, err := os.ReadFile(fullPath)
	if err != nil || !strings.Contains(string(data), ContextMarker) {
		return nil
	}

	lines := strings.Split(string(data), "\n")
	fileSyms := symbols.ForFile(f.RelPath)

	var docs []ContextDoc
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])

		content, isLine := lineCommentContent(trimmed, cs)
		inBlock := false
		if !isLine && cs.blockOpen != "" && strings.HasPrefix(trimmed, cs.blockOpen) {
			content = strings.TrimSpace(strings.TrimPrefix(trimmed, cs.blockOpen))
			inBlock = true
		} else if !isLine {
			continue
		}

		m := markerRe.FindStringSubmatch(content)
		if m == nil {
			continue
		}

		doc := ContextDoc{
			File:   f.RelPath,
			Symbol: m[1],
			Line:   i + 1,
			Source: "comment",
			Origin: f.RelPath,
		}

		var body []string
		if first := strings.TrimSpace(m[2]); first != "" && first != cs.blockClose {
			if inBlock {
				first = strings.TrimSpace(strings.TrimSuffix(first, cs.blockClose))
			}
			if first != "" {
				body = append(body, first)
			}
		}

		end := i // last line of the comment block
		if inBlock && !strings.Contains(content, cs.blockClose) {
			for j := i + 1; j < len(lines); j++ {
				end = j
				text := strings.TrimSpace(lines[j])
				closed := strings.Contains(text, cs.blockClose)
				if closed {
					text = strings.TrimSpace(text[:strings.Index(text, cs.blockClose)])
				}
				body = append(body, strings.TrimSpace(strings.TrimPrefix(text, "*")))
				if closed {
					break
				}
			}
		} else if !inBlock {
			for j := i + 1; j < len(lines); j++ {
				text, ok := lineCommentContent(strings.TrimSpace(lines[j]), cs)
				if !ok {
					break
				}
				// A new marker starts a new doc; stop this one.
				if markerRe.MatchString(text) {
					break
				}
				body = append(body, text)
				end = j
			}
		}

		doc.Body = trimDocBody(body)
		if doc.Body == "" {
			continue
		}

		// Positional attachment: a marked comment directly above a declaration
		// attaches to that symbol. A small window tolerates blank lines and
		// decorators/attributes between the comment and the declaration.
		if doc.Symbol == "" {
			doc.Symbol = symbolBelow(fileSyms, end+1, 6)
		}

		docs = append(docs, doc)
		i = end
	}
	return docs
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

// symbolBelow returns the name of the first symbol declared within `window`
// lines after the comment block ending at line `after` (1-indexed lines).
func symbolBelow(syms []Symbol, after, window int) string {
	best := ""
	bestLine := after + window + 1
	for i := range syms {
		l := syms[i].Line
		if l > after && l <= after+window && l < bestLine {
			best = syms[i].Name
			bestLine = l
		}
	}
	return best
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
