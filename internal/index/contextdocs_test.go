package index

import (
	"os"
	"path/filepath"
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
