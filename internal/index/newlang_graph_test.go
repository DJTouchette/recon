package index

import (
	"sort"
	"testing"

	"github.com/djtouchette/recon/internal/scan"
)

// Reference (call-site) extraction for the newer tree-sitter languages.
func TestNewLangReferences(t *testing.T) {
	cases := []struct {
		lang string
		src  string
		want []string
	}{
		{"lua", "local function run() foo(); obj.bar(); obj:baz() end\n", []string{"bar", "baz", "foo"}},
		{"shell", "#!/bin/bash\ngreet() { deploy arg; other_cmd; }\n", []string{"deploy", "other_cmd"}},
		{"julia", "function f()\n  foo(1)\n  Bar.baz(2)\nend\n", []string{"baz", "foo"}},
		{"zig", "fn f() void {\n  foo();\n  std.debug.print(\"x\");\n}\n", []string{"foo", "print"}},
	}
	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			refs, ok := extractReferencesTS([]byte(tc.src), "f", tc.lang)
			if !ok {
				t.Fatalf("%s: references not handled", tc.lang)
			}
			got := map[string]bool{}
			for _, r := range refs {
				got[r.Name] = true
			}
			for _, w := range tc.want {
				if !got[w] {
					names := make([]string, 0, len(got))
					for n := range got {
						names = append(names, n)
					}
					sort.Strings(names)
					t.Errorf("%s: missing call %q (got %v)", tc.lang, w, names)
				}
			}
		})
	}
}

// Call-site extraction for the remaining tree-sitter languages that previously
// had symbols but no call graph.
func TestMoreLangReferences(t *testing.T) {
	cases := []struct {
		lang string
		src  string
		want []string
	}{
		{"c", "int main(){ foo(); bar(1); return 0; }\n", []string{"bar", "foo"}},
		{"cpp", "void f(){ foo(); obj.method(); ns::func(); }\n", []string{"foo", "func", "method"}},
		{"scala", "object M { def f() = { foo(); obj.bar() } }\n", []string{"bar", "foo"}},
		{"kotlin", "fun f() { foo(); obj.bar(); a.b.chain() }\n", []string{"bar", "chain", "foo"}},
	}
	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			refs, ok := extractReferencesTS([]byte(tc.src), "f", tc.lang)
			if !ok {
				t.Fatalf("%s: references not handled", tc.lang)
			}
			got := map[string]bool{}
			for _, r := range refs {
				got[r.Name] = true
			}
			for _, w := range tc.want {
				if !got[w] {
					t.Errorf("%s: missing call %q", tc.lang, w)
				}
			}
		})
	}
}

// Ruby and Rust carry a directive on each import specifier (require vs
// require_relative; use vs mod), which tree-sitter extraction must preserve.
func TestRubyRustTaggedImports(t *testing.T) {
	taggedRuby := func(src string) []string {
		var specs []string
		tsImportEachMatch([]byte(src), "ruby", func(c map[string]string) {
			if c["path"] == "" {
				return
			}
			if c["_m"] == "require_relative" {
				specs = append(specs, "rel:"+c["path"])
			} else {
				specs = append(specs, "abs:"+c["path"])
			}
		})
		return sortedStrings(specs)
	}
	rb := taggedRuby("require_relative \"a/b\"\nrequire \"lib\"\n# require \"commented\"\nx = \"require 'instr'\"\n")
	wantRb := []string{"abs:lib", "rel:a/b"}
	if len(rb) != len(wantRb) || rb[0] != wantRb[0] || rb[1] != wantRb[1] {
		t.Errorf("ruby tagged specs: got %v, want %v", rb, wantRb)
	}

	taggedRust := func(src string) []string {
		var specs []string
		tsImportEachMatch([]byte(src), "rust", func(c map[string]string) {
			if u := c["use"]; u != "" {
				specs = append(specs, "use:"+u)
			}
			if m := c["mod"]; m != "" {
				specs = append(specs, "mod:"+m)
			}
		})
		return sortedStrings(specs)
	}
	ru := taggedRust("use crate::foo::Bar;\nuse crate::a::{B, C};\nmod child;\n// use crate::commented::X;\n")
	want := []string{"mod:child", "use:crate::a", "use:crate::foo::Bar"}
	if len(ru) != len(want) {
		t.Fatalf("rust tagged specs: got %v, want %v", ru, want)
	}
	for i := range want {
		if ru[i] != want[i] {
			t.Fatalf("rust tagged specs: got %v, want %v", ru, want)
		}
	}

	// And resolution still works end to end.
	rbIdx := mkPathIdx("lib/app.rb", "lib/auth/session.rb", "lib/util.rb")
	got := resolveRubySpecs([]string{"rel:../util", "abs:auth/session"}, "lib/sub/x.rb", rbIdx)
	_ = got // exercise path; detailed resolution covered in depgraph_resolve_test.go
	ruIdx := mkPathIdx("src/main.rs", "src/foo.rs", "src/foo/bar.rs")
	if r := resolveRustSpecs([]string{"use:crate::foo::Thing"}, "src/main.rs", ruIdx); len(r) == 0 {
		t.Errorf("rust resolution produced no edge for crate::foo::Thing (want src/foo.rs)")
	}
}

func mkPathIdx(paths ...string) *FileIndex {
	var es []scan.FileEntry
	for _, p := range paths {
		es = append(es, scan.FileEntry{RelPath: p, Lang: scan.LangFromExt(p), Class: scan.ClassSource})
	}
	return NewFileIndex(es)
}

// Import extraction + resolution for the newer tree-sitter languages.
func TestNewLangImports(t *testing.T) {
	mk := func(paths ...string) *FileIndex {
		var es []scan.FileEntry
		for _, p := range paths {
			es = append(es, scan.FileEntry{RelPath: p, Lang: scan.LangFromExt(p), Class: scan.ClassSource})
		}
		return NewFileIndex(es)
	}

	t.Run("zig", func(t *testing.T) {
		idx := mk("app/main.zig", "app/util.zig")
		specs, ok := tsImportSpecs([]byte(`const u = @import("util.zig");`+"\n"+`const std = @import("std");`), "zig")
		if !ok {
			t.Fatal("zig imports not handled")
		}
		got := resolveZigSpecs(specs, "app/main.zig", idx)
		if len(got) != 1 || got[0] != "app/util.zig" {
			t.Errorf("zig: got %v, want [app/util.zig]", got)
		}
	})

	t.Run("lua", func(t *testing.T) {
		idx := mk("src/main.lua", "src/foo/bar.lua")
		got := resolveLuaSpecs([]string{"foo.bar"}, "src/main.lua", idx)
		if len(got) != 1 || got[0] != "src/foo/bar.lua" {
			t.Errorf("lua: got %v, want [src/foo/bar.lua]", got)
		}
	})

	t.Run("julia", func(t *testing.T) {
		idx := mk("main.jl", "sub/mod.jl")
		got := resolveJuliaSpecs([]string{"sub/mod.jl"}, "main.jl", idx)
		if len(got) != 1 || got[0] != "sub/mod.jl" {
			t.Errorf("julia: got %v, want [sub/mod.jl]", got)
		}
	})

	t.Run("shell", func(t *testing.T) {
		idx := mk("run.sh", "lib.sh")
		got := resolveShellSpecs([]string{"./lib.sh", "$X/skip.sh"}, "run.sh", idx)
		if len(got) != 1 || got[0] != "lib.sh" {
			t.Errorf("shell: got %v, want [lib.sh]", got)
		}
	})
}
