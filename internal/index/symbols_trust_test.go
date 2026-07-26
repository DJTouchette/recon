package index

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/djtouchette/recon/internal/scan"
)

// Tests for the trust properties of symbol extraction: that what recon reports
// is what is actually there, that what it cannot do is visible rather than
// silent, and that repeated runs agree.

// symNames returns the names of every symbol in a file, sorted.
func symNames(si *SymbolIndex, file string) []string {
	var names []string
	for _, s := range si.ForFile(file) {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return names
}

// parseFor returns the parse record for a file, failing the test if absent.
func parseFor(t *testing.T, si *SymbolIndex, file string) FileParse {
	t.Helper()
	fp := si.ParseFor(file)
	if fp == nil {
		t.Fatalf("no parse record for %s", file)
	}
	return *fp
}

func wantNames(t *testing.T, got, want []string, ctx string) {
	t.Helper()
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: symbols = %v, want %v", ctx, got, want)
	}
}

// --- Defect 1: .ts must not be parsed with the TSX grammar ---

func TestTypeScriptAngleBracketCast(t *testing.T) {
	// `<number>raw` is a legal TypeScript type assertion that the TSX grammar
	// cannot parse: it reads `<number>` as the start of a JSX element. Parsing
	// every .ts file as TSX truncated the file at the cast — this fixture
	// reported 1 of 6 declarations.
	root, idx := writeTree(t, map[string]string{
		"cast.ts": `const raw: any = 42;
const n = <number>raw;
export function alpha() { return 1; }
export class Gamma {}
export interface Delta { a: number }
export type Eps = string;
`,
	})
	si := NewSymbolIndex(root, idx)

	wantNames(t, symNames(si, "cast.ts"),
		[]string{"raw", "n", "alpha", "Gamma", "Delta", "Eps"}, "cast.ts")

	if fp := parseFor(t, si, "cast.ts"); fp.Status != ParseOK {
		t.Errorf("cast.ts status = %q (%s), want %q", fp.Status, fp.Detail, ParseOK)
	}
}

func TestTSXStillParsesJSX(t *testing.T) {
	// The other half of the fix: .tsx must keep the TSX grammar, or moving
	// "typescript" to the TypeScript grammar would simply break React files.
	root, idx := writeTree(t, map[string]string{
		"App.tsx": `export const App = () => <div className="x">hi</div>;
export function Widget() { return <span />; }
export interface Props { a: number }
`,
	})
	si := NewSymbolIndex(root, idx)

	wantNames(t, symNames(si, "App.tsx"), []string{"App", "Widget", "Props"}, "App.tsx")
	if fp := parseFor(t, si, "App.tsx"); fp.Status != ParseOK {
		t.Errorf("App.tsx status = %q (%s), want %q", fp.Status, fp.Detail, ParseOK)
	}
}

func TestTSXFallbackForMisnamedFile(t *testing.T) {
	// A .ts file that actually contains JSX does not parse under the TypeScript
	// grammar. Rather than reporting a truncated list as complete, the
	// alternate grammar is tried and wins.
	root, idx := writeTree(t, map[string]string{
		"jsx.ts": `export function View() { return <div>hi</div>; }
export const other = 1;
`,
	})
	si := NewSymbolIndex(root, idx)
	wantNames(t, symNames(si, "jsx.ts"), []string{"View", "other"}, "jsx.ts")
}

// --- Defect 2: a failed parse must be visible ---

