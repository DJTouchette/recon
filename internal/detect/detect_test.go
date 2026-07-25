package detect

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

// buildRepo writes files into a temp dir and returns the root plus an index
// built from a real walk, so tests exercise the same classification the CLI
// does.
func buildRepo(t *testing.T, files map[string]string) (string, *index.FileIndex) {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	walk, err := scan.Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, index.NewFileIndex(walk.Files)
}

func detectRepo(t *testing.T, files map[string]string) *Result {
	t.Helper()
	root, idx := buildRepo(t, files)
	return Detect(idx, root)
}

func frameworkNames(r *Result) []string {
	out := make([]string, 0, len(r.Frameworks))
	for _, f := range r.Frameworks {
		out = append(out, f.Name)
	}
	return out
}

func dependencyNames(r *Result) []string {
	out := make([]string, 0, len(r.Dependencies))
	for _, d := range r.Dependencies {
		out = append(out, d.Name)
	}
	return out
}

func entrypointPaths(r *Result) []string {
	out := make([]string, 0, len(r.Entrypoints))
	for _, e := range r.Entrypoints {
		out = append(out, e.Path)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func assertHas(t *testing.T, list []string, want string) {
	t.Helper()
	if !contains(list, want) {
		t.Errorf("expected %q in %v", want, list)
	}
}

func assertLacks(t *testing.T, list []string, unwanted string) {
	t.Helper()
	if contains(list, unwanted) {
		t.Errorf("did not expect %q in %v", unwanted, list)
	}
}

func frameworkByName(r *Result, name string) (Framework, bool) {
	for _, f := range r.Frameworks {
		if f.Name == name {
			return f, true
		}
	}
	return Framework{}, false
}

// --- Frameworks are claims, dependencies are facts ---

func TestNodeManifestSeparatesFrameworksFromDependencies(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"package.json": `{
  "name": "my-ts-app",
  "dependencies": {"express": "^4.18.0"},
  "devDependencies": {"typescript": "^5.0.0", "eslint": "^8.0.0", "@types/node": "^20.0.0"}
}`,
		"src/index.ts": "export const x = 1;\n",
	})

	fws := frameworkNames(r)
	if !reflect.DeepEqual(fws, []string{"Express"}) {
		t.Fatalf("frameworks = %v, want [Express]", fws)
	}

	// The toolchain packages are still reported — as dependencies.
	deps := dependencyNames(r)
	for _, want := range []string{"@types/node", "eslint", "express", "typescript"} {
		assertHas(t, deps, want)
	}
}

func TestNodeFrameworkEvidenceNamesTheManifestEntry(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"package.json": `{"dependencies": {"express": "^4.0.0"}}`,
		"src/index.js": "module.exports = {};\n",
	})
	f, ok := frameworkByName(r, "Express")
	if !ok {
		t.Fatal("Express not detected")
	}
	if f.Evidence != "package.json: dependencies[express]" {
		t.Errorf("evidence = %q", f.Evidence)
	}
}

func TestNodeFrameworkLanguageIsNotTheDominantRepoLanguage(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"package.json": `{"dependencies": {"express": "^4.0.0"}}`,
		"src/a.ts":     "export const a = 1;\n",
		"src/b.ts":     "export const b = 1;\n",
	})
	f, ok := frameworkByName(r, "Express")
	if !ok {
		t.Fatal("Express not detected")
	}
	// express is a JavaScript package; a TypeScript-heavy repo does not make
	// it a TypeScript one.
	if f.Language != "javascript" {
		t.Errorf("language = %q, want javascript", f.Language)
	}
}

func TestNodeConfigMarkerProvesFrameworkWithoutManifest(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"next.config.js": "module.exports = {};\n",
		"src/page.js":    "export default function Page() {}\n",
	})
	assertHas(t, frameworkNames(r), "Next.js")
	f, _ := frameworkByName(r, "Next.js")
	if f.Evidence != "next.config.js" {
		t.Errorf("evidence = %q, want next.config.js", f.Evidence)
	}
}

func TestNodeImportProvesFrameworkWithoutManifest(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"server.js": "const express = require(\"express\");\nconst app = express();\n",
	})
	assertHas(t, frameworkNames(r), "Express")
}

func TestNodeEntrypointsFromManifest(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"package.json": `{"main": "./lib/entry.js", "bin": {"mycli": "bin/run.js"}}`,
		"lib/entry.js": "module.exports = 1;\n",
		"bin/run.js":   "#!/usr/bin/env node\n",
	})
	paths := entrypointPaths(r)
	assertHas(t, paths, "lib/entry.js")
	assertHas(t, paths, "bin/run.js")
}

