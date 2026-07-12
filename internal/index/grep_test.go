package index

import (
	"testing"
)

func grepMode(t *testing.T, pattern string, mode CaseMode, files map[string]string) []GrepMatch {
	t.Helper()
	root, idx := writeTree(t, files)
	matches, err := Grep(pattern, root, idx, nil, nil, mode)
	if err != nil {
		t.Fatalf("Grep(%q) returned error: %v", pattern, err)
	}
	return matches
}

func grepAll(t *testing.T, pattern string, files map[string]string) []GrepMatch {
	t.Helper()
	return grepMode(t, pattern, CaseSmart, files)
}

func matchedLines(matches []GrepMatch) map[int]string {
	out := make(map[int]string)
	for _, m := range matches {
		out[m.Line] = m.Text
	}
	return out
}

func TestGrepLiteralSmartCaseLowercase(t *testing.T) {
	// All-lowercase pattern matches case-insensitively under smart-case.
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

func TestGrepLiteralSmartCaseUppercase(t *testing.T) {
	// A pattern with an uppercase letter matches case-sensitively.
	matches := grepAll(t, "Payment", map[string]string{
		"a.go": `package a

var payment = 1
var Payment = 2
`,
	})
	if len(matches) != 1 || matches[0].Line != 4 {
		t.Fatalf("expected only line 4 to match, got %+v", matches)
	}
}

func TestGrepCaseModeOverrides(t *testing.T) {
	files := map[string]string{
		"a.go": `package a

var payment = 1
var Payment = 2
`,
	}

	// CaseInsensitive: uppercase pattern still matches both.
	matches := grepMode(t, "Payment", CaseInsensitive, files)
	if len(matches) != 2 {
		t.Errorf("CaseInsensitive: expected 2 matches, got %+v", matches)
	}

	// CaseSensitive: lowercase pattern matches only the lowercase line.
	matches = grepMode(t, "payment", CaseSensitive, files)
	if len(matches) != 1 || matches[0].Line != 3 {
		t.Errorf("CaseSensitive: expected only line 3 to match, got %+v", matches)
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
	files := map[string]string{
		"a.go": `package a

// TODO: refactor
// FIXME: broken
// note: fine
var hack = 1
`,
	}

	// Uppercase pattern is case-sensitive under smart-case: "hack" doesn't match.
	lines := matchedLines(grepAll(t, `TODO|FIXME|HACK`, files))
	if len(lines) != 2 {
		t.Fatalf("smart-case: expected 2 matches, got %v", lines)
	}
	for _, want := range []int{3, 4} {
		if _, ok := lines[want]; !ok {
			t.Errorf("smart-case: expected a match on line %d, got %v", want, lines)
		}
	}

	// With CaseInsensitive, "hack" on line 6 matches too.
	lines = matchedLines(grepMode(t, `TODO|FIXME|HACK`, CaseInsensitive, files))
	if _, ok := lines[6]; !ok || len(lines) != 3 {
		t.Errorf("insensitive: expected lines 3, 4, 6 to match, got %v", lines)
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

func TestGrepInvalidRegex(t *testing.T) {
	root, idx := writeTree(t, map[string]string{"a.go": "package a\n"})
	if _, err := Grep(`[unclosed`, root, idx, nil, nil, CaseSmart); err == nil {
		t.Fatal("expected error for invalid regex, got nil")
	}
}

func TestNewMatcherFastPath(t *testing.T) {
	m, err := newMatcher("plainLiteral_123", CaseSmart)
	if err != nil {
		t.Fatal(err)
	}
	if m.re != nil || m.literal == nil {
		t.Errorf("plain pattern should use the literal fast path, got %+v", m)
	}
	if !m.caseSensitive {
		t.Errorf("pattern with uppercase should be case-sensitive under smart-case")
	}

	m, err = newMatcher(`func \w+`, CaseSmart)
	if err != nil {
		t.Fatal(err)
	}
	if m.re == nil {
		t.Errorf("regex pattern should compile a regexp, got %+v", m)
	}
}

func TestHasUpper(t *testing.T) {
	cases := []struct {
		pattern string
		want    bool
	}{
		{"payment", false},
		{"Payment", true},
		{`func \w+`, false},
		{`\Wfoo`, false},   // escaped W is a character class, not a literal
		{`\\Wfoo`, true},   // escaped backslash, then a literal W
		{`todo|fixme`, false},
		{`TODO|fixme`, true},
	}
	for _, c := range cases {
		if got := hasUpper(c.pattern); got != c.want {
			t.Errorf("hasUpper(%q) = %v, want %v", c.pattern, got, c.want)
		}
	}
}
