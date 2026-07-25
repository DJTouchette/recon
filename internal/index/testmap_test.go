package index

import (
	"reflect"
	"testing"
)

// buildTestMap writes a tree and returns its test map.
func buildTestMap(t *testing.T, files map[string]string) *TestMap {
	t.Helper()
	_, idx := writeTree(t, files)
	return NewTestMap(idx)
}

func assertMapped(t *testing.T, tm *TestMap, testPath, wantSource string) {
	t.Helper()
	got, status := tm.LookupSource(testPath)
	if got != wantSource {
		t.Errorf("source for %s = %q, want %q (status %s, unmapped %v)",
			testPath, got, wantSource, status, tm.UnmappedTests())
		return
	}
	if status != TestMapMapped {
		t.Errorf("status for %s = %s, want mapped", testPath, status)
	}
	tests, st := tm.LookupTests(wantSource)
	if st != TestMapMapped {
		t.Errorf("reverse status for %s = %s", wantSource, st)
	}
	found := false
	for _, p := range tests {
		if p == testPath {
			found = true
		}
	}
	if !found {
		t.Errorf("reverse map for %s = %v, missing %s", wantSource, tests, testPath)
	}
}

// --- The verified gaps ---

func TestGoTestNamedForAFacetMapsToItsSource(t *testing.T) {
	// depgraph_resolve_test.go sits next to depgraph.go; there is no
	// depgraph_resolve.go.
	tm := buildTestMap(t, map[string]string{
		"internal/index/depgraph.go":              "package index\n",
		"internal/index/depgraph_resolve_test.go": "package index\n",
		"internal/index/depgraph_ts.go":           "package index\n",
		"internal/index/depgraph_ts_test.go":      "package index\n",
	})
	assertMapped(t, tm, "internal/index/depgraph_resolve_test.go", "internal/index/depgraph.go")
	// The more specific name still wins where it exists.
	assertMapped(t, tm, "internal/index/depgraph_ts_test.go", "internal/index/depgraph_ts.go")
}

func TestBehaviourNamedTestMapsToTheFileItExercises(t *testing.T) {
	tm := buildTestMap(t, map[string]string{
		"src/checkout.ts":                     "export function checkout() {}\n",
		"src/__tests__/checkout-flow.test.ts": "test('x', () => {});\n",
	})
	assertMapped(t, tm, "src/__tests__/checkout-flow.test.ts", "src/checkout.ts")
}

func TestTopLevelTestDirMirrorsSrc(t *testing.T) {
	tm := buildTestMap(t, map[string]string{
		"src/cart.ts":           "export function cart() {}\n",
		"test/src/cart.test.ts": "test('y', () => {});\n",
	})
	assertMapped(t, tm, "test/src/cart.test.ts", "src/cart.ts")
}

func TestTestsForDirectoryRootIsNotEmpty(t *testing.T) {
	// FilesInDir(".") used to build the prefix "./", which never matches a
	// cleaned relpath, so a repo-wide question returned nothing.
	_, idx := writeTree(t, map[string]string{
		"src/a.ts":      "export const a = 1;\n",
		"src/a.test.ts": "test('a', () => {});\n",
	})
	if n := len(idx.FilesUnderDir(".")); n != 2 {
		t.Errorf("FilesUnderDir(\".\") = %d files, want 2", n)
	}
	if n := len(idx.FilesUnderDir("")); n != 2 {
		t.Errorf("FilesUnderDir(\"\") = %d files, want 2", n)
	}
}

// --- The .NET solution layout ---