// --- Java / Maven ---

const springPom = `<project>
  <parent><groupId>org.springframework.boot</groupId><artifactId>spring-boot-starter-parent</artifactId></parent>
  <groupId>com.ex</groupId>
  <artifactId>my-own-app</artifactId>
  <dependencies>
    <dependency>
      <groupId>org.springframework.boot</groupId>
      <artifactId>spring-boot-starter-web</artifactId>
      <version>3.2.0</version>
    </dependency>
  </dependencies>
  <build><plugins><plugin><artifactId>maven-surefire-plugin</artifactId></plugin></plugins></build>
</project>`

const springApp = `package com.ex;

import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;

@SpringBootApplication
public class OrderServiceApplication {
    public static void main(String[] args) {
        SpringApplication.run(OrderServiceApplication.class, args);
    }
}
`

func TestPomDoesNotReportTheProjectAsItsOwnFramework(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"pom.xml": springPom,
		"src/main/java/com/ex/OrderServiceApplication.java": springApp,
	})

	fws := frameworkNames(r)
	assertHas(t, fws, "Spring Boot")
	assertLacks(t, fws, "my-own-app")
	assertLacks(t, fws, "spring-boot-starter-parent")
	assertLacks(t, fws, "maven-surefire-plugin")

	deps := dependencyNames(r)
	if !reflect.DeepEqual(deps, []string{"spring-boot-starter-web"}) {
		t.Errorf("dependencies = %v, want only the real <dependency>", deps)
	}
}

func TestPomDependencyKeepsVersion(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"pom.xml":              springPom,
		"src/main/java/A.java": "package a;\nclass A {}\n",
	})
	if len(r.Dependencies) != 1 || r.Dependencies[0].Version != "3.2.0" {
		t.Fatalf("dependencies = %+v", r.Dependencies)
	}
}

func TestSpringEntrypointIsFoundByAnnotationNotFilename(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"pom.xml": springPom,
		"src/main/java/com/ex/OrderServiceApplication.java": springApp,
	})
	if got := entrypointPaths(r); !reflect.DeepEqual(got, []string{"src/main/java/com/ex/OrderServiceApplication.java"}) {
		t.Fatalf("entrypoints = %v", got)
	}
	if r.EntrypointStatus != StatusFound {
		t.Errorf("status = %q, want found", r.EntrypointStatus)
	}
}

func TestJavaPlainMainMethodIsAnEntrypoint(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"src/main/java/com/ex/Tool.java": `package com.ex;
public class Tool {
    public static void main(String[] args) { System.out.println("x"); }
}
`,
	})
	assertHas(t, entrypointPaths(r), "src/main/java/com/ex/Tool.java")
}

func TestKotlinTopLevelMainIsAnEntrypoint(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"src/main/kotlin/App.kt": "package com.ex\n\nfun main(args: Array<String>) {\n    println(\"hi\")\n}\n",
	})
	assertHas(t, entrypointPaths(r), "src/main/kotlin/App.kt")
}

func TestGradleDependenciesAndPlugins(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"build.gradle.kts": `plugins {
    id("org.springframework.boot") version "3.2.0"
}
dependencies {
    implementation("org.springframework.boot:spring-boot-starter-web:3.2.0")
    testImplementation("org.junit.jupiter:junit-jupiter:5.10.0")
}
`,
		"src/main/java/A.java": "package a;\nclass A {}\n",
	})
	deps := dependencyNames(r)
	assertHas(t, deps, "spring-boot-starter-web")
	assertHas(t, deps, "junit-jupiter")
	fws := frameworkNames(r)
	assertHas(t, fws, "Spring Boot")
	assertHas(t, fws, "JUnit")
}

// --- Go ---

func TestGoEntrypointIsNotFilenameEquality(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"go.mod":               "module example.com/backend\n\ngo 1.22\n",
		"backend/cmd/serve.go": "package main\n\nfunc main() {\n\tprintln(\"serving\")\n}\n",
	})
	if got := entrypointPaths(r); !reflect.DeepEqual(got, []string{"backend/cmd/serve.go"}) {
		t.Fatalf("entrypoints = %v", got)
	}
	if r.Entrypoints[0].Kind != "cli" {
		t.Errorf("kind = %q, want cli (under cmd/)", r.Entrypoints[0].Kind)
	}
}

