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
