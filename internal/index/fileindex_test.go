package index

import (
	"reflect"
	"sort"
	"testing"

	"github.com/djtouchette/recon/internal/scan"
)

// pathsOf lists the relpaths of index entries, in index order.
func pathsOf(files []*scan.FileEntry) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.RelPath)
	}
	return out
}

func sorted(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

func TestFilesUnderDirIsRecursive(t *testing.T) {
	_, idx := writeTree(t, map[string]string{
		"internal/a.go":            "package internal\n",
		"internal/orders/b.go":     "package orders\n",
		"internal/orders/sub/c.go": "package sub\n",
		"cmd/main.go":              "package main\n",
	})

	got := pathsOf(idx.FilesUnderDir("internal"))
	want := []string{"internal/a.go", "internal/orders/b.go", "internal/orders/sub/c.go"}
	if !reflect.DeepEqual(sorted(got), want) {
		t.Errorf("FilesUnderDir(internal) = %v, want %v", got, want)
	}
}

func TestFilesDirectlyInIsNotRecursive(t *testing.T) {
	// The "same package" question: internal/a.go is not a sibling of
	// internal/orders/b.go, and treating it as one makes a same-package
	// signal fire on an entire tree.
	_, idx := writeTree(t, map[string]string{
		"internal/a.go":            "package internal\n",
		"internal/orders/b.go":     "package orders\n",
		"internal/orders/sub/c.go": "package sub\n",
	})

	got := pathsOf(idx.FilesDirectlyIn("internal"))
	if !reflect.DeepEqual(got, []string{"internal/a.go"}) {
		t.Errorf("FilesDirectlyIn(internal) = %v, want [internal/a.go]", got)
	}
	got = pathsOf(idx.FilesDirectlyIn("internal/orders"))
	if !reflect.DeepEqual(got, []string{"internal/orders/b.go"}) {
		t.Errorf("FilesDirectlyIn(internal/orders) = %v", got)
	}
}

func TestFilesUnderDirRootSpellings(t *testing.T) {
	_, idx := writeTree(t, map[string]string{
		"a.go":     "package main\n",
		"sub/b.go": "package sub\n",
	})
	for _, spelling := range []string{".", "", "./"} {
		if n := len(idx.FilesUnderDir(spelling)); n != 2 {
			t.Errorf("FilesUnderDir(%q) = %d files, want 2", spelling, n)
		}
	}
}

func TestFilesDirectlyInRootSpellings(t *testing.T) {
	_, idx := writeTree(t, map[string]string{
		"a.go":     "package main\n",
		"sub/b.go": "package sub\n",
	})
	for _, spelling := range []string{".", ""} {
		got := pathsOf(idx.FilesDirectlyIn(spelling))
		if !reflect.DeepEqual(got, []string{"a.go"}) {
			t.Errorf("FilesDirectlyIn(%q) = %v, want [a.go]", spelling, got)
		}
	}
}

func TestFilesUnderDirDoesNotMatchSiblingPrefix(t *testing.T) {
	_, idx := writeTree(t, map[string]string{
		"internal/a.go":  "package internal\n",
		"internals/b.go": "package internals\n",
		"internal2/c.go": "package internal2\n",
	})
	got := pathsOf(idx.FilesUnderDir("internal"))
	if !reflect.DeepEqual(got, []string{"internal/a.go"}) {
		t.Errorf("FilesUnderDir(internal) = %v, want only internal/a.go", got)
	}
}

func TestFilesInDirStillRecurses(t *testing.T) {
	// The deprecated name keeps its old meaning for existing callers.
	_, idx := writeTree(t, map[string]string{
		"internal/a.go":        "package internal\n",
		"internal/orders/b.go": "package orders\n",
	})
	if len(idx.FilesInDir("internal")) != len(idx.FilesUnderDir("internal")) {
		t.Error("FilesInDir diverged from FilesUnderDir")
	}
}

func TestLanguagesAreStablyOrdered(t *testing.T) {
	_, idx := writeTree(t, map[string]string{
		"a.go": "package a\n",
		"b.py": "x = 1\n",
		"c.rb": "class C; end\n",
		"d.rs": "fn f() {}\n",
		"e.ts": "export const e = 1;\n",
	})
	want := idx.Languages()
	for i := 0; i < 30; i++ {
		if got := idx.Languages(); !reflect.DeepEqual(want, got) {
			t.Fatalf("run %d: %+v != %+v", i, got, want)
		}
	}
	for i := 1; i < len(want); i++ {
		if want[i-1].Count == want[i].Count && want[i-1].Name > want[i].Name {
			t.Errorf("tied counts not name-ordered: %+v", want)
		}
	}
}

func TestTopDirsAreStablyOrdered(t *testing.T) {
	_, idx := writeTree(t, map[string]string{
		"alpha/a.go":   "package alpha\n",
		"beta/b.go":    "package beta\n",
		"gamma/c.go":   "package gamma\n",
		"delta/d.go":   "package delta\n",
		"epsilon/e.go": "package epsilon\n",
	})
	want := idx.TopDirs()
	for i := 0; i < 30; i++ {
		if got := idx.TopDirs(); !reflect.DeepEqual(want, got) {
			t.Fatalf("run %d: %+v != %+v", i, got, want)
		}
	}
	for i := 1; i < len(want); i++ {
		if want[i-1].FileCount == want[i].FileCount && want[i-1].Path > want[i].Path {
			t.Errorf("tied dir counts not path-ordered: %+v", want)
		}
	}
}