func TestPartialParseIsRecorded(t *testing.T) {
	root, idx := writeTree(t, map[string]string{
		"broken.go": "package main\n\nfunc Good() {}\n\nfunc Bad( {\n",
		"clean.go":  "package main\n\nfunc Fine() {}\n",
	})
	si := NewSymbolIndex(root, idx)

	broken := parseFor(t, si, "broken.go")
	if broken.Status != ParsePartial {
		t.Errorf("broken.go status = %q, want %q", broken.Status, ParsePartial)
	}
	if broken.Detail == "" {
		t.Error("broken.go: partial parse has no explanatory detail")
	}
	// The grammar stops at `func Bad( {` and loses everything from there. The
	// line patterns reach it, so the file is extracted by both and says so —
	// per-symbol provenance below keeps that exact rather than blurring it.
	if broken.Extractor != ExtractorMixed {
		t.Errorf("broken.go extractor = %q, want %q", broken.Extractor, ExtractorMixed)
	}

	var good, bad *Symbol
	syms := si.ForFile("broken.go")
	for i := range syms {
		switch syms[i].Name {
		case "Good":
			good = &syms[i]
		case "Bad":
			bad = &syms[i]
		}
	}
	if good == nil {
		t.Fatal("broken.go: Good was lost")
	}
	if good.Extractor != ExtractorTreeSitter {
		t.Errorf("Good extractor = %q, want %q — the grammar parsed it and its line is trustworthy", good.Extractor, ExtractorTreeSitter)
	}
	if bad == nil {
		t.Error("broken.go: Bad is past the syntax error and was not recovered by line patterns")
	} else if bad.Extractor != ExtractorRegex {
		t.Errorf("Bad extractor = %q, want %q — recovered approximately, and must not claim otherwise", bad.Extractor, ExtractorRegex)
	}

	if clean := parseFor(t, si, "clean.go"); clean.Status != ParseOK {
		t.Errorf("clean.go status = %q, want %q", clean.Status, ParseOK)
	}

	// Incomplete() is the caveat list: it must name the broken file and only it.
	inc := si.Incomplete()
	if len(inc) != 1 || inc[0].RelPath != "broken.go" {
		t.Errorf("Incomplete() = %v, want just broken.go", inc)
	}
}

// --- Defect 3: test-class files must contribute symbols ---

func TestTestFilesAreIndexed(t *testing.T) {
	root, idx := writeTree(t, map[string]string{
		"prod.go":            "package app\n\nfunc Run() {}\n",
		"prod_test.go":       "package app\n\nfunc TestRun(t *testing.T) {}\n",
		"test/fixtures.go":   "package test\n\ntype Fixture struct{}\n\nfunc NewFixture() *Fixture { return nil }\n",
		"spec/helpers.go":    "package spec\n\nfunc Helper() {}\n",
		"__tests__/setup.ts": "export function setupAll() {}\n",
	})
	si := NewSymbolIndex(root, idx)

	// Sanity: the classifier really does put all of these in ClassTest, which
	// is what made them invisible.
	for _, p := range []string{"prod_test.go", "test/fixtures.go", "spec/helpers.go", "__tests__/setup.ts"} {
		if got := idx.Get(p).Class; got != scan.ClassTest {
			t.Fatalf("%s classified as %v, expected test", p, got)
		}
	}

	for _, want := range []string{"Run", "TestRun", "Fixture", "NewFixture", "Helper", "setupAll"} {
		if len(si.Exact(want)) == 0 {
			t.Errorf("symbol %q not indexed", want)
		}
	}

	// The class is still available for callers that want to filter, which is
	// what makes indexing them safe.
	for _, s := range si.Exact("NewFixture") {
		if idx.Get(s.File).Class != scan.ClassTest {
			t.Errorf("NewFixture: file class not recoverable from the index")
		}
	}
}

// --- Defect 4: C++ headers ---

func TestCppHeaderSymbols(t *testing.T) {
	// .h maps to C, so a C++ header lost everything the C grammar could not
	// parse: this fixture reported 1 symbol (Point) of 4.
	root, idx := writeTree(t, map[string]string{
		"widget.h": `#pragma once
namespace app {
template<typename T>
class Widget {
public:
  void render();
};
struct Point { int x; int y; };
int freeFn(int a);
}
`,
	})
	si := NewSymbolIndex(root, idx)

	got := symNames(si, "widget.h")
	for _, want := range []string{"Widget", "render", "Point", "freeFn"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("C++ header: %q missing from %v", want, got)
		}
	}
}

func TestPlainCHeaderStillWorks(t *testing.T) {
	// Trying both grammars must not regress genuine C headers, and prototypes
	// (which are most of a header) must be captured.
	root, idx := writeTree(t, map[string]string{
		"api.h": `#ifndef API_H
#define API_H
struct Conn { int fd; };
int  conn_open(const char *host);
void conn_close(struct Conn *c);
typedef int conn_id;
#endif
`,
	})
	si := NewSymbolIndex(root, idx)
	for _, want := range []string{"Conn", "conn_open", "conn_close", "conn_id"} {
		if len(si.Exact(want)) == 0 {
			t.Errorf("C header: %q missing from %v", want, symNames(si, "api.h"))
		}
	}
}

// --- Defect 5: UTF-16 ---

