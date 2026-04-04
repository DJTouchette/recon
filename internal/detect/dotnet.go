package detect

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

var (
	// Matches <PackageReference Include="Foo.Bar" ... /> or <PackageReference Include="Foo.Bar">
	packageRefRe = regexp.MustCompile(`<PackageReference\s+Include="([^"]+)"`)
	// Matches <PackageVersion Include="Foo.Bar" ... /> (Directory.Packages.props central management)
	packageVerRe = regexp.MustCompile(`<PackageVersion\s+Include="([^"]+)"`)
	// Matches Sdk="Microsoft.NET.Sdk.Web" or <Project Sdk="...">
	sdkRe = regexp.MustCompile(`Sdk="([^"]+)"`)
)

type DotNetDetector struct{}

func (d *DotNetDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	csFiles := idx.ByLang("csharp")
	if len(csFiles) == 0 {
		return nil
	}

	var frameworks []Framework
	seen := make(map[string]bool)

	addPkg := func(name, evidence string) {
		if !seen[name] {
			seen[name] = true
			frameworks = append(frameworks, Framework{
				Name:     name,
				Language: "csharp",
				Evidence: evidence,
			})
		}
	}

	// Parse .csproj / .fsproj files
	for _, f := range idx.ByClass(scan.ClassConfig) {
		ext := strings.ToLower(filepath.Ext(f.RelPath))
		if ext != ".csproj" && ext != ".fsproj" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, f.RelPath))
		if err != nil {
			continue
		}
		content := string(data)

		// Detect SDK type (ASP.NET, MAUI, Worker, Blazor, etc.)
		for _, m := range sdkRe.FindAllStringSubmatch(content, -1) {
			sdk := m[1]
			if name, ok := sdkNames[sdk]; ok {
				addPkg(name, f.RelPath+": Sdk="+sdk)
			}
		}

		// Extract all PackageReferences
		for _, m := range packageRefRe.FindAllStringSubmatch(content, -1) {
			addPkg(m[1], f.RelPath)
		}
	}

	// Parse Directory.Build.props and Directory.Packages.props
	for _, propsFile := range []string{
		"Directory.Build.props",
		"Directory.Packages.props",
		"Directory.Build.targets",
	} {
		data, err := os.ReadFile(filepath.Join(root, propsFile))
		if err != nil {
			continue
		}
		content := string(data)
		for _, m := range packageRefRe.FindAllStringSubmatch(content, -1) {
			addPkg(m[1], propsFile)
		}
		for _, m := range packageVerRe.FindAllStringSubmatch(content, -1) {
			addPkg(m[1], propsFile)
		}
	}

	return frameworks
}

// sdkNames maps well-known SDK identifiers to friendly names.
// Everything else comes straight from PackageReference parsing.
var sdkNames = map[string]string{
	"Microsoft.NET.Sdk.Web":               "ASP.NET Core",
	"Microsoft.NET.Sdk.BlazorWebAssembly": "Blazor WebAssembly",
	"Microsoft.NET.Sdk.Razor":             "Razor",
	"Microsoft.NET.Sdk.Worker":            ".NET Worker Service",
	"Microsoft.Maui.Sdk":                  ".NET MAUI",
	"Tizen.NET.Sdk":                       "Tizen .NET",
}

func (d *DotNetDetector) DetectEntrypoints(idx *index.FileIndex) []Entrypoint {
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
		case "MauiProgram.cs":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		case "App.xaml.cs":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		case "AppShell.xaml.cs":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "route"})
		}

		if strings.HasSuffix(base, "Controller.cs") {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "handler"})
		}
		if strings.HasSuffix(base, ".razor.cs") {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "handler"})
		}
	}

	return eps
}
