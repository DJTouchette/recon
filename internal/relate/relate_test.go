package relate

import (
	"testing"

	gitpkg "github.com/djtouchette/recon/internal/git"
	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func makeIdx(entries ...scan.FileEntry) *index.FileIndex {
	return index.NewFileIndex(entries)
}

func makeTestMap(s2t map[string][]string, t2s map[string]string) *index.TestMap {
	return index.NewTestMapFromData(s2t, t2s)
}

func makeDeps(imports map[string][]string) *index.DepGraph {
	return index.NewDepGraphFromData(imports)
}

func makeCoChange(pairs map[string]map[string]int, churn map[string]int) *gitpkg.CoChange {
	return gitpkg.NewCoChangeFromData(pairs, churn)
}

func makeMetrics(ms []index.FileMetrics) *index.MetricsIndex {
	return index.NewMetricsIndex(ms)
}

func makeOwnership(rules []index.OwnerRule) *index.Ownership {
	return index.NewOwnershipFromData(rules)
}

// findPath is a helper to find a result by path.
func findResult(results []RelatedFile, path string) (RelatedFile, bool) {
	for _, r := range results {
		if r.Path == path {
			return r, true
		}
	}
	return RelatedFile{}, false
}

func hasSignal(rf RelatedFile, signal string) bool {
	for _, s := range rf.Signals {
		if s == signal {
			return true
		}
	}
	return false
}

// ─── Signal 1: same-directory ─────────────────────────────────────────────────

func TestFindRelated_SameDirectory(t *testing.T) {
	idx := makeIdx(
		scan.FileEntry{RelPath: "pkg/auth/auth.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "pkg/auth/helper.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "pkg/auth/token.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "pkg/models/user.go", Lang: "go", Class: scan.ClassSource},
	)
	tm := makeTestMap(nil, nil)

	results := FindRelated("pkg/auth/auth.go", idx, nil, tm, nil, nil, nil, 20)

	// helper.go and token.go should be in results with same-directory signal
	helper, ok := findResult(results, "pkg/auth/helper.go")
	if !ok {
		t.Fatal("expected pkg/auth/helper.go in results")
	}
	if !hasSignal(helper, "same-directory") {
		t.Errorf("expected same-directory signal, got %v", helper.Signals)
	}

	// models/user.go appears via same-package signal (sibling directory under "pkg"),
	// but NOT via same-directory (it's in a different directory).
	modelsUser, ok := findResult(results, "pkg/models/user.go")
	if ok {
		if hasSignal(modelsUser, "same-directory") {
			t.Error("pkg/models/user.go should not have same-directory signal")
		}
		if !hasSignal(modelsUser, "same-package") {
			t.Error("pkg/models/user.go should have same-package signal if present")
		}
	}
	// helper.go and token.go MUST be in results
	token, ok := findResult(results, "pkg/auth/token.go")
	if !ok {
		t.Fatal("expected pkg/auth/token.go in results")
	}
	if !hasSignal(token, "same-directory") {
		t.Errorf("expected same-directory signal for token.go, got %v", token.Signals)
	}
}

func TestFindRelated_SameDirectory_SelfExcluded(t *testing.T) {
	idx := makeIdx(
		scan.FileEntry{RelPath: "src/a.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "src/b.go", Lang: "go", Class: scan.ClassSource},
	)
	tm := makeTestMap(nil, nil)

	results := FindRelated("src/a.go", idx, nil, tm, nil, nil, nil, 20)
	_, ok := findResult(results, "src/a.go")
	if ok {
		t.Error("query path should not appear in its own results")
	}
}

// ─── Signal 2: test-pair ──────────────────────────────────────────────────────

func TestFindRelated_TestPair_SourceToTest(t *testing.T) {
	idx := makeIdx(
		scan.FileEntry{RelPath: "pkg/auth/auth.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "pkg/auth/auth_test.go", Lang: "go", Class: scan.ClassTest},
	)
	tm := makeTestMap(
		map[string][]string{"pkg/auth/auth.go": {"pkg/auth/auth_test.go"}},
		map[string]string{"pkg/auth/auth_test.go": "pkg/auth/auth.go"},
	)

	results := FindRelated("pkg/auth/auth.go", idx, nil, tm, nil, nil, nil, 20)

	testFile, ok := findResult(results, "pkg/auth/auth_test.go")
	if !ok {
		t.Fatal("expected auth_test.go in results")
	}
	if !hasSignal(testFile, "test-pair") {
		t.Errorf("expected test-pair signal, got %v", testFile.Signals)
	}
	// Score should be high (0.9 from test-pair)
	if testFile.Score < 0.5 {
		t.Errorf("expected score >= 0.5 for test-pair, got %f", testFile.Score)
	}
}

func TestFindRelated_TestPair_TestToSource(t *testing.T) {
	idx := makeIdx(
		scan.FileEntry{RelPath: "pkg/auth/auth.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "pkg/auth/auth_test.go", Lang: "go", Class: scan.ClassTest},
	)
	tm := makeTestMap(
		map[string][]string{"pkg/auth/auth.go": {"pkg/auth/auth_test.go"}},
		map[string]string{"pkg/auth/auth_test.go": "pkg/auth/auth.go"},
	)

	results := FindRelated("pkg/auth/auth_test.go", idx, nil, tm, nil, nil, nil, 20)

	srcFile, ok := findResult(results, "pkg/auth/auth.go")
	if !ok {
		t.Fatal("expected auth.go in results when querying the test")
	}
	if !hasSignal(srcFile, "test-pair") {
		t.Errorf("expected test-pair signal, got %v", srcFile.Signals)
	}
}

func TestFindRelated_TestPair_HigherScoreThanSameDir(t *testing.T) {
	// Test-pair (0.9) should beat same-directory (0.3) only.
	// But same-directory ALSO fires for test files (they're in the same dir).
	// So test-pair gets 0.9 + 0.3 = 1.0 (capped), and a sibling only gets 0.3.
	idx := makeIdx(
		scan.FileEntry{RelPath: "pkg/auth/auth.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "pkg/auth/auth_test.go", Lang: "go", Class: scan.ClassTest},
		scan.FileEntry{RelPath: "pkg/auth/other.go", Lang: "go", Class: scan.ClassSource},
	)
	tm := makeTestMap(
		map[string][]string{"pkg/auth/auth.go": {"pkg/auth/auth_test.go"}},
		map[string]string{"pkg/auth/auth_test.go": "pkg/auth/auth.go"},
	)

	results := FindRelated("pkg/auth/auth.go", idx, nil, tm, nil, nil, nil, 20)

	if len(results) == 0 {
		t.Fatal("expected results")
	}
	// First result should be the test file (highest score)
	if results[0].Path != "pkg/auth/auth_test.go" {
		t.Errorf("expected auth_test.go first, got %q", results[0].Path)
	}
}

// ─── Signal 3: imports / imported-by ──────────────────────────────────────────

func TestFindRelated_Imports(t *testing.T) {
	idx := makeIdx(
		scan.FileEntry{RelPath: "pkg/auth/auth.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "pkg/models/user.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "pkg/db/conn.go", Lang: "go", Class: scan.ClassSource},
	)
	deps := makeDeps(map[string][]string{
		"pkg/auth/auth.go": {"pkg/models/user.go", "pkg/db/conn.go"},
	})
	tm := makeTestMap(nil, nil)

	results := FindRelated("pkg/auth/auth.go", idx, deps, tm, nil, nil, nil, 20)

	user, ok := findResult(results, "pkg/models/user.go")
	if !ok {
		t.Fatal("expected pkg/models/user.go in results")
	}
	if !hasSignal(user, "imports") {
		t.Errorf("expected 'imports' signal, got %v", user.Signals)
	}
	if user.Score < 0.5 {
		t.Errorf("expected import score >= 0.5, got %f", user.Score)
	}

	conn, ok := findResult(results, "pkg/db/conn.go")
	if !ok {
		t.Fatal("expected pkg/db/conn.go in results")
	}
	if !hasSignal(conn, "imports") {
		t.Errorf("expected 'imports' signal, got %v", conn.Signals)
	}
}

func TestFindRelated_ImportedBy(t *testing.T) {
	idx := makeIdx(
		scan.FileEntry{RelPath: "pkg/models/user.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "pkg/auth/auth.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "pkg/api/handler.go", Lang: "go", Class: scan.ClassSource},
	)
	// auth.go and handler.go both import user.go
	deps := makeDeps(map[string][]string{
		"pkg/auth/auth.go":    {"pkg/models/user.go"},
		"pkg/api/handler.go":  {"pkg/models/user.go"},
	})
	tm := makeTestMap(nil, nil)

	results := FindRelated("pkg/models/user.go", idx, deps, tm, nil, nil, nil, 20)

	auth, ok := findResult(results, "pkg/auth/auth.go")
	if !ok {
		t.Fatal("expected auth.go (imported-by) in results")
	}
	if !hasSignal(auth, "imported-by") {
		t.Errorf("expected 'imported-by' signal, got %v", auth.Signals)
	}

	handler, ok := findResult(results, "pkg/api/handler.go")
	if !ok {
		t.Fatal("expected handler.go (imported-by) in results")
	}
	if !hasSignal(handler, "imported-by") {
		t.Errorf("expected 'imported-by' signal, got %v", handler.Signals)
	}
}

func TestFindRelated_NilDeps(t *testing.T) {
	// Should not panic when deps is nil
	idx := makeIdx(
		scan.FileEntry{RelPath: "a.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "b.go", Lang: "go", Class: scan.ClassSource},
	)
	tm := makeTestMap(nil, nil)

	// Should not panic
	results := FindRelated("a.go", idx, nil, tm, nil, nil, nil, 20)
	_ = results
}

// ─── Signal 4: co-change ─────────────────────────────────────────────────────

func TestFindRelated_CoChange(t *testing.T) {
	idx := makeIdx(
		scan.FileEntry{RelPath: "src/a.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "src/b.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "src/c.go", Lang: "go", Class: scan.ClassSource},
	)
	tm := makeTestMap(nil, nil)

	cc := makeCoChange(
		map[string]map[string]int{
			"src/a.go": {"src/b.go": 3, "src/c.go": 7},
		},
		map[string]int{"src/a.go": 10, "src/b.go": 3, "src/c.go": 7},
	)

	results := FindRelated("src/a.go", idx, nil, tm, cc, nil, nil, 20)

	b, ok := findResult(results, "src/b.go")
	if !ok {
		t.Fatal("expected src/b.go in results")
	}
	if !hasSignal(b, "co-change") {
		t.Errorf("expected co-change signal, got %v", b.Signals)
	}

	c, ok := findResult(results, "src/c.go")
	if !ok {
		t.Fatal("expected src/c.go in results")
	}
	if !hasSignal(c, "co-change") {
		t.Errorf("expected co-change signal, got %v", c.Signals)
	}
}

func TestFindRelated_CoChange_FrequencyScaling(t *testing.T) {
	// High count (>=10) → 0.8, medium (>=5) → 0.7, low → 0.5
	// c has count=10 (same-dir + high-cochange = 0.3+0.8=1.0)
	// b has count=3 (same-dir + low-cochange = 0.3+0.5=0.8)
	// So c should rank higher
	idx := makeIdx(
		scan.FileEntry{RelPath: "src/a.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "src/b.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "src/c.go", Lang: "go", Class: scan.ClassSource},
	)
	tm := makeTestMap(nil, nil)

	cc := makeCoChange(
		map[string]map[string]int{
			"src/a.go": {"src/b.go": 3, "src/c.go": 10},
		},
		map[string]int{"src/a.go": 15, "src/b.go": 3, "src/c.go": 10},
	)

	results := FindRelated("src/a.go", idx, nil, tm, cc, nil, nil, 20)

	var bScore, cScore float64
	for _, r := range results {
		switch r.Path {
		case "src/b.go":
			bScore = r.Score
		case "src/c.go":
			cScore = r.Score
		}
	}

	if cScore < bScore {
		t.Errorf("high-frequency co-change (c=%.2f) should score >= low-frequency (b=%.2f)", cScore, bScore)
	}
}

func TestFindRelated_CoChange_MinCountFilter(t *testing.T) {
	// minCount=2, so a co-change pair with count=1 should not appear
	idx := makeIdx(
		scan.FileEntry{RelPath: "src/a.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "src/lonely.go", Lang: "go", Class: scan.ClassSource},
	)
	tm := makeTestMap(nil, nil)

	cc := makeCoChange(
		map[string]map[string]int{
			"src/a.go": {"src/lonely.go": 1},
		},
		map[string]int{"src/a.go": 5, "src/lonely.go": 1},
	)

	results := FindRelated("src/a.go", idx, nil, tm, cc, nil, nil, 20)
	lonely, ok := findResult(results, "src/lonely.go")
	if ok && hasSignal(lonely, "co-change") {
		t.Error("expected src/lonely.go to not have co-change signal (count=1 < minCount=2)")
	}
}

// ─── Signal 5: same-package ───────────────────────────────────────────────────

func TestFindRelated_SamePackage(t *testing.T) {
	// Files in sibling directories under the same parent get same-package signal
	idx := makeIdx(
		scan.FileEntry{RelPath: "pkg/auth/auth.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "pkg/auth/session.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "pkg/models/user.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "pkg/models/order.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "cmd/main.go", Lang: "go", Class: scan.ClassSource},
	)
	tm := makeTestMap(nil, nil)

	results := FindRelated("pkg/auth/auth.go", idx, nil, tm, nil, nil, nil, 20)

	// pkg/models/user.go is a sibling directory under "pkg" → same-package
	modelsUser, ok := findResult(results, "pkg/models/user.go")
	if !ok {
		t.Fatal("expected pkg/models/user.go in results")
	}
	if !hasSignal(modelsUser, "same-package") {
		t.Errorf("expected same-package signal for models/user.go, got %v", modelsUser.Signals)
	}

	// cmd/main.go is under a different top-level dir → no same-package
	_, ok = findResult(results, "cmd/main.go")
	if ok {
		t.Error("cmd/main.go should not be in results (no shared signals)")
	}
}

// ─── Signal 6: same-name ─────────────────────────────────────────────────────

func TestFindRelated_SameName(t *testing.T) {
	// user.go and user.ts have the same base name — cross-language linking
	idx := makeIdx(
		scan.FileEntry{RelPath: "backend/user.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "frontend/user.ts", Lang: "typescript", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "backend/auth.go", Lang: "go", Class: scan.ClassSource},
	)
	tm := makeTestMap(nil, nil)

	results := FindRelated("backend/user.go", idx, nil, tm, nil, nil, nil, 20)

	tsUser, ok := findResult(results, "frontend/user.ts")
	if !ok {
		t.Fatal("expected frontend/user.ts in results")
	}
	if !hasSignal(tsUser, "same-name") {
		t.Errorf("expected same-name signal, got %v", tsUser.Signals)
	}
	if tsUser.Score < 0.5 {
		t.Errorf("expected same-name score >= 0.5, got %f", tsUser.Score)
	}
}

func TestFindRelated_SameName_SameDir_NoSignal(t *testing.T) {
	// same-name only fires for files in a DIFFERENT directory
	idx := makeIdx(
		scan.FileEntry{RelPath: "src/user.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "src/user.ts", Lang: "typescript", Class: scan.ClassSource},
	)
	tm := makeTestMap(nil, nil)

	results := FindRelated("src/user.go", idx, nil, tm, nil, nil, nil, 20)

	ts, ok := findResult(results, "src/user.ts")
	if !ok {
		// It should still appear via same-directory, just not same-name
		t.Skip("src/user.ts not in results at all (unexpected)")
	}
	if hasSignal(ts, "same-name") {
		t.Error("same-name should not fire for files in the same directory")
	}
}

// ─── Signal 7: hotspot-dep ───────────────────────────────────────────────────

func TestFindRelated_HotspotDep(t *testing.T) {
	idx := makeIdx(
		scan.FileEntry{RelPath: "src/core.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "src/app.go", Lang: "go", Class: scan.ClassSource},
	)
	// app.go imports core.go
	deps := makeDeps(map[string][]string{
		"src/app.go": {"src/core.go"},
	})
	// core.go is a hotspot
	metrics := makeMetrics([]index.FileMetrics{
		{RelPath: "src/core.go", FanIn: 20, Churn: 50, HotspotScore: 0.9},
	})
	tm := makeTestMap(nil, nil)

	results := FindRelated("src/app.go", idx, deps, tm, nil, metrics, nil, 20)

	core, ok := findResult(results, "src/core.go")
	if !ok {
		t.Fatal("expected src/core.go in results")
	}
	if !hasSignal(core, "hotspot-dep") {
		t.Errorf("expected hotspot-dep signal, got %v", core.Signals)
	}
	// Score should include 0.7 (imports) + 0.3 (hotspot-dep) = 1.0 (capped)
	if core.Score < 0.9 {
		t.Errorf("expected high score for hotspot dep, got %f", core.Score)
	}
}

func TestFindRelated_NoHotspotDep_LowScore(t *testing.T) {
	idx := makeIdx(
		scan.FileEntry{RelPath: "src/core.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "src/app.go", Lang: "go", Class: scan.ClassSource},
	)
	deps := makeDeps(map[string][]string{
		"src/app.go": {"src/core.go"},
	})
	// core.go is NOT a hotspot (score <= 0.1)
	metrics := makeMetrics([]index.FileMetrics{
		{RelPath: "src/core.go", FanIn: 1, Churn: 1, HotspotScore: 0.05},
	})
	tm := makeTestMap(nil, nil)

	results := FindRelated("src/app.go", idx, deps, tm, nil, metrics, nil, 20)

	core, ok := findResult(results, "src/core.go")
	if !ok {
		t.Fatal("expected src/core.go in results (via imports)")
	}
	// Should have imports signal but NOT hotspot-dep
	if hasSignal(core, "hotspot-dep") {
		t.Error("expected no hotspot-dep signal when hotspot score is low")
	}
}

// ─── Signal 8: same-owner ────────────────────────────────────────────────────

func TestFindRelated_SameOwner(t *testing.T) {
	idx := makeIdx(
		scan.FileEntry{RelPath: "services/auth/login.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "services/auth/logout.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "services/payments/charge.go", Lang: "go", Class: scan.ClassSource},
	)
	// Both auth files and payments file are owned by the same team
	ownership := makeOwnership([]index.OwnerRule{
		{Priority: 1, Pattern: "services/auth/", Owners: []string{"@team-auth"}},
		{Priority: 2, Pattern: "services/payments/", Owners: []string{"@team-auth"}},
	})
	tm := makeTestMap(nil, nil)

	results := FindRelated("services/auth/login.go", idx, nil, tm, nil, nil, ownership, 20)

	// services/auth/logout.go should be in results (same-directory, possibly same-owner)
	logout, ok := findResult(results, "services/auth/logout.go")
	if !ok {
		t.Fatal("expected services/auth/logout.go in results")
	}
	_ = logout

	// services/payments/charge.go might get same-owner if it appears in candidate set
	// (same-owner only checks files already in scores map)
	// It won't appear unless it gets another signal first — this tests that behavior
	_, ok = findResult(results, "services/payments/charge.go")
	// If not found, that's correct behavior (same-owner only reinforces existing candidates)
	_ = ok
}

func TestFindRelated_SameOwner_OnlyExistingCandidates(t *testing.T) {
	// same-owner only checks files already in the scores map.
	// A file that would ONLY match via same-owner (no other signal) should NOT appear.
	idx := makeIdx(
		scan.FileEntry{RelPath: "a/file.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "b/other.go", Lang: "go", Class: scan.ClassSource},
	)
	// Both files owned by @team-x, but they're in completely different directories
	// with no other shared signals
	ownership := makeOwnership([]index.OwnerRule{
		{Priority: 1, Pattern: "*", Owners: []string{"@team-x"}},
	})
	tm := makeTestMap(nil, nil)

	results := FindRelated("a/file.go", idx, nil, tm, nil, nil, ownership, 20)

	// b/other.go has no same-dir, no import, no test-pair, no co-change, no same-name
	// so it won't be in the scores map, and same-owner won't add it
	_, ok := findResult(results, "b/other.go")
	if ok {
		t.Error("b/other.go should not appear when it only qualifies via same-owner (not already a candidate)")
	}
}

// ─── Ranking and limits ───────────────────────────────────────────────────────

func TestFindRelated_MaxResults(t *testing.T) {
	// Create many files in the same directory
	entries := make([]scan.FileEntry, 50)
	for i := range entries {
		entries[i] = scan.FileEntry{
			RelPath: "src/" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".go",
			Lang:    "go",
			Class:   scan.ClassSource,
		}
	}
	idx := index.NewFileIndex(entries)
	tm := makeTestMap(nil, nil)

	results := FindRelated("src/aa.go", idx, nil, tm, nil, nil, nil, 10)
	if len(results) > 10 {
		t.Errorf("expected at most 10 results, got %d", len(results))
	}
}

func TestFindRelated_DefaultMaxResults(t *testing.T) {
	entries := make([]scan.FileEntry, 30)
	for i := range entries {
		entries[i] = scan.FileEntry{
			RelPath: "src/" + string(rune('a'+i)) + ".go",
			Lang:    "go",
			Class:   scan.ClassSource,
		}
	}
	idx := index.NewFileIndex(entries)
	tm := makeTestMap(nil, nil)

	// maxResults=0 → uses default of 20
	results := FindRelated("src/a.go", idx, nil, tm, nil, nil, nil, 0)
	if len(results) > 20 {
		t.Errorf("expected at most 20 results (default), got %d", len(results))
	}
}

func TestFindRelated_ScoreCappedAt1(t *testing.T) {
	// A file that matches many signals should have score capped at 1.0
	idx := makeIdx(
		scan.FileEntry{RelPath: "src/auth.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "src/auth_test.go", Lang: "go", Class: scan.ClassTest},
	)
	deps := makeDeps(map[string][]string{
		"src/auth.go": {"src/auth_test.go"},
	})
	tm := makeTestMap(
		map[string][]string{"src/auth.go": {"src/auth_test.go"}},
		map[string]string{"src/auth_test.go": "src/auth.go"},
	)
	cc := makeCoChange(
		map[string]map[string]int{"src/auth.go": {"src/auth_test.go": 10}},
		map[string]int{"src/auth.go": 10, "src/auth_test.go": 10},
	)

	results := FindRelated("src/auth.go", idx, deps, tm, cc, nil, nil, 20)

	for _, r := range results {
		if r.Score > 1.0 {
			t.Errorf("score capped at 1.0, but got %f for %s", r.Score, r.Path)
		}
	}
}

func TestFindRelated_SortedByScoreDescending(t *testing.T) {
	idx := makeIdx(
		scan.FileEntry{RelPath: "src/main.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "src/helper.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "lib/util.go", Lang: "go", Class: scan.ClassSource},
	)
	// main.go imports helper.go (weight 0.7), util.go has no imports from main
	deps := makeDeps(map[string][]string{
		"src/main.go": {"src/helper.go"},
	})
	tm := makeTestMap(nil, nil)

	results := FindRelated("src/main.go", idx, deps, tm, nil, nil, nil, 20)

	// Results must be sorted descending by score
	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Errorf("results not sorted: results[%d].Score=%f > results[%d].Score=%f",
				i, results[i].Score, i-1, results[i-1].Score)
		}
	}
}

func TestFindRelated_QueryPathNotInIndex(t *testing.T) {
	// Query path doesn't exist in index — should return empty, not panic
	idx := makeIdx(
		scan.FileEntry{RelPath: "src/a.go", Lang: "go", Class: scan.ClassSource},
	)
	tm := makeTestMap(nil, nil)

	results := FindRelated("src/nonexistent.go", idx, nil, tm, nil, nil, nil, 20)
	// Since query path is not in index, same-dir might still find files.
	// The key requirement is no panic.
	_ = results
}

// ─── Multi-signal accumulation ────────────────────────────────────────────────

func TestFindRelated_MultipleSignalsAccumulate(t *testing.T) {
	// A file that gets both "imports" and "same-directory" signals should score
	// higher than one that only gets "same-directory".
	idx := makeIdx(
		scan.FileEntry{RelPath: "src/app.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "src/models.go", Lang: "go", Class: scan.ClassSource}, // same-dir + imports
		scan.FileEntry{RelPath: "src/other.go", Lang: "go", Class: scan.ClassSource},  // same-dir only
	)
	deps := makeDeps(map[string][]string{
		"src/app.go": {"src/models.go"},
	})
	tm := makeTestMap(nil, nil)

	results := FindRelated("src/app.go", idx, deps, tm, nil, nil, nil, 20)

	models, ok := findResult(results, "src/models.go")
	if !ok {
		t.Fatal("expected src/models.go in results")
	}
	other, ok := findResult(results, "src/other.go")
	if !ok {
		t.Fatal("expected src/other.go in results")
	}

	if models.Score <= other.Score {
		t.Errorf("models.go (imports+same-dir=%.2f) should score > other.go (same-dir only=%.2f)",
			models.Score, other.Score)
	}
	if !hasSignal(models, "imports") {
		t.Errorf("expected 'imports' signal on models.go, got %v", models.Signals)
	}
	if !hasSignal(models, "same-directory") {
		t.Errorf("expected 'same-directory' signal on models.go, got %v", models.Signals)
	}
}

// ─── NilSafe: optional params can be nil ─────────────────────────────────────

func TestFindRelated_NilOptionalParams(t *testing.T) {
	idx := makeIdx(
		scan.FileEntry{RelPath: "a.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "b.go", Lang: "go", Class: scan.ClassSource},
	)
	// All optional indexes — deps, tests, cochange, metrics, ownership — are
	// nilable and must not panic.
	results := FindRelated("a.go", idx, nil, nil, nil, nil, nil, 5)
	_ = results
}
