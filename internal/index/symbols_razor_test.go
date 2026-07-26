package index

import (
	"sort"
	"strings"
	"testing"
)

// Tests for Razor (.cshtml / .razor) extraction.
//
// The defect these exist for: .cshtml was classified as C# and handed to the C#
// grammar. On a 13k-file ASP.NET repo all 173 .cshtml files came back "partial"
// with "tree-sitter (csharp) reported syntax errors", 172 of them with zero
// symbols, they made up 173 of the repo's 192 parse caveats, and they counted
// toward a reported 78.9% C#. No view had a single dependency edge, so the page
// model a view binds to — the most useful fact in the file — was nowhere.

// contactsView is the shape of a real Razor Pages view: a comment holding text
// that must not be read as directives, directives, an inline code block, and
// markup.
const contactsView = `@page "/contacts"
@*
    @model Not.A.Real.Directive
    @using Not.A.Real.Import
*@
@using Leroy.Contacts
@using Microsoft.AspNetCore.Authorization
@inject ITenantContext TenantCtx
@attribute [Authorize]
@model Leroy.Api.Pages.App.ContactsModel
@{
    var title = Model.Title;
}
<div class="@title">
    @using (Html.BeginForm())
    {
        <span>@Html.Raw(Model.Body)</span>
    }
</div>
`

func symKinds(si *SymbolIndex, file string) map[string]string {
	out := make(map[string]string)
	for _, s := range si.ForFile(file) {
		out[s.Name] = s.Kind
	}
	return out
}

func TestRazorIsNotParsedAsCSharp(t *testing.T) {
	root, idx := writeTree(t, map[string]string{
		"Pages/Contacts.cshtml": contactsView,
	})

	f := idx.Get("Pages/Contacts.cshtml")
	if f == nil {
		t.Fatal("view not indexed")
	}
	if f.Lang != razorLang {
		t.Errorf("lang = %q, want %q", f.Lang, razorLang)
	}

	si := NewSymbolIndex(root, idx)
	fp := parseFor(t, si, "Pages/Contacts.cshtml")

	// The whole point: no syntax-error caveat, and the file is not reported as
	// something a grammar read.
	if fp.Status != ParseOK {
		t.Errorf("status = %q (%s), want %q", fp.Status, fp.Detail, ParseOK)
	}
	if strings.Contains(fp.Detail, "syntax errors") {
		t.Errorf("razor still reports a C# syntax error: %q", fp.Detail)
	}
	// ...but the result must still read as approximate, never as a confident
	// grammar parse.
	if fp.Extractor != ExtractorRegex {
		t.Errorf("extractor = %q, want %q", fp.Extractor, ExtractorRegex)
	}
	for _, s := range si.ForFile("Pages/Contacts.cshtml") {
		if s.Extractor != ExtractorRegex {
			t.Errorf("symbol %s: extractor = %q, want %q", s.Name, s.Extractor, ExtractorRegex)
		}
	}
	if len(si.Incomplete()) != 0 {
		t.Errorf("Incomplete() = %v, want none", si.Incomplete())
	}
}

func TestRazorDirectivesAreExtracted(t *testing.T) {
	root, idx := writeTree(t, map[string]string{
		"Pages/Contacts.cshtml": contactsView,
	})
	si := NewSymbolIndex(root, idx)

	kinds := symKinds(si, "Pages/Contacts.cshtml")
	want := map[string]string{
		"Contacts":      "page",     // bare page name, from the file
		"/contacts":     "route",    // the declared route
		"ContactsModel": "model",    // the page model the view binds to
		"TenantCtx":     "property", // @inject declares a member
		"Authorize":     "attribute",
	}
	for name, kind := range want {
		if kinds[name] != kind {
			t.Errorf("symbol %q = kind %q, want %q (got %v)", name, kinds[name], kind, kinds)
		}
	}

	// A @using is an import, not a declaration — exactly as in C#, where a
	// using is a graph edge and never a symbol.
	for _, s := range si.ForFile("Pages/Contacts.cshtml") {
		if s.Name == "Leroy.Contacts" || s.Name == "Contacts" && s.Kind != "page" {
			t.Errorf("@using reported as a symbol: %+v", s)
		}
	}

	// The @model line, not the file's first line.
	for _, s := range si.ForFile("Pages/Contacts.cshtml") {
		if s.Name == "ContactsModel" && s.Line != 10 {
			t.Errorf("@model line = %d, want 10", s.Line)
		}
	}
}