func TestGoPackageMainWithoutFuncMainIsNotAnEntrypoint(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"go.mod":           "module example.com/x\n\ngo 1.22\n",
		"cmd/app/flags.go": "package main\n\nvar debug bool\n",
	})
	if len(r.Entrypoints) != 0 {
		t.Fatalf("entrypoints = %v, want none", entrypointPaths(r))
	}
	if r.EntrypointStatus != StatusNoneMatched {
		t.Errorf("status = %q, want none_matched", r.EntrypointStatus)
	}
}

func TestGoMainOutsideCmdIsKindMain(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"go.mod":  "module example.com/x\n\ngo 1.22\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})
	if len(r.Entrypoints) != 1 || r.Entrypoints[0].Kind != "main" {
		t.Fatalf("entrypoints = %+v", r.Entrypoints)
	}
}

func TestGoModDependenciesSkipIndirect(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"go.mod": `module example.com/x

go 1.22

require github.com/spf13/cobra v1.10.2

require (
	github.com/gin-gonic/gin v1.9.1
	github.com/stretchr/testify v1.8.4 // indirect
)
`,
		"main.go": "package main\n\nfunc main() {}\n",
	})
	deps := dependencyNames(r)
	assertHas(t, deps, "github.com/gin-gonic/gin")
	assertHas(t, deps, "github.com/spf13/cobra")
	assertLacks(t, deps, "github.com/stretchr/testify")

	fws := frameworkNames(r)
	assertHas(t, fws, "Gin")
	assertHas(t, fws, "Cobra")
}

func TestGoModuleMajorVersionSuffixStillMatches(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"go.mod":  "module example.com/x\n\ngo 1.22\n\nrequire github.com/go-chi/chi/v5 v5.0.0\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})
	assertHas(t, frameworkNames(r), "chi")
}

// --- Python ---

func TestFlaskAppWithNoManifestIsDetected(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"server.py": `from flask import Flask
app = Flask(__name__)

@app.route("/")
def index():
    return "hi"
`,
	})
	assertHas(t, frameworkNames(r), "Flask")
	if got := entrypointPaths(r); !reflect.DeepEqual(got, []string{"server.py"}) {
		t.Fatalf("entrypoints = %v", got)
	}
	if r.Entrypoints[0].Kind != "server" {
		t.Errorf("kind = %q, want server", r.Entrypoints[0].Kind)
	}
}

func TestPythonMainGuardIsAnEntrypoint(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"tools/run_job.py": "import sys\n\nif __name__ == \"__main__\":\n    sys.exit(0)\n",
	})
	assertHas(t, entrypointPaths(r), "tools/run_job.py")
}

func TestPyprojectPEP621Dependencies(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"pyproject.toml": `[project]
name = "svc"
dependencies = [
    "fastapi>=0.100",
    "uvicorn",
]

[tool.black]
line-length = 100
`,
		"app.py": "x = 1\n",
	})
	deps := dependencyNames(r)
	assertHas(t, deps, "fastapi")
	assertHas(t, deps, "uvicorn")
	assertLacks(t, deps, "line-length")
	assertHas(t, frameworkNames(r), "FastAPI")
	assertLacks(t, frameworkNames(r), "uvicorn")
}

func TestPyprojectPoetryDependencies(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"pyproject.toml": `[tool.poetry.dependencies]
python = "^3.11"
django = "^5.0"
requests = { version = "^2.31" }
`,
		"app.py": "x = 1\n",
	})
	deps := dependencyNames(r)
	assertHas(t, deps, "django")
	assertHas(t, deps, "requests")
	assertLacks(t, deps, "python")
	assertHas(t, frameworkNames(r), "Django")
}

func TestPipfileDependencies(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"Pipfile": "[packages]\nflask = \"*\"\n\n[dev-packages]\npytest = \"*\"\n",
		"app.py":  "x = 1\n",
	})
	deps := dependencyNames(r)
	assertHas(t, deps, "flask")
	assertHas(t, deps, "pytest")
}

func TestDjangoManageEntrypoint(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"manage.py": "import os\n\nif __name__ == \"__main__\":\n    pass\n",
	})
	assertHas(t, frameworkNames(r), "Django")
	for _, e := range r.Entrypoints {
		if e.Path == "manage.py" && e.Kind != "cli" {
			t.Errorf("manage.py kind = %q, want cli", e.Kind)
		}
	}
}

// --- Other ecosystems ---

