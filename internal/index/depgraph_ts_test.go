package index

import (
	"sort"
	"strings"
	"testing"
)

func TestImportQueriesCompile(t *testing.T) {
	// Every import query that ships must compile against its grammar, else that
	// language silently drops to the regex fallback.
	for _, g := range tsGrammars {
		if _, err := importQueryFS.ReadFile("queries/imports/" + g.lang + ".scm"); err != nil {
			continue // intentionally no import query for this language
		}
		if !hasTSImports(g.lang) {
			t.Errorf("%s: import query present but failed to compile/register", g.lang)
		}
	}
}

func TestTSImportSpecsJS(t *testing.T) {
	src := []byte(`import { A } from './single';
import {
  Foo,
  Bar,
} from './multiline';
export { X } from './reexport';
export * from './star';
import type { T } from './typeonly';
const dyn = await import('./dynamic');
const r = require('./required');
const decoy = "import y from './instring'";
// import z from './incomment';
import React from 'react';
`)
	got, ok := tsImportSpecs(src, "typescript")
	if !ok {
		t.Fatal("typescript import extraction not handled")
	}
	sort.Strings(got)

	want := []string{
		"./dynamic", "./multiline", "./reexport", "./required",
		"./single", "./star", "./typeonly", "react",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}

	// The regex matched inside a string and missed all the multi-line/re-export
	// cases — make sure tree-sitter did neither.
	for _, s := range got {
		if s == "./instring" || s == "./incomment" {
			t.Errorf("extracted %q from a string/comment", s)
		}
	}
}

// assertSpecs runs tsImportSpecs and asserts the captured set equals want
// (order-independent). It also fails if any "commented" / "instring" sentinel
// leaks through — the whole point of tree-sitter over regex.
func assertSpecs(t *testing.T, lang, src string, want []string) {
	t.Helper()
	got, ok := tsImportSpecs([]byte(src), lang)
	if !ok {
		t.Fatalf("%s: import extraction not handled", lang)
	}
	gs := sortedStrings(got)
	ws := sortedStrings(want)
	if len(gs) != len(ws) {
		t.Fatalf("%s: got %v, want %v", lang, gs, ws)
	}
	for i := range ws {
		if gs[i] != ws[i] {
			t.Fatalf("%s: got %v, want %v", lang, gs, ws)
		}
	}
	for _, s := range got {
		if strings.Contains(s, "commented") || strings.Contains(s, "instring") {
			t.Errorf("%s: extracted %q from a string/comment", lang, s)
		}
	}
}

func TestTSImportSpecsGo(t *testing.T) {
	// Multi-line block + single import; a commented import must NOT be captured.
	// Captured paths are bare (no quotes), matching the regex output.
	src := `package main
// import "github.com/commented/pkg"
import (
	"github.com/example/myapp/pkg/auth"
	_ "github.com/example/myapp/pkg/embedme"
	"fmt"
)
import "github.com/example/myapp/pkg/models"
var s = "import \"github.com/instring/pkg\""
`
	assertSpecs(t, "go", src, []string{
		"github.com/example/myapp/pkg/auth",
		"github.com/example/myapp/pkg/embedme",
		"fmt",
		"github.com/example/myapp/pkg/models",
	})
}

func TestTSImportSpecsJava(t *testing.T) {
	src := `package com.example;
// import com.commented.Thing;
import com.example.User;
import static com.example.MathUtils.pow;
import java.util.List;
`
	assertSpecs(t, "java", src, []string{
		"com.example.User",
		"com.example.MathUtils.pow",
		"java.util.List",
	})
}

func TestTSImportSpecsKotlin(t *testing.T) {
	src := `package com.example
// import com.commented.Thing
import com.example.Repository
import com.example.foo.bar
`
	assertSpecs(t, "kotlin", src, []string{
		"com.example.Repository",
		"com.example.foo.bar",
	})
}

func TestTSImportSpecsCSharp(t *testing.T) {
	src := `// using Commented.Ns;
using Public.Common.Services;
using Models;
using static App.MathHelpers;
using System;
`
	assertSpecs(t, "csharp", src, []string{
		"Public.Common.Services",
		"Models",
		"App.MathHelpers",
		"System",
	})
}

func TestTSImportSpecsPHP(t *testing.T) {
	src := `<?php
// use App\Commented;
use App\Models\User;
use Foo;
use function App\helper;
`
	assertSpecs(t, "php", src, []string{
		"App\\Models\\User",
		"Foo",
		"App\\helper",
	})
}

func TestTSImportSpecsScala(t *testing.T) {
	// Scala captures the whole import declaration; the resolver normalizes it.
	// A commented import must not be captured.
	src := `package com.example
// import com.commented.Thing
import com.example.models.User
import com.example.models.{User, Order}
import com.example.foo._
`
	assertSpecs(t, "scala", src, []string{
		"import com.example.models.User",
		"import com.example.models.{User, Order}",
		"import com.example.foo._",
	})

	// Normalization reproduces the historical regex-derived prefixes: the regex
	// drops only the final ".Segment", so a 4-segment import keeps 3 segments.
	if got := scalaNormalizeSpec("import com.example.models.User"); got != "com.example.models" {
		t.Errorf("scalaNormalizeSpec plain: got %q, want com.example.models", got)
	}
	if got := scalaNormalizeSpec("import com.example.User"); got != "com.example" {
		t.Errorf("scalaNormalizeSpec plain3: got %q, want com.example", got)
	}
	if got := scalaNormalizeSpec("import com.example.models.{User, Order}"); got != "com.example.models" {
		t.Errorf("scalaNormalizeSpec selectors: got %q, want com.example.models", got)
	}
	if got := scalaNormalizeSpec("import com.example.foo._"); got != "com.example.foo" {
		t.Errorf("scalaNormalizeSpec wildcard: got %q, want com.example.foo", got)
	}
}

func TestTSImportSpecsPython(t *testing.T) {
	src := []byte(`from .models import User
from ..shared.utils import helper
from . import sibling
import os
from typing import List
x = "from .fake import nothing"
`)
	got, ok := tsImportSpecs(src, "python")
	if !ok {
		t.Fatal("python import extraction not handled")
	}
	sort.Strings(got)
	want := []string{".", "..shared.utils", ".models"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