func TestRazorCommentedDirectivesAreNotExtracted(t *testing.T) {
	// Commenting a directive out with @* ... *@ is how it stops applying, so a
	// directive inside one is not there.
	root, idx := writeTree(t, map[string]string{
		"Pages/Contacts.cshtml": contactsView,
	})
	si := NewSymbolIndex(root, idx)

	for _, s := range si.ForFile("Pages/Contacts.cshtml") {
		if s.Name == "Directive" || strings.Contains(s.Signature, "Not.A.Real") {
			t.Errorf("directive inside an @* *@ comment was extracted: %+v", s)
		}
	}
}

func TestRazorUsingStatementIsNotAnImport(t *testing.T) {
	// `@using (Html.BeginForm())` is a C# using *statement* in markup. Reading
	// it as an import would invent a dependency on a namespace called "Html".
	specs := razorSpecsFromData([]byte(contactsView))
	for _, s := range specs {
		if strings.Contains(s, "Html") {
			t.Errorf("using statement read as an import: %q (all: %v)", s, specs)
		}
	}
	want := map[string]bool{
		"using:Leroy.Contacts":                     true,
		"using:Microsoft.AspNetCore.Authorization": true,
		"type:Leroy.Api.Pages.App.ContactsModel":   true,
	}
	for _, s := range specs {
		if !want[s] {
			t.Errorf("unexpected spec %q (all: %v)", s, specs)
		}
		delete(want, s)
	}
	for s := range want {
		t.Errorf("missing spec %q (got %v)", s, specs)
	}
}

func TestRazorCodeBlockMembers(t *testing.T) {
	// The 6-in-173 case: a @functions block holds real C# members, and Blazor
	// .razor files put everything in @code. Members are reported; the locals
	// inside a member body are not.
	root, idx := writeTree(t, map[string]string{
		"Pages/_Results.cshtml": `@model ResultsModel
@functions {
    bool IsEnabled(string t) =>
        Model.Enabled.Contains(t);

    static string Label(string? r)
    {
        var fallback = "none";
        return r ?? fallback;
    }
}
<div>@Label(Model.Result)</div>
`,
		"Components/Counter.razor": `@page "/counter"
@code {
    [Parameter] public int Start { get; set; }

    private int current;

    private void Increment()
    {
        var step = 1;
        current += step;
    }
}
<button @onclick="Increment">@current</button>
`,
	})
	si := NewSymbolIndex(root, idx)

	kinds := symKinds(si, "Pages/_Results.cshtml")
	if kinds["IsEnabled"] != "method" || kinds["Label"] != "method" {
		t.Errorf("@functions members = %v, want IsEnabled and Label as methods", kinds)
	}
	if _, ok := kinds["fallback"]; ok {
		t.Errorf("a local inside a member body was reported as a declaration: %v", kinds)
	}
	for _, s := range si.ForFile("Pages/_Results.cshtml") {
		if s.Name == "IsEnabled" && s.Line != 3 {
			t.Errorf("IsEnabled line = %d, want 3 (file lines, not block-relative)", s.Line)
		}
	}

	kinds = symKinds(si, "Components/Counter.razor")
	if kinds["Start"] != "property" || kinds["Increment"] != "method" {
		t.Errorf("@code members = %v, want Start property and Increment method", kinds)
	}
	if _, ok := kinds["step"]; ok {
		t.Errorf("a local inside a member body was reported as a declaration: %v", kinds)
	}
}

func TestRazorMarkupOnlyPartialIsHonestlyEmpty(t *testing.T) {
	// An icon partial or an email template declares nothing at all. Zero
	// symbols is then the true answer, and it must come with a clean status —
	// not the "partial, syntax errors" that 172 of these produced before.
	root, idx := writeTree(t, map[string]string{
		"Pages/Shared/_Icon.cshtml": "<svg viewBox=\"0 0 24 24\"><path d=\"M0 0\"/></svg>\n",
	})
	si := NewSymbolIndex(root, idx)

	if syms := si.ForFile("Pages/Shared/_Icon.cshtml"); len(syms) != 0 {
		t.Errorf("markup-only partial reported symbols: %v", syms)
	}
	if fp := parseFor(t, si, "Pages/Shared/_Icon.cshtml"); fp.Status != ParseOK {
		t.Errorf("status = %q (%s), want %q", fp.Status, fp.Detail, ParseOK)
	}
}