func TestRustCargoDependencies(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"Cargo.toml": `[package]
name = "svc"
version = "0.1.0"

[dependencies]
axum = "0.7"
serde = { version = "1.0", features = ["derive"] }
`,
		"src/main.rs": "fn main() {}\n",
	})
	deps := dependencyNames(r)
	assertHas(t, deps, "axum")
	assertHas(t, deps, "serde")
	assertLacks(t, deps, "features")
	assertLacks(t, deps, "name")
	assertHas(t, frameworkNames(r), "Axum")
	assertLacks(t, frameworkNames(r), "serde")
	assertHas(t, entrypointPaths(r), "src/main.rs")
}

func TestRubyGemfile(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"Gemfile":          "source \"https://rubygems.org\"\ngem \"rails\", \"~> 7.1\"\ngem \"rubocop\"\n",
		"config/routes.rb": "Rails.application.routes.draw do\nend\n",
		"app/model.rb":     "class Model; end\n",
	})
	assertHas(t, dependencyNames(r), "rubocop")
	assertHas(t, frameworkNames(r), "Rails")
	assertLacks(t, frameworkNames(r), "rubocop")
	assertHas(t, entrypointPaths(r), "config/routes.rb")
}

func TestElixirMixDeps(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"mix.exs": `defmodule App.MixProject do
  defp deps do
    [{:phoenix, "~> 1.7"}, {:credo, "~> 1.7", only: :dev}]
  end
end
`,
		"lib/app/application.ex": "defmodule App.Application do\n  use Application\nend\n",
	})
	assertHas(t, dependencyNames(r), "credo")
	assertHas(t, frameworkNames(r), "Phoenix")
	assertLacks(t, frameworkNames(r), "credo")
	assertHas(t, entrypointPaths(r), "lib/app/application.ex")
}

func TestDartPubspec(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"pubspec.yaml": `name: app
dependencies:
  flutter:
    sdk: flutter
  http: ^1.0.0
dev_dependencies:
  lints: ^3.0.0
`,
		"lib/main.dart": "void main() {\n  runApp();\n}\n",
	})
	assertHas(t, dependencyNames(r), "http")
	assertHas(t, frameworkNames(r), "Flutter")
	assertLacks(t, frameworkNames(r), "http")
	assertHas(t, entrypointPaths(r), "lib/main.dart")
}

func TestScalaSbt(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"build.sbt":                 `libraryDependencies += "org.http4s" %% "http4s-dsl" % "0.23.0"`,
		"src/main/scala/Main.scala": "object Main extends App {\n  println(\"hi\")\n}\n",
	})
	assertHas(t, frameworkNames(r), "http4s")
	assertHas(t, entrypointPaths(r), "src/main/scala/Main.scala")
}

func TestDotNetCsproj(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"src/Api/Api.csproj": `<Project Sdk="Microsoft.NET.Sdk.Web">
  <ItemGroup>
    <PackageReference Include="Serilog" Version="3.1.1" />
    <PackageReference Include="Microsoft.EntityFrameworkCore" Version="8.0.0" />
  </ItemGroup>
</Project>`,
		"src/Api/Program.cs": "var builder = WebApplication.CreateBuilder(args);\n",
	})
	deps := dependencyNames(r)
	assertHas(t, deps, "Serilog")
	fws := frameworkNames(r)
	assertHas(t, fws, "ASP.NET Core")
	assertHas(t, fws, "Entity Framework Core")
	assertHas(t, entrypointPaths(r), "src/Api/Program.cs")
}

// --- Status: empty is never ambiguous ---

func TestStatusUnsupportedWhenNoDetectorCoversTheRepo(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"notes.txt": "hello\n",
		"data.csv":  "a,b\n1,2\n",
		"main.cob":  "IDENTIFICATION DIVISION.\n",
	})
	if r.EntrypointStatus != StatusUnsupported {
		t.Errorf("entrypoint status = %q, want unsupported", r.EntrypointStatus)
	}
	if r.FrameworkStatus != StatusUnsupported {
		t.Errorf("framework status = %q, want unsupported", r.FrameworkStatus)
	}
}

func TestStatusNoneMatchedWhenLanguageIsKnownButNothingMatched(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"internal/util/strings.go": "package util\n\nfunc Trim(s string) string { return s }\n",
	})
	if r.EntrypointStatus != StatusNoneMatched {
		t.Errorf("entrypoint status = %q, want none_matched", r.EntrypointStatus)
	}
	if r.FrameworkStatus != StatusNoneMatched {
		t.Errorf("framework status = %q, want none_matched", r.FrameworkStatus)
	}
}

