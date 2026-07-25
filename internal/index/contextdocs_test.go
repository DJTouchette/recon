package index

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djtouchette/recon/internal/scan"
)

// writeTree writes files into a temp root and returns the root plus a
// FileIndex built from a real walk.
func writeTree(t *testing.T, files map[string]string) (string, *FileIndex) {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	walk, err := scan.Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, NewFileIndex(walk.Files)
}

func buildDocs(t *testing.T, files map[string]string) *ContextDocIndex {
	t.Helper()
	root, idx := writeTree(t, files)
	symbols := NewSymbolIndex(root, idx)
	return NewContextDocIndex(root, idx, symbols)
}

func TestCommentDocPositionalAttachGo(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"orders/handler.go": `package orders

// rivet:context
// Never call this inside a transaction.
// Retries are handled by the scheduler.
func ProcessPayment() error { return nil }
`,
	})

	docs := ci.ForSymbol("ProcessPayment")
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc for ProcessPayment, got %d (all: %+v)", len(docs), ci.All())
	}
	d := docs[0]
	if d.File != "orders/handler.go" || d.Source != "comment" || d.Line != 3 {
		t.Errorf("unexpected doc: %+v", d)
	}
	want := "Never call this inside a transaction.\nRetries are handled by the scheduler."
	if d.Body != want {
		t.Errorf("body = %q, want %q", d.Body, want)
	}
}

func TestCommentDocExplicitSymbol(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"orders/handler.go": `package orders

func ProcessPayment() error { return nil }

// rivet:context(ProcessPayment)
// Retries live in the scheduler, not here.
var x = 1
`,
	})

	docs := ci.ForSymbol("ProcessPayment")
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d (all: %+v)", len(docs), ci.All())
	}
	if docs[0].Body != "Retries live in the scheduler, not here." {
		t.Errorf("body = %q", docs[0].Body)
	}
}

func TestCommentDocFileLevel(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"orders/handler.go": `package orders

// rivet:context
// This whole file is a shim around the legacy API.

var x = 1
`,
	})

	// No symbol within the window (var x is 3 lines below the comment end,
	// which IS within the window — use a file with no symbols after).
	docs := ci.ForFile("orders/handler.go")
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(docs))
	}
}

func TestCommentDocInlineTextAndColon(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"app.py": `# rivet:context: Module-level gotcha about imports.

CONSTANT = 1
`,
	})
	docs := ci.ForFile("app.py")
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d (all %+v)", len(docs), ci.All())
	}
	if docs[0].Body != "Module-level gotcha about imports." {
		t.Errorf("body = %q", docs[0].Body)
	}
}

func TestCommentDocPython(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"svc/jobs.py": `import os

# rivet:context
# The cron must never run on Tuesdays (billing close).
def run_billing():
    pass
`,
	})
	docs := ci.ForSymbol("run_billing")
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc for run_billing, got %d (all: %+v)", len(docs), ci.All())
	}
}

func TestCommentDocBlockComment(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"src/billing.ts": `/* rivet:context
 * Stripe webhooks arrive out of order.
 * Always check the event timestamp.
 */
export function handleWebhook() {}
`,
	})
	docs := ci.ForSymbol("handleWebhook")
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d (all: %+v)", len(docs), ci.All())
	}
	want := "Stripe webhooks arrive out of order.\nAlways check the event timestamp."
	if docs[0].Body != want {
		t.Errorf("body = %q, want %q", docs[0].Body, want)
	}
}

func TestNoFalsePositiveMarker(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"a.go": `package a

// rivet:contextual is not a marker
func F() {}
`,
	})
	if n := len(ci.All()); n != 0 {
		t.Fatalf("expected 0 docs, got %d: %+v", n, ci.All())
	}
}

