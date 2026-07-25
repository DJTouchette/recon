package index

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/djtouchette/recon/internal/scan"
)

// testDemotion scales the final score of a test file when an implementation
// file also matched the query.
//
// Every tier of this scorer accumulates into one per-file total that is then
// clamped to 1.0, so `FooService.cs` (exact symbol 1.0 + basename 0.6 = 1.6)
// and `FooServiceTests.cs` (prefix symbol 0.9 + basename 0.6 = 1.5) both arrive
// at exactly 1.0 and the only thing separating them is the alphabetical
// path tiebreak. On a real C# tree that tiebreak decides at random: searching
// `MeApi` put `backend/tests/.../MeApiTests.cs` first and the actual
// `clients/mobile/src/.../MeApi.cs` fourth, because "backend" sorts before
// "clients". Sending an agent that asked "where is X" to the tests for X is the
// worst answer this tool can give.
//
// 0.85 is deliberately small. It is enough to break the 1.0 clamp tie against
// any implementation that also scored in the top tier, but not enough to push a
// top-tier test (0.85) below the file-path tier (0.6) or the preview/content
// tiers (0.3/0.2). Tests that are legitimately the only answer — E2E suites,
// golden-file suites, integration tests with no same-named source file — keep
// their position, because the demotion is uniform and so cannot reorder tests
// among themselves.
const testDemotion = 0.85

// testIntentTokens are whole query words that mean the caller is asking for
// tests. Only exact tokens count: a *suffix* rule would fire on domain nouns
// ("LabTest", "TestResult", "SmokeSpec"), which are common enough in real
// codebases — Leroy has LabTest, LabTestRepository and CombinedVaccinationTestData —
// that it would silently disable the demotion for a whole domain.
var testIntentTokens = map[string]bool{
	"test": true, "tests": true, "testing": true,
	"spec": true, "specs": true,
}

// SearchResult represents a single match from unified search.
type SearchResult struct {
	Path      string  `json:"path"`
	Score     float64 `json:"score"`
	MatchType string  `json:"match_type"` // "symbol", "file_path", "preview", "content"
	Context   string  `json:"context"`    // matched symbol signature, preview line, or path
	Symbol    *Symbol `json:"symbol,omitempty"`
}

