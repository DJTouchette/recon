package index

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/djtouchette/recon/internal/scan"
)

// Extractor identifies which extraction path produced a symbol. It is the
// provenance signal: a tree-sitter symbol came out of a real parse of the
// language grammar, a regex symbol came out of line-pattern matching and may be
// wrong in name, kind or line. The empty string means "unknown" — symbols
// loaded from a cache written before provenance existed.
const (
	ExtractorTreeSitter = "tree-sitter"
	ExtractorRegex      = "regex"
	ExtractorNone       = "none"
	// ExtractorMixed is a file-level value only: the grammar parsed part of the
	// file and line patterns supplied the rest. Individual symbols always carry
	// the single extractor that actually found them, so provenance stays exact
	// per symbol even when the file needed both.
	ExtractorMixed = "tree-sitter+regex"
)

// Parse statuses for FileParse.Status.
const (
	// ParseOK: the file was parsed and the symbol list is believed complete.
	ParseOK = "ok"
	// ParsePartial: extraction ran but the source did not fully parse (the
	// tree-sitter tree contains ERROR/MISSING nodes). Symbols reported are
	// real but the list is incomplete.
	ParsePartial = "partial"
	// ParseUnsupported: recon has neither a grammar nor a regex pattern set for
	// this language, so zero symbols is a gap in recon, not a property of the file.
	ParseUnsupported = "unsupported"
	// ParseFailed: the file could not be read or decoded as text.
	ParseFailed = "failed"
)

// Symbol represents a named declaration in a source file.
type Symbol struct {
	File      string `json:"file"`
	Name      string `json:"name"`
	Kind      string `json:"kind"` // function, method, class, interface, type, struct, enum, constant, module, trait
	Line      int    `json:"line"`
	Signature string `json:"signature"` // the full declaration line, trimmed

	// Extractor records how this symbol was found: ExtractorTreeSitter (parsed
	// from the real grammar — name, kind and line are trustworthy) or
	// ExtractorRegex (matched by a line pattern — approximate, and capable of
	// inventing a symbol or mislabelling its kind). Empty means unknown
	// provenance, e.g. a symbol loaded from a cache written before provenance
	// existed. Whether the symbol's *file* parsed completely is a file-level
	// fact; see FileParse.
	Extractor string `json:"extractor,omitempty"`
}

// FileParse records how one file was processed by the symbol extractor,
// including — especially — the files that produced nothing.
//
// This is the primary trust signal, because the failure modes that matter most
// yield zero symbols and so cannot be represented per-symbol: a language with
// no extractor, a file that could not be decoded, a parse that collapsed at the
// first construct the grammar didn't understand. Without a record for every
// file, "no symbols found" is ambiguous between "this file declares nothing",
// "recon has no extractor for this language" and "the parse failed" — and an
// agent will read the first meaning into all three.
type FileParse struct {
	RelPath     string `json:"path"`
	Lang        string `json:"lang"`
	Extractor   string `json:"extractor"` // tree-sitter | regex | tree-sitter+regex | none
	Status      string `json:"status"`    // ok | partial | unsupported | failed
	SymbolCount int    `json:"symbol_count"`
	// Detail is a human-readable reason for a non-ok status, suitable for
	// showing verbatim as a caveat.
	Detail string `json:"detail,omitempty"`
}

// Clean reports whether the file was fully parsed by a real extractor.
func (fp FileParse) Clean() bool { return fp.Status == ParseOK }

// FileExtra holds per-file metadata beyond the basic FileEntry.
type FileExtra struct {
	RelPath     string
	Preview     string // first meaningful lines
	ContentHash string // sha256 of file content
}

// SymbolIndex holds all extracted symbols.
type SymbolIndex struct {
	byFile  map[string][]Symbol
	all     []Symbol
	parses  []FileParse
	parseBy map[string]FileParse
}

