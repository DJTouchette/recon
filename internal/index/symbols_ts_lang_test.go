package index

import (
	"sort"
	"testing"
)

// TestQueriesCompile fails if any registered grammar's query stops compiling
// (e.g. a grammar upgrade renamed a node type). Such a language would silently
// drop to the regex fallback in production, so we guard it here.
func TestQueriesCompile(t *testing.T) {
	for _, g := range tsGrammars {
		if err := registerTSLang(g.lang, g.langPtr, g.query); err != nil {
			t.Errorf("%s: query failed to compile: %v", g.lang, err)
		}
	}
}

type langCase struct {
	lang   string
	src    string
	want   map[string]string // name -> kind (all must be present)
	forbid []string          // names that must NOT appear
}

func TestTreeSitterLanguages(t *testing.T) {
	cases := []langCase{
		{
			lang: "python",
			src: `MAX = 5
lower = 1
def top():
    pass
class Animal:
    def speak(self):
        # def Decoy(): pass
        return "def Hidden(): pass"
`,
			want:   map[string]string{"MAX": "constant", "top": "function", "Animal": "class", "speak": "method"},
			forbid: []string{"lower", "Decoy", "Hidden"},
		},
		{
			lang: "javascript",
			src: `export function foo() {}
const Bar = () => 1;
const MAX_N = 42;
class Widget { render() {} }
// function Decoy() {}
const s = "class Fake {}";
`,
			want:   map[string]string{"foo": "function", "Bar": "function", "MAX_N": "constant", "Widget": "class", "render": "method", "s": "constant"},
			forbid: []string{"Decoy", "Fake"},
		},
		{
			lang: "typescript",
			src: `export interface Shape { area(): number }
type ID = string;
enum Color { Red, Green }
export class Box implements Shape { area() { return 0 } }
function build(): void {}
const Make = (): number => 1;
`,
			want: map[string]string{
				"Shape": "interface", "ID": "type", "Color": "enum",
				"Box": "class", "area": "method", "build": "function", "Make": "function",
			},
		},
		{
			lang: "rust",
			src: `pub struct Point { x: i32 }
pub enum Dir { N, S }
pub trait Draw { fn draw(&self); }
pub type Id = u64;
pub const MAX: u32 = 9;
pub fn free() {}
impl Point { pub fn mag(&self) -> i32 { 0 } }
`,
			want: map[string]string{
				"Point": "struct", "Dir": "enum", "Draw": "trait", "Id": "type",
				"MAX": "constant", "free": "function", "mag": "method",
			},
		},
		{
			lang: "ruby",
			src: `module Shapes
  class Circle
    def area
    end
    def self.make
    end
  end
end
MAX = 10
`,
			want: map[string]string{"Shapes": "module", "Circle": "class", "area": "method", "make": "function", "MAX": "constant"},
		},
		{
			lang: "java",
			src: `public class Foo {
  public int bar() { return 1; }
}
interface Greeter { String greet(); }
enum Color { RED }
`,
			want: map[string]string{"Foo": "class", "bar": "method", "Greeter": "interface", "greet": "method", "Color": "enum"},
		},
		{
			lang: "csharp",
			src: `namespace App {
  public class Service {
    public int Count { get; set; }
    public void Run() {}
  }
  public interface IThing { void Do(); }
  public enum State { On, Off }
  public struct Vec { }
  public record Person(string Name);
}
`,
			want: map[string]string{
				"App": "module", "Service": "class", "Run": "method", "Count": "property",
				"IThing": "interface", "State": "enum", "Vec": "struct", "Person": "class",
			},
		},
		{
			lang: "php",
			src: `<?php
namespace App;
interface Handler { public function handle(); }
trait Loggable {}
class Service { public function run() {} }
function helper() {}
`,
			want: map[string]string{
				"App": "module", "Handler": "interface", "Loggable": "trait",
				"Service": "class", "run": "method", "helper": "function",
			},
		},
		{
			lang: "scala",
			src: `package demo
class Service { def run(): Unit = {} }
trait Greeter { def greet(): String }
object Main { }
case class Point(x: Int)
type Id = Long
val MaxN = 5
`,
			want: map[string]string{
				"Service": "class", "Greeter": "trait", "Main": "module",
				"Point": "class", "Id": "type", "MaxN": "constant",
			},
		},
		{
			lang: "kotlin",
			src: `class Service {
    fun run() {}
}
interface Greeter {
    fun greet(): String
}
object Registry { }
fun main() {}
`,
			want: map[string]string{"Service": "class", "run": "function", "Greeter": "class", "Registry": "module", "main": "function"},
		},
		{
			lang: "c",
			src: `typedef struct Node Node;
struct Point { int x; };
enum Color { RED };
int add(int a, int b) { return a + b; }
`,
			want: map[string]string{"Point": "struct", "Color": "enum", "add": "function", "Node": "type"},
		},
		{
			lang: "cpp",
			src: `namespace geo {
class Shape { public: virtual double area(); };
struct Vec { int x; };
enum Color { Red };
int add(int a, int b) { return a + b; }
double Shape::area() { return 0; }
}
`,
			want: map[string]string{"geo": "module", "Shape": "class", "Vec": "struct", "Color": "enum", "add": "function", "area": "method"},
		},
		{
			lang: "lua",
			src: `local M = {}
function M.greet(name) return "hi" end
function standalone() end
function M:method1() end
local helper = function() end
return M
`,
			want:   map[string]string{"greet": "function", "standalone": "function", "method1": "method", "helper": "function"},
			forbid: []string{"M"},
		},
		{
			lang: "shell",
			src: `#!/bin/bash
greet() { echo hi; }
function deploy { echo go; }
x="not a func"
`,
			want:   map[string]string{"greet": "function", "deploy": "function"},
			forbid: []string{"x"},
		},
		{
			lang: "julia",
			src: `module Foo
struct Point x::Int end
abstract type Shape end
function area(s) 0 end
macro mymac(x) end
end
`,
			want: map[string]string{"Foo": "module", "Point": "struct", "Shape": "type", "area": "function", "mymac": "macro"},
		},
		{
			lang: "zig",
			src: `const std = @import("std");
const Point = struct { x: i32, y: i32 };
const Color = enum { red, green };
pub fn add(a: i32, b: i32) i32 { return a + b; }
fn helper() void {}
`,
			want:   map[string]string{"Point": "type", "Color": "type", "add": "function", "helper": "function"},
			forbid: []string{"std"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			syms, ok := extractSymbolsTS([]byte(tc.src), "f."+tc.lang, tc.lang)
			if !ok {
				t.Fatalf("%s: not handled by tree-sitter", tc.lang)
			}
			got := make(map[string]string, len(syms))
			for _, s := range syms {
				got[s.Name] = s.Kind
			}
			for name, kind := range tc.want {
				if got[name] != kind {
					t.Errorf("%s: %q kind = %q, want %q", tc.lang, name, got[name], kind)
				}
			}
			for _, bad := range tc.forbid {
				if _, found := got[bad]; found {
					t.Errorf("%s: %q should not have been extracted", tc.lang, bad)
				}
			}
			if t.Failed() {
				names := make([]string, 0, len(got))
				for n, k := range got {
					names = append(names, n+":"+k)
				}
				sort.Strings(names)
				t.Logf("%s extracted: %v", tc.lang, names)
			}
		})
	}
}
