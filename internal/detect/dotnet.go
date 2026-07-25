package detect

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

var (
	// <PackageReference Include="Foo.Bar" Version="1.0" />
	packageRefRe = regexp.MustCompile(`<PackageReference\s+Include="([^"]+)"(?:[^>]*Version="([^"]*)")?`)
	// <PackageVersion Include="Foo.Bar" Version="1.0" /> (central management)
	packageVerRe = regexp.MustCompile(`<PackageVersion\s+Include="([^"]+)"(?:[^>]*Version="([^"]*)")?`)
	// Sdk="Microsoft.NET.Sdk.Web"
	sdkRe = regexp.MustCompile(`Sdk="([^"]+)"`)
	// C#'s classic entrypoint signature.
	csharpMainRe = regexp.MustCompile(`(?m)^\s*(?:public\s+|private\s+|internal\s+)?static\s+(?:async\s+)?(?:void|int|Task|Task<int>)\s+Main\s*\(`)
)

type DotNetDetector struct{}

func (d *DotNetDetector) Key() string         { return "dotnet" }
func (d *DotNetDetector) Languages() []string { return []string{"csharp", "fsharp"} }

func (d *DotNetDetector) Detect(idx *index.FileIndex, root string) DetectorResult {
	var res DetectorResult
	if len(idx.ByLang("csharp")) == 0 && len(idx.ByLang("fsharp")) == 0 {
		return res
	}

	addPkg := func(name, version, manifest string) {
		res.Dependencies = append(res.Dependencies, Dependency{
			Name:     name,
			Version:  version,
			Language: "csharp",
			Manifest: manifest,
		})
		if fw, ok := nugetFrameworks.lookup(name); ok {
			res.Frameworks = append(res.Frameworks, Framework{
				Name:     fw,
				Language: "csharp",
				Evidence: manifest + ": " + name,
			})
		}
	}

	mr := newManifestReader(root, "csharp")

	for _, f := range idx.ByClass(scan.ClassConfig) {
		ext := strings.ToLower(filepath.Ext(f.RelPath))
		if ext != ".csproj" && ext != ".fsproj" {
			continue
		}
		content, ok := mr.read(f.RelPath)
		if !ok {
			continue
		}
		for _, m := range sdkRe.FindAllStringSubmatch(content, -1) {
			if name, ok := sdkNames[m[1]]; ok {
				res.Frameworks = append(res.Frameworks, Framework{
					Name:     name,
					Language: "csharp",
					Evidence: f.RelPath + ": Sdk=" + m[1],
				})
			}
		}
		for _, m := range packageRefRe.FindAllStringSubmatch(content, -1) {
			addPkg(m[1], m[2], f.RelPath)
		}
	}

	for _, propsFile := range []string{
		"Directory.Build.props",
		"Directory.Packages.props",
		"Directory.Build.targets",
	} {
		content, ok := mr.read(propsFile)
		if !ok {
			continue
		}
		for _, m := range packageRefRe.FindAllStringSubmatch(content, -1) {
			addPkg(m[1], m[2], propsFile)
		}
		for _, m := range packageVerRe.FindAllStringSubmatch(content, -1) {
			addPkg(m[1], m[2], propsFile)
		}
	}

	eps, epIssues := d.entrypoints(idx, root)
	res.Entrypoints = eps
	res.ManifestIssues = append(mr.issues, epIssues...)
	return res
}

// sdkNames maps well-known SDK identifiers to friendly names.
var sdkNames = map[string]string{
	"Microsoft.NET.Sdk.Web":               "ASP.NET Core",
	"Microsoft.NET.Sdk.BlazorWebAssembly": "Blazor WebAssembly",
	"Microsoft.NET.Sdk.Razor":             "Razor",
	"Microsoft.NET.Sdk.Worker":            ".NET Worker Service",
	"Microsoft.Maui.Sdk":                  ".NET MAUI",
	"Tizen.NET.Sdk":                       "Tizen .NET",
}

func (d *DotNetDetector) entrypoints(idx *index.FileIndex, root string) ([]Entrypoint, []ManifestIssue) {
	var eps []Entrypoint

	for _, f := range idx.ByLang("csharp") {
		if f.Class != scan.ClassSource {
			continue
		}
		base := filepath.Base(f.RelPath)
		switch base {
		case "Program.cs":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		case "Startup.cs":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "server"})
		case "MauiProgram.cs", "App.xaml.cs":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		case "AppShell.xaml.cs":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "route"})
		}
		if strings.HasSuffix(base, "Controller.cs") {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "handler"})
		}
		if strings.HasSuffix(base, ".razor.cs") || strings.HasSuffix(base, ".cshtml.cs") {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "handler"})
		}
	}

	// A static Main can live in any file, not just Program.cs.
	issues := scanSource(idx, root, []string{"csharp"}, func(f *scan.FileEntry, content string) {
		if f.Class == scan.ClassSource && csharpMainRe.MatchString(content) {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		}
	})

	return eps, issues
}