// symbolSourceClasses lists the file classes whose symbols are indexed.
//
// Test files are included deliberately. They are ordinary declaration sites:
// this repo's own test tree defines 100+ helpers and fixtures, and excluding
// them meant `recon symbols TestQueriesCompile` answered "No symbols found" for
// a function defined three directories away. It compounded with the
// directory-based test classifier (scan.Classify treats every source file under
// test/, tests/, spec/, specs/ or __tests__/ as a test), so shared fixture code
// — `NewFixture`, `type Fixture` — was invisible to symbols, search, callers
// and doc attachment even though production code imports it. The class of every
// symbol's file is available via FileIndex.Get(sym.File).Class, so callers that
// want production-only results can filter; callers that want the truth get it.
var symbolSourceClasses = []scan.FileClass{
	scan.ClassSource,
	scan.ClassTest,
	// Script files (shell, etc.) define functions worth indexing too.
	scan.ClassScript,
}

// NewSymbolIndex extracts symbols from all source, test and script files in the
// index.
func NewSymbolIndex(root string, idx *FileIndex) *SymbolIndex {
	si := &SymbolIndex{
		byFile:  make(map[string][]Symbol),
		parseBy: make(map[string]FileParse),
	}

	var sources []*scan.FileEntry
	for _, c := range symbolSourceClasses {
		sources = append(sources, idx.ByClass(c)...)
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0)*2)

	for _, f := range sources {
		f := f
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			syms, fp := extractFileSymbols(root, f)

			mu.Lock()
			si.parses = append(si.parses, fp)
			si.parseBy[f.RelPath] = fp
			if len(syms) > 0 {
				si.byFile[f.RelPath] = syms
				si.all = append(si.all, syms...)
			}
			mu.Unlock()
		}()
	}

	wg.Wait()

	// Extraction is concurrent, so append order is goroutine-arrival order.
	// Sort so every rebuild of the same tree produces byte-identical output.
	sortSymbols(si.all)
	for k := range si.byFile {
		sortSymbols(si.byFile[k])
	}
	sort.Slice(si.parses, func(i, j int) bool { return si.parses[i].RelPath < si.parses[j].RelPath })
	return si
}

