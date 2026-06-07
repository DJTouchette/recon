package index

import (
	"sort"
	"testing"
)

// TestRefQueriesCompile fails if any registered refs query stops compiling
// (e.g. a grammar upgrade renamed a node type). Such a language would silently
// contribute no references in production, so we guard it here.
func TestRefQueriesCompile(t *testing.T) {
	langs := []string{"go", "typescript", "javascript", "python", "csharp", "java", "rust", "ruby", "php"}
	for _, lang := range langs {
		if !hasTSRefs(lang) {
			t.Errorf("%s: refs query did not register (failed to compile or missing)", lang)
		}
	}
}

func TestExtractReferences(t *testing.T) {
	cases := []struct {
		lang   string
		src    string
		want   []string // names that must appear
		forbid []string // names that must NOT appear (from strings/comments)
	}{
		{
			lang: "go",
			src: `package main

func main() {
	foo()
	pkg.Bar()
	obj.Method(1, 2)
	// notACall()
	s := "alsoNotACall()"
	_ = s
}
`,
			want:   []string{"foo", "Bar", "Method"},
			forbid: []string{"notACall", "alsoNotACall"},
		},
		{
			lang: "typescript",
			src: `function run() {
  doThing();
  obj.callMethod();
  // skipped();
  const s = "ignored()";
  return s;
}
`,
			want:   []string{"doThing", "callMethod"},
			forbid: []string{"skipped", "ignored"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			refs, ok := extractReferencesTS([]byte(tc.src), "f."+tc.lang, tc.lang)
			if !ok {
				t.Fatalf("%s: not handled by tree-sitter refs", tc.lang)
			}
			got := make(map[string]bool, len(refs))
			for _, r := range refs {
				got[r.Name] = true
				if r.Line <= 0 {
					t.Errorf("%s: reference %q has invalid line %d", tc.lang, r.Name, r.Line)
				}
			}
			for _, name := range tc.want {
				if !got[name] {
					t.Errorf("%s: expected reference %q not captured", tc.lang, name)
				}
			}
			for _, bad := range tc.forbid {
				if got[bad] {
					t.Errorf("%s: %q should not have been captured (string/comment)", tc.lang, bad)
				}
			}
			if t.Failed() {
				names := make([]string, 0, len(got))
				for n := range got {
					names = append(names, n)
				}
				sort.Strings(names)
				t.Logf("%s captured: %v", tc.lang, names)
			}
		})
	}
}
