package git

import (
	"sort"
	"strings"
)

// DefaultCoChangeMaxFiles is the per-commit file count above which a commit is
// excluded from CO-CHANGE (a 200-file reformat would otherwise manufacture
// ~20k spurious pairs). It deliberately does not apply to CHURN: those files
// genuinely changed, and dropping them is what makes a shallow clone or a
// large refactor look like a repository with no history at all.
const DefaultCoChangeMaxFiles = 50

// CoChangeOptions tunes the co-change build.
type CoChangeOptions struct {
	// MaxFilesPerCommit is the co-change cutoff. Zero means
	// DefaultCoChangeMaxFiles; negative means no cutoff.
	MaxFilesPerCommit int
}

func (o CoChangeOptions) maxFiles() int {
	if o.MaxFilesPerCommit == 0 {
		return DefaultCoChangeMaxFiles
	}
	return o.MaxFilesPerCommit
}

// CoChange tracks files that frequently appear in the same commits.
type CoChange struct {
	pairs map[string]map[string]int // file → file → co-occurrence count
	churn map[string]int            // file → number of commits touching it
	cov   Coverage
}

// NewCoChangeFromData creates a CoChange from pre-computed data.
// Coverage is unknown for cached data and reports as such.
func NewCoChangeFromData(pairs map[string]map[string]int, churn map[string]int) *CoChange {
	return &CoChange{pairs: pairs, churn: churn}
}

// Coverage reports how much history this co-change data was built from.
// The zero value (Repo false) means "unknown", e.g. when loaded from cache.
func (cc *CoChange) Coverage() Coverage {
	if cc == nil {
		return Coverage{}
	}
	return cc.cov
}

// SetCoverage attaches coverage to a CoChange built from cached data.
func (cc *CoChange) SetCoverage(cov Coverage) {
	if cc != nil {
		cc.cov = cov
	}
}

// Mine parses history under root and builds churn/co-change from it in one
// step, so callers get the data and its coverage together.
func Mine(root string, opts Options, ccOpts CoChangeOptions) (*CoChange, error) {
	res, err := ParseLogOpts(root, opts)
	if err != nil {
		return nil, err
	}
	cc := NewCoChangeOpts(res.Commits, ccOpts)
	cov := res.Coverage
	cov.CoChangeMaxFiles = cc.cov.CoChangeMaxFiles
	cov.CommitsOversized = cc.cov.CommitsOversized
	cc.cov = cov
	return cc, nil
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

// NewCoChange builds co-change data from parsed commits using the defaults.
func NewCoChange(commits []Commit) *CoChange {
	return NewCoChangeOpts(commits, CoChangeOptions{})
}

// NewCoChangeOpts builds co-change data from parsed commits.
//
// The per-commit file threshold applies to co-change PAIRS only. Churn counts
// every commit that touched a file, however large that commit was.
func NewCoChangeOpts(commits []Commit, opts CoChangeOptions) *CoChange {
	maxFiles := opts.maxFiles()

	cc := &CoChange{
		pairs: make(map[string]map[string]int),
		churn: make(map[string]int),
		cov:   Coverage{CoChangeMaxFiles: maxFiles},
	}

	for _, c := range commits {
		files := c.Files
		if len(files) == 0 {
			continue
		}
		cc.cov.CommitsWithFiles++

		// Churn: the file changed, no matter how big the commit was.
		for _, f := range files {
			cc.churn[f]++
		}

		if maxFiles > 0 && len(files) > maxFiles {
			// Co-change only: a sweeping commit pairs everything with
			// everything and would drown the real signal.
			cc.cov.CommitsOversized++
			continue
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