// ─── Code-behind must be untouched ───────────────────────────────────────────

func TestRazorCodeBehindStaysCSharp(t *testing.T) {
	// Contacts.cshtml.cs is ordinary C#. The extension logic keys on the final
	// extension, so it must keep its C# language, its grammar and its symbols.
	root, idx := writeTree(t, map[string]string{
		"Pages/Contacts.cshtml": contactsView,
		"Pages/Contacts.cshtml.cs": `namespace Leroy.Api.Pages.App;

public class ContactsModel : PageModel
{
    public string Title { get; set; } = "";

    public void OnGet()
    {
    }
}
`,
		"Components/Counter.razor.cs": "namespace App.Components;\npublic partial class Counter { public void Tick() {} }\n",
	})

	for _, p := range []string{"Pages/Contacts.cshtml.cs", "Components/Counter.razor.cs"} {
		f := idx.Get(p)
		if f == nil {
			t.Fatalf("%s not indexed", p)
		}
		if f.Lang != "csharp" {
			t.Errorf("%s lang = %q, want csharp", p, f.Lang)
		}
	}

	si := NewSymbolIndex(root, idx)
	fp := parseFor(t, si, "Pages/Contacts.cshtml.cs")
	if fp.Status != ParseOK || fp.Extractor != ExtractorTreeSitter {
		t.Errorf("code-behind parse = %s/%s (%s), want ok/tree-sitter", fp.Status, fp.Extractor, fp.Detail)
	}
	wantNames(t, symNames(si, "Pages/Contacts.cshtml.cs"),
		[]string{"ContactsModel", "OnGet", "Title"}, "Pages/Contacts.cshtml.cs")
	wantNames(t, symNames(si, "Components/Counter.razor.cs"),
		[]string{"Counter", "Tick"}, "Components/Counter.razor.cs")
}

// ─── Dependency graph ────────────────────────────────────────────────────────

func TestRazorViewImportsResolve(t *testing.T) {
	// @using resolves through the C# namespace map (the same resolver the .cs
	// files use), and @model resolves to the page-model file — which the
	// namespace alone cannot do, since Contacts.cshtml.cs shares its namespace
	// with every other page in the folder.
	dg := buildGraph(t, map[string]string{
		"src/Domain/Contacts/ContactsService.cs":  "namespace Leroy.Contacts;\npublic class ContactsService {}\n",
		"src/Domain/Contacts/IContactsService.cs": "namespace Leroy.Contacts;\npublic interface IContactsService {}\n",
		"src/Api/Pages/App/Contacts.cshtml":       contactsView,
		"src/Api/Pages/App/Contacts.cshtml.cs":    "namespace Leroy.Api.Pages.App;\npublic class ContactsModel {}\n",
		"src/Api/Pages/App/Patients.cshtml.cs":    "namespace Leroy.Api.Pages.App;\npublic class PatientsModel {}\n",
		"src/Api/Pages/App/_Sidebar.cshtml.cs":    "namespace Leroy.Api.Pages.App;\npublic class SidebarModel {}\n",
	})

	assertEdges(t, dg, "src/Api/Pages/App/Contacts.cshtml",
		"src/Domain/Contacts/ContactsService.cs",
		"src/Domain/Contacts/IContactsService.cs",
		"src/Api/Pages/App/Contacts.cshtml.cs",
	)
	// The namespace holds three page models; only the one @model names is an
	// edge.
	assertNoEdge(t, dg, "src/Api/Pages/App/Contacts.cshtml", "src/Api/Pages/App/Patients.cshtml.cs")

	// And the edge is visible from the other end: "where is the view for
	// ContactsModel".
	views := dg.ImportedBy("src/Api/Pages/App/Contacts.cshtml.cs")
	if len(views) != 1 || views[0] != "src/Api/Pages/App/Contacts.cshtml" {
		t.Errorf("ImportedBy(code-behind) = %v, want the view", views)
	}
}

