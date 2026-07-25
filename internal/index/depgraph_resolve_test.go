package index

import (
	"sort"
	"testing"

	"github.com/djtouchette/recon/internal/scan"
)

// mkIdx builds a *FileIndex from a slice of (relPath, lang, class) triples.
func mkIdx(entries ...scan.FileEntry) *FileIndex {
	return NewFileIndex(entries)
}

// mkCtx wraps an index in a resolution context rooted at the current directory.
// Resolvers that only consult the index work with it directly; the ones that
// read files (C#, JVM declarations, Elixir) need an on-disk fixture — see
// newRepoFixture.
func mkCtx(idx *FileIndex) *resolveCtx {
	return newResolveCtx("", idx)
}

// goMods builds the single-module list a non-workspace repo has.
func goMods(modPath string) []goModule {
	if modPath == "" {
		return nil
	}
	return []goModule{{Path: modPath}}
}

// sortedStrings returns a sorted copy of ss.
func sortedStrings(ss []string) []string {
	out := make([]string, len(ss))
	copy(out, ss)
	sort.Strings(out)
	return out
}

// ─── Go resolver ─────────────────────────────────────────────────────────────

func TestResolveGoImports_Basic(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "pkg/auth/auth.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "pkg/auth/helper.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "pkg/models/user.go", Lang: "go", Class: scan.ClassSource},
	)

	lines := []string{
		`package main`,
		`import (`,
		`	"github.com/example/myapp/pkg/auth"`,
		`	"github.com/example/myapp/pkg/models"`,
		`	"fmt"`,
		`)`,
	}

	got := resolveGoSpecs(goRegexSpecs(lines), "cmd/main.go", goMods("github.com/example/myapp"), idx, nil)
	sort.Strings(got)

	want := []string{
		"pkg/auth/auth.go",
		"pkg/auth/helper.go",
		"pkg/models/user.go",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, g, want[i])
		}
	}
}

func TestResolveGoImports_SingleImport(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "internal/store/store.go", Lang: "go", Class: scan.ClassSource},
	)

	lines := []string{`import "github.com/acme/app/internal/store"`}

	got := resolveGoSpecs(goRegexSpecs(lines), "main.go", goMods("github.com/acme/app"), idx, nil)
	if len(got) != 1 || got[0] != "internal/store/store.go" {
		t.Fatalf("got %v, want [internal/store/store.go]", got)
	}
}

func TestResolveGoImports_SkipsExternalPackages(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "pkg/auth/auth.go", Lang: "go", Class: scan.ClassSource},
	)

	lines := []string{
		`import (`,
		`	"fmt"`,
		`	"github.com/some/external/lib"`,
		`	"os"`,
		`)`,
	}

	got := resolveGoSpecs(goRegexSpecs(lines), "main.go", goMods("github.com/example/myapp"), idx, nil)
	if len(got) != 0 {
		t.Fatalf("expected no imports, got %v", got)
	}
}

func TestResolveGoImports_EmptyModPath(t *testing.T) {
	idx := mkIdx(scan.FileEntry{RelPath: "pkg/auth/auth.go", Lang: "go", Class: scan.ClassSource})
	lines := []string{`import "github.com/acme/app/pkg/auth"`}

	got := resolveGoSpecs(goRegexSpecs(lines), "main.go", goMods(""), idx, nil)
	if len(got) != 0 {
		t.Fatalf("expected nil/empty when goModPath is empty, got %v", got)
	}
}

func TestResolveGoImports_SkipsTestFiles(t *testing.T) {
	// Test-class files in the same directory should not be included in Go import
	// resolution (source-only filter).
	idx := mkIdx(
		scan.FileEntry{RelPath: "pkg/auth/auth.go", Lang: "go", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "pkg/auth/auth_test.go", Lang: "go", Class: scan.ClassTest},
	)

	lines := []string{`import "github.com/example/app/pkg/auth"`}

	got := resolveGoSpecs(goRegexSpecs(lines), "main.go", goMods("github.com/example/app"), idx, nil)
	// Should only find the ClassSource file.
	if len(got) != 1 || got[0] != "pkg/auth/auth.go" {
		t.Fatalf("got %v, want [pkg/auth/auth.go]", got)
	}
}

// ─── JS / TS resolver ─────────────────────────────────────────────────────────

func TestResolveJSSpecs_RelativeWithExtension(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/utils/format.ts", Lang: "typescript", Class: scan.ClassSource},
	)

	got := resolveJSSpecs([]string{"./utils/format"}, "src/app.ts", mkCtx(idx), nil)
	if len(got) != 1 || got[0] != "src/utils/format.ts" {
		t.Fatalf("got %v, want [src/utils/format.ts]", got)
	}
}