func TestUTF16SourceIsDecoded(t *testing.T) {
	const src = "namespace App {\n  public class Foo {\n    public int Bar() { return 1; }\n  }\n}\n"

	root := t.TempDir()
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Utf8.cs", []byte(src))
	write("Utf8Bom.cs", append([]byte(bomUTF8), src...))
	write("Utf16LE.cs", append([]byte(bomUTF16LE), encodeUTF16(src, true)...))
	write("Utf16BE.cs", append([]byte(bomUTF16BE), encodeUTF16(src, false)...))
	write("Utf16NoBom.cs", encodeUTF16(src, true))

	walk, err := scan.Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	si := NewSymbolIndex(root, NewFileIndex(walk.Files))

	base := symNames(si, "Utf8.cs")
	if len(base) == 0 {
		t.Fatal("UTF-8 baseline produced no symbols")
	}
	for _, f := range []string{"Utf8Bom.cs", "Utf16LE.cs", "Utf16BE.cs", "Utf16NoBom.cs"} {
		if got := symNames(si, f); !reflect.DeepEqual(got, base) {
			t.Errorf("%s symbols = %v, want same as UTF-8 %v", f, got, base)
		}
		if fp := parseFor(t, si, f); fp.Status != ParseOK {
			t.Errorf("%s status = %q (%s)", f, fp.Status, fp.Detail)
		}
	}
}

// encodeUTF16 renders s as UTF-16 bytes without a BOM.
func encodeUTF16(s string, littleEndian bool) []byte {
	var out []byte
	for _, r := range s {
		u := uint16(r)
		if littleEndian {
			out = append(out, byte(u), byte(u>>8))
		} else {
			out = append(out, byte(u>>8), byte(u))
		}
	}
	return out
}

func TestDecodeTextRejectsBinary(t *testing.T) {
	bin := []byte{0x7f, 'E', 'L', 'F', 0x02, 0x00, 0x01, 0x00, 0x00, 0x00}
	if _, ok := decodeText(bin); ok {
		t.Error("decodeText accepted binary content as text")
	}
	// Latin-1 is not valid UTF-8 but is perfectly scannable text.
	latin1 := []byte("func caf\xe9() {}\n")
	if _, ok := decodeText(latin1); !ok {
		t.Error("decodeText rejected Latin-1 text")
	}
}

// --- Defect 6: the regex fallback must not invent or miss symbols ---

func TestRegexFallbackIgnoresCommentsAndStrings(t *testing.T) {
	root, idx := writeTree(t, map[string]string{
		"a.swift": "/*\npublic func ghostFromComment() {}\nclass GhostClass {}\n*/\n" +
			"let banner = \"public func ghostFromString() {}\"\n" +
			"public func real() {}\n",
		"b.dart": "/* class GhostFromComment {} */\n" +
			"const greeting = 'class GhostFromString {}';\n" +
			"int realFn(int a) => a;\n",
		"c.ex": "defmodule M do\n  @moduledoc \"\"\"\n  def ghost_from_doc(x), do: x\n  \"\"\"\n" +
			"  # def ghost_from_comment(x), do: x\n" +
			"  @doc \"def ghost_from_string(y), do: y\"\n" +
			"  def real_fn(x), do: x\nend\n",
	})
	si := NewSymbolIndex(root, idx)

	for _, ghost := range []string{
		"ghostFromComment", "GhostClass", "ghostFromString",
		"GhostFromComment", "GhostFromString",
		"ghost_from_doc", "ghost_from_comment", "ghost_from_string",
	} {
		if hits := si.Exact(ghost); len(hits) > 0 {
			t.Errorf("invented symbol %q from non-code at %s:%d", ghost, hits[0].File, hits[0].Line)
		}
	}
	for _, real := range []string{"real", "realFn", "real_fn"} {
		if len(si.Exact(real)) == 0 {
			t.Errorf("real declaration %q was missed", real)
		}
	}
}

