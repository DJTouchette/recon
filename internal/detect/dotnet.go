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
			// Web
			"Microsoft.AspNetCore":                 "ASP.NET Core",
			"Microsoft.NET.Sdk.Web":                "ASP.NET Core",
			"Microsoft.NET.Sdk.BlazorWebAssembly":  "Blazor",
			"Blazor":                               "Blazor",
			"SignalR":                               "SignalR",
			// MAUI / Mobile / Desktop
			"Microsoft.Maui":                       ".NET MAUI",
			"Microsoft.NET.Sdk.Maui":               ".NET MAUI",
			"Xamarin.Forms":                         "Xamarin.Forms",
			"Xamarin.Essentials":                    "Xamarin",
			"Avalonia":                              "Avalonia UI",
			"Uno.WinUI":                             "Uno Platform",
			// ORM / Data
			"Microsoft.EntityFrameworkCore":         "Entity Framework Core",
			"Dapper":                                "Dapper",
			"Newtonsoft.Json":                       "Newtonsoft.Json",
			"System.Text.Json":                      "System.Text.Json",
			// Patterns / Architecture
			"MediatR":                               "MediatR",
			"AutoMapper":                            "AutoMapper",
			"FluentValidation":                      "FluentValidation",
			"Polly":                                 "Polly",
			// Logging / Observability
			"Serilog":                               "Serilog",
			"NLog":                                  "NLog",
			"OpenTelemetry":                         "OpenTelemetry",
			// API / Docs
			"Swashbuckle":                           "Swagger/Swashbuckle",
			"NSwag":                                 "NSwag",
			// Messaging / Jobs
			"MassTransit":                           "MassTransit",
			"Hangfire":                              "Hangfire",
			"RabbitMQ.Client":                       "RabbitMQ",
			// Identity / Auth
			"Microsoft.AspNetCore.Identity":         "ASP.NET Identity",
			"IdentityServer":                        "IdentityServer",
			"Duende.IdentityServer":                 "Duende IdentityServer",
			// Testing
			"xunit":                                 "xUnit",
			"NUnit":                                 "NUnit",
			"MSTest":                                "MSTest",
			"Moq":                                   "Moq",
			"NSubstitute":                           "NSubstitute",
			"FluentAssertions":                      "FluentAssertions",
			"Bogus":                                 "Bogus",
			// gRPC
			"Grpc.AspNetCore":                       "gRPC",
			"Google.Protobuf":                       "Protobuf",
			// Worker / Hosted Services
			"Microsoft.NET.Sdk.Worker":              ".NET Worker Service",
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
		case "MauiProgram.cs":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		case "App.xaml.cs":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "main"})
		case "AppShell.xaml.cs":
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "route"})
		}

		// Look for controller files
		if strings.HasSuffix(base, "Controller.cs") {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "handler"})
		}
		// Look for Razor pages / Blazor components
		if strings.HasSuffix(base, ".razor.cs") {
			eps = append(eps, Entrypoint{Path: f.RelPath, Kind: "handler"})
		}
	}

	return eps
}
