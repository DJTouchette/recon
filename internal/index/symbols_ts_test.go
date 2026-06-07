package index

import (
	"sort"
	"testing"
)

const goFixture = `package sample

import "fmt"

// Greeter greets.
type Greeter interface {
	Greet(name string) string
}

type Service struct {
	name string
}

type Handler = func(int) error

type IDList []int

const MaxRetries = 3

const (
	StatusOK   = 200
	StatusFail = 500
)

var DefaultName = "world"

// New makes a Service. The signature below spans multiple lines to make sure
// tree-sitter captures the whole header, not just the first line.
func New(
	name string,
	opts ...Option,
) *Service {
	// local declarations must NOT become top-level symbols
	const localConst = 1
	var localVar = 2
	_ = localConst
	_ = localVar
	return &Service{name: name}
}

func (s *Service) Greet(name string) string {
	// the string below mentions "func Decoy" and "type Fake" — neither is real
	note := "func Decoy() {} type Fake struct{}"
	return fmt.Sprintf("hello %s from %s %s", name, s.name, note)
}
`

func extractGo(t *testing.T, src string) map[string]string {
	t.Helper()
	syms, ok := extractSymbolsTS([]byte(src), "sample.go", "go")
	if !ok {
		t.Fatal("tree-sitter extraction not handled for go")
	}
	got := make(map[string]string, len(syms))
	for _, s := range syms {
		got[s.Name] = s.Kind
	}
	return got
}

func TestTreeSitterGoKinds(t *testing.T) {
	got := extractGo(t, goFixture)

	want := map[string]string{
		"Greeter":     "interface",
		"Service":     "struct",
		"Handler":     "type",
		"IDList":      "type",
		"MaxRetries":  "constant",
		"StatusOK":    "constant",
		"StatusFail":  "constant",
		"DefaultName": "var",
		"New":         "function",
		"Greet":       "method",
	}

	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("symbol %q: got kind %q, want %q", name, got[name], kind)
		}
	}
}

func TestTreeSitterGoNoFalsePositives(t *testing.T) {
	got := extractGo(t, goFixture)

	// Declarations that live inside comments, strings, or function bodies must
	// not be reported as top-level symbols.
	for _, bad := range []string{"localConst", "localVar", "Decoy", "Fake"} {
		if _, found := got[bad]; found {
			t.Errorf("unexpected symbol %q extracted (should be ignored)", bad)
		}
	}

	if len(got) != 10 {
		names := make([]string, 0, len(got))
		for n := range got {
			names = append(names, n)
		}
		sort.Strings(names)
		t.Errorf("expected 10 symbols, got %d: %v", len(got), names)
	}
}

func TestTreeSitterGoMultiLineSignature(t *testing.T) {
	syms, ok := extractSymbolsTS([]byte(goFixture), "sample.go", "go")
	if !ok {
		t.Fatal("not handled")
	}
	var sig string
	for _, s := range syms {
		if s.Name == "New" {
			sig = s.Signature
		}
	}
	// The whole multi-line header should be collapsed onto one line, with the
	// body excluded.
	want := "func New( name string, opts ...Option, ) *Service"
	if sig != want {
		t.Errorf("New signature:\n got: %q\nwant: %q", sig, want)
	}
}
