package index

import (
	"testing"
)

// runSearch builds a real index over a written tree and searches it.
func runSearch(t *testing.T, query string, files map[string]string) []SearchResult {
	t.Helper()
	root, idx := writeTree(t, files)
	symbols := NewSymbolIndex(root, idx)
	return Search(query, root, idx, symbols, nil, 30)
}

// rankOf returns the 0-based position of path in results, or -1.
func rankOf(results []SearchResult, path string) int {
	for i, r := range results {
		if r.Path == path {
			return i
		}
	}
	return -1
}

func scoreOf(results []SearchResult, path string) float64 {
	for _, r := range results {
		if r.Path == path {
			return r.Score
		}
	}
	return 0
}

func resultPaths(results []SearchResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.Path)
	}
	return out
}

// csharpService is the shape that made this tool answer "where is X" with the
// tests for X: a service, its interface, and a test class one directory over.
func csharpService() map[string]string {
	return map[string]string{
		"backend/src/Domains/Leroy.Accounts/AccountsService.cs": `namespace Leroy.Accounts;

public class AccountsService : IAccountsService
{
    public void Register() { }
}
`,
		"backend/src/Domains/Leroy.Accounts/IAccountsService.cs": `namespace Leroy.Accounts;

public interface IAccountsService
{
    void Register();
}
`,
		"backend/tests/Leroy.Accounts.Tests/AccountsServiceTests.cs": `namespace Leroy.Accounts.Tests;

public class AccountsServiceTests
{
    public void RegisterCreatesUser() { }
}
`,
	}
}

// Every scoring tier accumulates into one per-file total that is clamped to
// 1.0, so the service (exact symbol 1.0 + basename 0.6) and its test class
// (prefix symbol 0.9 + basename 0.6) both saturate and the alphabetical path
// tiebreak decides — "backend/tests/..." beat "backend/src/..." nowhere near
// often enough to be safe, and on the real tree it put MeApiTests.cs above
// MeApi.cs.
func TestImplementationOutranksItsTestsOnExactClassName(t *testing.T) {
	results := runSearch(t, "AccountsService", csharpService())

	impl := rankOf(results, "backend/src/Domains/Leroy.Accounts/AccountsService.cs")
	test := rankOf(results, "backend/tests/Leroy.Accounts.Tests/AccountsServiceTests.cs")
	if impl < 0 || test < 0 {
		t.Fatalf("expected both files in results, got %v", resultPaths(results))
	}
	if impl > test {
		t.Errorf("test file ranked above implementation: %v", resultPaths(results))
	}
	if s := scoreOf(results, "backend/tests/Leroy.Accounts.Tests/AccountsServiceTests.cs"); s >= 1.0 {
		t.Errorf("test file score = %v, want demoted below 1.0", s)
	}
}

// The demotion is a ranking signal, not a filter: the tests for a thing are
// still part of the answer to "where is that thing", just not the first part.
func TestTestsAreDemotedNotExcluded(t *testing.T) {
	results := runSearch(t, "AccountsService", csharpService())

	if rankOf(results, "backend/tests/Leroy.Accounts.Tests/AccountsServiceTests.cs") < 0 {
		t.Fatalf("test file dropped from results entirely: %v", resultPaths(results))
	}
	// It must stay well above the weakest tiers, not be buried at the bottom.
	if s := scoreOf(results, "backend/tests/Leroy.Accounts.Tests/AccountsServiceTests.cs"); s <= 0.6 {
		t.Errorf("test file score = %v, want above the file-path tier (0.6)", s)
	}
}

// Someone who types the test class name gets the test class, at full score.
func TestTestFileFoundWhenNamedDirectly(t *testing.T) {
	results := runSearch(t, "AccountsServiceTests", csharpService())

	if len(results) == 0 {
		t.Fatal("no results")
	}
	want := "backend/tests/Leroy.Accounts.Tests/AccountsServiceTests.cs"
	if results[0].Path != want {
		t.Errorf("top result = %s, want %s (%v)", results[0].Path, want, resultPaths(results))
	}
	if results[0].Score < 1.0 {
		t.Errorf("exactly-named test scored %v, want no demotion", results[0].Score)
	}
}