func TestSwiftFunctionVisibility(t *testing.T) {
	// The old patterns required public/open/internal at line start or a leading
	// indent before `func`, so a top-level unmodified func and every private
	// func in the language were invisible.
	root, idx := writeTree(t, map[string]string{
		"a.swift": `func topLevel(a: Int) -> Int { return a }
private func hidden() {}
public struct Model {
    var name: String
    private func helper() {
        func nestedLocal() {}
    }
    public func render(
        into target: String,
        scale: Int
    ) -> String { return target }
}
protocol Drawable {}
`,
	})
	si := NewSymbolIndex(root, idx)

	for _, want := range []string{"topLevel", "hidden", "Model", "name", "helper", "render", "Drawable"} {
		if len(si.Exact(want)) == 0 {
			t.Errorf("swift: %q missing from %v", want, symNames(si, "a.swift"))
		}
	}
	// A function declared inside another function's body is a local, not part
	// of the file's shape; it used to leak out as a top-level "method".
	if hits := si.Exact("nestedLocal"); len(hits) > 0 {
		t.Errorf("swift: nested local function leaked as a %s symbol", hits[0].Kind)
	}
	// Brace depth, not indentation, decides function vs method.
	if k := si.Exact("topLevel")[0].Kind; k != "function" {
		t.Errorf("swift: topLevel kind = %q, want function", k)
	}
	if k := si.Exact("helper")[0].Kind; k != "method" {
		t.Errorf("swift: helper kind = %q, want method", k)
	}
	// A declaration spanning several lines gets a whole signature.
	if sig := si.Exact("render")[0].Signature; !strings.Contains(sig, "scale: Int") {
		t.Errorf("swift: multi-line signature truncated: %q", sig)
	}
}

func TestDartMembers(t *testing.T) {
	root, idx := writeTree(t, map[string]string{
		"b.dart": `class Point {
  final int x;
  int? label;
  Point(this.x);
  Point.origin() : x = 0;
  int get doubled => x * 2;
  set label2(int v) { label = v; }
  Future<void> save(
      String path,
      {bool force = false}) async {
    void inner() {}
  }
}
int topFn(int a) => a;
`,
	})
	si := NewSymbolIndex(root, idx)

	for name, wantKind := range map[string]string{
		"Point":        "class",
		"x":            "property",
		"label":        "property",
		"Point.origin": "constructor",
		"doubled":      "property",
		"label2":       "property",
		"save":         "method",
		"topFn":        "function",
	} {
		hits := si.Exact(name)
		if len(hits) == 0 {
			t.Errorf("dart: %q missing from %v", name, symNames(si, "b.dart"))
			continue
		}
		found := false
		for _, h := range hits {
			if h.Kind == wantKind {
				found = true
			}
		}
		if !found {
			t.Errorf("dart: %q kind = %q, want %q", name, hits[0].Kind, wantKind)
		}
	}
	if hits := si.Exact("inner"); len(hits) > 0 {
		t.Errorf("dart: nested local function leaked as a %s symbol", hits[0].Kind)
	}
}

func TestElixirDefinitions(t *testing.T) {
	root, idx := writeTree(t, map[string]string{
		"c.ex": `defmodule My.Mod do
  defstruct [:a, :b]
  def public_fn(x), do: x
  defp private_fn(x), do: x
  def unquote(:dynamic)(x), do: x
  def valid?(x), do: true
  defmacro mac(x), do: x
end
`,
	})
	si := NewSymbolIndex(root, idx)

	for _, want := range []string{"public_fn", "private_fn", "valid?", "mac"} {
		if len(si.Exact(want)) == 0 {
			t.Errorf("elixir: %q missing from %v", want, symNames(si, "c.ex"))
		}
	}
	// `def unquote(:dynamic)(x)` declares a computed name; reporting "unquote"
	// invents a function that does not exist.
	if len(si.Exact("unquote")) > 0 {
		t.Error("elixir: metaprogramming `unquote` reported as a function")
	}
	// defstruct declares the enclosing module's struct. The old pattern had no
	// capture group and could never emit anything.
	var struct_ *Symbol
	for _, s := range si.ForFile("c.ex") {
		if s.Kind == "struct" {
			s := s
			struct_ = &s
		}
	}
	if struct_ == nil {
		t.Fatalf("elixir: defstruct produced no symbol; got %v", symNames(si, "c.ex"))
	}
	if struct_.Name != "My.Mod" {
		t.Errorf("elixir: struct name = %q, want My.Mod", struct_.Name)
	}
}

func TestMaskerHandlesNestedAndMultiline(t *testing.T) {
	m := newMasker("swift")
	lines := []string{
		`/* outer /* inner */ still comment */ func real() {}`,
		`let s = """`,
		`func ghost() {}`,
		`"""`,
		`func after() {}`,
	}
	var masked []string
	for _, l := range lines {
		masked = append(masked, m.mask(l))
	}
	if !strings.Contains(masked[0], "func real") {
		t.Errorf("nested block comment over-masked: %q", masked[0])
	}
	if strings.Contains(masked[2], "func ghost") {
		t.Errorf("heredoc body not masked: %q", masked[2])
	}
	if !strings.Contains(masked[4], "func after") {
		t.Errorf("code after heredoc masked: %q", masked[4])
	}
}

