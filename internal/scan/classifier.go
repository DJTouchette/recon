package scan

import (
	"path/filepath"
	"strings"
)

// FileClass categorizes files by their role in the project.
type FileClass uint8

const (
	ClassSource FileClass = iota
	ClassTest
	ClassConfig
	ClassGenerated
	ClassVendor
	ClassDoc
	ClassAsset
	ClassData
	ClassScript
	ClassOther
)

func (c FileClass) String() string {
	switch c {
	case ClassSource:
		return "source"
	case ClassTest:
		return "test"
	case ClassConfig:
		return "config"
	case ClassGenerated:
		return "generated"
	case ClassVendor:
		return "vendor"
	case ClassDoc:
		return "doc"
	case ClassAsset:
		return "asset"
	case ClassData:
		return "data"
	case ClassScript:
		return "script"
	default:
		return "other"
	}
}

// FileEntry holds metadata about a single file.
type FileEntry struct {
	RelPath string
	Size    int64
	ModTime int64 // unix nanoseconds
	Class   FileClass
	Lang    string
}

// Classify determines the FileClass for a file based on its path and name.
func Classify(relPath, name string) FileClass {
	ext := filepath.Ext(name)
	nameNoExt := strings.TrimSuffix(name, ext)
	dir := filepath.Dir(relPath)

	if isVendorPath(dir) {
		return ClassVendor
	}
	if isGeneratedPath(dir) || isGeneratedFile(name) {
		return ClassGenerated
	}
	if isTestFile(nameNoExt, ext, dir) {
		return ClassTest
	}
	if isConfigFile(name, ext) {
		return ClassConfig
	}
	if isDocFile(ext, dir) {
		return ClassDoc
	}
	if isAssetFile(ext) {
		return ClassAsset
	}
	if isDataFile(ext) {
		return ClassData
	}
	if isSourceFile(ext) {
		return ClassSource
	}
	if isScriptFile(ext) {
		return ClassScript
	}
	return ClassOther
}

// LangFromExt returns the language name for a file extension.
func LangFromExt(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if lang, ok := extToLang[ext]; ok {
		return lang
	}
	return ""
}

var extToLang = map[string]string{
	".go":  "go",
	".js":  "javascript",
	".jsx": "javascript",
	".mjs": "javascript",
	".cjs": "javascript",
	".ts":  "typescript",
	".tsx": "typescript",
	".mts": "typescript",
	".py":  "python",
	".pyw": "python",
	".rs":  "rust",
	".rb":  "ruby",
	".ex":  "elixir",
	".exs": "elixir",
	".erl": "erlang",
	".cs":  "csharp",
	// Razor is not C#. A .cshtml/.razor file is HTML markup with @-prefixed
	// directives and interleaved C# fragments, and feeding one to the C#
	// grammar fails on essentially every file: on a 13k-file ASP.NET repo all
	// 173 .cshtml files came back "partial" with a syntax error, 172 of them
	// produced no symbols at all, and they still counted toward the repo's C#
	// share (78.9%) while burying the 19 real parse failures under 173 bogus
	// caveats. It has its own language and its own extractor instead; see
	// internal/index/symbols_razor.go.
	//
	// Code-behind (Index.cshtml.cs, Counter.razor.cs) is ordinary C# and is not
	// affected: the language is keyed on the final extension, which for those
	// files is ".cs".
	".cshtml": "razor",
	".razor":  "razor",
	".fs":     "fsharp",
	".java":   "java",
	".kt":     "kotlin",
	".kts":    "kotlin",
	".swift":  "swift",
	".c":      "c",
	// .h is claimed by both C and C++ and there is no way to tell from the
	// name, so it is reported as C here and the symbol extractor resolves the
	// ambiguity by content: see grammarCandidates in internal/index. Do not
	// "fix" this by flipping it to cpp — that just moves the error onto C.
	".h":       "c",
	".cpp":     "cpp",
	".cc":      "cpp",
	".cxx":     "cpp",
	".hpp":     "cpp",
	".php":     "php",
	".lua":     "lua",
	".scala":   "scala",
	".clj":     "clojure",
	".dart":    "dart",
	".r":       "r",
	".jl":      "julia",
	".zig":     "zig",
	".nim":     "nim",
	".sh":      "shell",
	".bash":    "shell",
	".zsh":     "shell",
	".ps1":     "powershell",
	".psm1":    "powershell",
	".sql":     "sql",
	".tf":      "terraform",
	".hcl":     "hcl",
	".bicep":   "bicep",
	".yaml":    "yaml",
	".yml":     "yaml",
	".json":    "json",
	".xml":     "xml",
	".html":    "html",
	".htm":     "html",
	".css":     "css",
	".scss":    "scss",
	".sass":    "sass",
	".less":    "less",
	".vue":     "vue",
	".svelte":  "svelte",
	".astro":   "astro",
	".md":      "markdown",
	".mdx":     "markdown",
	".rst":     "rst",
	".toml":    "toml",
	".ini":     "ini",
	".proto":   "protobuf",
	".graphql": "graphql",
	".gql":     "graphql",
}

