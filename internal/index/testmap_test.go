package index

import (
	"reflect"
	"testing"
)

// buildTestMap writes a tree and returns its test map.
func buildTestMap(t *testing.T, files map[string]string) *TestMap {
	t.Helper()
	_, idx := writeTree(t, files)
	return NewTestMap(idx)
}

func assertMapped(t *testing.T, tm *TestMap, testPath, wantSource string) {
	t.Helper()
	got, status := tm.LookupSource(testPath)
	if got != wantSource {
		t.Errorf("source for %s = %q, want %q (status %s, unmapped %v)",
			testPath, got, wantSource, status, tm.UnmappedTests())
		return
	}
	if status != TestMapMapped {
		t.Errorf("status for %s = %s, want mapped", testPath, status)
	}
	tests, st := tm.LookupTests(wantSource)
	if st != TestMapMapped {
		t.Errorf("reverse status for %s = %s", wantSource, st)
	}
	found := false
	for _, p := range tests {
		if p == testPath {
			found = true
		}
	}
	if !found {
		t.Errorf("reverse map for %s = %v, missing %s", wantSource, tests, testPath)
	}
}

// --- The verified gaps ---

func TestGoTestNamedForAFacetMapsToItsSource(t *testing.T) {
	// depgraph_resolve_test.go sits next to depgraph.go; there is no
	// depgraph_resolve.go.
	tm := buildTestMap(t, map[string]string{
		"internal/index/depgraph.go":              "package index\n",
		"internal/index/depgraph_resolve_test.go": "package index\n",
		"internal/index/depgraph_ts.go":           "package index\n",
		"internal/index/depgraph_ts_test.go":      "package index\n",
	})
	assertMapped(t, tm, "internal/index/depgraph_resolve_test.go", "internal/index/depgraph.go")
	// The more specific name still wins where it exists.
	assertMapped(t, tm, "internal/index/depgraph_ts_test.go", "internal/index/depgraph_ts.go")
}

func TestBehaviourNamedTestMapsToTheFileItExercises(t *testing.T) {
	tm := buildTestMap(t, map[string]string{
		"src/checkout.ts":                     "export function checkout() {}\n",
		"src/__tests__/checkout-flow.test.ts": "test('x', () => {});\n",
	})
	assertMapped(t, tm, "src/__tests__/checkout-flow.test.ts", "src/checkout.ts")
}

func TestTopLevelTestDirMirrorsSrc(t *testing.T) {
	tm := buildTestMap(t, map[string]string{
		"src/cart.ts":           "export function cart() {}\n",
		"test/src/cart.test.ts": "test('y', () => {});\n",
	})
	assertMapped(t, tm, "test/src/cart.test.ts", "src/cart.ts")
}

func TestTestsForDirectoryRootIsNotEmpty(t *testing.T) {
	// FilesInDir(".") used to build the prefix "./", which never matches a
	// cleaned relpath, so a repo-wide question returned nothing.
	_, idx := writeTree(t, map[string]string{
		"src/a.ts":      "export const a = 1;\n",
		"src/a.test.ts": "test('a', () => {});\n",
	})
	if n := len(idx.FilesUnderDir(".")); n != 2 {
		t.Errorf("FilesUnderDir(\".\") = %d files, want 2", n)
	}
	if n := len(idx.FilesUnderDir("")); n != 2 {
		t.Errorf("FilesUnderDir(\"\") = %d files, want 2", n)
	}
}

// --- Languages that had no rules at all ---