func TestStatusFound(t *testing.T) {
	r := detectRepo(t, map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
	})
	if r.EntrypointStatus != StatusFound {
		t.Errorf("entrypoint status = %q, want found", r.EntrypointStatus)
	}
}

// --- Determinism ---

func TestDetectIsDeterministic(t *testing.T) {
	files := map[string]string{
		"package.json": `{"dependencies": {"express": "^4.0.0", "react": "^18.0.0", "vue": "^3.0.0"},
		                  "devDependencies": {"jest": "^29.0.0", "vitest": "^1.0.0", "typescript": "^5.0.0"}}`,
		"go.mod":        "module x\n\ngo 1.22\n\nrequire (\n\tgithub.com/gin-gonic/gin v1.9.1\n\tgorm.io/gorm v1.25.0\n\tgithub.com/spf13/cobra v1.8.0\n)\n",
		"main.go":       "package main\n\nfunc main() {}\n",
		"cmd/x/main.go": "package main\n\nfunc main() {}\n",
		"src/index.ts":  "export const a = 1;\n",
		"src/app.ts":    "export const b = 1;\n",
		"app.py":        "import flask\n",
		"lib.rb":        "class A; end\n",
	}
	root, idx := buildRepo(t, files)

	first := Detect(idx, root)
	for i := 0; i < 25; i++ {
		got := Detect(idx, root)
		if !reflect.DeepEqual(first.Frameworks, got.Frameworks) {
			t.Fatalf("run %d frameworks differ:\n%+v\n%+v", i, first.Frameworks, got.Frameworks)
		}
		if !reflect.DeepEqual(first.Dependencies, got.Dependencies) {
			t.Fatalf("run %d dependencies differ", i)
		}
		if !reflect.DeepEqual(first.Entrypoints, got.Entrypoints) {
			t.Fatalf("run %d entrypoints differ", i)
		}
		if !reflect.DeepEqual(first.Languages, got.Languages) {
			t.Fatalf("run %d languages differ:\n%+v\n%+v", i, first.Languages, got.Languages)
		}
	}
}

func TestLanguagesWithTiedCountsAreOrderedByName(t *testing.T) {
	// Three languages with exactly one file each: only a name tie-break can
	// make this stable.
	root, idx := buildRepo(t, map[string]string{
		"a.go": "package a\n",
		"b.py": "x = 1\n",
		"c.rb": "class C; end\n",
		"d.rs": "fn f() {}\n",
	})
	want := Detect(idx, root).Languages
	for i := 0; i < 25; i++ {
		if got := Detect(idx, root).Languages; !reflect.DeepEqual(want, got) {
			t.Fatalf("run %d: %+v != %+v", i, got, want)
		}
	}
	for i := 1; i < len(want); i++ {
		if want[i-1].FileCount == want[i].FileCount && want[i-1].Name > want[i].Name {
			t.Errorf("tied languages not sorted by name: %+v", want)
		}
	}
}

// --- Legacy API ---

func TestDetectAllMatchesDetect(t *testing.T) {
	root, idx := buildRepo(t, map[string]string{
		"go.mod":  "module x\n\ngo 1.22\n\nrequire github.com/spf13/cobra v1.8.0\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})
	full := Detect(idx, root)
	langs, fws, eps := DetectAll(idx, root)
	if !reflect.DeepEqual(langs, full.Languages) ||
		!reflect.DeepEqual(fws, full.Frameworks) ||
		!reflect.DeepEqual(eps, full.Entrypoints) {
		t.Error("DetectAll diverged from Detect")
	}
}

// --- Unit-level helpers ---

func TestNormalizeGoModule(t *testing.T) {
	cases := map[string]string{
		"github.com/go-chi/chi/v5": "github.com/go-chi/chi",
		"github.com/gin-gonic/gin": "github.com/gin-gonic/gin",
		"example.com/v":            "example.com/v",
		"example.com/verylong":     "example.com/verylong",
	}
	for in, want := range cases {
		if got := normalizeGoModule(in); got != want {
			t.Errorf("normalizeGoModule(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitGradleCoord(t *testing.T) {
	a, v := splitGradleCoord("org.springframework.boot:spring-boot-starter-web:3.2.0")
	if a != "spring-boot-starter-web" || v != "3.2.0" {
		t.Errorf("got %q %q", a, v)
	}
	a, v = splitGradleCoord("group:artifact")
	if a != "artifact" || v != "" {
		t.Errorf("got %q %q", a, v)
	}
}