var sourceExts = map[string]bool{
	".go": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
	".ts": true, ".tsx": true, ".mts": true,
	".py": true, ".pyw": true,
	".rs": true, ".rb": true,
	".ex": true, ".exs": true, ".erl": true,
	".cs": true, ".cshtml": true, ".razor": true, ".fs": true,
	".java": true, ".kt": true, ".kts": true, ".scala": true, ".clj": true,
	".swift": true, ".c": true, ".h": true, ".cpp": true, ".cc": true, ".cxx": true, ".hpp": true,
	".php": true, ".lua": true, ".dart": true, ".r": true, ".jl": true, ".zig": true, ".nim": true,
	".vue": true, ".svelte": true, ".astro": true,
	".html": true, ".htm": true, ".css": true, ".scss": true, ".sass": true, ".less": true,
	".proto": true, ".graphql": true, ".gql": true,
}

func isSourceFile(ext string) bool {
	return sourceExts[strings.ToLower(ext)]
}

func isTestFile(nameNoExt, ext, dir string) bool {
	lext := strings.ToLower(ext)
	switch lext {
	case ".go":
		if strings.HasSuffix(nameNoExt, "_test") {
			return true
		}
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".mts":
		if strings.HasSuffix(nameNoExt, ".test") || strings.HasSuffix(nameNoExt, ".spec") {
			return true
		}
	case ".py":
		if strings.HasPrefix(nameNoExt, "test_") || strings.HasSuffix(nameNoExt, "_test") {
			return true
		}
	case ".rb":
		if strings.HasSuffix(nameNoExt, "_spec") || strings.HasSuffix(nameNoExt, "_test") {
			return true
		}
	case ".exs":
		if strings.HasSuffix(nameNoExt, "_test") {
			return true
		}
	case ".cs":
		// A C# *name* ending in "Test"/"Tests" is weak evidence and must not
		// stand on its own. Unlike Go's compiler-enforced _test.go, C# has no
		// naming convention that makes a file a test; what makes it a test is
		// living in a test *project* (a .csproj that pulls in xunit/nunit/
		// mstest). "Test" is also an ordinary domain noun: real examples from a
		// veterinary-certificate codebase are LabTest.cs (a lab test record
		// linked to a certificate) and CertificateAnimalTest.cs, both plain
		// entity models under src/. Classifying those as tests inflated the
		// denominator of the tests command and made production code read as
		// test code in grep output.
		//
		// So require structural corroboration from the path. The generic
		// directory sweep below already covers test/, tests/ and Foo.Tests/;
		// this adds the dotless .NET test-project spellings (FooTests/,
		// Leroy.DocTests/) that a solution can use outside a tests/ tree.
		if isDotNetTestProjectDir(dir) {
			return true
		}
	case ".java":
		if strings.HasSuffix(nameNoExt, "Test") || strings.HasSuffix(nameNoExt, "Tests") || strings.HasSuffix(nameNoExt, "IT") {
			return true
		}
	case ".kt", ".kts":
		if strings.HasSuffix(nameNoExt, "Test") || strings.HasSuffix(nameNoExt, "Tests") {
			return true
		}
	case ".swift":
		if strings.HasSuffix(nameNoExt, "Tests") || strings.HasSuffix(nameNoExt, "Test") {
			return true
		}
	case ".php":
		if strings.HasSuffix(nameNoExt, "Test") {
			return true
		}
	case ".dart":
		if strings.HasSuffix(nameNoExt, "_test") {
			return true
		}
	case ".scala":
		if strings.HasSuffix(nameNoExt, "Spec") || strings.HasSuffix(nameNoExt, "Test") ||
			strings.HasSuffix(nameNoExt, "Tests") || strings.HasSuffix(nameNoExt, "Suite") {
			return true
		}
	case ".rs":
		if strings.HasSuffix(nameNoExt, "_test") || strings.HasSuffix(nameNoExt, "_tests") {
			return true
		}
	case ".c", ".h", ".cpp", ".cc", ".cxx", ".hpp":
		if strings.HasSuffix(nameNoExt, "_test") || strings.HasPrefix(nameNoExt, "test_") {
			return true
		}
	case ".zig":
		if strings.HasSuffix(nameNoExt, "_test") {
			return true
		}
	case ".lua":
		if strings.HasSuffix(nameNoExt, "_spec") || strings.HasSuffix(nameNoExt, "_test") {
			return true
		}
	case ".jl":
		if strings.HasPrefix(nameNoExt, "test_") || strings.HasSuffix(nameNoExt, "_test") {
			return true
		}
	case ".sh", ".bash":
		if strings.HasSuffix(nameNoExt, "_test") || strings.HasSuffix(nameNoExt, ".test") ||
			strings.HasPrefix(nameNoExt, "test_") {
			return true
		}
	}

	// Directory-based test detection (only for source files).
	//
	// This is deliberately broad: everything under test/, tests/, spec/,
	// specs/ or __tests__/ is test-class, including shared fixture and helper
	// code that declares types other code depends on. That breadth is correct
	// for "which files are tests" (the tests command, hotspot weighting), but
	// it must not be read as "these files declare nothing worth indexing" —
	// the symbol index covers ClassTest precisely because fixture code under
	// these directories is real API. See symbolSourceClasses in
	// internal/index/symbols.go.
	// Scripts count too. Gating this on isSourceFile alone meant a shell script
	// under tests/ could never be classified as a test, so the test map had
	// rules for those languages that a real walk could never reach.
	if isSourceFile(ext) || isScriptFile(ext) {
		parts := strings.Split(dir, "/")
		for _, p := range parts {
			lp := strings.ToLower(p)
			switch lp {
			case "__tests__", "test", "tests", "spec", "specs":
				return true
			}
			if strings.HasSuffix(lp, ".tests") || strings.HasSuffix(lp, ".test") ||
				strings.HasSuffix(lp, ".integrationtests") || strings.HasSuffix(lp, ".unittests") {
				return true
			}
		}
	}
	return false
}