// These go through a real walk, so they cover both halves of the gap: the
// scanner classifying the file as a test, and the test map having a rule that
// ties it to its source.
func TestPreviouslyUnsupportedLanguagesAreMapped(t *testing.T) {
	cases := []struct {
		name   string
		files  map[string]string
		test   string
		source string
	}{
		{
			name:   "c",
			files:  map[string]string{"src/parser.c": "int p(void){return 0;}\n", "src/parser_test.c": "int main(void){return 0;}\n"},
			test:   "src/parser_test.c",
			source: "src/parser.c",
		},
		{
			name:   "c header",
			files:  map[string]string{"src/parser.h": "int p(void);\n", "test/parser_test.c": "int main(void){return 0;}\n"},
			test:   "test/parser_test.c",
			source: "src/parser.h",
		},
		{
			name:   "cpp",
			files:  map[string]string{"src/engine.cpp": "int e(){return 0;}\n", "test/engine_test.cpp": "int main(){return 0;}\n"},
			test:   "test/engine_test.cpp",
			source: "src/engine.cpp",
		},
		{
			name:   "lua",
			files:  map[string]string{"src/util.lua": "return {}\n", "spec/util_spec.lua": "describe('x')\n"},
			test:   "spec/util_spec.lua",
			source: "src/util.lua",
		},
		{
			name:   "julia",
			files:  map[string]string{"src/solver.jl": "module S end\n", "test/test_solver.jl": "using Test\n"},
			test:   "test/test_solver.jl",
			source: "src/solver.jl",
		},
		{
			name:   "zig",
			files:  map[string]string{"src/alloc.zig": "pub fn a() void {}\n", "src/alloc_test.zig": "test \"a\" {}\n"},
			test:   "src/alloc_test.zig",
			source: "src/alloc.zig",
		},
		{
			name:   "rust",
			files:  map[string]string{"src/lexer.rs": "pub fn l() {}\n", "src/lexer_test.rs": "#[test]\nfn t() {}\n"},
			test:   "src/lexer_test.rs",
			source: "src/lexer.rs",
		},
		{
			name:   "shell",
			files:  map[string]string{"scripts/deploy.sh": "#!/bin/sh\necho x\n", "tests/deploy_test.sh": "#!/bin/sh\necho y\n"},
			test:   "tests/deploy_test.sh",
			source: "scripts/deploy.sh",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tm := buildTestMap(t, tc.files)
			assertMapped(t, tm, tc.test, tc.source)
		})
	}
}

// --- Established conventions must keep working ---