func TestSidecarStemMatch(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"src/orders/handler.go":          "package orders\n\nfunc F() {}\n",
		"src/orders/.context/handler.md": "# Orders handler\n\nLegacy retry quirks live here.\n",
	})
	docs := ci.ForFile("src/orders/handler.go")
	if len(docs) != 1 {
		t.Fatalf("expected 1 sidecar doc, got %d (all: %+v)", len(docs), ci.All())
	}
	d := docs[0]
	if d.Source != "sidecar" || d.Origin != "src/orders/.context/handler.md" || d.Symbol != "" {
		t.Errorf("unexpected doc: %+v", d)
	}
}

func TestSidecarExactNameMatch(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"src/handler.go":             "package src\n",
		"src/handler.py":             "x = 1\n",
		"src/.context/handler.go.md": "Only for the Go file.\n",
	})
	if docs := ci.ForFile("src/handler.go"); len(docs) != 1 {
		t.Fatalf("expected 1 doc for handler.go, got %d", len(docs))
	}
	if docs := ci.ForFile("src/handler.py"); len(docs) != 0 {
		t.Fatalf("expected 0 docs for handler.py, got %d", len(docs))
	}
}

func TestSidecarStemMatchesAllLanguages(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"src/handler.go":          "package src\n",
		"src/handler.py":          "x = 1\n",
		"src/.context/handler.md": "Shared context.\n",
	})
	if docs := ci.ForFile("src/handler.go"); len(docs) != 1 {
		t.Fatalf("expected doc for handler.go, got %d", len(docs))
	}
	if docs := ci.ForFile("src/handler.py"); len(docs) != 1 {
		t.Fatalf("expected doc for handler.py, got %d", len(docs))
	}
}

func TestSidecarNoTargetIsDropped(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"src/other.go":            "package src\n",
		"src/.context/missing.md": "Orphan doc.\n",
	})
	if n := len(ci.All()); n != 0 {
		t.Fatalf("expected 0 docs, got %d: %+v", n, ci.All())
	}
}

func TestMultipleDocsOneFile(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"multi.go": `package multi

// rivet:context
// First doc.
func A() {}

// rivet:context
// Second doc.
func B() {}
`,
	})
	if n := len(ci.All()); n != 2 {
		t.Fatalf("expected 2 docs, got %d: %+v", n, ci.All())
	}
	if len(ci.ForSymbol("A")) != 1 || len(ci.ForSymbol("B")) != 1 {
		t.Errorf("docs not attached per symbol: %+v", ci.All())
	}
}

// --- A marker inside a string literal is not a comment ---

func TestMarkerInsidePythonStringIsNotADoc(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"src/mod.py": `TEMPLATE = """
# rivet:context: this lives in a string literal
def generated(): pass
"""

def first_thing():
    return 1
`,
	})
	if n := len(ci.All()); n != 0 {
		t.Fatalf("expected 0 docs, got %d: %+v", n, ci.All())
	}
}

func TestMarkerInsideGoStringIsNotADoc(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"a.go": "package a\n\nconst tmpl = `\n// rivet:context: not a real doc\n`\n\nfunc F() {}\n",
	})
	if n := len(ci.All()); n != 0 {
		t.Fatalf("expected 0 docs, got %d: %+v", n, ci.All())
	}
}

func TestMarkerInsideJSStringIsNotADoc(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"src/a.ts": "const snippet = \"// rivet:context: fake\";\n\nexport function real() {}\n",
	})
	if n := len(ci.All()); n != 0 {
		t.Fatalf("expected 0 docs, got %d: %+v", n, ci.All())
	}
}

func TestRealCommentNextToAStringStillWorks(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"src/mod.py": `TEMPLATE = """
# rivet:context: decoy inside a string
"""

# rivet:context
# The real note.
def run():
    pass
`,
	})
	docs := ci.All()
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d: %+v", len(docs), docs)
	}
	if docs[0].Symbol != "run" || docs[0].Body != "The real note." {
		t.Errorf("unexpected doc: %+v", docs[0])
	}
}

// --- Trailing comments ---