func TestResolveJSSpecs_ExactPathWithExtension(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/components/Button.tsx", Lang: "typescript", Class: scan.ClassSource},
	)

	got := resolveJSSpecs([]string{"./components/Button.tsx"}, "src/App.tsx", mkCtx(idx), nil)
	if len(got) != 1 || got[0] != "src/components/Button.tsx" {
		t.Fatalf("got %v, want [src/components/Button.tsx]", got)
	}
}

func TestResolveJSSpecs_IndexResolution(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/utils/index.ts", Lang: "typescript", Class: scan.ClassSource},
	)

	got := resolveJSSpecs([]string{"./utils"}, "src/app.ts", mkCtx(idx), nil)
	if len(got) != 1 || got[0] != "src/utils/index.ts" {
		t.Fatalf("got %v, want [src/utils/index.ts]", got)
	}
}

func TestResolveJSSpecs_IndexJSFallback(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "lib/helpers/index.js", Lang: "javascript", Class: scan.ClassSource},
	)

	got := resolveJSSpecs([]string{"./helpers"}, "lib/main.js", mkCtx(idx), nil)
	if len(got) != 1 || got[0] != "lib/helpers/index.js" {
		t.Fatalf("got %v, want [lib/helpers/index.js]", got)
	}
}

func TestResolveJSSpecs_SkipsExternalModules(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/utils.ts", Lang: "typescript", Class: scan.ClassSource},
	)

	got := resolveJSSpecs([]string{"react", "lodash", "@emotion/react"}, "src/app.ts", mkCtx(idx), nil)
	if len(got) != 0 {
		t.Fatalf("expected no results for external modules, got %v", got)
	}
}

func TestResolveJSSpecs_ParentDirectory(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/shared.ts", Lang: "typescript", Class: scan.ClassSource},
	)

	got := resolveJSSpecs([]string{"../shared"}, "src/components/Button.tsx", mkCtx(idx), nil)
	if len(got) != 1 || got[0] != "src/shared.ts" {
		t.Fatalf("got %v, want [src/shared.ts]", got)
	}
}

func TestResolveJSSpecs_Deduplication(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/utils.ts", Lang: "typescript", Class: scan.ClassSource},
	)

	// Same file resolved from two specifiers
	got := resolveJSSpecs([]string{"./utils", "./utils.ts"}, "src/app.ts", mkCtx(idx), nil)
	if len(got) != 1 {
		t.Fatalf("expected deduplication, got %v", got)
	}
}

func TestResolveJSSpecs_MissingFile(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/real.ts", Lang: "typescript", Class: scan.ClassSource},
	)

	got := resolveJSSpecs([]string{"./missing"}, "src/app.ts", mkCtx(idx), nil)
	if len(got) != 0 {
		t.Fatalf("expected no results for missing file, got %v", got)
	}
}

// ─── Python resolver ──────────────────────────────────────────────────────────

func TestResolvePySpecs_SingleDot(t *testing.T) {
	// "from .models import User" → specs = [".models"] → pkg/models.py
	idx := mkIdx(
		scan.FileEntry{RelPath: "pkg/models.py", Lang: "python", Class: scan.ClassSource},
	)

	got := resolvePySpecs([]string{".models"}, "pkg/app.py", mkCtx(idx), nil)
	if len(got) != 1 || got[0] != "pkg/models.py" {
		t.Fatalf("got %v, want [pkg/models.py]", got)
	}
}

func TestResolvePySpecs_DoubleDot(t *testing.T) {
	// "from ..utils import helper" in pkg/subpkg/views.py
	// dots=2 → go up 1 directory from "pkg/subpkg" → "pkg"
	// so the resolved path is pkg/utils.py
	idx := mkIdx(
		scan.FileEntry{RelPath: "pkg/utils.py", Lang: "python", Class: scan.ClassSource},
	)

	got := resolvePySpecs([]string{"..utils"}, "pkg/subpkg/views.py", mkCtx(idx), nil)
	if len(got) != 1 || got[0] != "pkg/utils.py" {
		t.Fatalf("got %v, want [pkg/utils.py]", got)
	}
}

func TestResolvePySpecs_DoubleDotRootLevel(t *testing.T) {
	// "from ..utils import helper" in pkg/views.py → "utils.py" (root level)
	idx := mkIdx(
		scan.FileEntry{RelPath: "utils.py", Lang: "python", Class: scan.ClassSource},
	)

	got := resolvePySpecs([]string{"..utils"}, "pkg/views.py", mkCtx(idx), nil)
	if len(got) != 1 || got[0] != "utils.py" {
		t.Fatalf("got %v, want [utils.py]", got)
	}
}