// The reported defect: a 656-file C# project where the only test files for
// AprysePdfGenerationService.cs are three AprysePdf*Tests.cs in a sibling test
// project, and recon answered "No test files found."
//
// Two rules have to hold at once. The test project pairs with the whole source
// *project* (src/Leroy.Platform), so a source in a subdirectory the test tree
// does not mirror (Pdf/) is still in scope. And the test names share only a
// prefix with the source name, which shortening the test stem can never reach.
func TestDotNetTestProjectFindsSourceInUnmirroredSubdirectory(t *testing.T) {
	tm := buildTestMap(t, map[string]string{
		"backend/src/Leroy.Platform/Pdf/AprysePdfGenerationService.cs": "class AprysePdfGenerationService {}\n",
		"backend/src/Leroy.Platform/Pdf/IPdfGenerationService.cs":      "interface IPdfGenerationService {}\n",
		"backend/src/Leroy.Platform/Auth/TenantContext.cs":             "class TenantContext {}\n",
		"backend/tests/Leroy.Platform.Tests/AprysePdfGoldenTests.cs":   "class AprysePdfGoldenTests {}\n",
		"backend/tests/Leroy.Platform.Tests/AprysePdfRenderTests.cs":   "class AprysePdfRenderTests {}\n",
		"backend/tests/Leroy.Platform.Tests/AprysePdfVisualTests.cs":   "class AprysePdfVisualTests {}\n",
	})

	const src = "backend/src/Leroy.Platform/Pdf/AprysePdfGenerationService.cs"
	want := []string{
		"backend/tests/Leroy.Platform.Tests/AprysePdfGoldenTests.cs",
		"backend/tests/Leroy.Platform.Tests/AprysePdfRenderTests.cs",
		"backend/tests/Leroy.Platform.Tests/AprysePdfVisualTests.cs",
	}
	for _, tp := range want {
		assertMapped(t, tm, tp, src)
	}
	if got, _ := tm.LookupTests(src); !reflect.DeepEqual(got, want) {
		t.Errorf("LookupTests(%s) = %v, want all three", src, got)
	}
}

func TestDotNetTestProjectSuffixesAndNesting(t *testing.T) {
	tm := buildTestMap(t, map[string]string{
		// The production project is nested one level deeper than the test
		// project, so no rewriting of the test path's segments can produce it.
		"backend/src/Domains/Leroy.Certificates/Repositories/LabTestRepository.cs": "class LabTestRepository {}\n",
		"backend/src/Leroy.Api/PublicApi/PatientPhotosController.cs":               "class PatientPhotosController {}\n",
		"backend/src/Leroy.Worker/Jobs/ReminderJob.cs":                             "class ReminderJob {}\n",
		// A same-named decoy outside the project under test: without project
		// scoping the stem would be ambiguous and nothing would map.
		"backend/src/Leroy.Api/Jobs/ReminderJob.cs": "class ReminderJob {}\n",

		"backend/tests/Leroy.Certificates.Tests/LabTestRepositoryFamilyBlendTests.cs": "class LabTestRepositoryFamilyBlendTests {}\n",
		"backend/tests/Leroy.Api.IntegrationTests/PatientPhotosApiTests.cs":           "class PatientPhotosApiTests {}\n",
		"backend/tests/Leroy.Worker.UnitTests/ReminderJobTests.cs":                    "class ReminderJobTests {}\n",
	})

	assertMapped(t, tm,
		"backend/tests/Leroy.Certificates.Tests/LabTestRepositoryFamilyBlendTests.cs",
		"backend/src/Domains/Leroy.Certificates/Repositories/LabTestRepository.cs")
	assertMapped(t, tm,
		"backend/tests/Leroy.Api.IntegrationTests/PatientPhotosApiTests.cs",
		"backend/src/Leroy.Api/PublicApi/PatientPhotosController.cs")
	assertMapped(t, tm,
		"backend/tests/Leroy.Worker.UnitTests/ReminderJobTests.cs",
		"backend/src/Leroy.Worker/Jobs/ReminderJob.cs")
}

// --- Crossing a project boundary on an import edge ---

// buildTestMapWithImports writes a tree and returns its test map, with the
// import graph resolved from the files actually written — so these cases go
// through the same C# using→namespace resolution the real repo does, not a
// hand-written edge list.
func buildTestMapWithImports(t *testing.T, files map[string]string) *TestMap {
	t.Helper()
	root, idx := writeTree(t, files)
	return NewTestMapWithImports(idx, NewDepGraph(root, idx).AllImports())
}

