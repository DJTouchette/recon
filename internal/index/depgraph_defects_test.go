package index

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/djtouchette/recon/internal/scan"
)

// These tests are fixture-based: each writes a small real repo to a temp
// directory and runs the real scan + dependency-graph build over it. That
// matters because most of the defects they cover were invisible to a
// path-only index — they were in what the resolvers read from disk, or in
// which specifiers the tree-sitter queries captured in the first place.
//
// Every case asserts the edges that must be ABSENT as well as the ones that
// must be present. The loose resolvers this replaces survived a full test
// suite precisely because nothing ever asserted a wrong edge was missing.

// writeFixture materialises files under a temp dir and returns the root.
func writeFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	return root
}

// newRepoFixture writes a repo and returns its root, index and resolve context.
func newRepoFixture(t *testing.T, files map[string]string) (string, *FileIndex, *resolveCtx) {
	t.Helper()
	root := writeFixture(t, files)
	res, err := scan.Walk(root)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	idx := NewFileIndex(res.Files)
	return root, idx, newResolveCtx(root, idx)
}

// buildGraph writes a repo and builds the dependency graph over it exactly the
// way a real scan does.
func buildGraph(t *testing.T, files map[string]string) *DepGraph {
	t.Helper()
	root, idx, _ := newRepoFixture(t, files)
	_ = root
	return NewDepGraph(root, idx)
}

// assertEdges checks the import edges of one file exactly: every path in want
// must be present and nothing else may be.
func assertEdges(t *testing.T, dg *DepGraph, from string, want ...string) {
	t.Helper()
	got := sortedStrings(dg.ImportsOf(from))
	w := sortedStrings(want)
	if len(got) != len(w) {
		t.Fatalf("%s imports = %v, want exactly %v", from, got, w)
	}
	for i := range w {
		if got[i] != w[i] {
			t.Fatalf("%s imports = %v, want exactly %v", from, got, w)
		}
	}
}

// assertNoEdge is the negative assertion these tests exist for.
func assertNoEdge(t *testing.T, dg *DepGraph, from, to string) {
	t.Helper()
	for _, e := range dg.ImportsOf(from) {
		if e == to {
			t.Errorf("fabricated edge %s → %s", from, to)
		}
	}
}

// ─── 1. Python absolute imports ───────────────────────────────────────────────

func TestPythonAbsoluteImportsResolve(t *testing.T) {
	dg := buildGraph(t, map[string]string{
		"app/__init__.py":         "",
		"app/core/__init__.py":    "",
		"app/core/engine.py":      "def process(x):\n    return x\n",
		"app/pkg/__init__.py":     "",
		"app/pkg/mod.py":          "VALUE = 1\n",
		"main.py":                 "from app.core.engine import process\nimport app.pkg.mod\n",
		"requirements.txt":        "django\n",
		"third_party_consumer.py": "from django.db import models\nimport os\n",
	})

	assertEdges(t, dg, "main.py", "app/core/engine.py", "app/pkg/mod.py")

	if got := len(dg.ImportedBy("app/core/engine.py")); got != 1 {
		t.Errorf("fan_in for app/core/engine.py = %d, want 1", got)
	}

	// A third-party absolute import must not be guessed into an edge.
	if got := dg.ImportsOf("third_party_consumer.py"); len(got) != 0 {
		t.Errorf("third-party imports produced edges: %v", got)
	}
	st, ok := dg.ImportStatsOf("third_party_consumer.py")
	if !ok {
		t.Fatal("no import stats recorded for third_party_consumer.py")
	}
	if st.External != 2 || st.Unresolved != 0 {
		t.Errorf("stats = %+v, want 2 external / 0 unresolved", st)
	}
}