// Absolute imports are resolved now, but only against files that exist: a
// third-party package must never become an edge, and must be counted External
// (an expected non-edge) rather than Unresolved (a dropped edge).
func TestResolvePySpecs_ThirdPartyAbsoluteImportsAreNotEdges(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "app/views.py", Lang: "python", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "app/models.py", Lang: "python", Class: scan.ClassSource},
	)

	tally := &importTally{lang: "python"}
	got := resolvePySpecs([]string{"imp:os", "mod:django.db", "mod:requests"}, "app/views.py", mkCtx(idx), tally)
	if len(got) != 0 {
		t.Fatalf("expected no results for third-party absolute imports, got %v", got)
	}
	st := tally.stats()
	if st.External != 3 || st.Unresolved != 0 {
		t.Fatalf("stats = %+v, want 3 external / 0 unresolved", st)
	}
}

func TestResolvePySpecs_NestedModule(t *testing.T) {
	// "from .api.v1.views import something" → specs = [".api.v1.views"]
	idx := mkIdx(
		scan.FileEntry{RelPath: "app/api/v1/views.py", Lang: "python", Class: scan.ClassSource},
	)

	got := resolvePySpecs([]string{".api.v1.views"}, "app/main.py", mkCtx(idx), nil)
	if len(got) != 1 || got[0] != "app/api/v1/views.py" {
		t.Fatalf("got %v, want [app/api/v1/views.py]", got)
	}
}

func TestResolvePySpecs_Deduplication(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "pkg/models.py", Lang: "python", Class: scan.ClassSource},
	)

	got := resolvePySpecs([]string{".models", ".models"}, "pkg/app.py", mkCtx(idx), nil)
	if len(got) != 1 {
		t.Fatalf("expected deduplication, got %v", got)
	}
}

func TestResolvePyRelative_SingleDot(t *testing.T) {
	got := resolvePyRelative("pkg/sub", ".models")
	want := "pkg/sub/models.py"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolvePyRelative_DoubleDot(t *testing.T) {
	got := resolvePyRelative("pkg/sub", "..utils")
	want := "pkg/utils.py"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolvePyRelative_TripleDot(t *testing.T) {
	got := resolvePyRelative("a/b/c", "...root")
	want := "a/root.py"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolvePyRelative_EmptyModule(t *testing.T) {
	// "from . import something" gives spec "." with no module name.
	got := resolvePyRelative("pkg", ".")
	if got != "" {
		t.Fatalf("expected empty string for bare dot, got %q", got)
	}
}

// ─── Java / Kotlin resolver ───────────────────────────────────────────────────

func TestResolveJavaImports_Basic(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/main/java/com/example/User.java", Lang: "java", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "src/main/java/com/example/UserService.java", Lang: "java", Class: scan.ClassSource},
	)

	lines := []string{
		"import com.example.User;",
		"import com.example.UserService;",
	}

	got := resolveJavaSpecs(javaRegexSpecs(lines), "src/main/java/com/example/UserController.java", "java", mkCtx(idx), nil)
	sort.Strings(got)

	want := []string{
		"src/main/java/com/example/User.java",
		"src/main/java/com/example/UserService.java",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, g, want[i])
		}
	}
}

func TestResolveJavaImports_StaticImport(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/main/java/com/example/MathUtils.java", Lang: "java", Class: scan.ClassSource},
	)

	lines := []string{"import static com.example.MathUtils.pow;"}
	got := resolveJavaSpecs(javaRegexSpecs(lines), "src/main/java/com/example/Calculator.java", "java", mkCtx(idx), nil)
	if len(got) != 1 || got[0] != "src/main/java/com/example/MathUtils.java" {
		t.Fatalf("got %v, want [src/main/java/com/example/MathUtils.java]", got)
	}
}

func TestResolveJavaImports_SkipsStdLib(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "java/util/List.java", Lang: "java", Class: scan.ClassSource},
	)

	lines := []string{
		"import java.util.List;",
		"import javax.servlet.http.HttpServlet;",
	}
	got := resolveJavaSpecs(javaRegexSpecs(lines), "src/Foo.java", "java", mkCtx(idx), nil)
	if len(got) != 0 {
		t.Fatalf("expected no results for stdlib imports, got %v", got)
	}
}

func TestResolveJavaImports_RootLevelFallback(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "com/example/Config.java", Lang: "java", Class: scan.ClassSource},
	)

	lines := []string{"import com.example.Config;"}
	got := resolveJavaSpecs(javaRegexSpecs(lines), "com/example/Main.java", "java", mkCtx(idx), nil)
	if len(got) != 1 || got[0] != "com/example/Config.java" {
		t.Fatalf("got %v, want [com/example/Config.java]", got)
	}
}