// Search performs a unified search across symbols, file paths, previews, and file content.
func Search(query string, root string, idx *FileIndex, symbols *SymbolIndex, extras map[string]*FileExtra, maxResults int) []SearchResult {
	if maxResults <= 0 {
		maxResults = 30
	}
	if query == "" {
		return nil
	}

	q := strings.ToLower(query)
	tokens := strings.Fields(q)

	// Score accumulator per file path
	type fileScore struct {
		path  string
		score float64
		// namedExactly is set when the query *is* this file's own name — an
		// exact symbol name or an exact basename. A test file the caller asked
		// for by name is not demoted: `AprysePdfGoldenTests` should return
		// AprysePdfGoldenTests.cs at full score.
		namedExactly bool
		matches      []SearchResult
	}
	scores := make(map[string]*fileScore)

	get := func(path string) *fileScore {
		fs, ok := scores[path]
		if !ok {
			fs = &fileScore{path: path}
			scores[path] = fs
		}
		return fs
	}

	addMatch := func(path string, score float64, result SearchResult) {
		fs := get(path)
		fs.score += score
		fs.matches = append(fs.matches, result)
	}

	// --- Symbol matches (highest weight) ---
	if symbols != nil {
		for _, sym := range symbols.All() {
			nameLower := strings.ToLower(sym.Name)
			matched := false
			for _, tok := range tokens {
				if strings.Contains(nameLower, tok) {
					matched = true
				} else {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}

			// Exact name match scores higher
			weight := 0.7
			if nameLower == q {
				weight = 1.0
			} else if strings.HasPrefix(nameLower, q) {
				weight = 0.9
			}

			if nameLower == q {
				get(sym.File).namedExactly = true
			}

			s := sym // copy
			addMatch(sym.File, weight, SearchResult{
				Path:      sym.File,
				Score:     weight,
				MatchType: "symbol",
				Context:   sym.Signature,
				Symbol:    &s,
			})
		}
	}

	// --- File path matches (medium weight) ---
	for _, f := range idx.All() {
		pathLower := strings.ToLower(f.RelPath)
		matched := true
		for _, tok := range tokens {
			if !strings.Contains(pathLower, tok) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}

		// Only count source and test files for path matching
		if f.Class != scan.ClassSource && f.Class != scan.ClassTest && f.Class != scan.ClassConfig {
			continue
		}

		weight := 0.4
		baseLower := strings.ToLower(strings.TrimSuffix(f.RelPath[strings.LastIndex(f.RelPath, "/")+1:], ""))
		if strings.Contains(baseLower, q) {
			weight = 0.6 // basename match is stronger
		}
		if strings.TrimSuffix(baseLower, strings.ToLower(filepath.Ext(baseLower))) == q {
			get(f.RelPath).namedExactly = true
		}

		addMatch(f.RelPath, weight, SearchResult{
			Path:      f.RelPath,
			Score:     weight,
			MatchType: "file_path",
			Context:   f.RelPath,
		})
	}

	// --- Preview matches (lower weight) ---
	for path, extra := range extras {
		if extra.Preview == "" {
			continue
		}
		previewLower := strings.ToLower(extra.Preview)
		matched := true
		for _, tok := range tokens {
			if !strings.Contains(previewLower, tok) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}

		// Extract the matching line from the preview
		contextLine := ""
		for _, line := range strings.Split(extra.Preview, "\n") {
			lineLower := strings.ToLower(line)
			allTokens := true
			for _, tok := range tokens {
				if !strings.Contains(lineLower, tok) {
					allTokens = false
					break
				}
			}
			if allTokens {
				contextLine = strings.TrimSpace(line)
				break
			}
		}
		if contextLine == "" {
			contextLine = strings.TrimSpace(strings.Split(extra.Preview, "\n")[0])
		}

		addMatch(path, 0.3, SearchResult{
			Path:      path,
			Score:     0.3,
			MatchType: "preview",
			Context:   contextLine,
		})
	}

	// --- Content grep (lowest weight) ---
	// Only grep files not already matched by higher-tier searches.
	if root != "" {
		for _, f := range idx.All() {
			if f.Class != scan.ClassSource && f.Class != scan.ClassTest && f.Class != scan.ClassConfig {
				continue
			}
			if _, already := scores[f.RelPath]; already {
				continue
			}
			if line := grepFile(filepath.Join(root, f.RelPath), tokens); line != "" {
				addMatch(f.RelPath, 0.2, SearchResult{
					Path:      f.RelPath,
					Score:     0.2,
					MatchType: "content",
					Context:   line,
				})
			}
		}
	}

	// Should test files be demoted? Only when the caller did not ask for tests
	// and something other than a test also matched — a demotion applied to a
	// result set that is entirely tests would deflate every score without
	// changing a single position.
	demoteTests := true
	for _, tok := range tokens {
		if testIntentTokens[tok] {
			demoteTests = false
			break
		}
	}
	if demoteTests {
		demoteTests = false
		for path := range scores {
			if f := idx.Get(path); f != nil && f.Class != scan.ClassTest {
				demoteTests = true
				break
			}
		}
	}

	// Flatten: pick the best match per file, use accumulated score for ranking
	var results []SearchResult
	for _, fs := range scores {
		// Find the highest-scoring individual match for this file
		best := fs.matches[0]
		for _, m := range fs.matches[1:] {
			if m.Score > best.Score {
				best = m
			}
		}
		best.Score = fs.score
		if best.Score > 1.0 {
			best.Score = 1.0
		}
		// Demote after the clamp, not before: every top-tier match saturates at
		// 1.0, so a demotion folded in beforehand would be clamped away.
		if demoteTests && !fs.namedExactly {
			if f := idx.Get(fs.path); f != nil && f.Class == scan.ClassTest {
				best.Score *= testDemotion
			}
		}
		results = append(results, best)
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Path < results[j].Path
	})

	if len(results) > maxResults {
		results = results[:maxResults]
	}

	return results
}

// grepFile scans a file line-by-line and returns the first line containing all tokens.
// Returns empty string if no match or file can't be read.
//
// This reads the file rather than using a bufio.Scanner: the default scanner
// gives up at the first line longer than 64KB and returns an error nobody
// checked, so everything after a minified line in a bundle was silently
// unsearchable. readSource also rejects binary content and normalises UTF-16.
func grepFile(path string, tokens []string) string {
	data, err := readSource(path, maxFileSize)
	if err != nil {
		return ""
	}

	for _, line := range splitLines(data) {
		lineLower := strings.ToLower(line)
		allMatch := true
		for _, tok := range tokens {
			if !strings.Contains(lineLower, tok) {
				allMatch = false
				break
			}
		}
		if allMatch {
			trimmed := strings.TrimSpace(line)
			if len(trimmed) > 200 {
				trimmed = trimmed[:200]
			}
			return trimmed
		}
	}
	return ""
}