// --- Defect 7: no silent truncation at a 64KB line ---

func TestLongLineDoesNotTruncateFile(t *testing.T) {
	huge := strings.Repeat("x", 80*1024)
	root, idx := writeTree(t, map[string]string{
		// Swift takes the regex path, which is where the scanner limit lived.
		"big.swift": "let blob = \"" + huge + "\"\npublic func afterTheLongLine() {}\n",
		"big.dart":  "const blob = '" + huge + "';\nint afterInDart(int a) => a;\n",
	})
	si := NewSymbolIndex(root, idx)

	if len(si.Exact("afterTheLongLine")) == 0 {
		t.Error("swift: declaration after an 80KB line was dropped")
	}
	if len(si.Exact("afterInDart")) == 0 {
		t.Error("dart: declaration after an 80KB line was dropped")
	}
}

func TestSplitLinesHasNoLineLimit(t *testing.T) {
	data := []byte(strings.Repeat("y", 200*1024) + "\ntail\n")
	lines := splitLines(data)
	if len(lines) != 2 || lines[1] != "tail" {
		t.Fatalf("splitLines dropped content after a long line: %d lines", len(lines))
	}
	// CRLF must not leave stray carriage returns behind.
	if got := splitLines([]byte("a\r\nb\r\n")); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("splitLines(CRLF) = %q", got)
	}
}

// --- Defect 8: binary content must not become a preview ---