// razorSolution is the shape the mapping used to be blind to: the Razor page
// models live in the web project, and the tests for them live in a test project
// that pairs with a *different* production project (Leroy.Platform). The
// project-mirror scoping confines name matching to Leroy.Platform, which is
// correct — reaching into a neighbouring project on a name alone is exactly what
// the prefix fences forbid — so the only thing that can bridge the gap is the
// test file's own `using`.
//
// Three separate spellings have to line up: the file is Certificates.cshtml.cs
// (stem "Certificates.cshtml"), the class is CertificatesModel, and the test is
// CertificatesPageModelTests.cs.
func razorSolution() map[string]string {
	return map[string]string{
		"backend/src/Leroy.Api/Pages/App/Certificates.cshtml.cs": "namespace Leroy.Api.Pages.App;\npublic class CertificatesModel {}\n",
		"backend/src/Leroy.Api/Pages/App/Contacts.cshtml.cs":     "namespace Leroy.Api.Pages.App;\npublic class ContactsModel {}\n",
		"backend/src/Leroy.Api/Pages/Verify.cshtml.cs":           "namespace Leroy.Api.Pages;\npublic class VerifyModel {}\n",
		"backend/src/Leroy.Platform/Auth/TenantContext.cs":       "namespace Leroy.Platform.Auth;\npublic class TenantContext {}\n",

		"backend/tests/Leroy.Platform.Tests/CertificatesPageModelTests.cs": "using Leroy.Api.Pages.App;\nusing Leroy.Platform.Auth;\nnamespace Leroy.Platform.Tests;\npublic class CertificatesPageModelTests {}\n",
		"backend/tests/Leroy.Platform.Tests/VerifyModelTests.cs":           "using Leroy.Api.Pages;\nnamespace Leroy.Platform.Tests;\npublic class VerifyModelTests {}\n",
	}
}

func TestRazorPageModelIsFoundThroughTheImportGraph(t *testing.T) {
	tm := buildTestMapWithImports(t, razorSolution())

	// "…PageModelTests" → the page model class of Certificates.cshtml.cs.
	assertMapped(t, tm,
		"backend/tests/Leroy.Platform.Tests/CertificatesPageModelTests.cs",
		"backend/src/Leroy.Api/Pages/App/Certificates.cshtml.cs")
	// "…ModelTests" → the class name the framework derives from the page.
	assertMapped(t, tm,
		"backend/tests/Leroy.Platform.Tests/VerifyModelTests.cs",
		"backend/src/Leroy.Api/Pages/Verify.cshtml.cs")
}

// Without the import graph the same tree maps nothing new: the edge is what
// authorises leaving the mirrored project, so a caller that has no graph gets
// the old, narrower answer rather than a guess.
func TestRazorPageModelNeedsTheImportEdge(t *testing.T) {
	tm := buildTestMap(t, razorSolution())
	const tp = "backend/tests/Leroy.Platform.Tests/CertificatesPageModelTests.cs"
	if src := tm.SourceFor(tp); src != "" {
		t.Errorf("mapped %s to %q without an import graph", tp, src)
	}
}

// The one `using Leroy.Api.Pages.App` that reaches Certificates.cshtml.cs
// reaches every other page model in that namespace too, so an import edge can
// never be a mapping by itself. A test whose name matches two of them declines.
func TestAmbiguousImportedNameIsNotMapped(t *testing.T) {
	tm := buildTestMapWithImports(t, map[string]string{
		"backend/src/Leroy.Api/Pages/App/Forms.cshtml.cs":           "namespace Leroy.Api.Pages.App;\npublic class FormsModel {}\n",
		"backend/src/Leroy.Api/Pages/Tenant/Forms.cshtml.cs":        "namespace Leroy.Api.Pages.App;\npublic class FormsModel {}\n",
		"backend/src/Leroy.Platform/Auth/TenantContext.cs":          "namespace Leroy.Platform.Auth;\npublic class TenantContext {}\n",
		"backend/tests/Leroy.Platform.Tests/FormsPageModelTests.cs": "using Leroy.Api.Pages.App;\nnamespace Leroy.Platform.Tests;\npublic class FormsPageModelTests {}\n",
	})
	const tp = "backend/tests/Leroy.Platform.Tests/FormsPageModelTests.cs"
	src, status := tm.LookupSource(tp)
	if src != "" {
		t.Errorf("rivalled imported name mapped %s to %q", tp, src)
	}
	if status != TestMapNoMatch {
		t.Errorf("status for %s = %s, want no_match", tp, status)
	}
}