func TestPythonSrcLayoutAndPackageRelatives(t *testing.T) {
	dg := buildGraph(t, map[string]string{
		"pyproject.toml":                  "[project]\nname = \"mylib\"\n",
		"src/mylib/__init__.py":           "",
		"src/mylib/sibling.py":            "X = 1\n",
		"src/mylib/util/__init__.py":      "",
		"src/mylib/util/helpers.py":       "def help():\n    pass\n",
		"src/mylib/sub/__init__.py":       "d = 1\n",
		"src/mylib/app.py":                "from mylib.util.helpers import help\nfrom . import sibling\nfrom .sub import d\n",
		"src/mylib/util/uses_absolute.py": "import mylib.sibling\n",
	})

	// src-layout absolute import, `from . import <module>` (module name lives in
	// the imported name, not the module_name), and `from .sub import d` where
	// sub/ is a package with only an __init__.py.
	assertEdges(t, dg, "src/mylib/app.py",
		"src/mylib/util/helpers.py",
		"src/mylib/__init__.py",
		"src/mylib/sibling.py",
		"src/mylib/sub/__init__.py",
	)
	assertEdges(t, dg, "src/mylib/util/uses_absolute.py", "src/mylib/sibling.py")
}

func TestPythonUnresolvedLocalImportIsReported(t *testing.T) {
	dg := buildGraph(t, map[string]string{
		"app/__init__.py": "",
		"app/main.py":     "from app.missing.module import thing\n",
	})

	if got := dg.ImportsOf("app/main.py"); len(got) != 0 {
		t.Fatalf("expected no edges, got %v", got)
	}
	st, _ := dg.ImportStatsOf("app/main.py")
	if st.Unresolved != 1 {
		t.Fatalf("stats = %+v, want 1 unresolved (the package exists locally, the module does not)", st)
	}
}

// ─── 2. Go multi-module repos ─────────────────────────────────────────────────

func TestGoMultiModuleWorkspace(t *testing.T) {
	dg := buildGraph(t, map[string]string{
		"go.work":               "go 1.22\n\nuse (\n\t./services/api\n\t./libs/util\n)\n",
		"libs/util/go.mod":      "module example.com/libs/util\n\ngo 1.22\n",
		"libs/util/util.go":     "package util\n\nfunc Helper() string { return \"x\" }\n",
		"libs/util/sub/sub.go":  "package sub\n\nfunc S() {}\n",
		"services/api/go.mod":   "module example.com/services/api\n\ngo 1.22\n",
		"services/api/main.go":  "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/libs/util\"\n\t\"example.com/libs/util/sub\"\n)\n\nfunc main() { fmt.Println(util.Helper()); sub.S() }\n",
		"services/api/other.go": "package main\n",
	})

	// Both the module's own root package and a subpackage of it must resolve.
	assertEdges(t, dg, "services/api/main.go", "libs/util/util.go", "libs/util/sub/sub.go")

	if got := len(dg.ImportedBy("libs/util/util.go")); got != 1 {
		t.Errorf("fan_in for a library whose root IS its API = %d, want 1", got)
	}
}

