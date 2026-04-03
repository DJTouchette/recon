package index

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/djtouchette/recon/internal/scan"
)

// Symbol represents a named declaration in a source file.
type Symbol struct {
	File      string `json:"file"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`      // function, method, class, interface, type, struct, enum, constant, module, trait
	Line      int    `json:"line"`
	Signature string `json:"signature"` // the full declaration line, trimmed
}

// FileExtra holds per-file metadata beyond the basic FileEntry.
type FileExtra struct {
	RelPath     string
	Preview     string // first meaningful lines
	ContentHash string // sha256 of file content
}

// SymbolIndex holds all extracted symbols.
type SymbolIndex struct {
	byFile map[string][]Symbol
	all    []Symbol
}

// NewSymbolIndex extracts symbols from all source files in the index.
func NewSymbolIndex(root string, idx *FileIndex) *SymbolIndex {
	si := &SymbolIndex{
		byFile: make(map[string][]Symbol),
	}

	sources := make([]*scan.FileEntry, 0, len(idx.ByClass(scan.ClassSource))+len(idx.ByClass(scan.ClassTest)))
	sources = append(sources, idx.ByClass(scan.ClassSource)...)

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0)*2)

	for _, f := range sources {
		patterns := patternsForLang(f.Lang)
		if len(patterns) == 0 {
			continue
		}

		f := f
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			syms := extractSymbols(filepath.Join(root, f.RelPath), f.RelPath, patterns)
			if len(syms) == 0 {
				return
			}

			mu.Lock()
			si.byFile[f.RelPath] = syms
			si.all = append(si.all, syms...)
			mu.Unlock()
		}()
	}

	wg.Wait()
	return si
}