func TestEstablishedConventions(t *testing.T) {
	cases := []struct {
		name   string
		files  map[string]string
		test   string
		source string
	}{
		{
			name:   "go same dir",
			files:  map[string]string{"pkg/a.go": "package pkg\n", "pkg/a_test.go": "package pkg\n"},
			test:   "pkg/a_test.go",
			source: "pkg/a.go",
		},
		{
			name:   "python test_ prefix in mirrored tests dir",
			files:  map[string]string{"app/orders.py": "x = 1\n", "tests/test_orders.py": "y = 1\n"},
			test:   "tests/test_orders.py",
			source: "app/orders.py",
		},
		{
			name: "java maven layout",
			files: map[string]string{
				"src/main/java/com/ex/Order.java":     "package com.ex;\nclass Order {}\n",
				"src/test/java/com/ex/OrderTest.java": "package com.ex;\nclass OrderTest {}\n",
			},
			test:   "src/test/java/com/ex/OrderTest.java",
			source: "src/main/java/com/ex/Order.java",
		},
		{
			name: "dotnet parallel project",
			files: map[string]string{
				"src/MyApp/Order.cs":            "class Order {}\n",
				"src/MyApp.Tests/OrderTests.cs": "class OrderTests {}\n",
			},
			test:   "src/MyApp.Tests/OrderTests.cs",
			source: "src/MyApp/Order.cs",
		},
		{
			name: "swift package layout",
			files: map[string]string{
				"Sources/Orders/Cart.swift":         "struct Cart {}\n",
				"Tests/OrdersTests/CartTests.swift": "import XCTest\n",
			},
			test:   "Tests/OrdersTests/CartTests.swift",
			source: "Sources/Orders/Cart.swift",
		},
		{
			name: "elixir test mirrors lib",
			files: map[string]string{
				"lib/app/orders.ex":        "defmodule App.Orders do\nend\n",
				"test/app/orders_test.exs": "defmodule App.OrdersTest do\nend\n",
			},
			test:   "test/app/orders_test.exs",
			source: "lib/app/orders.ex",
		},
		{
			name: "ruby spec mirrors app",
			files: map[string]string{
				"app/models/user.rb":       "class User; end\n",
				"spec/models/user_spec.rb": "describe User do\nend\n",
			},
			test:   "spec/models/user_spec.rb",
			source: "app/models/user.rb",
		},
		{
			name: "php psr-4 tests/Unit",
			files: map[string]string{
				"src/Payment.php":            "<?php class Payment {}\n",
				"tests/Unit/PaymentTest.php": "<?php class PaymentTest {}\n",
			},
			test:   "tests/Unit/PaymentTest.php",
			source: "src/Payment.php",
		},
		{
			name: "dart test mirrors lib",
			files: map[string]string{
				"lib/models/user.dart":       "class User {}\n",
				"test/models/user_test.dart": "void main() {}\n",
			},
			test:   "test/models/user_test.dart",
			source: "lib/models/user.dart",
		},
		{
			name: "scala sbt layout",
			files: map[string]string{
				"src/main/scala/Order.scala":     "class Order\n",
				"src/test/scala/OrderSpec.scala": "class OrderSpec\n",
			},
			test:   "src/test/scala/OrderSpec.scala",
			source: "src/main/scala/Order.scala",
		},
		{
			name: "typescript sibling spec",
			files: map[string]string{
				"src/cart.ts":      "export const c = 1;\n",
				"src/cart.spec.ts": "test('c', () => {});\n",
			},
			test:   "src/cart.spec.ts",
			source: "src/cart.ts",
		},
		{
			name: "tsx test maps to tsx source",
			files: map[string]string{
				"src/Button.tsx":      "export const B = () => null;\n",
				"src/Button.test.tsx": "test('b', () => {});\n",
			},
			test:   "src/Button.test.tsx",
			source: "src/Button.tsx",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tm := buildTestMap(t, tc.files)
			assertMapped(t, tm, tc.test, tc.source)
		})
	}
}

// --- "I don't know" is not "no" ---

func TestUnsupportedLanguageIsDistinguishableFromNoTests(t *testing.T) {
	tm := buildTestMap(t, map[string]string{
		"src/main.hs":  "main = return ()\n",
		"src/thing.go": "package src\n",
	})

	if _, status := tm.LookupTests("src/main.hs"); status != TestMapUnsupported {
		t.Errorf("Haskell status = %s, want unsupported", status)
	}
	if _, status := tm.LookupTests("src/thing.go"); status != TestMapNoMatch {
		t.Errorf("Go status = %s, want no_match", status)
	}
	if !SupportsTestMapping("a/b.go") || SupportsTestMapping("a/b.hs") {
		t.Error("SupportsTestMapping disagrees with LookupTests")
	}
}

func TestUnmappedTestsAreReported(t *testing.T) {
	tm := buildTestMap(t, map[string]string{
		"src/a.go":                 "package src\n",
		"src/a_test.go":            "package src\n",
		"src/nothing_here_test.go": "package src\n",
	})
	if got := tm.UnmappedTests(); !reflect.DeepEqual(got, []string{"src/nothing_here_test.go"}) {
		t.Errorf("UnmappedTests = %v", got)
	}
}

func TestUnsupportedTestFileIsNotCountedAsUnmapped(t *testing.T) {
	// A test recon has no rules for is not evidence of a broken mapping.
	tm := buildTestMap(t, map[string]string{
		"test/thing_test.hs": "main = return ()\n",
	})
	if got := tm.UnmappedTests(); len(got) != 0 {
		t.Errorf("UnmappedTests = %v, want empty", got)
	}
}

// --- No fabricated mappings ---