// The same defect, the same fix, in a language with a completely different test
// naming convention. Go's `_test.go` suffix sorts after `.go`, so the
// alphabetical tiebreak happens to hide the defect here; the signal must still
// be applied, or the moment a Go test lives in a directory that sorts first the
// same failure returns.
func TestImplementationOutranksItsTestsInGo(t *testing.T) {
	results := runSearch(t, "TestMap", map[string]string{
		"internal/index/testmap.go": `package index

type TestMap struct{}

func NewTestMap() *TestMap { return &TestMap{} }
`,
		"internal/index/testmap_test.go": `package index

import "testing"

func TestTestMapLookup(t *testing.T) {}
`,
	})

	impl := rankOf(results, "internal/index/testmap.go")
	test := rankOf(results, "internal/index/testmap_test.go")
	if impl < 0 || test < 0 {
		t.Fatalf("expected both files in results, got %v", resultPaths(results))
	}
	if impl > test {
		t.Errorf("test file ranked above implementation: %v", resultPaths(results))
	}
	if implScore, testScore := scoreOf(results, "internal/index/testmap.go"),
		scoreOf(results, "internal/index/testmap_test.go"); testScore >= implScore {
		t.Errorf("Go test scored %v vs implementation %v, want strictly lower", testScore, implScore)
	}
}

// A query whose whole word is "tests" is asking for tests. Demoting them there
// would be fighting the query.
func TestQueryAskingForTestsIsNotDemoted(t *testing.T) {
	results := runSearch(t, "accounts service tests", csharpService())

	if len(results) == 0 {
		t.Fatal("no results")
	}
	want := "backend/tests/Leroy.Accounts.Tests/AccountsServiceTests.cs"
	if results[0].Path != want {
		t.Errorf("top result = %s, want %s (%v)", results[0].Path, want, resultPaths(results))
	}
	if results[0].Score < 1.0 {
		t.Errorf("score = %v, want no demotion for a test-directed query", results[0].Score)
	}
}

// "test" is a domain noun in plenty of codebases — a veterinary tree has
// LabTest, LabTestData, LabTestRepository. Backing the demotion off on a
// *suffix* would disable it for that whole domain, so only whole words count.
func TestDomainNounEndingInTestStillDemotesTests(t *testing.T) {
	results := runSearch(t, "LabTest", map[string]string{
		"backend/src/Domains/Leroy.Certificates/Models/LabTest.cs": `namespace Leroy.Certificates.Models;

public class LabTest
{
    public string Result { get; set; }
}
`,
		"backend/tests/Leroy.Certificates.Tests/LabTestTests.cs": `namespace Leroy.Certificates.Tests;

public class LabTestTests
{
    public void ResultRoundTrips() { }
}
`,
	})

	impl := rankOf(results, "backend/src/Domains/Leroy.Certificates/Models/LabTest.cs")
	test := rankOf(results, "backend/tests/Leroy.Certificates.Tests/LabTestTests.cs")
	if impl < 0 || test < 0 {
		t.Fatalf("expected both files in results, got %v", resultPaths(results))
	}
	if impl > test {
		t.Errorf("test file ranked above implementation: %v", resultPaths(results))
	}
}

// An E2E suite with no same-named source file is the right answer to its own
// name. The demotion has to be small enough that a weak content match on an
// unrelated source file cannot displace it.
func TestTestStaysTopWhenItIsTheOnlyRealAnswer(t *testing.T) {
	results := runSearch(t, "OwnerPortal", map[string]string{
		"backend/tests/Leroy.E2E/OwnerPortalTests.cs": `namespace Leroy.E2E;

public class OwnerPortalTests
{
    public void OwnerSeesCertificates() { }
}
`,
		// Mentions the term in passing only: a content-tier match.
		"backend/src/Leroy.Api/Pages/App/Certificates.cshtml.cs": `namespace Leroy.Api.Pages;

public class CertificatesModel
{
    // link target for the OwnerPortal view
    public string Url { get; set; }
}
`,
	})

	if len(results) == 0 {
		t.Fatal("no results")
	}
	want := "backend/tests/Leroy.E2E/OwnerPortalTests.cs"
	if results[0].Path != want {
		t.Errorf("top result = %s, want %s (%v)", results[0].Path, want, resultPaths(results))
	}
}

// When nothing but tests matched there is no implementation to promote, so the
// scores are reported as-is rather than uniformly deflated.
func TestNoDemotionWhenOnlyTestsMatch(t *testing.T) {
	results := runSearch(t, "AprysePdfGolden", map[string]string{
		"backend/tests/Leroy.Platform.Tests/AprysePdfGoldenTests.cs": `namespace Leroy.Platform.Tests;

public class AprysePdfGoldenTests
{
    public void GoldenFilesMatch() { }
}
`,
		"backend/src/Leroy.Platform/Unrelated.cs": "namespace Leroy.Platform;\n\npublic class Unrelated { }\n",
	})

	if len(results) == 0 {
		t.Fatal("no results")
	}
	if results[0].Path != "backend/tests/Leroy.Platform.Tests/AprysePdfGoldenTests.cs" {
		t.Fatalf("top result = %s", results[0].Path)
	}
	if results[0].Score < 1.0 {
		t.Errorf("score = %v, want 1.0 when no implementation competes", results[0].Score)
	}
}
