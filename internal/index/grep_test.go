package index

import (
	"testing"
)

func grepAll(t *testing.T, pattern string, files map[string]string) []GrepMatch {
	t.Helper()
	root, idx := writeTree(t, files)
	matches, err := Grep(pattern, root, idx, nil, nil)
	if err != nil {
		t.Fatalf("Grep(%q) returned error: %v", pattern, err)
	}
	return matches
}

func matchedLines(matches []GrepMatch) map[int]string {
	out := make(map[int]string)
	for _, m := range matches {
		out[m.Line] = m.Text
	}
	return out
}

func TestGrepLiteralCaseInsensitive(t *testing.T) {
	matches := grepAll(t, "processpayment", map[string]string{
		"orders/handler.go": `package orders

func ProcessPayment() error { return nil }
`,
	})
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d: %+v", len(matches), matches)
	}
	if matches[0].Line != 3 {
		t.Errorf("match on line %d, want 3", matches[0].Line)
	}
}

func TestGrepRegexWordBoundary(t *testing.T) {
	matches := grepAll(t, `\bGet\b`, map[string]string{
		"a.go": `package a

var widget = "Widget"
var town = "Gettysburg"
var money = "budget"
func Get() {}
`,
	})
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d: %+v", len(matches), matches)
	}
	if matches[0].Line != 6 {
		t.Errorf("match on line %d, want 6 (func Get)", matches[0].Line)
	}
}

func TestGrepRegexShape(t *testing.T) {
	matches := grepAll(t, `func \w+Payment`, map[string]string{
		"a.go": `package a

func ProcessPayment() {}
func RefundPayment() {}
func PaymentHelper() {}
var payment = 1
`,
	})
	lines := matchedLines(matches)
	if len(lines) != 2 {
		t.Fatalf("expected 2 matches, got %d: %+v", len(lines), matches)
	}
	for _, want := range []int{3, 4} {
		if _, ok := lines[want]; !ok {
			t.Errorf("expected a match on line %d, got %v", want, lines)
		}
	}
}

func TestGrepRegexAlternation(t *testing.T) {
	matches := grepAll(t, `TODO|FIXME|HACK`, map[string]string{
		"a.go": `package a

// TODO: refactor
// FIXME: broken
// note: fine
var hack = 1
`,
	})
	lines := matchedLines(matches)
	// Alternation is case-insensitive by default, so "hack" on line 6 matches too.
	for _, want := range []int{3, 4, 6} {
		if _, ok := lines[want]; !ok {
			t.Errorf("expected a match on line %d, got %v", want, lines)
		}
	}
	if _, ok := lines[5]; ok {
		t.Errorf("line 5 should not match: %v", lines)
	}
}

func TestGrepRegexLineAnchors(t *testing.T) {
	matches := grepAll(t, `^func`, map[string]string{
		"a.go": `package a

func Top() {}
var f = func() {}
`,
	})
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d: %+v", len(matches), matches)
	}
	if matches[0].Line != 3 {
		t.Errorf("match on line %d, want 3", matches[0].Line)
	}
}

func TestGrepRegexCaseSensitiveOptOut(t *testing.T) {
	files := map[string]string{
		"a.go": `package a

var a = "get"
var b = "Get"
`,
	}
	matches := grepAll(t, `(?-i)Get\b`, files)
	if len(matches) != 1 || matches[0].Line != 4 {
		t.Fatalf("expected only line 4 to match, got %+v", matches)
	}
}

func TestGrepInvalidRegex(t *testing.T) {
	root, idx := writeTree(t, map[string]string{"a.go": "package a\n"})
	if _, err := Grep(`[unclosed`, root, idx, nil, nil); err == nil {
		t.Fatal("expected error for invalid regex, got nil")
	}
}

func TestNewMatcherFastPath(t *testing.T) {
	m, err := newMatcher("plainLiteral_123")
	if err != nil {
		t.Fatal(err)
	}
	if m.re != nil || m.literal == nil {
		t.Errorf("plain pattern should use the literal fast path, got %+v", m)
	}

	m, err = newMatcher(`func \w+`)
	if err != nil {
		t.Fatal(err)
	}
	if m.re == nil {
		t.Errorf("regex pattern should compile a regexp, got %+v", m)
	}
}