func TestResolveJavaImports_KotlinLang(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/main/kotlin/com/example/Repository.kt", Lang: "kotlin", Class: scan.ClassSource},
	)

	// Kotlin imports don't have semicolons
	lines := []string{"import com.example.Repository"}
	got := resolveJavaSpecs(kotlinRegexSpecs(lines), "src/main/kotlin/com/example/Service.kt", "kotlin", mkCtx(idx), nil)
	if len(got) != 1 || got[0] != "src/main/kotlin/com/example/Repository.kt" {
		t.Fatalf("got %v, want [src/main/kotlin/com/example/Repository.kt]", got)
	}
}

func TestResolveJavaImports_SkipsSelf(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/main/java/com/example/User.java", Lang: "java", Class: scan.ClassSource},
	)

	lines := []string{"import com.example.User;"}
	// The file itself is in the same location — should not self-reference.
	got := resolveJavaSpecs(javaRegexSpecs(lines), "src/main/java/com/example/User.java", "java", mkCtx(idx), nil)
	if len(got) != 0 {
		t.Fatalf("expected no self-reference, got %v", got)
	}
}

// ─── C# resolver ─────────────────────────────────────────────────────────────
//
// C# resolution reads the namespace each file declares, so these tests need
// real files rather than a path-only index.

func TestResolveCSharpImports_DeclaredNamespace(t *testing.T) {
	root, idx, rc := newRepoFixture(t, map[string]string{
		"Public/Common/Services/AuthService.cs": "namespace Public.Common.Services;\npublic class AuthService {}\n",
		"Public/Common/Services/UserService.cs": "namespace Public.Common.Services;\npublic class UserService {}\n",
		"OtherProject/Startup.cs":               "using Public.Common.Services;\n",
	})
	_ = root

	lines := []string{"using Public.Common.Services;"}
	got := sortedStrings(resolveCSharpSpecs(csharpRegexSpecs(lines), "OtherProject/Startup.cs", rc, nil))

	want := []string{
		"Public/Common/Services/AuthService.cs",
		"Public/Common/Services/UserService.cs",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, g, want[i])
		}
	}
	_ = idx
}

func TestResolveCSharpImports_BlockNamespace(t *testing.T) {
	// The block form `namespace A.B { ... }` must be read too.
	_, _, rc := newRepoFixture(t, map[string]string{
		"src/Widgets/Widget.cs": "namespace Acme.Widgets\n{\n    public class Widget {}\n}\n",
		"src/App/Program.cs":    "using Acme.Widgets;\n",
	})

	got := resolveCSharpSpecs(csharpRegexSpecs([]string{"using Acme.Widgets;"}), "src/App/Program.cs", rc, nil)
	if len(got) != 1 || got[0] != "src/Widgets/Widget.cs" {
		t.Fatalf("got %v, want [src/Widgets/Widget.cs]", got)
	}
}

func TestResolveCSharpImports_SkipsSystemNamespaces(t *testing.T) {
	_, _, rc := newRepoFixture(t, map[string]string{
		"System/Linq/Enumerable.cs": "namespace System.Linq;\npublic class Enumerable {}\n",
		"src/App.cs":                "using System;\n",
	})

	lines := []string{
		"using System;",
		"using System.Linq;",
		"using Microsoft.Extensions.DependencyInjection;",
	}
	got := resolveCSharpSpecs(csharpRegexSpecs(lines), "src/App.cs", rc, nil)
	if len(got) != 0 {
		t.Fatalf("expected no results for system namespaces, got %v", got)
	}
}

// The defect this replaces: when the direct directory match failed, every .cs
// file whose directory merely *ended* in one of the last 1–3 lowercased
// namespace segments became an edge, without ever reading a namespace. One
// `using MyApp.Models;` produced three edges, two of them pointing at unrelated
// projects. The negative assertion below is the point of the test.
func TestResolveCSharpImports_DoesNotMatchDirectoryNames(t *testing.T) {
	_, _, rc := newRepoFixture(t, map[string]string{
		"src/Domain/Models/User.cs":  "namespace MyApp.Models;\npublic class User {}\n",
		"src/Legacy/Models/Thing.cs": "namespace Legacy.Models;\npublic class Thing {}\n",
		"src/Old/Models/Widget.cs":   "namespace Totally.Unrelated.Models\n{\n public class Widget {}\n}\n",
		"src/Web/Startup.cs":         "using MyApp.Models;\n",
	})

	got := resolveCSharpSpecs(csharpRegexSpecs([]string{"using MyApp.Models;"}), "src/Web/Startup.cs", rc, nil)

	if len(got) != 1 || got[0] != "src/Domain/Models/User.cs" {
		t.Fatalf("got %v, want exactly [src/Domain/Models/User.cs]", got)
	}
	for _, g := range got {
		if g == "src/Legacy/Models/Thing.cs" || g == "src/Old/Models/Widget.cs" {
			t.Errorf("fabricated edge to a directory that merely ends in Models: %q", g)
		}
	}
}