// The fence this tier needs. A test importing a whole namespace has 150+ files
// in reach, and a shortened stem will find *something* in a pool that size:
// PatientVaccinationOrchestrationTests shortens to "PatientVaccination" and
// lands on the domain model, while its real subject is
// VaccinationCertificateOrchestrationService. Only the full name counts here.
func TestShortenedNameDoesNotMatchAcrossTheImportGraph(t *testing.T) {
	tm := buildTestMapWithImports(t, map[string]string{
		"backend/src/Domains/Leroy.Patients/Models/PatientVaccination.cs":               "namespace Leroy.Patients.Models;\npublic class PatientVaccination {}\n",
		"backend/src/Leroy.Orchestration/VaccinationCertificateOrchestrationService.cs": "namespace Leroy.Orchestration;\npublic class VaccinationCertificateOrchestrationService {}\n",
		"backend/src/Leroy.Platform/Auth/TenantContext.cs":                              "namespace Leroy.Platform.Auth;\npublic class TenantContext {}\n",
		"backend/tests/Leroy.Platform.Tests/PatientVaccinationOrchestrationTests.cs":    "using Leroy.Patients.Models;\nusing Leroy.Orchestration;\nnamespace Leroy.Platform.Tests;\npublic class PatientVaccinationOrchestrationTests {}\n",
	})
	const tp = "backend/tests/Leroy.Platform.Tests/PatientVaccinationOrchestrationTests.cs"
	if src := tm.SourceFor(tp); src != "" {
		t.Errorf("shortened stem mapped %s to %q across the import graph", tp, src)
	}
}

func TestSubjectNames(t *testing.T) {
	cases := map[string][]string{
		"src/App/Pages/Certificates.cshtml.cs": {"Certificates.cshtml", "Certificates", "CertificatesModel", "CertificatesPageModel"},
		"src/App/Counter.razor.cs":             {"Counter.razor", "Counter", "CounterModel", "CounterPageModel"},
		"src/App/CviController.cs":             {"CviController"},
	}
	for path, want := range cases {
		if got := subjectNames(path); !reflect.DeepEqual(got, want) {
			t.Errorf("subjectNames(%q) = %v, want %v", path, got, want)
		}
	}
}

// --- Where the prefix rule stops ---

func TestPrefixMatchDoesNotCrossProjects(t *testing.T) {
	tm := buildTestMap(t, map[string]string{
		"backend/src/Leroy.Platform/Pdf/AprysePdfGenerationService.cs": "class AprysePdfGenerationService {}\n",
		"backend/src/Leroy.Accounts/Users/UserService.cs":              "class UserService {}\n",
		"backend/tests/Leroy.Platform.Tests/AprysePdfGoldenTests.cs":   "class AprysePdfGoldenTests {}\n",
		// Same prefix, different project. Nothing in Leroy.Accounts is named
		// AprysePdf*, and reaching into Leroy.Platform for it would be a lie.
		"backend/tests/Leroy.Accounts.Tests/AprysePdfRogueTests.cs": "class AprysePdfRogueTests {}\n",
	})

	assertMapped(t, tm,
		"backend/tests/Leroy.Platform.Tests/AprysePdfGoldenTests.cs",
		"backend/src/Leroy.Platform/Pdf/AprysePdfGenerationService.cs")

	const rogue = "backend/tests/Leroy.Accounts.Tests/AprysePdfRogueTests.cs"
	src, status := tm.LookupSource(rogue)
	if src != "" {
		t.Errorf("cross-project prefix mapped %s to %q", rogue, src)
	}
	if status != TestMapNoMatch {
		t.Errorf("status for %s = %s, want no_match", rogue, status)
	}
}