// NewSymbolIndexFromData creates a SymbolIndex from pre-loaded data.
func NewSymbolIndexFromData(symbols []Symbol) *SymbolIndex {
	si := &SymbolIndex{
		byFile: make(map[string][]Symbol),
		all:    symbols,
	}
	for i := range symbols {
		s := &symbols[i]
		si.byFile[s.File] = append(si.byFile[s.File], *s)
	}
	return si
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

func extractPreview(path, lang string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var lines []string
	lineNum := 0
	maxLines := 200 // scan up to 200 lines to find meaningful content
	collected := 0
	maxCollect := 5 // collect up to 5 meaningful lines

	for scanner.Scan() && lineNum < maxLines && collected < maxCollect {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

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

		lines = append(lines, line)
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

// --- Symbol extraction patterns ---

type symbolPattern struct {
	regex *regexp.Regexp
	kind  string
}

var langPatterns = map[string][]symbolPattern{
	"go":         goPatterns,
	"typescript":  tsPatterns,
	"javascript":  tsPatterns, // shares TS patterns
	"csharp":      csharpPatterns,
	"java":        javaPatterns,
	"kotlin":      javaPatterns, // close enough
	"python":      pythonPatterns,
	"rust":        rustPatterns,
	"ruby":        rubyPatterns,
	"elixir":      elixirPatterns,
	"php":         phpPatterns,
	"swift":       swiftPatterns,
}

func patternsForLang(lang string) []symbolPattern {
	return langPatterns[lang]
}

func extractSymbols(fullPath, relPath string, patterns []symbolPattern) []Symbol {
	file, err := os.Open(fullPath)
	if err != nil {
		return nil
	}
	defer file.Close()

	var symbols []Symbol
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
			continue
		}

		for _, p := range patterns {
			m := p.regex.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			name := m[1]
			// Skip private/underscore names in some languages
			if name == "_" || name == "" {
				continue
			}
			symbols = append(symbols, Symbol{
				File:      relPath,
				Name:      name,
				Kind:      p.kind,
				Line:      lineNum,
				Signature: trimSig(trimmed),
			})
			break // one match per line
		}
	}

	return symbols
}

// trimSig truncates long signatures to keep DB compact.
func trimSig(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// --- Compiled patterns per language ---

var goPatterns = compilePatterns([]rawPattern{
	{`^func\s+(\w+)\s*\(`, "function"},
	{`^func\s+\([^)]+\)\s+(\w+)\s*\(`, "method"},
	{`^type\s+(\w+)\s+struct\b`, "struct"},
	{`^type\s+(\w+)\s+interface\b`, "interface"},
	{`^type\s+(\w+)\s+`, "type"},
	{`^var\s+(\w+)\s+`, "var"},
	{`^const\s+(\w+)\s+`, "constant"},
})

var tsPatterns = compilePatterns([]rawPattern{
	{`^export\s+(?:async\s+)?function\s+(\w+)`, "function"},
	{`^export\s+class\s+(\w+)`, "class"},
	{`^export\s+abstract\s+class\s+(\w+)`, "class"},
	{`^export\s+interface\s+(\w+)`, "interface"},
	{`^export\s+type\s+(\w+)`, "type"},
	{`^export\s+const\s+(\w+)`, "constant"},
	{`^export\s+enum\s+(\w+)`, "enum"},
	{`^export\s+default\s+(?:class|function)\s+(\w+)`, "function"},
	{`^\s*(?:async\s+)?function\s+(\w+)`, "function"},
	{`^\s*class\s+(\w+)`, "class"},
	{`^\s*interface\s+(\w+)`, "interface"},
	{`^\s*(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?\(`, "function"},      // arrow fn
	{`^\s*(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?function`, "function"}, // fn expression
})

var csharpPatterns = compilePatterns([]rawPattern{
	{`(?:public|internal|protected|private)\s+(?:static\s+)?(?:partial\s+)?(?:abstract\s+)?class\s+(\w+)`, "class"},
	{`(?:public|internal|protected|private)\s+(?:static\s+)?interface\s+(\w+)`, "interface"},
	{`(?:public|internal|protected|private)\s+(?:static\s+)?enum\s+(\w+)`, "enum"},
	{`(?:public|internal|protected|private)\s+(?:static\s+)?struct\s+(\w+)`, "struct"},
	{`(?:public|internal|protected|private)\s+(?:static\s+)?record\s+(\w+)`, "class"},
	// Methods — match return-type + name + paren
	{`(?:public|internal|protected|private)\s+(?:static\s+)?(?:virtual\s+)?(?:override\s+)?(?:async\s+)?(?:[\w<>\[\],\s]+?)\s+(\w+)\s*\(`, "method"},
	// Properties
	{`(?:public|internal|protected|private)\s+(?:static\s+)?(?:[\w<>\[\]?]+)\s+(\w+)\s*\{\s*get`, "property"},
	// Delegate
	{`(?:public|internal|protected|private)\s+delegate\s+\S+\s+(\w+)\s*\(`, "delegate"},
})

var javaPatterns = compilePatterns([]rawPattern{
	{`(?:public|protected|private)\s+(?:static\s+)?(?:abstract\s+)?(?:final\s+)?class\s+(\w+)`, "class"},
	{`(?:public|protected|private)\s+interface\s+(\w+)`, "interface"},
	{`(?:public|protected|private)\s+enum\s+(\w+)`, "enum"},
	{`(?:public|protected|private)\s+(?:static\s+)?(?:final\s+)?(?:synchronized\s+)?(?:[\w<>\[\]]+)\s+(\w+)\s*\(`, "method"},
	{`@interface\s+(\w+)`, "annotation"},
})

var pythonPatterns = compilePatterns([]rawPattern{
	{`^class\s+(\w+)`, "class"},
	{`^def\s+(\w+)`, "function"},
	{`^async\s+def\s+(\w+)`, "function"},
	{`^\s{4}def\s+(\w+)`, "method"},
	{`^\s{4}async\s+def\s+(\w+)`, "method"},
	{`^([A-Z][A-Z_0-9]+)\s*=`, "constant"}, // UPPER_SNAKE constants
})

var rustPatterns = compilePatterns([]rawPattern{
	{`^pub(?:\(crate\))?\s+(?:async\s+)?fn\s+(\w+)`, "function"},
	{`^pub(?:\(crate\))?\s+struct\s+(\w+)`, "struct"},
	{`^pub(?:\(crate\))?\s+enum\s+(\w+)`, "enum"},
	{`^pub(?:\(crate\))?\s+trait\s+(\w+)`, "trait"},
	{`^pub(?:\(crate\))?\s+type\s+(\w+)`, "type"},
	{`^pub(?:\(crate\))?\s+const\s+(\w+)`, "constant"},
	{`^pub(?:\(crate\))?\s+static\s+(\w+)`, "constant"},
	{`^\s+pub(?:\(crate\))?\s+(?:async\s+)?fn\s+(\w+)`, "method"},
	{`^\s+fn\s+(\w+)`, "method"}, // impl methods
})

var rubyPatterns = compilePatterns([]rawPattern{
	{`^class\s+(\w+)`, "class"},
	{`^module\s+(\w+)`, "module"},
	{`^\s+def\s+self\.(\w+)`, "function"},
	{`^\s+def\s+(\w+)`, "method"},
	{`^\s+attr_(?:reader|writer|accessor)\s+:(\w+)`, "property"},
	{`([A-Z][A-Z_0-9]+)\s*=`, "constant"},
})

var elixirPatterns = compilePatterns([]rawPattern{
	{`^defmodule\s+([\w.]+)`, "module"},
	{`^\s+def\s+(\w+)`, "function"},
	{`^\s+defp\s+(\w+)`, "function"},
	{`^\s+defmacro\s+(\w+)`, "macro"},
	{`^\s+defstruct\b`, "struct"},
})

var phpPatterns = compilePatterns([]rawPattern{
	{`(?:abstract\s+)?class\s+(\w+)`, "class"},
	{`interface\s+(\w+)`, "interface"},
	{`trait\s+(\w+)`, "trait"},
	{`(?:public|protected|private)\s+(?:static\s+)?function\s+(\w+)`, "method"},
	{`^function\s+(\w+)`, "function"},
})

var swiftPatterns = compilePatterns([]rawPattern{
	{`(?:public|open|internal)\s+class\s+(\w+)`, "class"},
	{`(?:public|open|internal)\s+struct\s+(\w+)`, "struct"},
	{`(?:public|open|internal)\s+enum\s+(\w+)`, "enum"},
	{`(?:public|open|internal)\s+protocol\s+(\w+)`, "interface"},
	{`(?:public|open|internal)\s+func\s+(\w+)`, "function"},
	{`^\s+func\s+(\w+)`, "method"},
})

type rawPattern struct {
	pattern string
	kind    string
}

func compilePatterns(raw []rawPattern) []symbolPattern {
	compiled := make([]symbolPattern, len(raw))
	for i, r := range raw {
		compiled[i] = symbolPattern{
			regex: regexp.MustCompile(r.pattern),
			kind:  r.kind,
		}
	}
	return compiled
}

// ScanFileSymbols extracts symbols for specific files (incremental).
func ScanFileSymbols(root string, files []*scan.FileEntry) []Symbol {
	var all []Symbol
	for _, f := range files {
		patterns := patternsForLang(f.Lang)
		if len(patterns) == 0 {
			continue
		}
		syms := extractSymbols(filepath.Join(root, f.RelPath), f.RelPath, patterns)
		all = append(all, syms...)
	}
	return all
}
