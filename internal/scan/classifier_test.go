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
		// A C# name suffix alone is not enough; see TestCSharpTestNameNeedsProjectEvidence.
		{"src/FooTests.cs", "FooTests.cs", ClassSource},
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

// TestCSharpTestNameNeedsProjectEvidence pins that "Test"/"Tests" in a C# file
// name is not on its own enough to call the file a test.
//
// C# has no _test.go equivalent: nothing about the file name makes the compiler
// or the test runner treat a type as a test, so "Test" is free to be an
// ordinary domain noun. The false positives below are real files from a
// veterinary-certificate codebase where they are plain entity models sitting in
// a production project; counting them as tests inflated the tests-command
// denominator and made production code read as test code in grep output.
//
// The true positives are the same codebase's actual tests. They stay tests
// because of where they live, not what they are called — a test project
// (Foo.Tests, Foo.IntegrationTests, FooDocTests) or a tests/ tree.
func TestCSharpTestNameNeedsProjectEvidence(t *testing.T) {
	falsePositives := []struct{ relPath, name string }{
		// Domain models whose type name happens to end in "Test".
		{"backend/src/Domains/Leroy.Certificates/Models/LabTest.cs", "LabTest.cs"},
		{"backend/src/Domains/Leroy.Certificates/Models/CertificateAnimalTest.cs", "CertificateAnimalTest.cs"},
		// Same shape, other plausible spellings.
		{"backend/src/Domains/Leroy.Patients/Models/SkinTest.cs", "SkinTest.cs"},
		{"src/App/Models/StressTest.cs", "StressTest.cs"},
		{"src/App/Models/LabTests.cs", "LabTests.cs"},
		// "Latest" ends in "test": neither the file name nor a directory named
		// "Latest" may trip the C# rule.
		{"src/App/Models/Latest.cs", "Latest.cs"},
		{"src/App/Latest/Snapshot.cs", "Snapshot.cs"},
	}
	for _, f := range falsePositives {
		if got := Classify(f.relPath, f.name); got != ClassSource {
			t.Errorf("Classify(%q) = %v, want ClassSource", f.relPath, got)
		}
	}

	truePositives := []struct{ relPath, name string }{
		// Under a tests/ tree.
		{"backend/tests/Leroy.Patients.Tests/PatientRepositoryTests.cs", "PatientRepositoryTests.cs"},
		{"backend/tests/Leroy.Api.IntegrationTests/AuthTests.cs", "AuthTests.cs"},
		{"backend/tests/Leroy.DocTests/CreateAContactDocTest.cs", "CreateAContactDocTest.cs"},
		{"backend/tests/Leroy.E2E/AuthSmokeTests.cs", "AuthSmokeTests.cs"},
		// Test project outside a tests/ tree, dotted and dotless spellings.
		{"infrastructure.tests/LeroyStackTests.cs", "LeroyStackTests.cs"},
		{"clients/mobile/tests/Leroy.Mobile.Core.Tests/SessionTests.cs", "SessionTests.cs"},
		{"src/Foo.UnitTests/BarTests.cs", "BarTests.cs"},
		{"src/FooTests/Bar.cs", "Bar.cs"},
		{"src/Foo.Specs/BarSpec.cs", "BarSpec.cs"},
		// Fixtures and helpers inside a test project stay test-class, matching
		// TestDirectoryTestClassificationIsBroad.
		{"backend/tests/Leroy.Platform.Tests/Fixtures/DbFixture.cs", "DbFixture.cs"},
		{"infrastructure.tests/Helpers.cs", "Helpers.cs"},
	}
	for _, f := range truePositives {
		if got := Classify(f.relPath, f.name); got != ClassTest {
			t.Errorf("Classify(%q) = %v, want ClassTest", f.relPath, got)
		}
	}
}

// TestNonCSharpTestNamingUnaffected pins that the C# narrowing above is scoped
// to .cs. Every other language keeps the signal it had, including the ones
// where a name suffix really is the convention.
func TestNonCSharpTestNamingUnaffected(t *testing.T) {
	tests := []struct {
		relPath, name string
		want          FileClass
	}{
		{"src/parser_test.go", "parser_test.go", ClassTest},
		{"lib/app_test.exs", "app_test.exs", ClassTest},
		{"src/app.spec.ts", "app.spec.ts", ClassTest},
		{"src/test_parser.py", "test_parser.py", ClassTest},
		{"src/parser_test.rs", "parser_test.rs", ClassTest},
		{"src/UserTest.java", "UserTest.java", ClassTest},
		{"src/UserTest.kt", "UserTest.kt", ClassTest},
		{"src/UserTest.php", "UserTest.php", ClassTest},
		{"src/UserSpec.scala", "UserSpec.scala", ClassTest},
		{"src/UserTests.swift", "UserTests.swift", ClassTest},
		// A Rust module with #[cfg(test)] inside an ordinary file is content,
		// not name, and was never classified here.
		{"src/parser.rs", "parser.rs", ClassSource},
	}
	for _, tt := range tests {
		if got := Classify(tt.relPath, tt.name); got != tt.want {
			t.Errorf("Classify(%q) = %v, want %v", tt.relPath, got, tt.want)
		}
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
		// Razor markup is its own language; its code-behind is not. The
		// language is keyed on the final extension, which is what keeps
		// Contacts.cshtml.cs — ordinary C# that parses fine — out of it.
		{"Contacts.cshtml", "razor"},
		{"Counter.razor", "razor"},
		{"Contacts.cshtml.cs", "csharp"},
		{"Counter.razor.cs", "csharp"},
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
