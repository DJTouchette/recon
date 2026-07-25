package git

import (
	"fmt"
	"testing"
)

func bigCommit(hash, subject string, n int) Commit {
	c := Commit{Hash: hash, Message: subject}
	for i := 0; i < n; i++ {
		c.Files = append(c.Files, fmt.Sprintf("f%03d.go", i))
	}
	return c
}

// Defect 4: an oversized commit must still count towards churn; only the
// co-change pairs it would manufacture are dropped.
func TestOversizedCommitCountsForChurnButNotCoChange(t *testing.T) {
	commits := []Commit{
		bigCommit("aaa", "mass reformat", DefaultCoChangeMaxFiles+10),
		{Hash: "bbb", Message: "small", Files: []string{"f000.go", "f001.go"}},
	}

	cc := NewCoChange(commits)
	churn := cc.AllChurn()

	if got := len(churn); got != DefaultCoChangeMaxFiles+10 {
		t.Errorf("churn entries = %d, want %d", got, DefaultCoChangeMaxFiles+10)
	}
	if churn["f000.go"] != 2 {
		t.Errorf("churn[f000.go] = %d, want 2 (both commits touched it)", churn["f000.go"])
	}
	if churn["f050.go"] != 1 {
		t.Errorf("churn[f050.go] = %d, want 1 (only the big commit touched it)", churn["f050.go"])
	}

	// Only the small commit may contribute pairs.
	pairs := cc.AllPairs()
	total := 0
	for _, m := range pairs {
		for _, n := range m {
			total += n
		}
	}
	if total != 1 {
		t.Errorf("pair count = %d, want 1 (the big commit must not pair)", total)
	}
	if pairs["f000.go"]["f001.go"] != 1 {
		t.Errorf("expected the small commit's pair, got %v", pairs)
	}

	cov := cc.Coverage()
	if cov.CommitsOversized != 1 {
		t.Errorf("CommitsOversized = %d, want 1", cov.CommitsOversized)
	}
	if cov.CommitsWithFiles != 2 {
		t.Errorf("CommitsWithFiles = %d, want 2", cov.CommitsWithFiles)
	}
	if cov.CoChangeMaxFiles != DefaultCoChangeMaxFiles {
		t.Errorf("CoChangeMaxFiles = %d, want %d", cov.CoChangeMaxFiles, DefaultCoChangeMaxFiles)
	}
}

func TestCoChangeThresholdIsConfigurable(t *testing.T) {
	commits := []Commit{bigCommit("aaa", "sixty files", 60)}

	tight := NewCoChangeOpts(commits, CoChangeOptions{MaxFilesPerCommit: 10})
	if len(tight.AllPairs()) != 0 {
		t.Error("tight threshold still produced pairs")
	}
	if tight.Coverage().CommitsOversized != 1 {
		t.Error("tight threshold did not report the commit as oversized")
	}

	loose := NewCoChangeOpts(commits, CoChangeOptions{MaxFilesPerCommit: 100})
	if len(loose.AllPairs()) == 0 {
		t.Error("loose threshold dropped pairs it should have kept")
	}

	unlimited := NewCoChangeOpts(commits, CoChangeOptions{MaxFilesPerCommit: -1})
	if unlimited.Coverage().CommitsOversized != 0 {
		t.Error("unlimited threshold still excluded a commit")
	}
	if got := len(unlimited.AllPairs()); got != 59 {
		t.Errorf("pair heads = %d, want 59", got)
	}

	// All variants agree on churn.
	for name, cc := range map[string]*CoChange{"tight": tight, "loose": loose, "unlimited": unlimited} {
		if len(cc.AllChurn()) != 60 {
			t.Errorf("%s: churn entries = %d, want 60", name, len(cc.AllChurn()))
		}
	}
}

func TestCoChangeSkipsEmptyCommits(t *testing.T) {
	cc := NewCoChange([]Commit{
		{Hash: "aaa", Message: "empty"},
		{Hash: "bbb", Message: "real", Files: []string{"a.go"}},
	})
	if cc.Coverage().CommitsWithFiles != 1 {
		t.Errorf("CommitsWithFiles = %d, want 1", cc.Coverage().CommitsWithFiles)
	}
	if len(cc.AllChurn()) != 1 {
		t.Errorf("churn = %v, want 1 entry", cc.AllChurn())
	}
}

func TestMineReportsCombinedCoverage(t *testing.T) {
	r := newTestRepo(t)
	for i := 0; i < DefaultCoChangeMaxFiles+5; i++ {
		r.write(fmt.Sprintf("f%03d.go", i), "x\n")
	}
	r.commit("import")
	r.write("f000.go", "y\n")
	r.write("f001.go", "y\n")
	r.commit("small change")

	cc, err := Mine(r.dir, Options{}, CoChangeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cov := cc.Coverage()

	if !cov.Repo || cov.Shallow {
		t.Errorf("coverage = %+v", cov)
	}
	if cov.CommitsScanned != 2 || cov.CommitsWithFiles != 2 {
		t.Errorf("scanned = %d, withFiles = %d, want 2/2", cov.CommitsScanned, cov.CommitsWithFiles)
	}
	if cov.CommitsOversized != 1 {
		t.Errorf("CommitsOversized = %d, want 1 (the import commit)", cov.CommitsOversized)
	}
	if !cov.ChurnTrustworthy() {
		t.Errorf("ChurnTrustworthy = false: %s", cov.Reason())
	}
	if cc.AllChurn()["f000.go"] != 2 {
		t.Errorf("churn[f000.go] = %d, want 2", cc.AllChurn()["f000.go"])
	}
	if cc.AllChurn()["f040.go"] != 1 {
		t.Errorf("churn[f040.go] = %d, want 1 (import commit must count)", cc.AllChurn()["f040.go"])
	}

	// Cached data reports unknown coverage rather than pretending.
	cached := NewCoChangeFromData(cc.AllPairs(), cc.AllChurn())
	if cached.Coverage().Repo {
		t.Error("cached CoChange claims to know its coverage")
	}
	cached.SetCoverage(cov)
	if !cached.Coverage().ChurnTrustworthy() {
		t.Error("SetCoverage did not take")
	}
}

func TestCoChangedWithAndChurnTopN(t *testing.T) {
	cc := NewCoChange([]Commit{
		{Hash: "a", Files: []string{"a.go", "b.go"}},
		{Hash: "b", Files: []string{"a.go", "b.go"}},
		{Hash: "c", Files: []string{"a.go", "c.go"}},
	})

	got := cc.CoChangedWith("a.go", 2)
	if len(got) != 1 || got[0].File != "b.go" || got[0].Count != 2 {
		t.Errorf("CoChangedWith = %+v, want b.go x2", got)
	}
	if got := cc.CoChangedWith("b.go", 1); len(got) != 1 || got[0].File != "a.go" {
		t.Errorf("reverse lookup = %+v", got)
	}

	top := cc.Churn(2)
	if len(top) != 2 || top[0].File != "a.go" || top[0].Commits != 3 {
		t.Errorf("Churn(2) = %+v", top)
	}
}

func TestAreasFromFiles(t *testing.T) {
	got := AreasFromFiles([]string{"src/a.go", "src/b.go", "docs/x.md", "top.txt"})
	want := []string{"src", "docs", "top.txt"}
	if len(got) != len(want) {
		t.Fatalf("areas = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("areas = %v, want %v", got, want)
		}
	}
}
