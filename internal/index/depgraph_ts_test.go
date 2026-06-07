package index

import (
	"sort"
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
