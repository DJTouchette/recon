package git

import (
	"sort"
	"strings"
)

// CoChange tracks files that frequently appear in the same commits.
type CoChange struct {
	pairs map[string]map[string]int // file → file → co-occurrence count
	churn map[string]int            // file → number of commits touching it
}

// NewCoChangeFromData creates a CoChange from pre-computed data.
func NewCoChangeFromData(pairs map[string]map[string]int, churn map[string]int) *CoChange {
	return &CoChange{pairs: pairs, churn: churn}
}

// AllPairs returns the full co-change pair map.
func (cc *CoChange) AllPairs() map[string]map[string]int {
	if cc == nil {
		return nil
	}
	return cc.pairs
}

// AllChurn returns the full churn map.
func (cc *CoChange) AllChurn() map[string]int {
	if cc == nil {
		return nil
	}
	return cc.churn
}

// NewCoChange builds co-change data from parsed commits.
func NewCoChange(commits []Commit) *CoChange {
	cc := &CoChange{
		pairs: make(map[string]map[string]int),
		churn: make(map[string]int),
	}

	for _, c := range commits {
		files := c.Files
		if len(files) > 50 {
			// Skip very large commits (likely merges/reformats)
			continue
		}

		for _, f := range files {
			cc.churn[f]++
		}

		// Build pairs
		for i := 0; i < len(files); i++ {
			for j := i + 1; j < len(files); j++ {
				a, b := files[i], files[j]
				if a > b {
					a, b = b, a
				}
				if cc.pairs[a] == nil {
					cc.pairs[a] = make(map[string]int)
				}
				cc.pairs[a][b]++
			}
		}
	}

	return cc
}

// CoChangedWith returns files that frequently co-change with the given file,
// sorted by frequency descending. Only returns pairs with minCount or more co-occurrences.
func (cc *CoChange) CoChangedWith(path string, minCount int) []CoChangePair {
	if cc == nil {
		return nil
	}

	var pairs []CoChangePair

	// Check both directions since we normalize a < b
	if m, ok := cc.pairs[path]; ok {
		for other, count := range m {
			if count >= minCount {
				pairs = append(pairs, CoChangePair{File: other, Count: count})
			}
		}
	}

	// Also check where path is the "b" in normalized pairs
	for a, m := range cc.pairs {
		if count, ok := m[path]; ok && count >= minCount && a != path {
			pairs = append(pairs, CoChangePair{File: a, Count: count})
		}
	}

	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].Count > pairs[j].Count
	})

	return pairs
}

type CoChangePair struct {
	File  string
	Count int
}

// Churn returns the top N files by commit frequency.
func (cc *CoChange) Churn(topN int) []ChurnEntry {
	var entries []ChurnEntry
	for f, count := range cc.churn {
		entries = append(entries, ChurnEntry{File: f, Commits: count})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Commits > entries[j].Commits
	})

	if topN > 0 && len(entries) > topN {
		entries = entries[:topN]
	}
	return entries
}

type ChurnEntry struct {
	File    string
	Commits int
}

// AreasFromFiles extracts top-level directory areas from file paths.
func AreasFromFiles(files []string) []string {
	seen := make(map[string]bool)
	var areas []string
	for _, f := range files {
		parts := strings.SplitN(f, "/", 2)
		area := parts[0]
		if !seen[area] {
			seen[area] = true
			areas = append(areas, area)
		}
	}
	return areas
}