func TestAmbiguousPrefixIsNotMapped(t *testing.T) {
	// Three sources in the project under test wear the prefix. Picking the
	// shortest or the alphabetically first would be a coin toss.
	tm := buildTestMap(t, map[string]string{
		"backend/src/Leroy.Documents/DocumentFieldTypes.cs":                     "class DocumentFieldTypes {}\n",
		"backend/src/Leroy.Documents/DocumentFieldSources.cs":                   "class DocumentFieldSources {}\n",
		"backend/src/Leroy.Documents/DocumentFieldValidator.cs":                 "class DocumentFieldValidator {}\n",
		"backend/tests/Leroy.Documents.Tests/DocumentFieldRepositoryDbTests.cs": "class DocumentFieldRepositoryDbTests {}\n",
	})
	const tp = "backend/tests/Leroy.Documents.Tests/DocumentFieldRepositoryDbTests.cs"
	src, status := tm.LookupSource(tp)
	if src != "" {
		t.Errorf("ambiguous prefix mapped %s to %q", tp, src)
	}
	if status != TestMapNoMatch {
		t.Errorf("status for %s = %s, want no_match", tp, status)
	}
}

func TestSingleWordPrefixIsNotEnough(t *testing.T) {
	// "Invoice" alone would tie every Invoice* source to every Invoice* test.
	tm := buildTestMap(t, map[string]string{
		"backend/src/Leroy.Billing/InvoiceGenerator.cs":           "class InvoiceGenerator {}\n",
		"backend/tests/Leroy.Billing.Tests/InvoiceGoldenTests.cs": "class InvoiceGoldenTests {}\n",
	})
	const tp = "backend/tests/Leroy.Billing.Tests/InvoiceGoldenTests.cs"
	if src := tm.SourceFor(tp); src != "" {
		t.Errorf("one-word prefix mapped %s to %q", tp, src)
	}
}

func TestSourceNamedLikeATestDoesNotClaimItsNeighbour(t *testing.T) {
	// LabTest.cs is a domain model, but its name ends in "Test" so it is
	// classified as a test file. It must not go on to claim the
	// longer-named file next to it as its subject — a file's own untouched
	// stem is not evidence of anything.
	tm := buildTestMap(t, map[string]string{
		"backend/src/Leroy.Certificates/Models/LabTest.cs":     "class LabTest {}\n",
		"backend/src/Leroy.Certificates/Models/LabTestData.cs": "class LabTestData {}\n",
	})
	if src := tm.SourceFor("backend/src/Leroy.Certificates/Models/LabTest.cs"); src != "" {
		t.Errorf("LabTest.cs mapped to %q, want no mapping", src)
	}
}

// --- Languages that had no rules at all ---

// These go through a real walk, so they cover both halves of the gap: the
// scanner classifying the file as a test, and the test map having a rule that
// ties it to its source.
func TestPreviouslyUnsupportedLanguagesAreMapped(t *testing.T) {
	cases := []struct {
		name   string
		files  map[string]string
		test   string
		source string
	}{
		{
			name:   "c",
			files:  map[string]string{"src/parser.c": "int p(void){return 0;}\n", "src/parser_test.c": "int main(void){return 0;}\n"},
			test:   "src/parser_test.c",
			source: "src/parser.c",
		},
		{
			name:   "c header",
			files:  map[string]string{"src/parser.h": "int p(void);\n", "test/parser_test.c": "int main(void){return 0;}\n"},
			test:   "test/parser_test.c",
			source: "src/parser.h",
		},
		{
			name:   "cpp",
			files:  map[string]string{"src/engine.cpp": "int e(){return 0;}\n", "test/engine_test.cpp": "int main(){return 0;}\n"},
			test:   "test/engine_test.cpp",
			source: "src/engine.cpp",
		},
		{
			name:   "lua",
			files:  map[string]string{"src/util.lua": "return {}\n", "spec/util_spec.lua": "describe('x')\n"},
			test:   "spec/util_spec.lua",
			source: "src/util.lua",
		},
		{
			name:   "julia",
			files:  map[string]string{"src/solver.jl": "module S end\n", "test/test_solver.jl": "using Test\n"},
			test:   "test/test_solver.jl",
			source: "src/solver.jl",
		},
		{
			name:   "zig",
			files:  map[string]string{"src/alloc.zig": "pub fn a() void {}\n", "src/alloc_test.zig": "test \"a\" {}\n"},
			test:   "src/alloc_test.zig",
			source: "src/alloc.zig",
		},
		{
			name:   "rust",
			files:  map[string]string{"src/lexer.rs": "pub fn l() {}\n", "src/lexer_test.rs": "#[test]\nfn t() {}\n"},
			test:   "src/lexer_test.rs",
			source: "src/lexer.rs",
		},
		{
			name:   "shell",
			files:  map[string]string{"scripts/deploy.sh": "#!/bin/sh\necho x\n", "tests/deploy_test.sh": "#!/bin/sh\necho y\n"},
			test:   "tests/deploy_test.sh",
			source: "scripts/deploy.sh",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tm := buildTestMap(t, tc.files)
			assertMapped(t, tm, tc.test, tc.source)
		})
	}
}

