package index

import (
	"math"
	"sort"

	gitpkg "github.com/djtouchette/recon/internal/git"
)

// FileMetrics holds dependency and churn metrics for a single file.
type FileMetrics struct {
	RelPath      string  `json:"rel_path"`
	FanIn        int     `json:"fan_in"`        // files that import this
	FanOut       int     `json:"fan_out"`       // files this imports
	Churn        int     `json:"churn"`         // commits touching this file
	HotspotScore float64 `json:"hotspot_score"` // normalized fan_in * churn — high = risky to change
}

// ComputeMetrics calculates fan-in, fan-out, churn, and hotspot scores.
func ComputeMetrics(deps *DepGraph, cochange *gitpkg.CoChange) []FileMetrics {
	fanIn := make(map[string]int)
	fanOut := make(map[string]int)

	if deps != nil {
		for src, targets := range deps.AllImports() {
			fanOut[src] = len(targets)
			for _, t := range targets {
				fanIn[t]++
			}
		}
	}

	churn := make(map[string]int)
	if cochange != nil {
		for path, commits := range cochange.AllChurn() {
			churn[path] = commits
		}
	}

	// Collect all files that have any metric
	allFiles := make(map[string]bool)
	for f := range fanIn {
		allFiles[f] = true
	}
	for f := range fanOut {
		allFiles[f] = true
	}
	for f := range churn {
		allFiles[f] = true
	}

	var metrics []FileMetrics
	var maxRaw float64

	for f := range allFiles {
		m := FileMetrics{
			RelPath: f,
			FanIn:   fanIn[f],
			FanOut:  fanOut[f],
			Churn:   churn[f],
		}
		raw := float64(m.FanIn) * float64(m.Churn)
		if raw > maxRaw {
			maxRaw = raw
		}
		m.HotspotScore = raw
		metrics = append(metrics, m)
	}

	// Normalize hotspot scores to 0-1
	if maxRaw > 0 {
		for i := range metrics {
			metrics[i].HotspotScore = metrics[i].HotspotScore / maxRaw
		}
	}

	sort.Slice(metrics, func(i, j int) bool {
		return metrics[i].HotspotScore > metrics[j].HotspotScore
	})

	return metrics
}

// MetricsIndex provides fast lookup of file metrics.
type MetricsIndex struct {
	byPath map[string]*FileMetrics
	ranked []FileMetrics // sorted by hotspot descending
}

// NewMetricsIndex creates a lookup from computed metrics.
func NewMetricsIndex(metrics []FileMetrics) *MetricsIndex {
	mi := &MetricsIndex{
		byPath: make(map[string]*FileMetrics, len(metrics)),
		ranked: metrics,
	}
	for i := range metrics {
		mi.byPath[metrics[i].RelPath] = &metrics[i]
	}
	return mi
}

// Get returns metrics for a file.
func (mi *MetricsIndex) Get(path string) *FileMetrics {
	if mi == nil {
		return nil
	}
	return mi.byPath[path]
}

// Hotspots returns the top N files by hotspot score.
func (mi *MetricsIndex) Hotspots(n int) []FileMetrics {
	if mi == nil {
		return nil
	}
	if n <= 0 || n > len(mi.ranked) {
		n = len(mi.ranked)
	}
	// Filter to files with non-zero hotspot
	var result []FileMetrics
	for _, m := range mi.ranked {
		if m.HotspotScore <= 0 {
			break
		}
		result = append(result, m)
		if len(result) >= n {
			break
		}
	}
	return result
}

// All returns all metrics.
func (mi *MetricsIndex) All() []FileMetrics {
	if mi == nil {
		return nil
	}
	return mi.ranked
}

// HighFanIn returns files with fan-in above the given percentile (0-1).
func (mi *MetricsIndex) HighFanIn(percentile float64) []FileMetrics {
	if mi == nil || len(mi.ranked) == 0 {
		return nil
	}

	// Compute threshold
	fanIns := make([]int, 0, len(mi.ranked))
	for _, m := range mi.ranked {
		if m.FanIn > 0 {
			fanIns = append(fanIns, m.FanIn)
		}
	}
	if len(fanIns) == 0 {
		return nil
	}
	sort.Ints(fanIns)
	idx := int(math.Floor(percentile * float64(len(fanIns)-1)))
	threshold := fanIns[idx]

	var result []FileMetrics
	for _, m := range mi.ranked {
		if m.FanIn >= threshold {
			result = append(result, m)
		}
	}
	return result
}