// sortSymbols orders symbols deterministically by file, then line, then name
// and kind, so identical inputs always produce identical output.
func sortSymbols(syms []Symbol) {
	sort.Slice(syms, func(i, j int) bool {
		a, b := &syms[i], &syms[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Kind < b.Kind
	})
}

// NewSymbolIndexFromData creates a SymbolIndex from pre-loaded symbols only.
//
// The resulting index has no per-file parse records, so Files() and ParseFor()
// return nothing: it knows which symbols exist but not which files were skipped
// for lack of an extractor or truncated by a failed parse. Prefer
// NewSymbolIndexFromCache wherever the parse records are available — a cached
// read is the common case, and it is exactly the case where a missing trust
// signal is most misleading.
func NewSymbolIndexFromData(symbols []Symbol) *SymbolIndex {
	return NewSymbolIndexFromCache(symbols, nil)
}

// NewSymbolIndexFromCache creates a SymbolIndex from pre-loaded symbols and
// their per-file parse records, restoring the full trust signal on a cache hit.
func NewSymbolIndexFromCache(symbols []Symbol, parses []FileParse) *SymbolIndex {
	si := &SymbolIndex{
		byFile:  make(map[string][]Symbol),
		all:     symbols,
		parses:  parses,
		parseBy: make(map[string]FileParse, len(parses)),
	}
	sortSymbols(si.all)
	for i := range si.all {
		s := &si.all[i]
		si.byFile[s.File] = append(si.byFile[s.File], *s)
	}
	sort.Slice(si.parses, func(i, j int) bool { return si.parses[i].RelPath < si.parses[j].RelPath })
	for _, fp := range si.parses {
		si.parseBy[fp.RelPath] = fp
	}
	return si
}

// Files returns the per-file parse record for every file extraction was
// attempted on, sorted by path. Use it to distinguish "no symbols" from "no
// extractor" or "parse failed".
func (si *SymbolIndex) Files() []FileParse {
	if si == nil {
		return nil
	}
	return si.parses
}

// ParseFor returns the parse record for one file, or nil when extraction was
// never attempted on it (or the index was loaded from cache).
func (si *SymbolIndex) ParseFor(path string) *FileParse {
	if si == nil {
		return nil
	}
	fp, ok := si.parseBy[path]
	if !ok {
		return nil
	}
	return &fp
}

// Incomplete returns the parse records that did not complete cleanly: files
// recon could not fully extract. An empty symbol list plus an empty Incomplete
// means the repo really has no declarations; anything else is a caveat.
func (si *SymbolIndex) Incomplete() []FileParse {
	if si == nil {
		return nil
	}
	var out []FileParse
	for _, fp := range si.parses {
		if !fp.Clean() {
			out = append(out, fp)
		}
	}
	return out
}

// ForFile returns symbols in the given file.
func (si *SymbolIndex) ForFile(path string) []Symbol {
	if si == nil {
		return nil
	}
	return si.byFile[path]
}

// All returns every extracted symbol.
func (si *SymbolIndex) All() []Symbol {
	if si == nil {
		return nil
	}
	return si.all
}

// Exact returns symbols whose name exactly matches the given name.
func (si *SymbolIndex) Exact(name string) []Symbol {
	if si == nil {
		return nil
	}
	var results []Symbol
	for i := range si.all {
		if si.all[i].Name == name {
			results = append(results, si.all[i])
		}
	}
	return results
}

// Search returns symbols whose name contains the query (case-insensitive).
func (si *SymbolIndex) Search(query string) []Symbol {
	if si == nil {
		return nil
	}
	q := strings.ToLower(query)
	var results []Symbol
	for i := range si.all {
		if strings.Contains(strings.ToLower(si.all[i].Name), q) {
			results = append(results, si.all[i])
		}
	}
	return results
}

// --- File extras (preview + hash) ---

// ExtractFileExtras computes previews and content hashes for source files.
func ExtractFileExtras(root string, idx *FileIndex) []FileExtra {
	sources := idx.ByClass(scan.ClassSource)
	extras := make([]FileExtra, 0, len(sources))

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0)*2)

	for _, f := range sources {
		f := f
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			fullPath := filepath.Join(root, f.RelPath)
			preview := extractPreview(fullPath, f.Lang)
			hash := fileHash(fullPath)

			mu.Lock()
			extras = append(extras, FileExtra{
				RelPath:     f.RelPath,
				Preview:     preview,
				ContentHash: hash,
			})
			mu.Unlock()
		}()
	}

	wg.Wait()
	return extras
}

// ExtractFileExtrasForPaths computes extras for specific files only (incremental).
func ExtractFileExtrasForPaths(root string, files []*scan.FileEntry) []FileExtra {
	var extras []FileExtra
	for _, f := range files {
		if f.Class != scan.ClassSource {
			continue
		}
		fullPath := filepath.Join(root, f.RelPath)
		extras = append(extras, FileExtra{
			RelPath:     f.RelPath,
			Preview:     extractPreview(fullPath, f.Lang),
			ContentHash: fileHash(fullPath),
		})
	}
	return extras
}

// previewReadLimit caps how much of a file is read to build a preview. The
// preview only ever uses the first 200 lines, so reading the whole of a
// multi-megabyte file would be wasted work.
const previewReadLimit = 128 << 10

func extractPreview(path, lang string) string {
	// readSource rejects binary content. Without this a file with a source
	// extension but binary contents (a stray blob named .go, a Git LFS
	// pointer's payload) had its raw bytes stored as the "preview" and emitted
	// as hundreds of bytes of escapes in `recon context`.
	data, err := readSource(path, previewReadLimit)
	if err != nil {
		return ""
	}

	var lines []string
	lineNum := 0
	maxLines := 200 // scan up to 200 lines to find meaningful content
	collected := 0
	maxCollect := 5 // collect up to 5 meaningful lines

	for _, raw := range splitLines(data) {
		if lineNum >= maxLines || collected >= maxCollect {
			break
		}
		lineNum++
		line := strings.TrimSpace(raw)

		// Skip empty lines, imports, comments at top of file
		if line == "" {
			continue
		}
		if isImportLine(line, lang) {
			continue
		}
		if isBoilerplateLine(line, lang) {
			continue
		}

		// A single minified line can be hundreds of kilobytes; keep previews
		// bounded the same way signatures are.
		lines = append(lines, trimSig(line))
		collected++
	}

	return strings.Join(lines, "\n")
}