func TestGoModuleRootPackageIsImportable(t *testing.T) {
	// The old `prefix := goModPath + "/"` meant an import of the module path
	// itself never matched, so a single-module library whose API lives at its
	// root scored fan_in 0 from its own cmd/.
	idx := mkIdx(
		scan.FileEntry{RelPath: "lib.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "cmd/tool/main.go", Lang: "go", Class: scan.ClassSource},
	)
	got := resolveGoSpecs([]string{"example.com/lib"}, "cmd/tool/main.go", goMods("example.com/lib"), idx, nil)
	if len(got) != 1 || got[0] != "lib.go" {
		t.Fatalf("got %v, want [lib.go]", got)
	}
}

func TestGoNestedModuleWinsOverParent(t *testing.T) {
	mods := []goModule{
		{Path: "example.com/app/tools", Dir: "tools"},
		{Path: "example.com/app", Dir: ""},
	}
	sort.Slice(mods, func(i, j int) bool { return len(mods[i].Path) > len(mods[j].Path) })

	idx := mkIdx(
		scan.FileEntry{RelPath: "tools/gen/gen.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "app.go", Lang: "go", Class: scan.ClassSource},
	)
	got := resolveGoSpecs([]string{"example.com/app/tools/gen"}, "app.go", mods, idx, nil)
	if len(got) != 1 || got[0] != "tools/gen/gen.go" {
		t.Fatalf("got %v, want [tools/gen/gen.go]", got)
	}
}

// ─── 3. C# fabricated edges ───────────────────────────────────────────────────

func TestCSharpDoesNotFabricateEdgesFromDirectoryNames(t *testing.T) {
	dg := buildGraph(t, map[string]string{
		"src/Domain/Models/User.cs":  "namespace MyApp.Models;\n\npublic class User { public string Name { get; set; } }\n",
		"src/Legacy/Models/Thing.cs": "namespace Legacy.Models;\n\npublic class Thing { }\n",
		"src/Old/Models/Widget.cs":   "namespace Totally.Unrelated.Models\n{\n    public class Widget { }\n}\n",
		"src/Web/Startup.cs":         "using System;\nusing MyApp.Models;\n\nnamespace MyApp.Web;\n\npublic class Startup { public User U; }\n",
	})

	assertEdges(t, dg, "src/Web/Startup.cs", "src/Domain/Models/User.cs")
	assertNoEdge(t, dg, "src/Web/Startup.cs", "src/Legacy/Models/Thing.cs")
	assertNoEdge(t, dg, "src/Web/Startup.cs", "src/Old/Models/Widget.cs")

	if got := len(dg.ImportedBy("src/Legacy/Models/Thing.cs")); got != 0 {
		t.Errorf("fan_in for an unrelated Models directory = %d, want 0", got)
	}
}

// ─── 3b. C# third-party usings are External, not Unresolved ───────────────────
//
// Unresolved is the bucket that tells a caller "recon dropped a real edge".
// On a real 656-file C# repo, 413 of 3338 specifiers landed there and nearly
// all of them were vendor SDK and NuGet namespaces — an expected non-edge, no
// different from System.*. A caveat that fires on most files is one people
// learn to ignore, so it has to be reserved for the ambiguous cases.
func TestCSharpThirdPartyUsingsAreExternalNotUnresolved(t *testing.T) {
	files := map[string]string{
		"src/Models/User.cs":  "namespace MyApp.Models;\n\npublic class User { }\n",
		"src/Docs/Dto/Doc.cs": "namespace MyApp.Docs.Dto;\n\npublic class Doc { }\n",
		"src/Web/Startup.cs": "using System;\n" +
			"using pdftron;\n" +
			"using pdftron.Common;\n" +
			"using pdftron.PDF;\n" +
			"using pdftron.PDF.Annots;\n" +
			"using pdftron.SDF;\n" +
			"using QRCoder;\n" +
			"using MyApp.Models;\n" +
			"using MyApp.Docs;\n" +
			"\nnamespace MyApp.Web;\n\npublic class Startup { public User U; }\n",
	}
	dg := buildGraph(t, files)

	// The one real edge survives; nothing was fabricated for the vendor usings.
	assertEdges(t, dg, "src/Web/Startup.cs", "src/Models/User.cs")

	st, ok := dg.ImportStatsOf("src/Web/Startup.cs")
	if !ok {
		t.Fatal("no import stats for src/Web/Startup.cs")
	}
	// 9 usings: 1 resolved (MyApp.Models), 1 unresolved (MyApp.Docs — declared
	// only as a parent of MyApp.Docs.Dto, so a type recon cannot see may live
	// there), 7 external (System + 5 pdftron + QRCoder).
	if st.Extracted != 9 || st.Resolved != 1 || st.External != 7 || st.Unresolved != 1 {
		t.Fatalf("stats = %+v, want 9 extracted / 1 resolved / 7 external / 1 unresolved", st)
	}
	for _, spec := range st.UnresolvedSpecs {
		if spec != "MyApp.Docs" {
			t.Errorf("third-party using reported as unresolved: %q", spec)
		}
	}
}

// The reclassification moves specifiers between the External and Unresolved
// counters only. Edges are what the dependency graph is for, so this pins the
// whole edge set of a repo that mixes resolvable, third-party and ambiguous
// usings: if a future tweak to the classification adds or drops an edge, this
// fails rather than silently changing fan-in.
func TestCSharpClassificationDoesNotChangeEdges(t *testing.T) {
	dg := buildGraph(t, map[string]string{
		"src/Models/User.cs":      "namespace MyApp.Models;\n\npublic class User { }\n",
		"src/Models/Order.cs":     "namespace MyApp.Models;\n\npublic class Order { }\n",
		"src/Models/Dto/Line.cs":  "namespace MyApp.Models.Dto;\n\npublic class Line { }\n",
		"src/Helpers/MathHelp.cs": "namespace MyApp.Helpers;\n\npublic static class MathHelp { }\n",
		"src/Web/Startup.cs": "using System.Linq;\nusing QRCoder;\nusing MyApp.Models;\n" +
			"using MyApp.Nowhere;\nusing static MyApp.Helpers.MathHelp;\n" +
			"\nnamespace MyApp.Web;\n\npublic class Startup { }\n",
	})

	want := map[string][]string{
		"src/Web/Startup.cs": {
			"src/Helpers/MathHelp.cs",
			"src/Models/Order.cs",
			"src/Models/User.cs",
		},
	}
	got := map[string][]string{}
	for src, targets := range dg.AllImports() {
		if len(targets) > 0 {
			got[src] = sortedStrings(targets)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("edge map = %v, want %v", got, want)
	}
	for src, w := range want {
		g := got[src]
		if len(g) != len(w) {
			t.Fatalf("%s imports = %v, want %v", src, g, w)
		}
		for i := range w {
			if g[i] != w[i] {
				t.Fatalf("%s imports = %v, want %v", src, g, w)
			}
		}
	}

	// MyApp.Nowhere is not declared and nothing nests under it: External.
	st, _ := dg.ImportStatsOf("src/Web/Startup.cs")
	if st.Unresolved != 0 {
		t.Errorf("unresolved = %d (%v), want 0", st.Unresolved, st.UnresolvedSpecs)
	}
	if st.Resolved != 2 || st.External != 3 {
		t.Errorf("stats = %+v, want 2 resolved (MyApp.Models, using static) / 3 external", st)
	}
}

// ─── 4. Rust super:: ──────────────────────────────────────────────────────────

func TestRustSuperFromNonModFile(t *testing.T) {
	dg := buildGraph(t, map[string]string{
		"Cargo.toml":                 "[package]\nname = \"demo\"\nversion = \"0.1.0\"\n",
		"src/main.rs":                "mod models;\nmod helper;\nfn main() {}\n",
		"src/helper.rs":              "pub fn wrong() {}\n",
		"src/models/mod.rs":          "pub mod user;\npub mod helper;\n",
		"src/models/helper.rs":       "pub fn right() {}\n",
		"src/models/user.rs":         "use super::helper;\n\npub fn f() { helper::right() }\n",
		"src/models/detail.rs":       "pub fn d() {}\n",
		"src/models/user2.rs":        "use self::detail::d;\n",
		"src/models/user2/mod.rs":    "pub mod detail;\n",
		"src/models/user2/detail.rs": "pub fn d() {}\n",
	})

	// src/models/user.rs is module models::user; its parent module owns
	// src/models, so super::helper is src/models/helper.rs — never src/helper.rs.
	assertEdges(t, dg, "src/models/user.rs", "src/models/helper.rs")
	assertNoEdge(t, dg, "src/models/user.rs", "src/helper.rs")

	// self:: from a non-mod.rs file addresses that file's own submodule dir.
	assertEdges(t, dg, "src/models/user2.rs", "src/models/user2/detail.rs")
	assertNoEdge(t, dg, "src/models/user2.rs", "src/models/detail.rs")
}

func TestRustSuperFromModFileStillWorks(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/config.rs", Lang: "rust", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "src/handlers/mod.rs", Lang: "rust", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "src/handlers/config.rs", Lang: "rust", Class: scan.ClassSource},
	)
	got := resolveRustSpecs([]string{"use:super::config"}, "src/handlers/mod.rs", idx, nil)
	if len(got) != 1 || got[0] != "src/config.rs" {
		t.Fatalf("got %v, want [src/config.rs]", got)
	}
}

// ─── 5. Elixir comments and strings ───────────────────────────────────────────

func TestElixirIgnoresCommentsAndStrings(t *testing.T) {
	dg := buildGraph(t, map[string]string{
		"mix.exs":             "defmodule MyApp.MixProject do\n  use Mix.Project\nend\n",
		"lib/myapp/mailer.ex": "defmodule MyApp.Mailer do\n  def send(_), do: :ok\nend\n",
		"lib/myapp/sms.ex":    "defmodule MyApp.Sms do\n  def send(_), do: :ok\nend\n",
		"lib/myapp/audit.ex":  "defmodule MyApp.Audit do\n  def log(_), do: :ok\nend\n",
		"lib/myapp/notifier.ex": "defmodule MyApp.Notifier do\n" +
			"  @moduledoc \"\"\"\n  Delegates to MyApp.Mailer for email.\n  \"\"\"\n\n" +
			"  # Historically used MyApp.Sms\n\n" +
			"  def notify(_) do\n" +
			"    label = \"MyApp.Sms\"\n" +
			"    MyApp.Audit.log(label)\n" +
			"  end\n" +
			"end\n",
	})

	assertEdges(t, dg, "lib/myapp/notifier.ex", "lib/myapp/audit.ex")
	assertNoEdge(t, dg, "lib/myapp/notifier.ex", "lib/myapp/mailer.ex")
	assertNoEdge(t, dg, "lib/myapp/notifier.ex", "lib/myapp/sms.ex")
}

// The Elixir module map used to be built by joining paths against the process
// working directory, so the same repo produced a different graph depending on
// where recon was invoked from.
func TestElixirGraphIsIndependentOfWorkingDirectory(t *testing.T) {
	files := map[string]string{
		"lib/myapp/store.ex":  "defmodule MyApp.Store do\n  def get(_), do: nil\nend\n",
		"lib/myapp/reader.ex": "defmodule MyApp.Reader do\n  def read(k), do: MyApp.Store.get(k)\nend\n",
	}
	root := writeFixture(t, files)
	res, err := scan.Walk(root)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	idx := NewFileIndex(res.Files)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	first := NewDepGraph(root, idx).ImportsOf("lib/myapp/reader.ex")

	elsewhere := t.TempDir()
	if err := os.Chdir(elsewhere); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	second := NewDepGraph(root, idx).ImportsOf("lib/myapp/reader.ex")

	if len(first) != 1 || first[0] != "lib/myapp/store.ex" {
		t.Fatalf("imports = %v, want [lib/myapp/store.ex]", first)
	}
	if strings.Join(sortedStrings(first), ",") != strings.Join(sortedStrings(second), ",") {
		t.Fatalf("graph depends on the working directory: %v vs %v", first, second)
	}
}

// parseSwiftPackageTargets had the same working-directory bug as the Elixir
// module map: it read "Package.swift" relative to the process CWD.
func TestSwiftPackageManifestIsReadFromScanRoot(t *testing.T) {
	root := writeFixture(t, map[string]string{
		"Package.swift": "// swift-tools-version:5.9\nimport PackageDescription\n\n" +
			"let package = Package(\n  targets: [\n" +
			"    .target(name: \"Core\", path: \"Lib/Core\"),\n" +
			"    .target(name: \"App\"),\n  ]\n)\n",
		"Lib/Core/Engine.swift":  "public struct Engine {}\n",
		"Sources/App/main.swift": "import Core\n\nlet e = Engine()\n",
	})

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	targets := parseSwiftPackageTargets(root)
	if got := targets["Core"]; len(got) != 1 || got[0] != "Lib/Core" {
		t.Fatalf("Core target = %v, want [Lib/Core] — the manifest was not read from the scan root", got)
	}
	if got := targets["App"]; len(got) != 1 || got[0] != "Sources/App" {
		t.Errorf("App target = %v, want [Sources/App]", got)
	}
}

func TestStripElixirNonCode(t *testing.T) {
	in := []string{
		`defmodule A do`,
		`  @moduledoc """`,
		`  See B.C for details.`,
		`  """`,
		`  # also D.E`,
		`  def f, do: G.H.call("I.J")`,
		`end`,
	}
	out := strings.Join(stripElixirNonCode(in), "\n")
	for _, gone := range []string{"B.C", "D.E", "I.J"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q survived stripping:\n%s", gone, out)
		}
	}
	if !strings.Contains(out, "G.H") {
		t.Errorf("real code reference G.H was stripped:\n%s", out)
	}
}

// ─── 6. JS/TS modern resolution ───────────────────────────────────────────────

func TestTypeScriptNodeNextAndPathAliases(t *testing.T) {
	dg := buildGraph(t, map[string]string{
		"package.json": "{ \"name\": \"modern\", \"type\": \"module\" }\n",
		"tsconfig.json": "{\n  // NodeNext requires .js specifiers for .ts sources\n" +
			"  \"compilerOptions\": {\n    \"module\": \"NodeNext\",\n    \"moduleResolution\": \"NodeNext\",\n" +
			"    \"baseUrl\": \".\",\n    \"paths\": { \"@app/*\": [\"src/*\"] },\n  }\n}\n",
		"src/util/fmt.ts":   "export function fmt(x: string) { return x; }\n",
		"src/util/index.ts": "export * from \"./fmt.js\";\n",
		"src/deep.ts":       "export const deep = 1;\n",
		"src/main.ts":       "import { fmt } from \"./util/fmt.js\";\nimport { deep } from \"@app/deep.js\";\nimport React from \"react\";\n",
	})

	assertEdges(t, dg, "src/main.ts", "src/util/fmt.ts", "src/deep.ts")
	assertEdges(t, dg, "src/util/index.ts", "src/util/fmt.ts")

	st, _ := dg.ImportStatsOf("src/main.ts")
	if st.Resolved != 2 || st.External != 1 || st.Unresolved != 0 {
		t.Errorf("stats = %+v, want 2 resolved / 1 external (react) / 0 unresolved", st)
	}
}

func TestJSExtensionRewriteDoesNotInventEdges(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/main.ts", Lang: "typescript", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "src/other.ts", Lang: "typescript", Class: scan.ClassSource},
	)
	tally := &importTally{lang: "typescript"}
	got := resolveJSSpecs([]string{"./missing.js"}, "src/main.ts", mkCtx(idx), tally)
	if len(got) != 0 {
		t.Fatalf("expected no edge for a specifier with no backing file, got %v", got)
	}
	if st := tally.stats(); st.Unresolved != 1 {
		t.Errorf("stats = %+v, want 1 unresolved", st)
	}
}