// --- Established conventions must keep working ---

func TestEstablishedConventions(t *testing.T) {
	cases := []struct {
		name   string
		files  map[string]string
		test   string
		source string
	}{
		{
			name:   "go same dir",
			files:  map[string]string{"pkg/a.go": "package pkg\n", "pkg/a_test.go": "package pkg\n"},
			test:   "pkg/a_test.go",
			source: "pkg/a.go",
		},
		{
			name:   "python test_ prefix in mirrored tests dir",
			files:  map[string]string{"app/orders.py": "x = 1\n", "tests/test_orders.py": "y = 1\n"},
			test:   "tests/test_orders.py",
			source: "app/orders.py",
		},
		{
			name: "java maven layout",
			files: map[string]string{
				"src/main/java/com/ex/Order.java":     "package com.ex;\nclass Order {}\n",
				"src/test/java/com/ex/OrderTest.java": "package com.ex;\nclass OrderTest {}\n",
			},
			test:   "src/test/java/com/ex/OrderTest.java",
			source: "src/main/java/com/ex/Order.java",
		},
		{
			name: "dotnet parallel project",
			files: map[string]string{
				"src/MyApp/Order.cs":            "class Order {}\n",
				"src/MyApp.Tests/OrderTests.cs": "class OrderTests {}\n",
			},
			test:   "src/MyApp.Tests/OrderTests.cs",
			source: "src/MyApp/Order.cs",
		},
		{
			name: "swift package layout",
			files: map[string]string{
				"Sources/Orders/Cart.swift":         "struct Cart {}\n",
				"Tests/OrdersTests/CartTests.swift": "import XCTest\n",
			},
			test:   "Tests/OrdersTests/CartTests.swift",
			source: "Sources/Orders/Cart.swift",
		},
		{
			name: "elixir test mirrors lib",
			files: map[string]string{
				"lib/app/orders.ex":        "defmodule App.Orders do\nend\n",
				"test/app/orders_test.exs": "defmodule App.OrdersTest do\nend\n",
			},
			test:   "test/app/orders_test.exs",
			source: "lib/app/orders.ex",
		},
		{
			name: "ruby spec mirrors app",
			files: map[string]string{
				"app/models/user.rb":       "class User; end\n",
				"spec/models/user_spec.rb": "describe User do\nend\n",
			},
			test:   "spec/models/user_spec.rb",
			source: "app/models/user.rb",
		},
		{
			name: "php psr-4 tests/Unit",
			files: map[string]string{
				"src/Payment.php":            "<?php class Payment {}\n",
				"tests/Unit/PaymentTest.php": "<?php class PaymentTest {}\n",
			},
			test:   "tests/Unit/PaymentTest.php",
			source: "src/Payment.php",
		},
		{
			name: "dart test mirrors lib",
			files: map[string]string{
				"lib/models/user.dart":       "class User {}\n",
				"test/models/user_test.dart": "void main() {}\n",
			},
			test:   "test/models/user_test.dart",
			source: "lib/models/user.dart",
		},
		{
			name: "scala sbt layout",
			files: map[string]string{
				"src/main/scala/Order.scala":     "class Order\n",
				"src/test/scala/OrderSpec.scala": "class OrderSpec\n",
			},
			test:   "src/test/scala/OrderSpec.scala",
			source: "src/main/scala/Order.scala",
		},
		{
			name: "typescript sibling spec",
			files: map[string]string{
				"src/cart.ts":      "export const c = 1;\n",
				"src/cart.spec.ts": "test('c', () => {});\n",
			},
			test:   "src/cart.spec.ts",
			source: "src/cart.ts",
		},
		{
			name: "tsx test maps to tsx source",
			files: map[string]string{
				"src/Button.tsx":      "export const B = () => null;\n",
				"src/Button.test.tsx": "test('b', () => {});\n",
			},
			test:   "src/Button.test.tsx",
			source: "src/Button.tsx",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tm := buildTestMap(t, tc.files)
			assertMapped(t, tm, tc.test, tc.source)
		})
	}
}