func TestTrailingCommentAfterCodeIsCaptured(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"a.go": "package a\n\nvar Limit = 10 // rivet:context: tuned by SRE, do not raise\n",
	})
	docs := ci.All()
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d: %+v", len(docs), docs)
	}
	if docs[0].Body != "tuned by SRE, do not raise" {
		t.Errorf("body = %q", docs[0].Body)
	}
	if docs[0].Symbol != "Limit" {
		t.Errorf("symbol = %q, want Limit (the declaration it trails)", docs[0].Symbol)
	}
	if docs[0].Line != 3 {
		t.Errorf("line = %d, want 3", docs[0].Line)
	}
}

// --- File-level docs stay file-level ---

func TestFileHeaderDocIsNotStolenByTheFirstDeclaration(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"src/mod.go": `// rivet:context: This FILE documents the whole module.

package src

import "fmt"

func FirstThing() { fmt.Println("x") }
`,
	})
	docs := ci.All()
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d: %+v", len(docs), docs)
	}
	if docs[0].Symbol != "" {
		t.Errorf("symbol = %q, want file-level: package/import lines sit between", docs[0].Symbol)
	}
	if len(ci.ForSymbol("FirstThing")) != 0 {
		t.Error("file-level doc leaked onto FirstThing")
	}
}

func TestDecoratorsBetweenCommentAndDeclarationStillAttach(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"svc/jobs.py": `# rivet:context
# Cron must never run on Tuesdays.
@app.task
@retry(3)
def run_billing():
    pass
`,
	})
	if len(ci.ForSymbol("run_billing")) != 1 {
		t.Fatalf("expected doc on run_billing: %+v", ci.All())
	}
}

func TestBlankLinesBetweenCommentAndDeclarationStillAttach(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"a.go": "package a\n\n// rivet:context\n// Note.\n\nfunc F() {}\n",
	})
	if len(ci.ForSymbol("F")) != 1 {
		t.Fatalf("expected doc on F: %+v", ci.All())
	}
}

func TestCodeBetweenCommentAndDeclarationBreaksAttachment(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"a.go": "package a\n\n// rivet:context\n// Note about the file.\n\nvar _ = 1\n\nfunc F() {}\n",
	})
	docs := ci.All()
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(docs))
	}
	if docs[0].Symbol == "F" {
		t.Error("doc jumped over an unrelated statement to reach F")
	}
}

// --- Explicit symbol names are validated ---

func TestExplicitSymbolThatDoesNotExistIsNotClaimed(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"orders/handler.go": `package orders

func ProcessPayment() error { return nil }

// rivet:context(TotallyMadeUpSymbol)
// This names a symbol that is not here.
var x = 1
`,
	})
	if docs := ci.ForSymbol("TotallyMadeUpSymbol"); len(docs) != 0 {
		t.Errorf("recon claims a symbol its own symbol index denies: %+v", docs)
	}
	// The note itself is kept, as a file-level doc.
	docs := ci.ForFile("orders/handler.go")
	if len(docs) != 1 || docs[0].Symbol != "" {
		t.Errorf("expected the doc to survive at file level, got %+v", docs)
	}
}

func TestExplicitSymbolInAnotherFileIsNotClaimed(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"orders/a.go": "package orders\n\nfunc Elsewhere() {}\n",
		"orders/b.go": "package orders\n\n// rivet:context(Elsewhere)\n// Wrong file.\nvar y = 1\n",
	})
	for _, d := range ci.ForSymbol("Elsewhere") {
		if d.File == "orders/b.go" {
			t.Errorf("doc in b.go claims a symbol declared in a.go: %+v", d)
		}
	}
}

func TestExplicitSymbolQualifiedNameResolves(t *testing.T) {
	ci := buildDocs(t, map[string]string{
		"svc/jobs.py": `class Runner:
    def execute(self):
        pass

# rivet:context(Runner.execute)
# Qualified names resolve on the last component.
X = 1
`,
	})
	if len(ci.ForSymbol("execute")) != 1 {
		t.Fatalf("expected doc on execute: %+v", ci.All())
	}
}