func TestRazorModelDoesNotGuessBetweenRivals(t *testing.T) {
	// Two FormsModel classes in different folders is a real .NET layout. A bare
	// @model that could mean either must produce no edge rather than a guess:
	// a fabricated edge inflates fan-in and corrupts hotspot ranking.
	dg := buildGraph(t, map[string]string{
		"src/Api/Pages/App/Forms.cshtml.cs":    "namespace Leroy.Api.Pages.App;\npublic class FormsModel {}\n",
		"src/Api/Pages/Tenant/Forms.cshtml.cs": "namespace Leroy.Api.Pages.Tenant;\npublic class FormsModel {}\n",
		"src/Api/Pages/App/Forms.cshtml":       "@page\n@model FormsModel\n<div></div>\n",
	})

	assertEdges(t, dg, "src/Api/Pages/App/Forms.cshtml")
}

func TestRazorPrimitiveModelIsNotAnEdge(t *testing.T) {
	// `@model string?` is a partial taking a string. There is nothing to
	// resolve, and counting it unresolved would be a caveat about nothing.
	dg := buildGraph(t, map[string]string{
		"src/Api/Pages/_Text.cshtml": "@model string?\n<p>@Model</p>\n",
	})
	assertEdges(t, dg, "src/Api/Pages/_Text.cshtml")

	if st, ok := dg.ImportStatsOf("src/Api/Pages/_Text.cshtml"); ok && st.Unresolved != 0 {
		t.Errorf("primitive @model counted as unresolved: %+v", st)
	}
}

func TestRazorGenericModelResolvesItsArgument(t *testing.T) {
	// `@model IEnumerable<Contact>` binds the view to Contact. Reading only the
	// outer type would resolve to a framework interface and lose the one type
	// in the expression the repo actually owns.
	dg := buildGraph(t, map[string]string{
		"src/Domain/Contact.cs":      "namespace Leroy.Contacts;\npublic class Contact {}\n",
		"src/Api/Pages/_List.cshtml": "@model IEnumerable<Leroy.Contacts.Contact>\n<ul></ul>\n",
	})
	assertEdges(t, dg, "src/Api/Pages/_List.cshtml", "src/Domain/Contact.cs")
}

func TestRazorInheritsResolvesLikeModel(t *testing.T) {
	// Blazor's @inherits is the .razor counterpart of @model.
	dg := buildGraph(t, map[string]string{
		"src/App/Shared/AppComponentBase.cs": "namespace App.Shared;\npublic abstract class AppComponentBase {}\n",
		"src/App/Pages/Counter.razor":        "@page \"/counter\"\n@inherits App.Shared.AppComponentBase\n<h1>hi</h1>\n",
	})
	assertEdges(t, dg, "src/App/Pages/Counter.razor", "src/App/Shared/AppComponentBase.cs")
}

// ─── References ──────────────────────────────────────────────────────────────

func TestRazorReferencesComeFromCodeOnly(t *testing.T) {
	// The C# refs grammar over a whole Razor file read the markup as code: 4064
	// "references" from 173 views on a real repo, 96% of them naming nothing
	// the repo declares, including 668 calls to "@if". Over the projection it
	// sees the file's real calls and nothing else.
	root, idx := writeTree(t, map[string]string{
		"Pages/Detail.cshtml": `@model DetailModel
@{
    var name = Formatter.Clean(Model.Name);
}
<div class="grid grid-cols-2 gap-4">
    @if (Model.HasPhone)
    {
        <dd>@PhoneHelper.FormatPhone(Model.Phone)</dd>
    }
    <span>@Model.Title</span>
    <script>document.getElementById("x").addEventListener("click", go);</script>
</div>
`,
	})

	ri := NewReferenceIndex(root, idx)
	var got []string
	for _, r := range ri.All() {
		got = append(got, r.Name)
	}
	sort.Strings(got)

	want := []string{"Clean", "FormatPhone"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("references = %v, want exactly %v", got, want)
	}

	// The line number must still point into the original file.
	for _, r := range ri.ForName("FormatPhone") {
		if r.Line != 8 {
			t.Errorf("FormatPhone line = %d, want 8", r.Line)
		}
	}
}