func isImportLine(line, lang string) bool {
	switch lang {
	case "go":
		return strings.HasPrefix(line, "import ") || strings.HasPrefix(line, "package ") ||
			line == "import (" || line == ")"
	case "typescript", "javascript":
		return strings.HasPrefix(line, "import ") || strings.HasPrefix(line, "require(") ||
			strings.HasPrefix(line, "const ") && strings.Contains(line, "require(")
	case "python":
		return strings.HasPrefix(line, "import ") || strings.HasPrefix(line, "from ")
	case "csharp":
		return strings.HasPrefix(line, "using ") || strings.HasPrefix(line, "namespace ")
	case "java", "kotlin":
		return strings.HasPrefix(line, "import ") || strings.HasPrefix(line, "package ")
	case "rust":
		return strings.HasPrefix(line, "use ") || strings.HasPrefix(line, "mod ") ||
			strings.HasPrefix(line, "extern ")
	case "ruby":
		return strings.HasPrefix(line, "require ") || strings.HasPrefix(line, "require_relative ")
	case "elixir":
		return strings.HasPrefix(line, "import ") || strings.HasPrefix(line, "alias ") ||
			strings.HasPrefix(line, "use ")
	case "dart":
		return strings.HasPrefix(line, "import ") || strings.HasPrefix(line, "export ") ||
			strings.HasPrefix(line, "part ") || strings.HasPrefix(line, "library ")
	case "scala":
		return strings.HasPrefix(line, "import ") || strings.HasPrefix(line, "package ")
	case "php":
		return strings.HasPrefix(line, "use ") || strings.HasPrefix(line, "namespace ") ||
			strings.HasPrefix(line, "require ") || strings.HasPrefix(line, "include ")
	}
	return false
}

func isBoilerplateLine(line, lang string) bool {
	// Skip single-line comments at the very top (license headers, etc.)
	if strings.HasPrefix(line, "//") || strings.HasPrefix(line, "#!") ||
		strings.HasPrefix(line, "# frozen_string_literal") ||
		strings.HasPrefix(line, "# -*-") || strings.HasPrefix(line, "# encoding:") ||
		line == "/*" || line == "*/" || strings.HasPrefix(line, "* ") ||
		line == "{" || line == "}" || line == "(" || line == ")" {
		return true
	}
	// Skip pragma/directive lines
	if strings.HasPrefix(line, "#pragma") || strings.HasPrefix(line, "#region") ||
		strings.HasPrefix(line, "#nullable") || strings.HasPrefix(line, "'use strict'") ||
		line == "\"use strict\";" || line == "'use strict';" {
		return true
	}
	return false
}

