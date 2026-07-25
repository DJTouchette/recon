package cache

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/djtouchette/recon/internal/scan"
)

func TestStaleReasonStrings(t *testing.T) {
	cases := []struct {
		r    StaleReason
		want string
	}{
		{NotStale, "not stale"},
		{NoCacheData, "no cache data"},
		{HeadChanged, "HEAD changed"},
		{KeyFileChanged, "key file changed"},
		{StaleReason(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.r.String(); got != c.want {
			t.Errorf("StaleReason(%d).String() = %q, want %q", c.r, got, c.want)
		}
	}
	if !NoCacheData.NeedsRebuild() {
		t.Error("NoCacheData must force a rebuild")
	}
	for _, r := range []StaleReason{NotStale, HeadChanged, KeyFileChanged} {
		if r.NeedsRebuild() {
			t.Errorf("%v should be refreshable, not a full rebuild", r)
		}
	}
}

func TestCheckStalenessEmptyCache(t *testing.T) {
	s := newStore(t)
	if got := CheckStaleness(s); got != NoCacheData {
		t.Errorf("CheckStaleness on empty cache = %v, want NoCacheData", got)
	}
}

func TestCheckStalenessKeyFileMtime(t *testing.T) {
	s := newStore(t)
	gomod := filepath.Join(s.Root, "go.mod")
	if err := os.WriteFile(gomod, []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateFiles([]scan.FileEntry{{RelPath: "go.mod"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveKeyFileMtimes(s); err != nil {
		t.Fatal(err)
	}
	if got := CheckStaleness(s); got != NotStale {
		t.Fatalf("CheckStaleness right after save = %v, want NotStale", got)
	}

	// Touching a manifest invalidates the cache.
	info, err := os.Stat(gomod)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta("mtime:go.mod", strconv.FormatInt(info.ModTime().UnixNano()+1, 10)); err != nil {
		t.Fatal(err)
	}
	if got := CheckStaleness(s); got != KeyFileChanged {
		t.Errorf("CheckStaleness after mtime change = %v, want KeyFileChanged", got)
	}

	// Deleting a manifest that was present invalidates too.
	if err := SaveKeyFileMtimes(s); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gomod); err != nil {
		t.Fatal(err)
	}
	if got := CheckStaleness(s); got != KeyFileChanged {
		t.Errorf("CheckStaleness after manifest deletion = %v, want KeyFileChanged", got)
	}
}

func TestCheckStalenessHeadChange(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	commit := func(name string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", name}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v (%s)", args, err, out)
			}
		}
	}
	commit("a.txt")

	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpdateFiles([]scan.FileEntry{{RelPath: "a.txt"}}, nil); err != nil {
		t.Fatal(err)
	}

	head, ok := s.GetMeta("head_sha")
	if ok {
		t.Fatalf("unexpected stored head %q", head)
	}
	// Nothing stored yet — an unknown HEAD must not be reported as stale.
	if got := CheckStaleness(s); got != NotStale {
		t.Errorf("CheckStaleness with no stored HEAD = %v, want NotStale", got)
	}

	if err := s.SetMeta("head_sha", "0000000000000000000000000000000000000000"); err != nil {
		t.Fatal(err)
	}
	if got := CheckStaleness(s); got != HeadChanged {
		t.Errorf("CheckStaleness after HEAD moved = %v, want HeadChanged", got)
	}
}
