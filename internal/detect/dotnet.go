package detect

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

type DotNetDetector struct{}

func (d *DotNetDetector) DetectFrameworks(idx *index.FileIndex, root string) []Framework {
	csFiles := idx.ByLang("csharp")
	if len(csFiles) == 0 {
		return nil
	}

	var frameworks []Framework
	seen := make(map[string]bool)

	// Check .csproj files for framework references
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

		csprojFws := map[string]string{
			"Microsoft.AspNetCore":      "ASP.NET Core",
			"Microsoft.NET.Sdk.Web":     "ASP.NET Core",
			"Microsoft.EntityFrameworkCore": "Entity Framework Core",
			"Swashbuckle":               "Swagger/Swashbuckle",
			"MediatR":                   "MediatR",
			"AutoMapper":                "AutoMapper",
			"FluentValidation":          "FluentValidation",
			"Serilog":                   "Serilog",
			"NLog":                      "NLog",
			"Dapper":                    "Dapper",
			"Newtonsoft.Json":           "Newtonsoft.Json",
			"xunit":                     "xUnit",
			"NUnit":                     "NUnit",
			"MSTest":                    "MSTest",
			"Moq":                       "Moq",
			"FluentAssertions":          "FluentAssertions",
			"Polly":                     "Polly",
			"MassTransit":               "MassTransit",
			"Hangfire":                  "Hangfire",
			"SignalR":                   "SignalR",
			"Blazor":                    "Blazor",
			"Microsoft.NET.Sdk.BlazorWebAssembly": "Blazor",
		}

		for dep, name := range csprojFws {
			if strings.Contains(content, dep) && !seen[name] {
				seen[name] = true
				frameworks = append(frameworks, Framework{
					Name:     name,
					Language: "csharp",
					Evidence: f.RelPath + ": " + dep,
				})
			}
		}
	}

	// Check for .sln file (indicates a .NET solution)
	for _, f := range idx.ByClass(scan.ClassConfig) {
		if strings.HasSuffix(strings.ToLower(f.RelPath), ".sln") && !seen[".NET Solution"] {
			seen[".NET Solution"] = true
			// Don't add as framework, it's just structure
			break
		}
	}

	return frameworks
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
		}

		// Look for controller files
		if strings.HasSuffix(base, "Controller.cs") {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "handler"})
		}
	}

	return eps
}
