package recon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// These cover the defect that mattered most: recon could not see the working
// tree. CheckStaleness compares HEAD and a handful of manifest mtimes, and the
// "not stale" verdict used to mean "serve the cache untouched" — so an
// uncommitted edit was invisible, and recon reported symbols that had been
// deleted as though they were live. For a caller that edits code before asking
// questions about it, that is the normal case.

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func gitCommitAll(t *testing.T, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", msg}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// symbolNames returns the set of symbol names recon reports for a file.
func symbolNames(t *testing.T, root, relPath string) map[string]bool {
	t.Helper()
	r, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Close()

	syms, err := r.Symbols("file:"+relPath, -1)
	if err != nil {
		t.Fatalf("Symbols: %v", err)
	}
	got := make(map[string]bool, len(syms))
	for _, s := range syms {
		got[s.Name] = true
	}
	return got
}

func TestUncommittedEditIsVisible(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	write(t, filepath.Join(root, "go.mod"), "module x\n\ngo 1.23\n")
	src := filepath.Join(root, "src", "alpha.go")
	write(t, src, "package src\n\nfunc AlphaOne() {}\nfunc AlphaTwo() {}\n")
	gitCommitAll(t, root, "init")

	if got := symbolNames(t, root, "src/alpha.go"); !got["AlphaTwo"] {
		t.Fatalf("baseline should see AlphaTwo, got %v", got)
	}

	// Edit WITHOUT committing — HEAD is unchanged and no manifest file moved,
	// so the cheap staleness check still says "fresh".
	write(t, src, "package src\n\nfunc AlphaOne() {}\nfunc AlphaRenamed() {}\n")

	got := symbolNames(t, root, "src/alpha.go")
	if got["AlphaTwo"] {
		t.Error("AlphaTwo was deleted but is still reported — the cache is being served blind")
	}
	if !got["AlphaRenamed"] {
		t.Errorf("AlphaRenamed was added but is not reported, got %v", got)
	}
}

// A file can change content while keeping its mtime: tar -x, rsync -t, cp -p,
// and Docker/CI workspace restores all do this. Size was already stored and
// simply never compared.
func TestContentChangeWithPreservedMtimeIsDetected(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	write(t, filepath.Join(root, "go.mod"), "module x\n\ngo 1.23\n")
	src := filepath.Join(root, "src", "alpha.go")
	write(t, src, "package src\n\nfunc Original() {}\n")
	gitCommitAll(t, root, "init")

	if got := symbolNames(t, root, "src/alpha.go"); !got["Original"] {
		t.Fatalf("baseline should see Original, got %v", got)
	}

	info, err := os.Stat(src)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	write(t, src, "package src\n\nfunc Replaced() {}\nfunc Extra() {}\n")
	if err := os.Chtimes(src, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// Confirm the premise: mtime really is unchanged, so only size can catch it.
	after, err := os.Stat(src)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !after.ModTime().Equal(info.ModTime()) {
		t.Skip("filesystem did not preserve mtime; nothing to assert")
	}
	if after.Size() == info.Size() {
		t.Fatal("fixture is wrong: sizes must differ for this test to mean anything")
	}

	got := symbolNames(t, root, "src/alpha.go")
	if got["Original"] {
		t.Error("stale symbol survived a same-mtime content change")
	}
	if !got["Replaced"] {
		t.Errorf("new symbol not picked up, got %v", got)
	}
}

// Deleting a file must drop its symbols, not leave them behind as phantoms.
func TestUncommittedDeletionIsVisible(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	write(t, filepath.Join(root, "go.mod"), "module x\n\ngo 1.23\n")
	gone := filepath.Join(root, "src", "gone.go")
	write(t, gone, "package src\n\nfunc Doomed() {}\n")
	write(t, filepath.Join(root, "src", "keep.go"), "package src\n\nfunc Kept() {}\n")
	gitCommitAll(t, root, "init")

	if got := symbolNames(t, root, "src/gone.go"); !got["Doomed"] {
		t.Fatalf("baseline should see Doomed, got %v", got)
	}

	if err := os.Remove(gone); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if got := symbolNames(t, root, "src/gone.go"); got["Doomed"] {
		t.Error("symbols from a deleted file are still reported")
	}
	if got := symbolNames(t, root, "src/keep.go"); !got["Kept"] {
		t.Errorf("unrelated file lost its symbols, got %v", got)
	}
}