// --- "I don't know" is not "no" ---

func TestUnsupportedLanguageIsDistinguishableFromNoTests(t *testing.T) {
	tm := buildTestMap(t, map[string]string{
		"src/main.hs":  "main = return ()\n",
		"src/thing.go": "package src\n",
	})

	if _, status := tm.LookupTests("src/main.hs"); status != TestMapUnsupported {
		t.Errorf("Haskell status = %s, want unsupported", status)
	}
	if _, status := tm.LookupTests("src/thing.go"); status != TestMapNoMatch {
		t.Errorf("Go status = %s, want no_match", status)
	}
	if !SupportsTestMapping("a/b.go") || SupportsTestMapping("a/b.hs") {
		t.Error("SupportsTestMapping disagrees with LookupTests")
	}
}

func TestUnmappedTestsAreReported(t *testing.T) {
	tm := buildTestMap(t, map[string]string{
		"src/a.go":                 "package src\n",
		"src/a_test.go":            "package src\n",
		"src/nothing_here_test.go": "package src\n",
	})
	if got := tm.UnmappedTests(); !reflect.DeepEqual(got, []string{"src/nothing_here_test.go"}) {
		t.Errorf("UnmappedTests = %v", got)
	}
}

func TestUnsupportedTestFileIsNotCountedAsUnmapped(t *testing.T) {
	// A test recon has no rules for is not evidence of a broken mapping.
	tm := buildTestMap(t, map[string]string{
		"test/thing_test.hs": "main = return ()\n",
	})
	if got := tm.UnmappedTests(); len(got) != 0 {
		t.Errorf("UnmappedTests = %v, want empty", got)
	}
}

// --- No fabricated mappings ---

func TestAmbiguousDistantStemIsNotMapped(t *testing.T) {
	// Two plausible "user.go" sources in unrelated trees and a test that
	// lives near neither: guessing one would be worse than saying nothing.
	tm := buildTestMap(t, map[string]string{
		"services/billing/user.go": "package billing\n",
		"services/auth/user.go":    "package auth\n",
		"qa/cases/user_test.go":    "package cases\n",
	})
	if src := tm.SourceFor("qa/cases/user_test.go"); src != "" {
		t.Errorf("mapped to %q, want no mapping", src)
	}
}

func TestSameDirectoryBeatsMirroredDirectory(t *testing.T) {
	tm := buildTestMap(t, map[string]string{
		"src/cart.ts":             "export const a = 1;\n",
		"src/nested/cart.ts":      "export const b = 1;\n",
		"src/nested/cart.test.ts": "test('x', () => {});\n",
	})
	assertMapped(t, tm, "src/nested/cart.test.ts", "src/nested/cart.ts")
}

func TestTestFileIsNeverItsOwnSource(t *testing.T) {
	tm := buildTestMap(t, map[string]string{
		"src/a_test.go": "package src\n",
	})
	if src := tm.SourceFor("src/a_test.go"); src != "" {
		t.Errorf("mapped to itself: %q", src)
	}
}

// --- Determinism ---