func TestPreviewSkipsBinary(t *testing.T) {
	root := t.TempDir()
	blob := make([]byte, 4096)
	for i := range blob {
		blob[i] = byte(i * 7 % 251)
	}
	blob[10] = 0 // the null byte that marks it as binary
	if err := os.WriteFile(filepath.Join(root, "blob.go"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	walk, err := scan.Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	extras := ExtractFileExtras(root, NewFileIndex(walk.Files))

	for _, e := range extras {
		switch e.RelPath {
		case "blob.go":
			if e.Preview != "" {
				t.Errorf("binary file produced a preview of %d bytes", len(e.Preview))
			}
			if e.ContentHash == "" {
				t.Error("binary file lost its content hash")
			}
		case "real.go":
			if !strings.Contains(e.Preview, "func main") {
				t.Errorf("real.go preview = %q", e.Preview)
			}
		}
	}
}

func TestPreviewLinesAreBounded(t *testing.T) {
	root := t.TempDir()
	minified := "var a=1;" + strings.Repeat("b", 100*1024) + ";\n"
	if err := os.WriteFile(filepath.Join(root, "bundle.js"), []byte(minified), 0o644); err != nil {
		t.Fatal(err)
	}
	walk, err := scan.Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ExtractFileExtras(root, NewFileIndex(walk.Files)) {
		if len(e.Preview) > 1024 {
			t.Errorf("preview for %s is %d bytes; previews must stay bounded", e.RelPath, len(e.Preview))
		}
	}
}

// --- Defect 9: languages with no extractor must be recorded ---

func TestUnsupportedLanguagesAreRecorded(t *testing.T) {
	root, idx := writeTree(t, map[string]string{
		"m.erl":    "-module(m).\nfoo() -> ok.\n",
		"core.clj": "(defn foo [] 1)\n",
		"A.vue":    "<template><div/></template>\n",
		"B.svelte": "<script>let x = 1;</script>\n",
		"F.fs":     "let foo x = x\n",
		"n.nim":    "proc foo() = discard\n",
		"s.r":      "foo <- function(x) x\n",
	})
	si := NewSymbolIndex(root, idx)

	if got := len(si.All()); got != 0 {
		t.Fatalf("expected no symbols for unsupported languages, got %d", got)
	}
	// The point of the fix: zero symbols is now accompanied by a reason for
	// every file, so "no symbols found" cannot be read as "no declarations".
	inc := si.Incomplete()
	if len(inc) != 7 {
		t.Fatalf("Incomplete() has %d records, want 7: %v", len(inc), inc)
	}
	for _, fp := range inc {
		if fp.Status != ParseUnsupported {
			t.Errorf("%s status = %q, want %q", fp.RelPath, fp.Status, ParseUnsupported)
		}
		if fp.Extractor != ExtractorNone {
			t.Errorf("%s extractor = %q, want %q", fp.RelPath, fp.Extractor, ExtractorNone)
		}
		if !strings.Contains(fp.Detail, fp.Lang) {
			t.Errorf("%s detail %q does not name the language %q", fp.RelPath, fp.Detail, fp.Lang)
		}
	}
}

func TestUnreadableFileIsRecordedAsFailed(t *testing.T) {
	root := t.TempDir()
	blob := make([]byte, 512)
	blob[0] = 0
	if err := os.WriteFile(filepath.Join(root, "blob.go"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	walk, err := scan.Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	si := NewSymbolIndex(root, NewFileIndex(walk.Files))

	fp := parseFor(t, si, "blob.go")
	if fp.Status != ParseFailed {
		t.Errorf("binary .go status = %q, want %q", fp.Status, ParseFailed)
	}
	if fp.Detail == "" {
		t.Error("failed parse carries no detail")
	}
}

// --- Defect 10: provenance ---

func TestSymbolProvenance(t *testing.T) {
	root, idx := writeTree(t, map[string]string{
		"a.go":    "package a\n\nfunc Parsed() {}\n",
		"b.swift": "public func Guessed() {}\n",
	})
	si := NewSymbolIndex(root, idx)

	got := si.Exact("Parsed")
	if len(got) != 1 || got[0].Extractor != ExtractorTreeSitter {
		t.Errorf("go symbol provenance = %+v, want %q", got, ExtractorTreeSitter)
	}
	got = si.Exact("Guessed")
	if len(got) != 1 || got[0].Extractor != ExtractorRegex {
		t.Errorf("swift symbol provenance = %+v, want %q", got, ExtractorRegex)
	}

	// Every file looked at gets a record, and its extractor agrees with the
	// symbols it produced.
	for _, fp := range si.Files() {
		for _, s := range si.ForFile(fp.RelPath) {
			if s.Extractor != fp.Extractor {
				t.Errorf("%s: symbol %q extractor %q disagrees with file record %q",
					fp.RelPath, s.Name, s.Extractor, fp.Extractor)
			}
		}
		if fp.SymbolCount != len(si.ForFile(fp.RelPath)) {
			t.Errorf("%s: SymbolCount %d != %d symbols", fp.RelPath, fp.SymbolCount, len(si.ForFile(fp.RelPath)))
		}
	}
}

func TestEveryIndexedFileHasAParseRecord(t *testing.T) {
	root, idx := writeTree(t, map[string]string{
		"a.go":      "package a\nfunc A() {}\n",
		"a_test.go": "package a\nfunc TestA(t *testing.T) {}\n",
		"run.sh":    "#!/bin/sh\nhello() { echo hi; }\n",
		"m.erl":     "-module(m).\n",
		"empty.go":  "package a\n",
		"README.md": "# docs\n",
	})
	si := NewSymbolIndex(root, idx)

	recorded := map[string]bool{}
	for _, fp := range si.Files() {
		recorded[fp.RelPath] = true
	}
	for _, want := range []string{"a.go", "a_test.go", "run.sh", "m.erl", "empty.go"} {
		if !recorded[want] {
			t.Errorf("no parse record for %s", want)
		}
	}
	// Docs are not source and are not claimed to have been examined.
	if recorded["README.md"] {
		t.Error("doc file got a symbol parse record")
	}
}

func TestCachedIndexRestoresParseRecords(t *testing.T) {
	// A cache hit is the common case, so provenance that only survives a full
	// rebuild is provenance that is almost never there when it matters.
	root, idx := writeTree(t, map[string]string{
		"a.go":  "package a\nfunc A() {}\n",
		"m.erl": "-module(m).\n",
	})
	fresh := NewSymbolIndex(root, idx)

	reloaded := NewSymbolIndexFromCache(fresh.All(), fresh.Files())
	if !reflect.DeepEqual(reloaded.All(), fresh.All()) {
		t.Error("reloaded symbols differ from the fresh scan")
	}
	if !reflect.DeepEqual(reloaded.Files(), fresh.Files()) {
		t.Errorf("reloaded parse records = %v, want %v", reloaded.Files(), fresh.Files())
	}
	if fp := reloaded.ParseFor("m.erl"); fp == nil || fp.Status != ParseUnsupported {
		t.Errorf("reloaded ParseFor(m.erl) = %v, want an unsupported record", fp)
	}
	if got := reloaded.Exact("A"); len(got) != 1 || got[0].Extractor != ExtractorTreeSitter {
		t.Errorf("reloaded symbol lost its provenance: %+v", got)
	}

	// The symbols-only constructor still works, and is honest about having no
	// parse records rather than implying everything was clean.
	old := NewSymbolIndexFromData(fresh.All())
	if len(old.Files()) != 0 || old.ParseFor("a.go") != nil {
		t.Error("NewSymbolIndexFromData invented parse records")
	}
}

// --- Defect 11: determinism ---

func TestReferenceIndexIsDeterministic(t *testing.T) {
	files := map[string]string{}
	// Enough files that concurrent extraction genuinely races.
	for i := 0; i < 40; i++ {
		files[filepath.Join("pkg", "f"+itoa(i)+".go")] =
			"package pkg\n\nfunc Caller" + itoa(i) + "() { Target(); Target() }\n"
	}
	root, idx := writeTree(t, files)

	var first []Reference
	for run := 0; run < 6; run++ {
		ri := NewReferenceIndex(root, idx)
		got := ri.ForName("Target")
		if len(got) == 0 {
			t.Fatal("no references found")
		}
		if run == 0 {
			first = got
			continue
		}
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d: reference order differs\n got %v\nwant %v", run, got[:3], first[:3])
		}
	}
	// And the order is the documented one, not merely stable by luck.
	for i := 1; i < len(first); i++ {
		a, b := first[i-1], first[i]
		if a.File > b.File || (a.File == b.File && a.Line > b.Line) {
			t.Fatalf("references not sorted by file/line: %v then %v", a, b)
		}
	}
}

func TestSymbolIndexIsDeterministic(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 40; i++ {
		files[filepath.Join("pkg", "f"+itoa(i)+".go")] =
			"package pkg\n\nfunc Fn" + itoa(i) + "() {}\n\ntype T" + itoa(i) + " struct{}\n"
	}
	root, idx := writeTree(t, files)

	var first []Symbol
	for run := 0; run < 6; run++ {
		got := NewSymbolIndex(root, idx).All()
		if run == 0 {
			first = got
			continue
		}
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d: symbol order differs", run)
		}
	}
}

// --- Grammar selection ---

func TestGrammarCandidates(t *testing.T) {
	cases := []struct {
		lang, path string
		want       []string
	}{
		{"typescript", "src/a.ts", []string{"typescript", "tsx"}},
		{"typescript", "src/a.mts", []string{"typescript", "tsx"}},
		{"typescript", "src/App.tsx", []string{"tsx", "typescript"}},
		{"c", "inc/api.h", []string{"c", "cpp"}},
		{"c", "src/main.c", []string{"c"}},
		{"go", "main.go", []string{"go"}},
		{"erlang", "m.erl", nil},
	}
	for _, tc := range cases {
		if got := grammarCandidates(tc.lang, tc.path); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("grammarCandidates(%q, %q) = %v, want %v", tc.lang, tc.path, got, tc.want)
		}
	}
}

func TestRefGrammarCandidatesMatchSymbolGrammars(t *testing.T) {
	// A .tsx file must use the TSX grammar for references too, or moving
	// "typescript" to the TypeScript grammar would silently drop every call
	// site in a React component.
	if got := refGrammarCandidates("typescript", "App.tsx"); len(got) == 0 || got[0] != tsxLangKey {
		t.Errorf("refGrammarCandidates for .tsx = %v, want tsx first", got)
	}
	if got := refGrammarCandidates("typescript", "a.ts"); len(got) == 0 || got[0] != "typescript" {
		t.Errorf("refGrammarCandidates for .ts = %v, want typescript first", got)
	}
}

func TestTSXReferencesStillResolve(t *testing.T) {
	root, idx := writeTree(t, map[string]string{
		"App.tsx": "import { helper } from './h';\nexport function App() { return <div>{helper()}</div>; }\n",
		"a.ts":    "const x = <number>1;\nexport function go() { helper(); }\n",
	})
	ri := NewReferenceIndex(root, idx)
	refs := ri.ForName("helper")
	files := map[string]bool{}
	for _, r := range refs {
		files[r.File] = true
	}
	if !files["App.tsx"] {
		t.Error("no reference to helper found in App.tsx")
	}
	if !files["a.ts"] {
		t.Error("no reference to helper found in a.ts (angle-bracket cast broke the parse)")
	}
}

// tree-sitter-c-sharp v0.23.5 — the latest release — misparses a collection
// expression whose single element is an identifier named after a member-level
// attribute target. `[type]` and `[field]` are indistinguishable from the start
// of an attribute list with a `type:` / `field:` target, so the grammar commits
// to that reading and the parse errors there.
//
// The seven member-level targets (field, event, method, param, property,
// return, type) all reproduce it; assembly and module do not, because they are
// only legal where a collection expression cannot appear.
//
// The code is valid and current, and there is no newer grammar to move to. What
// must hold is that the file is still usable and still honest: declarations
// after the error are reported, and the file says its parse was incomplete
// rather than presenting a short list as a full one.
func TestCSharpCollectionExpressionSyntaxIsHandled(t *testing.T) {
	const src = `namespace N;

internal class Svc(IRepo repo) : ISvc
{
    public void Assign(Tenant created, string type)
    {
        created.Types = [type];
    }

    public void AfterTheError() { }

    public string Name => "x";
}
`
	root, idx := writeTree(t, map[string]string{"Svc.cs": src})
	si := NewSymbolIndex(root, idx)

	names := map[string]bool{}
	for _, s := range si.ForFile("Svc.cs") {
		names[s.Name] = true
	}
	for _, want := range []string{"Svc", "Assign", "AfterTheError"} {
		if !names[want] {
			t.Errorf("%q missing; the parse error swallowed it (got %v)", want, names)
		}
	}

	fp := parseFor(t, si, "Svc.cs")
	switch fp.Status {
	case ParseOK:
		// Upstream fixed the grammar. Nothing further to assert.
	case ParsePartial:
		if fp.Detail == "" {
			t.Error("incomplete parse reported without a reason")
		}
	default:
		t.Errorf("status = %q, want %q or %q", fp.Status, ParsePartial, ParseOK)
	}
}

// .NET 10 file-based apps open with `#:package`/`#:sdk`/`#:property` lines that
// the SDK consumes and the C# grammar cannot parse. Left in, they collapsed the
// parse at line 1: three real scripts reported zero symbols under a "syntax
// errors" caveat, which reads as "recon could not understand this file" when
// the truth is that a script declares nothing. Blanked, the parse is clean and
// the empty result is honest.
func TestCSharpFileBasedAppDirectivesDoNotBreakTheParse(t *testing.T) {
	root, idx := writeTree(t, map[string]string{
		"tool.cs": `#:package dbup-sqlserver@7.2.0
#:package Microsoft.Data.SqlClient@6.1.4

using DbUp;

var conn = Environment.GetEnvironmentVariable("CONN");
Console.WriteLine(conn);

class Helper
{
    public void Run() { }
}
`,
	})
	si := NewSymbolIndex(root, idx)

	fp := parseFor(t, si, "tool.cs")
	if fp.Status != ParseOK {
		t.Errorf("status = %q (%s), want %q — #: lines are an SDK preamble, not a syntax error",
			fp.Status, fp.Detail, ParseOK)
	}

	names := map[string]bool{}
	for _, s := range si.ForFile("tool.cs") {
		names[s.Name] = true
	}
	// Declarations below the directives must survive; blanking preserves line
	// numbers, so Helper's reported line still matches the file on disk.
	if !names["Helper"] || !names["Run"] {
		t.Errorf("declarations after the directives were lost: %v", names)
	}
	for _, s := range si.ForFile("tool.cs") {
		if s.Name == "Helper" && s.Line != 9 {
			t.Errorf("Helper reported at line %d, want 9 — line numbers must survive stripping", s.Line)
		}
	}
}

// A partial parse that the line patterns cannot improve on must not claim a
// recovery it did not make.
func TestPartialParseWithNothingToRecoverStaysTreeSitter(t *testing.T) {
	root, idx := writeTree(t, map[string]string{
		// Broken, and containing no further declaration for the patterns to find.
		"only.go": "package main\n\nfunc Bad( {\n",
	})
	si := NewSymbolIndex(root, idx)

	fp := parseFor(t, si, "only.go")
	if fp.Status != ParsePartial {
		t.Skipf("grammar parsed this cleanly; nothing to assert (status %q)", fp.Status)
	}
	for _, s := range si.ForFile("only.go") {
		if s.Extractor == ExtractorRegex && s.Name != "Bad" {
			t.Errorf("unexpected recovered symbol %q", s.Name)
		}
	}
}