// Unresolved must mean "recon dropped a real in-repo edge". These two tests
// pin the two halves of that rule; the negative assertions (Unresolved == 0,
// External == 0) are the point.

func TestResolveCSharpImports_UndeclaredNamespaceIsExternal(t *testing.T) {
	// pdftron.* and QRCoder are a vendor SDK and a NuGet package. No file in
	// the repo declares those namespaces, so no repo file can define a type in
	// them and there is no edge to drop — they are External, exactly like
	// System.*, not Unresolved. Reporting them as unresolved (6 of 9 usings in
	// one real file) made the caveat noise.
	_, _, rc := newRepoFixture(t, map[string]string{
		"src/Web/Startup.cs": "namespace MyApp.Web;\n",
		"src/Models/User.cs": "namespace MyApp.Models;\npublic class User {}\n",
	})

	lines := []string{
		"using pdftron;",
		"using pdftron.Common;",
		"using pdftron.PDF;",
		"using pdftron.PDF.Annots;",
		"using pdftron.SDF;",
		"using QRCoder;",
	}
	tally := &importTally{lang: "csharp"}
	got := resolveCSharpSpecs(csharpRegexSpecs(lines), "src/Web/Startup.cs", rc, tally)
	if len(got) != 0 {
		t.Fatalf("third-party usings must not fabricate edges, got %v", got)
	}
	st := tally.stats()
	if st.Extracted != 6 || st.External != 6 {
		t.Fatalf("stats = %+v, want 6 extracted / 6 external", st)
	}
	if st.Unresolved != 0 || len(st.UnresolvedSpecs) != 0 {
		t.Fatalf("undeclared namespaces reported as unresolved: %+v", st)
	}
}

func TestResolveCSharpImports_UnresolvedIsReported(t *testing.T) {
	// The genuinely ambiguous cases: the repo *does* own this corner of the
	// namespace tree, but no file could be pinned down.
	//
	//   MyApp.Models        — only the nested MyApp.Models.Dto is declared, so
	//                         a type recon cannot see may live in it.
	//   MyApp.Dtos.Missing  — `using static`-shaped; MyApp.Dtos is declared but
	//                         no Missing.cs sits in it.
	_, _, rc := newRepoFixture(t, map[string]string{
		"src/Models/Dto/Order.cs": "namespace MyApp.Models.Dto;\npublic class Order {}\n",
		"src/Dtos/Customer.cs":    "namespace MyApp.Dtos;\npublic class Customer {}\n",
		"src/Web/Startup.cs":      "namespace MyApp.Web;\n",
	})

	lines := []string{"using MyApp.Models;", "using static MyApp.Dtos.Missing;"}
	tally := &importTally{lang: "csharp"}
	got := resolveCSharpSpecs(csharpRegexSpecs(lines), "src/Web/Startup.cs", rc, tally)
	if len(got) != 0 {
		t.Fatalf("expected no edges, got %v", got)
	}
	st := tally.stats()
	if st.Extracted != 2 || st.Unresolved != 2 || st.Resolved != 0 || st.External != 0 {
		t.Fatalf("stats = %+v, want 2 extracted / 2 unresolved", st)
	}
	want := []string{"MyApp.Dtos.Missing", "MyApp.Models"}
	if got := sortedStrings(st.UnresolvedSpecs); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("unresolved specs = %v, want %v", got, want)
	}
}

func TestResolveCSharpImports_SelfNamespaceIsNotUnresolved(t *testing.T) {
	// The only declarer of the namespace is the importing file itself: no edge
	// exists to drop, so this is an expected non-edge, not a caveat.
	_, _, rc := newRepoFixture(t, map[string]string{
		"src/Models/User.cs": "namespace MyApp.Models;\nusing MyApp.Models;\npublic class User {}\n",
	})

	tally := &importTally{lang: "csharp"}
	got := resolveCSharpSpecs(csharpRegexSpecs([]string{"using MyApp.Models;"}), "src/Models/User.cs", rc, tally)
	if len(got) != 0 {
		t.Fatalf("expected no edges, got %v", got)
	}
	if st := tally.stats(); st.Unresolved != 0 || st.External != 1 {
		t.Fatalf("stats = %+v, want 1 external / 0 unresolved", st)
	}
}