func TestTestMapIsDeterministic(t *testing.T) {
	files := map[string]string{
		"src/a.ts":                "export const a = 1;\n",
		"src/b.ts":                "export const b = 1;\n",
		"src/a.test.ts":           "test('a', () => {});\n",
		"src/b.test.ts":           "test('b', () => {});\n",
		"src/__tests__/a.test.js": "test('a2', () => {});\n",
		"pkg/x.go":                "package pkg\n",
		"pkg/x_test.go":           "package pkg\n",
	}
	_, idx := writeTree(t, files)
	want := NewTestMap(idx)
	for i := 0; i < 20; i++ {
		got := NewTestMap(idx)
		if !reflect.DeepEqual(want.AllMappings(), got.AllMappings()) {
			t.Fatalf("run %d differs:\n%v\n%v", i, want.AllMappings(), got.AllMappings())
		}
		if !reflect.DeepEqual(want.TestToSourceMap(), got.TestToSourceMap()) {
			t.Fatalf("run %d reverse map differs", i)
		}
	}
}

// --- Unit-level helpers ---

func TestCandidateStems(t *testing.T) {
	conv := testConventions[".go"]
	exact, fuzzy := candidateStems("depgraph_resolve_test", conv)
	if len(exact) == 0 || exact[0] != "depgraph_resolve" {
		t.Errorf("exact = %v", exact)
	}
	if !containsStr(fuzzy, "depgraph") {
		t.Errorf("fuzzy = %v, want depgraph", fuzzy)
	}
}

func TestCandidateStemsCamelCase(t *testing.T) {
	conv := testConventions[".java"]
	exact, fuzzy := candidateStems("CheckoutFlowTest", conv)
	if !containsStr(exact, "CheckoutFlow") {
		t.Errorf("exact = %v", exact)
	}
	if !containsStr(fuzzy, "Checkout") {
		t.Errorf("fuzzy = %v", fuzzy)
	}
}

func TestDotNetTestDirBase(t *testing.T) {
	cases := map[string]string{
		"MyApp.Tests":                "MyApp",
		"Leroy.Platform.Tests":       "Leroy.Platform",
		"Leroy.Api.IntegrationTests": "Leroy.Api",
		"Leroy.Worker.UnitTests":     "Leroy.Worker",
		"Payments.AcceptanceSpecs":   "Payments",
		"Leroy.Api.Contracts":        "", // a production project, not a test one
		"Leroy.Platform":             "",
		"MyApp":                      "",
		".Tests":                     "",
	}
	for seg, want := range cases {
		if got := dotNetTestDir(seg); got != want {
			t.Errorf("dotNetTestDir(%q) = %q, want %q", seg, got, want)
		}
	}
}

func TestNameComponentCount(t *testing.T) {
	cases := map[string]int{
		"AprysePdf":                  2,
		"Pdf":                        1,
		"AprysePdfGenerationService": 4,
		"checkout_flow":              2,
		"checkout":                   1,
		"UserDetail.cshtml":          3,
	}
	for stem, want := range cases {
		if got := nameComponentCount(stem); got != want {
			t.Errorf("nameComponentCount(%q) = %d, want %d", stem, got, want)
		}
	}
}

func TestMirrorDirsRewritesTestSegments(t *testing.T) {
	dirs := mirrorDirs("test/src")
	if !dirs["src"] {
		t.Errorf("mirrorDirs(test/src) missing src: %v", keysOf(dirs))
	}
	dirs = mirrorDirs("src/test/java/com/ex")
	if !dirs["src/main/java/com/ex"] {
		t.Errorf("mirrorDirs missing maven main path: %v", keysOf(dirs))
	}
}

func TestClassifyTestKind(t *testing.T) {
	cases := map[string]string{
		"e2e/checkout.spec.ts":         "e2e",
		"tests/integration/db_test.go": "integration",
		"internal/index/grep_test.go":  "unit",
		"playwright/login.spec.ts":     "e2e",
	}
	for path, want := range cases {
		if got := ClassifyTestKind(path); got != want {
			t.Errorf("ClassifyTestKind(%q) = %q, want %q", path, got, want)
		}
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