func TestJSAliasMissIsUnresolvedNotExternal(t *testing.T) {
	root, idx, rc := newRepoFixture(t, map[string]string{
		"tsconfig.json": "{ \"compilerOptions\": { \"paths\": { \"@app/*\": [\"src/*\"] } } }\n",
		"src/main.ts":   "import x from \"@app/nope.js\";\nimport y from \"lodash\";\n",
	})
	_ = root
	_ = idx

	tally := &importTally{lang: "typescript"}
	resolveJSSpecs([]string{"@app/nope.js", "lodash"}, "src/main.ts", rc, tally)
	st := tally.stats()
	if st.Unresolved != 1 || st.External != 1 {
		t.Errorf("stats = %+v, want 1 unresolved (aliased) / 1 external (lodash)", st)
	}
}

func TestStripJSONComments(t *testing.T) {
	in := []byte("{\n  // line\n  /* block */\n  \"a\": \"http://not-a-comment\",\n  \"b\": [1, 2,],\n}\n")
	var out struct {
		A string `json:"a"`
		B []int  `json:"b"`
	}
	if err := json.Unmarshal(stripJSONComments(in), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.A != "http://not-a-comment" {
		t.Errorf("a = %q, want the URL intact", out.A)
	}
	if len(out.B) != 2 {
		t.Errorf("b = %v, want [1 2]", out.B)
	}
}

// ─── 7. Shell expansions ──────────────────────────────────────────────────────

func TestShellExpansionDoesNotResolveToWrongFile(t *testing.T) {
	dg := buildGraph(t, map[string]string{
		"lib/util.sh":        "real() { echo real; }\n",
		"scripts/util.sh":    "wrong() { echo wrong; }\n",
		"scripts/run.sh":     "#!/usr/bin/env bash\nLIB=\"$(dirname \"$0\")/../lib\"\nsource \"$LIB/util.sh\"\nsource ./helpers.sh\n",
		"scripts/helpers.sh": "helper() { :; }\n",
	})

	assertEdges(t, dg, "scripts/run.sh", "scripts/helpers.sh")
	assertNoEdge(t, dg, "scripts/run.sh", "scripts/util.sh")
	assertNoEdge(t, dg, "scripts/run.sh", "lib/util.sh")

	st, _ := dg.ImportStatsOf("scripts/run.sh")
	if st.Unresolved != 1 {
		t.Errorf("stats = %+v, want the runtime-computed source recorded as unresolved", st)
	}
}

// ─── 8. Julia joinpath includes ───────────────────────────────────────────────

func TestJuliaJoinpathInclude(t *testing.T) {
	dg := buildGraph(t, map[string]string{
		"Project.toml":       "name = \"Demo\"\n",
		"src/lit.jl":         "g() = 2\n",
		"src/dyn.jl":         "f() = 1\n",
		"src/nested/deep.jl": "h() = 3\n",
		"src/Demo.jl": "module Demo\ninclude(\"lit.jl\")\ninclude(joinpath(@__DIR__, \"dyn.jl\"))\n" +
			"include(joinpath(@__DIR__, \"nested\", \"deep.jl\"))\nfor f in files\n  include(f)\nend\nend\n",
	})

	assertEdges(t, dg, "src/Demo.jl", "src/lit.jl", "src/dyn.jl", "src/nested/deep.jl")

	st, _ := dg.ImportStatsOf("src/Demo.jl")
	if st.Unresolved != 1 {
		t.Errorf("stats = %+v, want the include(f) variable form recorded as unresolved", st)
	}
}

func TestParseJuliaInclude(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{`("lit.jl")`, "lit.jl", true},
		{`(joinpath(@__DIR__, "dyn.jl"))`, "dyn.jl", true},
		{`(joinpath(@__DIR__, "a", "b.jl"))`, "a/b.jl", true},
		{`(joinpath(dirname(@__FILE__), "c.jl"))`, "c.jl", true},
		{`(f)`, "", false},
		{`(joinpath(basedir, "d.jl"))`, "", false},
		{`("$(name).jl")`, "", false},
	}
	for _, c := range cases {
		got, ok := parseJuliaInclude(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseJuliaInclude(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// ─── 9. Java/Kotlin wildcards and multi-type files ────────────────────────────

func TestJavaWildcardImport(t *testing.T) {
	dg := buildGraph(t, map[string]string{
		"src/main/java/com/example/models/User.java":  "package com.example.models;\npublic class User {}\n",
		"src/main/java/com/example/models/Order.java": "package com.example.models;\npublic class Order {}\n",
		"src/main/java/com/example/other/Thing.java":  "package com.example.other;\npublic class Thing {}\n",
		"src/main/java/com/example/app/Main.java":     "package com.example.app;\nimport com.example.models.*;\npublic class Main { User u; }\n",
	})

	assertEdges(t, dg, "src/main/java/com/example/app/Main.java",
		"src/main/java/com/example/models/User.java",
		"src/main/java/com/example/models/Order.java",
	)
	assertNoEdge(t, dg, "src/main/java/com/example/app/Main.java", "src/main/java/com/example/other/Thing.java")
}

func TestKotlinMultipleTypesPerFile(t *testing.T) {
	dg := buildGraph(t, map[string]string{
		"src/main/kotlin/com/ex/data/Repo.kt":   "package com.ex.data\n\nclass Repo\nclass Dao\nenum class Mode { A, B }\n",
		"src/main/kotlin/com/ex/data/Other.kt":  "package com.ex.data\n\nclass Other\n",
		"src/main/kotlin/com/ex/util/Text.kt":   "package com.ex.util\n\nfun slugify(s: String) = s\n",
		"src/main/kotlin/com/ex/app/Service.kt": "package com.ex.app\n\nimport com.ex.data.Dao\nimport com.ex.data.Mode\nimport com.ex.util.slugify\n\nclass Service(val d: Dao)\n",
	})

	// Dao and Mode both live in Repo.kt; slugify is a top-level function.
	assertEdges(t, dg, "src/main/kotlin/com/ex/app/Service.kt",
		"src/main/kotlin/com/ex/data/Repo.kt",
		"src/main/kotlin/com/ex/util/Text.kt",
	)
	assertNoEdge(t, dg, "src/main/kotlin/com/ex/app/Service.kt", "src/main/kotlin/com/ex/data/Other.kt")
}

func TestJVMUnknownImportIsUnresolvedNotAnEdge(t *testing.T) {
	dg := buildGraph(t, map[string]string{
		"src/main/kotlin/com/ex/app/Service.kt": "package com.ex.app\n\nimport com.ex.data.Missing\nimport kotlin.math.abs\n",
	})
	if got := dg.ImportsOf("src/main/kotlin/com/ex/app/Service.kt"); len(got) != 0 {
		t.Fatalf("expected no edges, got %v", got)
	}
	st, _ := dg.ImportStatsOf("src/main/kotlin/com/ex/app/Service.kt")
	if st.Unresolved != 1 || st.External != 1 {
		t.Errorf("stats = %+v, want 1 unresolved / 1 external (kotlin.math)", st)
	}
}

// ─── 10. Telemetry ────────────────────────────────────────────────────────────

func TestImportCoverageDistinguishesUnsupportedFromEmpty(t *testing.T) {
	dg := buildGraph(t, map[string]string{
		"go.mod":     "module example.com/app\n\ngo 1.22\n",
		"pkg/a/a.go": "package a\n\nfunc A() {}\n",
		"main.go":    "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/app/pkg/a\"\n\t\"example.com/app/pkg/missing\"\n)\n\nfunc main() { fmt.Println(a.A) }\n",
	})

	st, ok := dg.ImportStatsOf("main.go")
	if !ok {
		t.Fatal("no stats for main.go")
	}
	if st.Lang != "go" {
		t.Errorf("lang = %q, want go", st.Lang)
	}
	if st.Extracted != 3 || st.Resolved != 1 || st.External != 1 || st.Unresolved != 1 {
		t.Fatalf("stats = %+v, want 3 extracted / 1 resolved / 1 external (fmt) / 1 unresolved", st)
	}
	if len(st.UnresolvedSpecs) != 1 || st.UnresolvedSpecs[0] != "example.com/app/pkg/missing" {
		t.Errorf("unresolved specs = %v", st.UnresolvedSpecs)
	}

	cov := dg.ImportCoverage()
	if len(cov) != 1 || cov[0].Lang != "go" {
		t.Fatalf("coverage = %+v, want one go entry", cov)
	}
	if cov[0].Resolved != 1 || cov[0].Unresolved != 1 {
		t.Errorf("coverage = %+v", cov[0])
	}
}

func TestImportStatsRecordedForFilesWithNoEdges(t *testing.T) {
	// The whole point: a file whose imports we could not resolve must still be
	// visible in the telemetry, otherwise fan_in 0 is indistinguishable from
	// "recon does not understand this import style".
	dg := buildGraph(t, map[string]string{
		"go.mod":  "module example.com/app\n\ngo 1.22\n",
		"main.go": "package main\n\nimport \"example.com/app/pkg/gone\"\n",
	})
	st, ok := dg.ImportStatsOf("main.go")
	if !ok {
		t.Fatal("a file with zero resolved imports recorded no telemetry")
	}
	if st.Unresolved != 1 {
		t.Errorf("stats = %+v, want 1 unresolved", st)
	}
}

func TestImportTallyIsNilSafe(t *testing.T) {
	var t0 *importTally
	t0.extract(3)
	t0.hit()
	t0.skip()
	t0.miss("x")
	if got := t0.stats(); got.Extracted != 0 {
		t.Errorf("nil tally produced %+v", got)
	}
}