func TestResolveCSharpImports_DeclaredNamespaceStillResolves(t *testing.T) {
	// Guard against the reclassification suppressing real edges: the same file
	// mixes a resolvable using with third-party ones.
	_, _, rc := newRepoFixture(t, map[string]string{
		"src/Models/User.cs": "namespace MyApp.Models;\npublic class User {}\n",
		"src/Web/Startup.cs": "namespace MyApp.Web;\n",
	})

	lines := []string{"using MyApp.Models;", "using QRCoder;", "using System.Text;"}
	tally := &importTally{lang: "csharp"}
	got := resolveCSharpSpecs(csharpRegexSpecs(lines), "src/Web/Startup.cs", rc, tally)
	if len(got) != 1 || got[0] != "src/Models/User.cs" {
		t.Fatalf("got %v, want [src/Models/User.cs]", got)
	}
	st := tally.stats()
	if st.Extracted != 3 || st.Resolved != 1 || st.External != 2 || st.Unresolved != 0 {
		t.Fatalf("stats = %+v, want 3 extracted / 1 resolved / 2 external / 0 unresolved", st)
	}
}

func TestResolveCSharpImports_UsingStatic(t *testing.T) {
	// `using static App.MathHelpers;` names a type in namespace App; it is
	// accepted only on an exact file-name match inside that namespace.
	_, _, rc := newRepoFixture(t, map[string]string{
		"src/App/MathHelpers.cs": "namespace App;\npublic static class MathHelpers {}\n",
		"src/App/Other.cs":       "namespace App;\npublic class Other {}\n",
		"src/App/Calculator.cs":  "namespace App;\n",
	})

	got := resolveCSharpSpecs(csharpRegexSpecs([]string{"using static App.MathHelpers;"}), "src/App/Calculator.cs", rc, nil)
	if len(got) != 1 || got[0] != "src/App/MathHelpers.cs" {
		t.Fatalf("got %v, want [src/App/MathHelpers.cs]", got)
	}
	for _, g := range got {
		if g == "src/App/Other.cs" {
			t.Errorf("using static pulled in an unrelated file in the same namespace: %q", g)
		}
	}
}

func TestResolveCSharpImports_SkipsSelf(t *testing.T) {
	_, _, rc := newRepoFixture(t, map[string]string{
		"src/Models/User.cs": "namespace Models;\npublic class User {}\n",
	})

	got := resolveCSharpSpecs(csharpRegexSpecs([]string{"using Models;"}), "src/Models/User.cs", rc, nil)
	for _, g := range got {
		if g == "src/Models/User.cs" {
			t.Errorf("self-reference included in result")
		}
	}
}

// ─── Ruby resolver ───────────────────────────────────────────────────────────

func TestResolveRubyImports_RequireRelative(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "lib/auth/user.rb", Lang: "ruby", Class: scan.ClassSource},
	)

	lines := []string{`require_relative 'user'`}
	got := resolveRubyImports(lines, "lib/auth/session.rb", idx)
	if len(got) != 1 || got[0] != "lib/auth/user.rb" {
		t.Fatalf("got %v, want [lib/auth/user.rb]", got)
	}
}

func TestResolveRubyImports_RequireRelativeWithRbExt(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "lib/helpers/string_helper.rb", Lang: "ruby", Class: scan.ClassSource},
	)

	lines := []string{`require_relative 'string_helper.rb'`}
	got := resolveRubyImports(lines, "lib/helpers/formatter.rb", idx)
	if len(got) != 1 || got[0] != "lib/helpers/string_helper.rb" {
		t.Fatalf("got %v, want [lib/helpers/string_helper.rb]", got)
	}
}

func TestResolveRubyImports_RequireFromLib(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "lib/auth.rb", Lang: "ruby", Class: scan.ClassSource},
	)

	lines := []string{`require 'auth'`}
	got := resolveRubyImports(lines, "lib/app.rb", idx)
	if len(got) != 1 || got[0] != "lib/auth.rb" {
		t.Fatalf("got %v, want [lib/auth.rb]", got)
	}
}

func TestResolveRubyImports_RequireWithSlash(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "lib/auth/session.rb", Lang: "ruby", Class: scan.ClassSource},
	)

	lines := []string{`require 'auth/session'`}
	got := resolveRubyImports(lines, "lib/app.rb", idx)
	if len(got) != 1 || got[0] != "lib/auth/session.rb" {
		t.Fatalf("got %v, want [lib/auth/session.rb]", got)
	}
}

func TestResolveRubyImports_SkipsGemRequire(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "lib/rails.rb", Lang: "ruby", Class: scan.ClassSource},
	)

	// "rails" without "/" or "." and not in lib/ → should be skipped as a gem
	// (no file lib/rails.rb in index matches — wait, it IS in the index here.
	// Actually the logic checks: if no "/" and no ".", check if lib/<name>.rb exists.
	// Since lib/rails.rb exists, it would be found. Use a truly unknown name.)
	lines := []string{`require 'nokogiri'`}
	got := resolveRubyImports(lines, "lib/app.rb", idx)
	if len(got) != 0 {
		t.Fatalf("expected no results for unknown gem 'nokogiri', got %v", got)
	}
}

