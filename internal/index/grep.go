package index

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/djtouchette/recon/internal/scan"
)

// GrepMatch is a single enriched grep match.
type GrepMatch struct {
	Path         string
	Line         int
	Text         string
	MatchType    string // "definition", "reference", "comment", "test"
	FanIn        int
	FanOut       int
	HotspotScore float64
}

// maxFileSize is the largest file we'll grep (1MB). Skip likely binaries/generated.
const maxFileSize = 1 << 20

// matcher matches lines against either a literal pattern (fast byte scan) or
// a compiled regular expression. Matching is case-insensitive in both modes;
// prefix the pattern with (?-i) to opt into case-sensitive regex matching.
type matcher struct {
	literal []byte         // lowered pattern when it has no regex metacharacters
	re      *regexp.Regexp // compiled pattern otherwise
}

func newMatcher(pattern string) (*matcher, error) {
	// Literal fast path: a pattern with no regex metacharacters is matched
	// with byte-level substring scanning, which is equivalent and faster.
	if regexp.QuoteMeta(pattern) == pattern {
		return &matcher{literal: bytes.ToLower([]byte(pattern))}, nil
	}
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex %q: %w", pattern, err)
	}
	return &matcher{re: re}, nil
}

func (m *matcher) matchLine(line []byte) bool {
	if m.re != nil {
		return m.re.Match(line)
	}
	return bytesContainsLower(line, m.literal)
}

// Grep searches all source/test/config files for a pattern and enriches
// matches with symbol classification and file metrics. The pattern is a Go
// regular expression; patterns without metacharacters use a faster literal
// scan. Returns an error if the pattern is not a valid regex.
// Uses parallel file processing and bytes-level matching for speed.
func Grep(pattern string, root string, idx *FileIndex, symbols *SymbolIndex, metrics *MetricsIndex) ([]GrepMatch, error) {
	m, err := newMatcher(pattern)
	if err != nil {
		return nil, err
	}

	// Build definition location set: "path:line" → kind.
	defs := buildDefSet(symbols)

	// Collect eligible files.
	var files []*scan.FileEntry
	for _, f := range idx.All() {
		switch f.Class {
		case scan.ClassSource, scan.ClassTest, scan.ClassConfig:
			files = append(files, f)
		}
	}

	// Parallel grep across files.
	workers := runtime.GOMAXPROCS(0) * 2
	if workers > len(files) {
		workers = len(files)
	}
	if workers < 1 {
		workers = 1
	}

	type result struct {
		matches []GrepMatch
	}

	ch := make(chan *scan.FileEntry, len(files))
	for _, f := range files {
		ch <- f
	}
	close(ch)

	var mu sync.Mutex
	var allMatches []GrepMatch
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range ch {
				fileMatches := grepFileBytes(
					filepath.Join(root, f.RelPath),
					f.RelPath,
					m,
					f.Class,
					defs,
				)
				if len(fileMatches) == 0 {
					continue
				}

				// Enrich with metrics (read-only, safe without lock).
				if metrics != nil {
					if m := metrics.Get(f.RelPath); m != nil {
						for i := range fileMatches {
							fileMatches[i].FanIn = m.FanIn
							fileMatches[i].FanOut = m.FanOut
							fileMatches[i].HotspotScore = m.HotspotScore
						}
					}
				}

				mu.Lock()
				allMatches = append(allMatches, fileMatches...)
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	// Sort: definitions first, then by hotspot score desc, then path, then line.
	sort.Slice(allMatches, func(i, j int) bool {
		di := allMatches[i].MatchType == "definition"
		dj := allMatches[j].MatchType == "definition"
		if di != dj {
			return di
		}
		if allMatches[i].HotspotScore != allMatches[j].HotspotScore {
			return allMatches[i].HotspotScore > allMatches[j].HotspotScore
		}
		if allMatches[i].Path != allMatches[j].Path {
			return allMatches[i].Path < allMatches[j].Path
		}
		return allMatches[i].Line < allMatches[j].Line
	})

	return allMatches, nil
}

// grepFileBytes reads the entire file into memory and scans it line by line
// with the matcher. Skips files larger than maxFileSize.
func grepFileBytes(fullPath, relPath string, m *matcher, class scan.FileClass, defs map[string]string) []GrepMatch {
	// Stat first to skip large/binary files without reading.
	info, err := os.Stat(fullPath)
	if err != nil || info.Size() > maxFileSize || info.Size() == 0 {
		return nil
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil
	}

	// Quick check for literal patterns: if the whole file doesn't contain the
	// pattern, skip line scanning. Regex patterns go straight to the line scan
	// because anchors (^, $, \z) are relative to each line, not the file.
	if m.re == nil && !bytesContainsLower(data, m.literal) {
		return nil
	}

	// Check for binary content (null bytes in first 512 bytes).
	probe := data
	if len(probe) > 512 {
		probe = probe[:512]
	}
	if bytes.ContainsRune(probe, 0) {
		return nil
	}

	isTest := class == scan.ClassTest
	var matches []GrepMatch
	lineNum := 0
	offset := 0

	for offset < len(data) {
		lineNum++

		// Find end of line.
		end := bytes.IndexByte(data[offset:], '\n')
		var line []byte
		if end == -1 {
			line = data[offset:]
			offset = len(data)
		} else {
			line = data[offset : offset+end]
			offset += end + 1
		}

		if !m.matchLine(line) {
			continue
		}

		trimmed := strings.TrimSpace(string(line))
		matchType := classifyMatch(relPath, lineNum, trimmed, isTest, defs)

		if len(trimmed) > 200 {
			trimmed = trimmed[:200]
		}

		matches = append(matches, GrepMatch{
			Path:      relPath,
			Line:      lineNum,
			Text:      trimmed,
			MatchType: matchType,
		})
	}

	return matches
}

// bytesContainsLower checks if line contains pattern (already lowered)
// by lowering each byte of line inline — avoids allocating a lowered copy.
func bytesContainsLower(line, patternLower []byte) bool {
	pLen := len(patternLower)
	lLen := len(line)
	if pLen > lLen {
		return false
	}
	// Scan through line looking for pattern match.
	limit := lLen - pLen + 1
	for i := 0; i < limit; i++ {
		found := true
		for j := 0; j < pLen; j++ {
			c := line[i+j]
			// Fast ASCII lowercase.
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != patternLower[j] {
				found = false
				break
			}
		}
		if found {
			return true
		}
	}
	return false
}

func buildDefSet(symbols *SymbolIndex) map[string]string {
	defs := make(map[string]string)
	if symbols == nil {
		return defs
	}
	for _, sym := range symbols.All() {
		key := sym.File + ":" + itoa(sym.Line)
		defs[key] = sym.Kind
	}
	return defs
}

// classifyMatch determines if a line is a definition, comment, test, or reference.
func classifyMatch(path string, line int, text string, isTest bool, defs map[string]string) string {
	key := path + ":" + itoa(line)
	if _, ok := defs[key]; ok {
		return "definition"
	}

	// Comment patterns (check first byte for speed).
	if len(text) > 0 {
		switch text[0] {
		case '/', '#', '*':
			if strings.HasPrefix(text, "//") || strings.HasPrefix(text, "/*") ||
				strings.HasPrefix(text, "#") || strings.HasPrefix(text, "* ") {
				return "comment"
			}
		case '@':
			if strings.HasPrefix(text, "@doc") || strings.HasPrefix(text, "@moduledoc") ||
				strings.HasPrefix(text, "@spec") {
				return "comment"
			}
		}
	}

	if isTest {
		return "test"
	}

	return "reference"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
