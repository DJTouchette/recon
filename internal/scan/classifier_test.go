package scan

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		relPath string
		name    string
		want    FileClass
	}{
		{"src/main.go", "main.go", ClassSource},
		{"src/main_test.go", "main_test.go", ClassTest},
		{"src/app.test.ts", "app.test.ts", ClassTest},
		{"src/app.spec.tsx", "app.spec.tsx", ClassTest},
		{"test_main.py", "test_main.py", ClassTest},
		{"go.mod", "go.mod", ClassConfig},
		{"package.json", "package.json", ClassConfig},
		{"backend/Foo.csproj", "Foo.csproj", ClassConfig},
		{"README.md", "README.md", ClassDoc},
		{"docs/guide.txt", "guide.txt", ClassDoc},
		{"assets/logo.png", "logo.png", ClassAsset},
		{"vendor/lib.go", "lib.go", ClassVendor},
		{"generated/types.pb.go", "types.pb.go", ClassGenerated},
		{"src/utils.min.js", "utils.min.js", ClassGenerated},
		{"deploy.sh", "deploy.sh", ClassScript},
		{"scripts/run.ps1", "run.ps1", ClassScript},
		{"data.csv", "data.csv", ClassData},
		{"__tests__/foo.tsx", "foo.tsx", ClassTest},
		{"backend/Foo.Tests/BarTest.cs", "BarTest.cs", ClassTest},
		{"src/FooTests.cs", "FooTests.cs", ClassTest},
		{"src/app.ts", "app.ts", ClassSource},
	}

	for _, tt := range tests {
		t.Run(tt.relPath, func(t *testing.T) {
			got := Classify(tt.relPath, tt.name)
			if got != tt.want {
				t.Errorf("Classify(%q, %q) = %v, want %v", tt.relPath, tt.name, got, tt.want)
			}
		})
	}
}

// TestDirectoryTestClassificationIsBroad pins the behaviour that any source
// file under a test directory is test-class, including shared fixture code that
// declares real API.
//
// This is intentional and load-bearing for the tests command and hotspot
// weighting. It is recorded here because it used to combine with a symbol index
// that skipped ClassTest entirely, which made every fixture type and helper in
// such a directory invisible to symbols, search and callers. The fix belongs on
// the indexing side (internal/index indexes ClassTest and lets callers filter),
// not here — narrowing this would misclassify genuine tests.
func TestDirectoryTestClassificationIsBroad(t *testing.T) {
	fixtures := []struct{ relPath, name string }{
		{"test/fixtures.go", "fixtures.go"},
		{"tests/support/builder.go", "builder.go"},
		{"spec/factories.rb", "factories.rb"},
		{"specs/helpers.ts", "helpers.ts"},
		{"__tests__/setup.ts", "setup.ts"},
		{"src/Foo.UnitTests/Helper.cs", "Helper.cs"},
	}
	for _, f := range fixtures {
		if got := Classify(f.relPath, f.name); got != ClassTest {
			t.Errorf("Classify(%q) = %v, want ClassTest", f.relPath, got)
		}
	}

	// Non-source files under a test directory are classified on their own
	// merits, not swept into ClassTest.
	if got := Classify("test/README.md", "README.md"); got != ClassDoc {
		t.Errorf("Classify(test/README.md) = %v, want ClassDoc", got)
	}
	// A directory whose name merely contains "test" is not a test directory.
	if got := Classify("src/testing/util.go", "util.go"); got != ClassSource {
		t.Errorf("Classify(src/testing/util.go) = %v, want ClassSource", got)
	}
}

// TestHeaderLanguageIsAmbiguous records that .h reports C even for C++ headers.
// Symbol extraction resolves this by trying both grammars on the content; the
// classifier cannot, because it only sees the name.
func TestHeaderLanguageIsAmbiguous(t *testing.T) {
	for _, name := range []string{"widget.h", "api.h"} {
		if got := LangFromExt(name); got != "c" {
			t.Errorf("LangFromExt(%q) = %q, want c", name, got)
		}
	}
	if got := LangFromExt("widget.hpp"); got != "cpp" {
		t.Errorf("LangFromExt(widget.hpp) = %q, want cpp", got)
	}
}

func TestLangFromExt(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"main.go", "go"},
		{"app.ts", "typescript"},
		{"app.tsx", "typescript"},
		{"index.js", "javascript"},
		{"app.py", "python"},
		{"lib.rs", "rust"},
		{"Foo.cs", "csharp"},
		{"Main.java", "java"},
		{"app.rb", "ruby"},
		{"app.ex", "elixir"},
		{"unknown.xyz", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LangFromExt(tt.name)
			if got != tt.want {
				t.Errorf("LangFromExt(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}