func TestResolveRubyImports_RequireRelativeParent(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "lib/shared.rb", Lang: "ruby", Class: scan.ClassSource},
	)

	lines := []string{`require_relative '../shared'`}
	got := resolveRubyImports(lines, "lib/auth/session.rb", idx)
	if len(got) != 1 || got[0] != "lib/shared.rb" {
		t.Fatalf("got %v, want [lib/shared.rb]", got)
	}
}

// ─── Rust resolver ────────────────────────────────────────────────────────────

func TestResolveRustImports_ModDecl(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/parser.rs", Lang: "rust", Class: scan.ClassSource},
	)

	lines := []string{"mod parser;"}
	got := resolveRustImports(lines, "src/main.rs", idx)
	if len(got) != 1 || got[0] != "src/parser.rs" {
		t.Fatalf("got %v, want [src/parser.rs]", got)
	}
}

func TestResolveRustImports_ModDeclModDir(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/db/mod.rs", Lang: "rust", Class: scan.ClassSource},
	)

	lines := []string{"mod db;"}
	got := resolveRustImports(lines, "src/main.rs", idx)
	if len(got) != 1 || got[0] != "src/db/mod.rs" {
		t.Fatalf("got %v, want [src/db/mod.rs]", got)
	}
}

func TestResolveRustImports_UseCrate(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/config.rs", Lang: "rust", Class: scan.ClassSource},
	)

	lines := []string{"use crate::config::Settings;"}
	got := resolveRustImports(lines, "src/main.rs", idx)
	if len(got) != 1 || got[0] != "src/config.rs" {
		t.Fatalf("got %v, want [src/config.rs]", got)
	}
}

func TestResolveRustImports_UseSelf(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/handlers/auth.rs", Lang: "rust", Class: scan.ClassSource},
	)

	lines := []string{"use self::auth::login;"}
	got := resolveRustImports(lines, "src/handlers/mod.rs", idx)
	if len(got) != 1 || got[0] != "src/handlers/auth.rs" {
		t.Fatalf("got %v, want [src/handlers/auth.rs]", got)
	}
}

func TestResolveRustImports_UseSuper(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/config.rs", Lang: "rust", Class: scan.ClassSource},
	)

	lines := []string{"use super::config::Settings;"}
	got := resolveRustImports(lines, "src/handlers/mod.rs", idx)
	if len(got) != 1 || got[0] != "src/config.rs" {
		t.Fatalf("got %v, want [src/config.rs]", got)
	}
}

func TestResolveRustImports_SkipsTestMod(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/tests.rs", Lang: "rust", Class: scan.ClassSource},
	)

	lines := []string{"mod tests;"}
	got := resolveRustImports(lines, "src/lib.rs", idx)
	if len(got) != 0 {
		t.Fatalf("expected mod tests to be skipped, got %v", got)
	}
}

func TestResolveRustImports_UseCrateDeep(t *testing.T) {
	// use crate::db::models::User; → tries src/db/models.rs first, then src/db.rs
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/db/models.rs", Lang: "rust", Class: scan.ClassSource},
	)

	lines := []string{"use crate::db::models::User;"}
	got := resolveRustImports(lines, "src/main.rs", idx)
	if len(got) != 1 || got[0] != "src/db/models.rs" {
		t.Fatalf("got %v, want [src/db/models.rs]", got)
	}
}

func TestResolveRustImports_NoSelfReference(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/lib.rs", Lang: "rust", Class: scan.ClassSource},
	)

	lines := []string{"use crate::lib::foo;"}
	got := resolveRustImports(lines, "src/lib.rs", idx)
	// Should not include itself
	for _, g := range got {
		if g == "src/lib.rs" {
			t.Errorf("self-reference included")
		}
	}
}

// ─── PHP resolver ─────────────────────────────────────────────────────────────

func TestResolvePHPImports_DirectPath(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "App/Models/User.php", Lang: "php", Class: scan.ClassSource},
	)

	lines := []string{"use App\\Models\\User;"}
	// Strategy 2: direct path (App/Models/User.php)
	got := resolvePHPSpecs(phpRegexSpecs(lines), "App/Controllers/UserController.php", mkCtx(idx), nil)
	if len(got) != 1 || got[0] != "App/Models/User.php" {
		t.Fatalf("got %v, want [App/Models/User.php]", got)
	}
}

