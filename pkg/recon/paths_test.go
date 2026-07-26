package recon

import (
	"os"
	"path/filepath"
	"testing"
)

// fixtureRepo builds a tiny indexed repo and returns its root and a Recon.
func fixtureRepo(t *testing.T) (string, *Recon) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src", "email"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package email\n\nfunc Send() {}\n"
	if err := os.WriteFile(filepath.Join(root, "src", "email", "send.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := New(root, WithCacheDir(filepath.Join(t.TempDir(), "cache")))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Rebuild(); err != nil {
		t.Fatal(err)
	}
	return root, r
}

// Callers do not have the repo-relative form to hand. An agent gets a path
// from a file listing or a previous tool result and it is absolute; a Windows
// caller has backslashes. Neither used to resolve.
func TestResolvePathAcceptsTheFormsCallersActuallyHave(t *testing.T) {
	root, r := fixtureRepo(t)
	const want = "src/email/send.go"

	for _, in := range []string{
		want,
		"./" + want,
		filepath.Join(root, "src", "email", "send.go"),
		`src\email\send.go`,
		"  " + want + "  ",
	} {
		if got := r.resolvePath(in); got != want {
			t.Errorf("resolvePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// The resolver must never rewrite speculatively: an unknown path must not be
// bent onto some real file just because a candidate happened to match.
func TestResolvePathLeavesUnknownPathsAlone(t *testing.T) {
	_, r := fixtureRepo(t)

	for _, in := range []string{"src/email/missing.go", "/etc/passwd", "../outside.go"} {
		if got := r.resolvePath(in); r.Indexed(got) {
			t.Errorf("resolvePath(%q) = %q, which is indexed — an unknown path must not resolve onto a real file", in, got)
		}
	}
}

// The zeros for an unscanned path are identical to those of a file with no
// dependents, and "nothing depends on this" is the most consequential thing
// Context can say.
func TestContextSeparatesUnknownPathFromZeroMetrics(t *testing.T) {
	root, r := fixtureRepo(t)

	known, err := r.Context(filepath.Join(root, "src", "email", "send.go"))
	if err != nil {
		t.Fatal(err)
	}
	if known.Status != StatusIndexed {
		t.Errorf("absolute path to a real file: status = %q, want %q", known.Status, StatusIndexed)
	}
	if known.Path != "src/email/send.go" {
		t.Errorf("path = %q, want the repo-relative form", known.Path)
	}

	unknown, err := r.Context("src/email/nope.go")
	if err != nil {
		t.Fatal(err)
	}
	if unknown.Status != StatusNotIndexed {
		t.Errorf("unknown path: status = %q, want %q", unknown.Status, StatusNotIndexed)
	}
}

func TestIndexedReportsScannedFilesOnly(t *testing.T) {
	root, r := fixtureRepo(t)

	if !r.Indexed(filepath.Join(root, "src", "email", "send.go")) {
		t.Error("an absolute path to a scanned file must be Indexed")
	}
	if !r.Indexed("src/email/send.go") {
		t.Error("the repo-relative form must be Indexed")
	}
	if r.Indexed("src/email/nope.go") {
		t.Error("a path that was never scanned must not be Indexed")
	}
	if r.Indexed("/etc/passwd") {
		t.Error("a path outside the repo must not be Indexed")
	}
}

// file:-scoped symbol listing is the call that surfaced this: it returned "No
// symbols found" for every absolute path while name search found the same
// symbols fine.
func TestSymbolsFileScopeAcceptsAbsolutePaths(t *testing.T) {
	root, r := fixtureRepo(t)

	rel, err := r.Symbols("file:src/email/send.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rel) == 0 {
		t.Fatal("relative file: scope found nothing; fixture is wrong")
	}

	abs, err := r.Symbols("file:"+filepath.Join(root, "src", "email", "send.go"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(abs) != len(rel) {
		t.Errorf("absolute file: scope returned %d symbols, relative returned %d", len(abs), len(rel))
	}
}
