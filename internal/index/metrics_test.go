package index

import (
	"testing"

	gitpkg "github.com/djtouchette/recon/internal/git"
)

// ComputeMetrics builds its slice by ranging a map, so without a tie-break the
// output order is Go's randomized map order for every equal score. Ties are the
// common case here — fan-in is shared across a package, so scores collapse into
// a handful of distinct values — and callers take the top N, which means
// *membership* changed between runs, not just order. Files drifted in and out of
// "the riskiest 20" on an unchanged tree.
func TestComputeMetricsIsDeterministic(t *testing.T) {
	deps := NewDepGraphFromData(map[string][]string{
		"a.go": {"z.go"},
		"b.go": {"z.go"},
		"c.go": {"y.go"},
		"d.go": {"y.go"},
		"e.go": {"x.go"},
	})
	cc := gitpkg.NewCoChangeFromData(nil, map[string]int{
		"z.go": 2, "y.go": 2, "x.go": 2, "a.go": 1, "b.go": 1,
	})

	first := ComputeMetrics(deps, cc)

	// Many runs, because map iteration order is randomized per-range: a single
	// repeat can pass by luck.
	for i := 0; i < 50; i++ {
		got := ComputeMetrics(deps, cc)
		if len(got) != len(first) {
			t.Fatalf("run %d: length %d, want %d", i, len(got), len(first))
		}
		for j := range got {
			if got[j].RelPath != first[j].RelPath {
				t.Fatalf("run %d position %d: %q, want %q — ordering is not stable",
					i, j, got[j].RelPath, first[j].RelPath)
			}
		}
	}
}

// Equal scores order by path; unequal scores still order by score.
func TestComputeMetricsOrdersByScoreThenPath(t *testing.T) {
	// zebra and apple each have fan-in 2 and equal churn, so they tie.
	// hot.go has a strictly higher product and must lead regardless of path.
	deps := NewDepGraphFromData(map[string][]string{
		"i1.go": {"zebra.go", "apple.go", "hot.go"},
		"i2.go": {"zebra.go", "apple.go", "hot.go"},
	})
	cc := gitpkg.NewCoChangeFromData(nil, map[string]int{
		"zebra.go": 1, "apple.go": 1, "hot.go": 9,
	})

	got := ComputeMetrics(deps, cc)
	if len(got) == 0 {
		t.Fatal("expected metrics")
	}

	if got[0].RelPath != "hot.go" {
		t.Errorf("highest score should lead, got %q", got[0].RelPath)
	}

	var tied []string
	for _, m := range got {
		if m.RelPath == "zebra.go" || m.RelPath == "apple.go" {
			tied = append(tied, m.RelPath)
		}
	}
	if len(tied) != 2 {
		t.Fatalf("expected both tied files, got %v", tied)
	}
	if tied[0] != "apple.go" {
		t.Errorf("tied entries should order by path, got %v", tied)
	}
}