func fileHash(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// --- Symbol extraction dispatch ---

// trimSig truncates long signatures to keep DB compact.
func trimSig(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// hasExtractor reports whether symbols can be extracted for lang by either a
// tree-sitter grammar or the regex pattern set.
func hasExtractor(lang string) bool {
	return hasTSLang(lang) || len(patternsForLang(lang)) > 0
}

// extractFileSymbols extracts symbols for one file and reports how it went.
//
// The tree-sitter grammar is always preferred; the regex patterns are a
// fallback for languages with no grammar. The returned FileParse is recorded
// even when no symbols are found, so an empty result is never mistaken for a
// clean, complete "this file declares nothing".
func extractFileSymbols(root string, f *scan.FileEntry) ([]Symbol, FileParse) {
	fp := FileParse{RelPath: f.RelPath, Lang: f.Lang}

	if !hasExtractor(f.Lang) {
		fp.Extractor = ExtractorNone
		fp.Status = ParseUnsupported
		fp.Detail = "no tree-sitter grammar or pattern set for " + langLabel(f.Lang)
		return nil, fp
	}

	fullPath := filepath.Join(root, f.RelPath)
	data, err := readSource(fullPath, 0)
	if err != nil {
		fp.Extractor = ExtractorNone
		fp.Status = ParseFailed
		fp.Detail = "could not read as text: " + err.Error()
		return nil, fp
	}

	data = stripNonGrammarPreamble(data, f.Lang)

	if cands := grammarCandidates(f.Lang, f.RelPath); len(cands) > 0 {
		if syms, res, ok := extractWithGrammars(data, f.RelPath, cands); ok {
			fp.Extractor = ExtractorTreeSitter
			if res.clean {
				fp.SymbolCount = len(syms)
				fp.Status = ParseOK
				if res.key != f.Lang {
					// e.g. a .h classified as C that parsed better as C++.
					fp.Detail = "parsed with the " + res.key + " grammar"
				}
				return syms, fp
			}

			// The grammar hit a construct it does not understand. Everything
			// after that point is lost, and the loss is silent: the file
			// reports a symbol list that looks like a list, just shorter.
			//
			// Seen in the wild on a current C# codebase — tree-sitter-c-sharp
			// v0.23.5 (the latest release) misparses a collection expression
			// whose single element is named after a member-level attribute
			// target, so `x.Types = [type];` and `M(pdf, [field]);` are read as
			// attribute lists and the parse collapses there. Three files
			// dropped to zero symbols and six lost everything below the first
			// occurrence. Nothing is wrong with the code, and upstream has no
			// newer release to move to.
			//
			// So when a parse is incomplete, run the line patterns too and add
			// back what the grammar never reached. Tree-sitter's own symbols
			// always win — its names, kinds and lines are trustworthy where it
			// succeeded — and each recovered symbol keeps ExtractorRegex, so a
			// reader can tell which half of the file it came from.
			syms, recovered := supplementWithRegex(syms, data, f)
			fp.SymbolCount = len(syms)
			fp.Status = ParsePartial
			fp.Detail = "tree-sitter (" + res.key + ") reported syntax errors; symbol list may be incomplete"
			if recovered > 0 {
				fp.Extractor = ExtractorMixed
				fp.Detail += fmt.Sprintf("; %d further symbol(s) recovered by line patterns and marked approximate", recovered)
			}
			return syms, fp
		}
	}

	patterns := patternsForLang(f.Lang)
	if len(patterns) == 0 {
		fp.Extractor = ExtractorNone
		fp.Status = ParseFailed
		fp.Detail = "grammar parse failed and no fallback patterns for " + langLabel(f.Lang)
		return nil, fp
	}

	syms := extractSymbolsRegex(data, f.RelPath, f.Lang, patterns)
	fp.Extractor = ExtractorRegex
	fp.SymbolCount = len(syms)
	fp.Status = ParseOK
	fp.Detail = "line-pattern extraction: names, kinds and lines are approximate"
	return syms, fp
}

// stripNonGrammarPreamble blanks lines that a language's toolchain consumes
// before the compiler ever sees them, and which its grammar therefore cannot
// parse.
//
// Today that is C#'s file-based apps (.NET 10), whose `#:package dbup@7.2.0`,
// `#:sdk` and `#:property` lines sit above ordinary top-level statements. They
// are a preamble for the SDK, not C#, and tree-sitter fails on the first one —
// so a perfectly valid script was reported as "syntax errors" with zero
// symbols, which reads as "recon could not understand this file" when the truth
// is that the file declares nothing. Blanking the directives turns that into a
// clean parse and an honest empty result.
//
// Lines are replaced rather than removed so every symbol's reported line number
// still matches the file on disk.
//
// `#:` is not a valid preprocessor directive in any C# version, so this cannot
// collide with #if/#region/#pragma and does not need to be anchored to the top
// of the file.
func stripNonGrammarPreamble(data []byte, lang string) []byte {
	if lang != "csharp" || !bytes.Contains(data, []byte("#:")) {
		return data
	}

	lines := bytes.Split(data, []byte("\n"))
	changed := false
	for i, line := range lines {
		if bytes.HasPrefix(bytes.TrimLeft(line, " \t"), []byte("#:")) {
			lines[i] = nil
			changed = true
		}
	}
	if !changed {
		return data
	}
	return bytes.Join(lines, []byte("\n"))
}

// supplementWithRegex adds line-pattern symbols the grammar never reached to a
// partial tree-sitter result, returning the merged list and how many were
// added.
//
// Deduplication is on name+kind, which is deliberately conservative: it will
// decline to add a second overload of a method the grammar already found,
// preferring to under-recover rather than emit a duplicate symbol that an agent
// would read as two distinct declarations. Lines from the two extractors do not
// agree closely enough to dedupe on position.
//
// The merged list is sorted by line so the output does not depend on which
// extractor produced an entry.
func supplementWithRegex(tsSyms []Symbol, data []byte, f *scan.FileEntry) ([]Symbol, int) {
	patterns := patternsForLang(f.Lang)
	if len(patterns) == 0 {
		return tsSyms, 0
	}

	seen := make(map[string]bool, len(tsSyms))
	for _, s := range tsSyms {
		seen[s.Name+"\x00"+s.Kind] = true
	}

	merged := tsSyms
	added := 0
	for _, s := range extractSymbolsRegex(data, f.RelPath, f.Lang, patterns) {
		key := s.Name + "\x00" + s.Kind
		if seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, s)
		added++
	}
	if added == 0 {
		return tsSyms, 0
	}

	sortSymbols(merged)
	return merged, added
}

func langLabel(lang string) string {
	if lang == "" {
		return "unknown language"
	}
	return lang
}

// grammarCandidates returns the tree-sitter registry keys to try for a file, in
// preference order.
//
// A recon "language" does not always map to one grammar. TypeScript is the
// clearest case: .ts and .tsx are different languages that share an extension
// family, and the TSX grammar cannot parse angle-bracket type assertions
// (`<number>x`), which are legal and common in .ts. Parsing every .ts file as
// TSX silently truncated files at the first legacy cast. C headers are the
// other case: .h is claimed by both C and C++, and there is no way to tell from
// the name.
func grammarCandidates(lang, relPath string) []string {
	ext := strings.ToLower(filepath.Ext(relPath))
	switch {
	case lang == "typescript" && ext == ".tsx":
		return []string{tsxLangKey, "typescript"}
	case lang == "typescript":
		return []string{"typescript", tsxLangKey}
	case lang == "c" && ext == ".h":
		// A .h holding C++ (templates, namespaces, classes) parses badly under
		// the C grammar; a .h holding C parses fine under both. Try both and
		// keep the better result rather than guessing from the extension.
		return []string{"c", "cpp"}
	}
	if hasTSLang(lang) {
		return []string{lang}
	}
	return nil
}

// ScanFileSymbols extracts symbols for specific files (incremental).
func ScanFileSymbols(root string, files []*scan.FileEntry) []Symbol {
	var all []Symbol
	for _, f := range files {
		syms, _ := extractFileSymbols(root, f)
		all = append(all, syms...)
	}
	sortSymbols(all)
	return all
}

// ScanFileParses extracts symbols and per-file parse records for specific
// files (incremental), mirroring ScanFileSymbols.
func ScanFileParses(root string, files []*scan.FileEntry) ([]Symbol, []FileParse) {
	var all []Symbol
	parses := make([]FileParse, 0, len(files))
	for _, f := range files {
		syms, fp := extractFileSymbols(root, f)
		all = append(all, syms...)
		parses = append(parses, fp)
	}
	sortSymbols(all)
	sort.Slice(parses, func(i, j int) bool { return parses[i].RelPath < parses[j].RelPath })
	return all, parses
}