// isDotNetTestProjectDir reports whether any component of dir names a .NET test
// project or a conventional test tree: Foo.Tests, Foo.IntegrationTests,
// FooTests, Foo.Specs, tests/, spec/ and friends.
//
// Only the plural bare suffix ("...Tests") is accepted without a separating
// dot. "Latest", "Manifest" and "Contest" all end in "test", so accepting a
// bare singular suffix would sweep in unrelated directories; a singular suffix
// is only honoured in the dotted project form ("Foo.Test").
//
// This is C#-only on purpose. Every other language keeps whatever signal it
// already had.
func isDotNetTestProjectDir(dir string) bool {
	for _, p := range strings.Split(dir, "/") {
		lp := strings.ToLower(p)
		switch lp {
		case "test", "tests", "spec", "specs", "__tests__":
			return true
		}
		if strings.HasSuffix(lp, "tests") || strings.HasSuffix(lp, "specs") {
			return true
		}
		if strings.HasSuffix(lp, ".test") || strings.HasSuffix(lp, ".spec") {
			return true
		}
	}
	return false
}

func isConfigFile(name, ext string) bool {
	lname := strings.ToLower(name)
	switch lname {
	case "go.mod", "go.sum",
		"package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock",
		"pyproject.toml", "setup.py", "setup.cfg", "pipfile", "pipfile.lock",
		"cargo.toml", "cargo.lock",
		"gemfile", "gemfile.lock",
		"mix.exs", "mix.lock",
		"pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts",
		"makefile", "cmakelists.txt", "rakefile",
		"dockerfile", "docker-compose.yml", "docker-compose.yaml",
		"tsconfig.json", "jsconfig.json",
		"nuget.config", "directory.build.props", "directory.build.targets", "global.json",
		".editorconfig", ".prettierrc", ".prettierrc.json", ".prettierrc.yml",
		".gitignore", ".gitattributes", ".dockerignore":
		return true
	}
	lext := strings.ToLower(ext)
	switch lext {
	case ".csproj", ".fsproj", ".vbproj", ".sln", ".props", ".targets":
		return true
	case ".tf", ".hcl", ".bicep":
		return true
	}
	if strings.HasSuffix(lname, ".config.js") || strings.HasSuffix(lname, ".config.ts") ||
		strings.HasSuffix(lname, ".config.mjs") || strings.HasSuffix(lname, ".config.cjs") {
		return true
	}
	if strings.HasPrefix(lname, ".eslintrc") || strings.HasPrefix(lname, ".babelrc") {
		return true
	}
	return false
}