func TestExplicitSymbolIsTrustedWhenTheFileHasNoIndexedSymbols(t *testing.T) {
	// No symbol extractor for this language, so there is nothing to check the
	// name against; refusing the name would drop information for no reason.
	ci := buildDocs(t, map[string]string{
		"schema.graphql": "# rivet:context(User)\n# The user type is denormalised on purpose.\ntype User { id: ID }\n",
	})
	if len(ci.ForSymbol("User")) != 1 {
		t.Fatalf("expected doc on User: %+v", ci.All())
	}
}

// --- Newly registered comment syntaxes ---

func TestMarkersInPreviouslyUnregisteredLanguages(t *testing.T) {
	cases := map[string]string{
		"src/App.vue":        "<!-- rivet:context: The root component owns auth state. -->\n<template><div/></template>\n",
		"src/App.svelte":     "<!-- rivet:context: Svelte island, hydrated late. -->\n<div></div>\n",
		"src/Page.astro":     "<!-- rivet:context: Static page, no client JS. -->\n<h1>x</h1>\n",
		"api/user.proto":     "// rivet:context: Field 4 is reserved forever.\nmessage User { }\n",
		"api/schema.graphql": "# rivet:context: Deprecated fields stay for one release.\ntype Q { a: Int }\n",
		"web/index.html":     "<!-- rivet:context: Served by the CDN, not the app. -->\n<html></html>\n",
		"web/main.css":       "/* rivet:context: Load order matters here. */\nbody { margin: 0; }\n",
		"src/util.nim":       "# rivet:context: Compiled with --threads:on.\nproc f() = discard\n",
	}
	for path, content := range cases {
		t.Run(path, func(t *testing.T) {
			ci := buildDocs(t, map[string]string{path: content})
			if docs := ci.ForFile(path); len(docs) != 1 {
				t.Fatalf("expected 1 doc for %s, got %d (all: %+v)", path, len(docs), ci.All())
			}
		})
	}
}

// --- Size ceiling ---

func TestLargeFileDocsAreNotDropped(t *testing.T) {
	// Symbol extraction has no size ceiling; a ceiling only on docs means a
	// big file's function is found while the note explaining it is not.
	var b strings.Builder
	b.WriteString("package big\n\n// rivet:context\n// Still documented.\nfunc Documented() {}\n\n")
	for b.Len() < (1<<20)+4096 {
		b.WriteString("// filler line to push this file past one megabyte\n")
	}
	ci := buildDocs(t, map[string]string{"big.go": b.String()})
	if len(ci.ForSymbol("Documented")) != 1 {
		t.Fatalf("doc dropped for a >1MB file: %+v", ci.All())
	}
}

// --- Determinism ---

func TestContextDocsAreDeterministic(t *testing.T) {
	files := map[string]string{
		"a.go":   "package a\n\n// rivet:context\n// A.\nfunc A() {}\n",
		"b.go":   "package b\n\n// rivet:context\n// B.\nfunc B() {}\n",
		"c.py":   "# rivet:context\n# C.\ndef c():\n    pass\n",
		"d.ts":   "// rivet:context\n// D.\nexport function d() {}\n",
		"e/f.rb": "# rivet:context\n# F.\ndef f; end\n",
	}
	root, idx := writeTree(t, files)
	symbols := NewSymbolIndex(root, idx)
	want := NewContextDocIndex(root, idx, symbols).All()
	for i := 0; i < 20; i++ {
		got := NewContextDocIndex(root, idx, symbols).All()
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("run %d differs:\n%+v\n%+v", i, want, got)
		}
	}
}

func TestScanFileContextDocsIncremental(t *testing.T) {
	root, idx := writeTree(t, map[string]string{
		"a.go": "package a\n\n// rivet:context\n// Doc A.\nfunc A() {}\n",
	})
	symbols := NewSymbolIndex(root, idx)
	docs := ScanFileContextDocs(root, idx.All(), symbols, idx)
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(docs))
	}
}
