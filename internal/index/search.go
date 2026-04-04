package index

import (
	"sort"
	"strings"

	"github.com/djtouchette/recon/internal/scan"
)

// SearchResult represents a single match from unified search.
type SearchResult struct {
	Path      string   `json:"path"`
	Score     float64  `json:"score"`
	MatchType string   `json:"match_type"` // "symbol", "file_path", "preview"
	Context   string   `json:"context"`    // matched symbol signature, preview line, or path
	Symbol    *Symbol  `json:"symbol,omitempty"`
}

// Search performs a unified search across symbols, file paths, and previews.
func Search(query string, idx *FileIndex, symbols *SymbolIndex, extras map[string]*FileExtra, maxResults int) []SearchResult {
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
		path    string
		score   float64
		matches []SearchResult
	}
	scores := make(map[string]*fileScore)

	addMatch := func(path string, score float64, result SearchResult) {
		fs, ok := scores[path]
		if !ok {
			fs = &fileScore{path: path}
			scores[path] = fs
		}
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