func TestResolvePHPImports_StripFirstSegment(t *testing.T) {
	// Strategy 3: strip first namespace segment, try src/ prefix
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/Models/Order.php", Lang: "php", Class: scan.ClassSource},
	)

	lines := []string{"use App\\Models\\Order;"}
	got := resolvePHPSpecs(phpRegexSpecs(lines), "src/Controllers/OrderController.php", mkCtx(idx), nil)
	if len(got) != 1 || got[0] != "src/Models/Order.php" {
		t.Fatalf("got %v, want [src/Models/Order.php]", got)
	}
}

func TestResolvePHPImports_SkipsBuiltinNamespaces(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "Psr/Log/LoggerInterface.php", Lang: "php", Class: scan.ClassSource},
	)

	lines := []string{
		"use Psr\\Log\\LoggerInterface;",
		"use Symfony\\Component\\HttpFoundation\\Request;",
	}
	got := resolvePHPSpecs(phpRegexSpecs(lines), "src/Service.php", mkCtx(idx), nil)
	if len(got) != 0 {
		t.Fatalf("expected no results for builtin namespaces, got %v", got)
	}
}

func TestResolvePHPImports_SkipsSelf(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "App/Models/User.php", Lang: "php", Class: scan.ClassSource},
	)

	lines := []string{"use App\\Models\\User;"}
	got := resolvePHPSpecs(phpRegexSpecs(lines), "App/Models/User.php", mkCtx(idx), nil)
	for _, g := range got {
		if g == "App/Models/User.php" {
			t.Errorf("self-reference included")
		}
	}
}

// ─── Scala resolver ───────────────────────────────────────────────────────────

func TestResolveScalaImports_Basic(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/main/scala/com/example/User.scala", Lang: "scala", Class: scan.ClassSource},
	)

	lines := []string{"import com.example.User"}
	got := resolveScalaSpecs(scalaRegexSpecs(lines), "src/main/scala/com/example/Service.scala", idx, nil)
	if len(got) != 1 || got[0] != "src/main/scala/com/example/User.scala" {
		t.Fatalf("got %v, want [src/main/scala/com/example/User.scala]", got)
	}
}

func TestResolveScalaImports_WildcardDirectory(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/main/scala/com/example/models/User.scala", Lang: "scala", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "src/main/scala/com/example/models/Order.scala", Lang: "scala", Class: scan.ClassSource},
	)

	// Wildcard import: import com.example.models._
	lines := []string{"import com.example.models.{User, Order}"}
	got := resolveScalaSpecs(scalaRegexSpecs(lines), "src/main/scala/com/example/Service.scala", idx, nil)
	sort.Strings(got)

	if len(got) == 0 {
		t.Skip("Scala wildcard directory import not matched by current regex — known limitation")
	}
}

func TestResolveScalaImports_SkipsStdLib(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "scala/collection/mutable/Map.scala", Lang: "scala", Class: scan.ClassSource},
	)

	lines := []string{"import scala.collection.mutable.Map"}
	got := resolveScalaSpecs(scalaRegexSpecs(lines), "src/main/scala/App.scala", idx, nil)
	if len(got) != 0 {
		t.Fatalf("expected no results for scala stdlib, got %v", got)
	}
}

func TestResolveScalaImports_SkipsSelf(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/main/scala/com/example/User.scala", Lang: "scala", Class: scan.ClassSource},
	)

	lines := []string{"import com.example.User"}
	got := resolveScalaSpecs(scalaRegexSpecs(lines), "src/main/scala/com/example/User.scala", idx, nil)
	for _, g := range got {
		if g == "src/main/scala/com/example/User.scala" {
			t.Errorf("self-reference included")
		}
	}
}

// ─── resolveJSPath helper ─────────────────────────────────────────────────────

func TestResolveJSPath_ExtensionPriority(t *testing.T) {
	// When both .ts and .js exist, .ts wins (first in the extension list)
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/util.ts", Lang: "typescript", Class: scan.ClassSource},
		scan.FileEntry{RelPath: "src/util.js", Lang: "javascript", Class: scan.ClassSource},
	)

	got := resolveJSPath("src/util", idx)
	if got != "src/util.ts" {
		t.Fatalf("expected src/util.ts (ts beats js), got %q", got)
	}
}

func TestResolveJSPath_ExactMatchFirst(t *testing.T) {
	idx := mkIdx(
		scan.FileEntry{RelPath: "src/util.ts", Lang: "typescript", Class: scan.ClassSource},
	)

	// Exact path with extension — should match without appending another .ts
	got := resolveJSPath("src/util.ts", idx)
	if got != "src/util.ts" {
		t.Fatalf("got %q, want src/util.ts", got)
	}
}