func TestAmbiguousDistantStemIsNotMapped(t *testing.T) {
	// Two plausible "user.go" sources in unrelated trees and a test that
	// lives near neither: guessing one would be worse than saying nothing.
	tm := buildTestMap(t, map[string]string{
		"services/billing/user.go": "package billing\n",
		"services/auth/user.go":    "package auth\n",
		"qa/cases/user_test.go":    "package cases\n",
	})
	if src := tm.SourceFor("qa/cases/user_test.go"); src != "" {
		t.Errorf("mapped to %q, want no mapping", src)
	}
}

func TestSameDirectoryBeatsMirroredDirectory(t *testing.T) {
	tm := buildTestMap(t, map[string]string{
		"src/cart.ts":             "export const a = 1;\n",
		"src/nested/cart.ts":      "export const b = 1;\n",
		"src/nested/cart.test.ts": "test('x', () => {});\n",
	})
	assertMapped(t, tm, "src/nested/cart.test.ts", "src/nested/cart.ts")
}

func TestTestFileIsNeverItsOwnSource(t *testing.T) {
	tm := buildTestMap(t, map[string]string{
		"src/a_test.go": "package src\n",
	})
	if src := tm.SourceFor("src/a_test.go"); src != "" {
		t.Errorf("mapped to itself: %q", src)
	}
}

// --- Determinism ---

func TestTestMapIsDeterministic(t *testing.T) {
	files := map[string]string{
		"src/a.ts":                "export const a = 1;\n",
		"src/b.ts":                "export const b = 1;\n",
		"src/a.test.ts":           "test('a', () => {});\n",
		"src/b.test.ts":           "test('b', () => {});\n",
		"src/__tests__/a.test.js": "test('a2', () => {});\n",
		"pkg/x.go":                "package pkg\n",
		"pkg/x_test.go":           "package pkg\n",
	}
	_, idx := writeTree(t, files)
	want := NewTestMap(idx)
	for i := 0; i < 20; i++ {
		got := NewTestMap(idx)
		if !reflect.DeepEqual(want.AllMappings(), got.AllMappings()) {
			t.Fatalf("run %d differs:\n%v\n%v", i, want.AllMappings(), got.AllMappings())
		}
		if !reflect.DeepEqual(want.TestToSourceMap(), got.TestToSourceMap()) {
			t.Fatalf("run %d reverse map differs", i)
		}
	}
}

// --- Unit-level helpers ---

func TestCandidateStems(t *testing.T) {
	conv := testConventions[".go"]
	exact, fuzzy := candidateStems("depgraph_resolve_test", conv)
	if len(exact) == 0 || exact[0] != "depgraph_resolve" {
		t.Errorf("exact = %v", exact)
	}
	if !containsStr(fuzzy, "depgraph") {
		t.Errorf("fuzzy = %v, want depgraph", fuzzy)
	}
}

func TestCandidateStemsCamelCase(t *testing.T) {
	conv := testConventions[".java"]
	exact, fuzzy := candidateStems("CheckoutFlowTest", conv)
	if !containsStr(exact, "CheckoutFlow") {
		t.Errorf("exact = %v", exact)
	}
	if !containsStr(fuzzy, "Checkout") {
		t.Errorf("fuzzy = %v", fuzzy)
	}
}

func TestMirrorDirsRewritesTestSegments(t *testing.T) {
	dirs := mirrorDirs("test/src")
	if !dirs["src"] {
		t.Errorf("mirrorDirs(test/src) missing src: %v", keysOf(dirs))
	}
	dirs = mirrorDirs("src/test/java/com/ex")
	if !dirs["src/main/java/com/ex"] {
		t.Errorf("mirrorDirs missing maven main path: %v", keysOf(dirs))
	}
}

func TestClassifyTestKind(t *testing.T) {
	cases := map[string]string{
		"e2e/checkout.spec.ts":         "e2e",
		"tests/integration/db_test.go": "integration",
		"internal/index/grep_test.go":  "unit",
		"playwright/login.spec.ts":     "e2e",
	}
	for path, want := range cases {
		if got := ClassifyTestKind(path); got != want {
			t.Errorf("ClassifyTestKind(%q) = %q, want %q", path, got, want)
		}
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