func isDocFile(ext, dir string) bool {
	lext := strings.ToLower(ext)
	switch lext {
	case ".md", ".mdx", ".rst", ".txt", ".adoc":
		return true
	}
	parts := strings.Split(dir, "/")
	for _, p := range parts {
		lp := strings.ToLower(p)
		if lp == "docs" || lp == "doc" || lp == "documentation" {
			return true
		}
	}
	return false
}

func isAssetFile(ext string) bool {
	lext := strings.ToLower(ext)
	switch lext {
	case ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".webp", ".bmp",
		".mp4", ".webm", ".avi", ".mov",
		".mp3", ".wav", ".ogg",
		".ttf", ".otf", ".woff", ".woff2", ".eot",
		".pdf":
		return true
	}
	return false
}

func isDataFile(ext string) bool {
	lext := strings.ToLower(ext)
	switch lext {
	case ".csv", ".tsv", ".parquet", ".avro", ".ndjson", ".jsonl":
		return true
	}
	return false
}

func isScriptFile(ext string) bool {
	lext := strings.ToLower(ext)
	switch lext {
	case ".sh", ".bash", ".zsh", ".fish", ".ps1", ".psm1", ".bat", ".cmd":
		return true
	}
	return false
}

func isVendorPath(dir string) bool {
	parts := strings.Split(dir, "/")
	for _, p := range parts {
		switch p {
		case "vendor", "node_modules", "third_party", "external", "deps":
			return true
		}
	}
	return false
}

func isGeneratedPath(dir string) bool {
	parts := strings.Split(dir, "/")
	for _, p := range parts {
		lp := strings.ToLower(p)
		switch lp {
		case "generated", "gen", "__generated__", "auto-generated":
			return true
		}
	}
	return false
}

func isGeneratedFile(name string) bool {
	lname := strings.ToLower(name)
	if strings.HasSuffix(lname, ".pb.go") || strings.HasSuffix(lname, ".pb.ts") ||
		strings.HasSuffix(lname, ".generated.cs") || strings.HasSuffix(lname, ".designer.cs") ||
		strings.HasSuffix(lname, ".min.js") || strings.HasSuffix(lname, ".min.css") ||
		strings.HasSuffix(lname, ".g.cs") || strings.HasSuffix(lname, ".g.dart") {
		return true
	}
	return false
}
