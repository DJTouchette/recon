package index

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/djtouchette/recon/internal/scan"
)

// DepGraph holds the import/require dependency graph between files.
type DepGraph struct {
	imports    map[string][]string // file → files it imports
	importedBy map[string][]string // file → files that import it
}

// NewDepGraph builds a dependency graph by scanning source files for import statements.
func NewDepGraph(root string, idx *FileIndex) *DepGraph {
	dg := &DepGraph{
		imports:    make(map[string][]string),
		importedBy: make(map[string][]string),
	}

	// Detect Go module path
	goModPath := detectGoModulePath(root)

	sources := idx.ByClass(scan.ClassSource)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0)*2)

	for _, f := range sources {
		f := f
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			imports := extractImports(root, f, goModPath, idx)
			if len(imports) == 0 {
				return
			}

			mu.Lock()
			dg.imports[f.RelPath] = imports
			for _, imp := range imports {
				dg.importedBy[imp] = append(dg.importedBy[imp], f.RelPath)
			}
			mu.Unlock()
		}()
	}

	wg.Wait()
	return dg
}

// NewDepGraphFromData creates a DepGraph from pre-computed import edges.
func NewDepGraphFromData(imports map[string][]string) *DepGraph {
	dg := &DepGraph{
		imports:    imports,
		importedBy: make(map[string][]string),
	}
	for src, targets := range imports {
		for _, t := range targets {
			dg.importedBy[t] = append(dg.importedBy[t], src)
		}
	}
	return dg
}

// ImportsOf returns files imported by the given file.
func (dg *DepGraph) ImportsOf(path string) []string {
	return dg.imports[path]
}

// ImportedBy returns files that import the given file.
func (dg *DepGraph) ImportedBy(path string) []string {
	return dg.importedBy[path]
}

// AllImports returns the full import map (source → targets).
func (dg *DepGraph) AllImports() map[string][]string {
	return dg.imports
}

// ScanFileImports extracts imports for specific files. Used during incremental refresh.
func ScanFileImports(root string, files []*scan.FileEntry, idx *FileIndex) map[string][]string {
	goModPath := detectGoModulePath(root)
	result := make(map[string][]string)
	for _, f := range files {
		imports := extractImports(root, f, goModPath, idx)
		if len(imports) > 0 {
			result[f.RelPath] = imports
		}
	}
	return result
}

func detectGoModulePath(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// Import extraction regexes — compiled once.
var (
	goImportSingle = regexp.MustCompile(`import\s+"([^"]+)"`)
	goImportBlock  = regexp.MustCompile(`import\s*\(([^)]+)\)`)
	goImportLine   = regexp.MustCompile(`"([^"]+)"`)

	jsImportFrom = regexp.MustCompile(`(?:import\s+.*?from\s+|require\s*\(\s*)['"]([^'"]+)['"]`)

	pyImportFrom = regexp.MustCompile(`^from\s+(\S+)\s+import`)
	pyImport     = regexp.MustCompile(`^import\s+(\S+)`)

	csUsing = regexp.MustCompile(`^using\s+(?:static\s+)?([A-Za-z][\w.]*)\s*;`)

	javaImportRe   = regexp.MustCompile(`^import\s+(?:static\s+)?([A-Za-z][\w.]*)\s*;`)
	kotlinImportRe = regexp.MustCompile(`^import\s+([A-Za-z][\w.]*)\s*$`)

	rbRequire         = regexp.MustCompile(`^\s*require\s+['"]([^'"]+)['"]`)
	rbRequireRelative = regexp.MustCompile(`^\s*require_relative\s+['"]([^'"]+)['"]`)

	// Elixir module reference patterns
	exModuleRef = regexp.MustCompile(`\b([A-Z][A-Za-z0-9]*(?:\.[A-Z][A-Za-z0-9]*)+)`)

	// Rust import patterns
	rsUseStmt = regexp.MustCompile(`^use\s+((?:crate|self|super)(?:::\w+)+)`)
	rsModDecl = regexp.MustCompile(`^(?:pub\s+)?mod\s+(\w+)\s*[;{]`)

	// PHP use statement pattern
	phpUseStmt = regexp.MustCompile(`^use\s+(?:function\s+|const\s+)?([A-Z][\w\\]+)`)

	// Swift import pattern
	swiftImportRe = regexp.MustCompile(`^import\s+([A-Za-z_]\w*)`)

	// Dart import/export pattern
	dartImportRe = regexp.MustCompile(`^(?:import|export)\s+['"]([^'"]+)['"]`)

	// Scala import pattern
	scalaImportRe = regexp.MustCompile(`^import\s+([A-Za-z_][\w.]*)(?:\.\{|\.[\w*]+)`)
)

func extractImports(root string, f *scan.FileEntry, goModPath string, idx *FileIndex) []string {
	fullPath := filepath.Join(root, f.RelPath)

	switch f.Lang {
	case "go":
		lines, err := readHeadLines(fullPath, 150)
		if err != nil {
			return nil
		}
		return resolveGoImports(lines, f.RelPath, goModPath, idx)
	case "javascript", "typescript":
		lines, err := readHeadLines(fullPath, 150)
		if err != nil {
			return nil
		}
		return resolveJSImports(lines, f.RelPath, idx)
	case "python":
		lines, err := readHeadLines(fullPath, 150)
		if err != nil {
			return nil
		}
		return resolvePyImports(lines, f.RelPath, idx)
	case "java":
		lines, err := readHeadLines(fullPath, 100)
		if err != nil {
			return nil
		}
		return resolveJavaImports(lines, f.RelPath, "java", idx)
	case "kotlin":
		lines, err := readHeadLines(fullPath, 100)
		if err != nil {
			return nil
		}
		return resolveJavaImports(lines, f.RelPath, "kotlin", idx)
	case "csharp":
		lines, err := readHeadLines(fullPath, 100)
		if err != nil {
			return nil
		}
		return resolveCSharpImports(lines, f.RelPath, idx)
	case "ruby":
		lines, err := readHeadLines(fullPath, 100)
		if err != nil {
			return nil
		}
		return resolveRubyImports(lines, f.RelPath, idx)
	case "rust":
		lines, err := readHeadLines(fullPath, 150)
		if err != nil {
			return nil
		}
		return resolveRustImports(lines, f.RelPath, idx)
	case "swift":
		lines, err := readHeadLines(fullPath, 50)
		if err != nil {
			return nil
		}
		return resolveSwiftImports(lines, f.RelPath, idx)
	case "php":
		lines, err := readHeadLines(fullPath, 100)
		if err != nil {
			return nil
		}
		return resolvePHPImports(lines, f.RelPath, root, idx)
	case "dart":
		lines, err := readHeadLines(fullPath, 100)
		if err != nil {
			return nil
		}
		return resolveDartImports(lines, f.RelPath, root, idx)
	case "scala":
		lines, err := readHeadLines(fullPath, 100)
		if err != nil {
			return nil
		}
		return resolveScalaImports(lines, f.RelPath, idx)
	case "elixir":
		// Elixir needs full file scan — module refs appear anywhere, not just top
		lines, err := readAllLines(fullPath)
		if err != nil {
			return nil
		}
		return resolveElixirImports(lines, f.RelPath, idx)
	default:
		return nil
	}
}

func readHeadLines(path string, maxLines int) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() && len(lines) < maxLines {
		lines = append(lines, scanner.Text())
	}
	return lines, nil
}

func resolveGoImports(lines []string, filePath, goModPath string, idx *FileIndex) []string {
	if goModPath == "" {
		return nil
	}

	content := strings.Join(lines, "\n")
	var rawImports []string

	// Single imports
	for _, m := range goImportSingle.FindAllStringSubmatch(content, -1) {
		rawImports = append(rawImports, m[1])
	}

	// Block imports
	for _, block := range goImportBlock.FindAllStringSubmatch(content, -1) {
		for _, m := range goImportLine.FindAllStringSubmatch(block[1], -1) {
			rawImports = append(rawImports, m[1])
		}
	}

	// Resolve to local files
	var resolved []string
	prefix := goModPath + "/"
	for _, imp := range rawImports {
		if !strings.HasPrefix(imp, prefix) {
			continue
		}
		localPath := strings.TrimPrefix(imp, prefix)
		// Go packages map to directories; find any .go file in that dir
		files := idx.ByDir(localPath)
		for _, f := range files {
			if f.Lang == "go" && f.Class == scan.ClassSource {
				resolved = append(resolved, f.RelPath)
			}
		}
	}
	return resolved
}

func resolveJSImports(lines []string, filePath string, idx *FileIndex) []string {
	dir := filepath.Dir(filePath)
	var resolved []string

	for _, line := range lines {
		matches := jsImportFrom.FindAllStringSubmatch(line, -1)
		for _, m := range matches {
			imp := m[1]
			if !strings.HasPrefix(imp, ".") {
				continue // external package
			}
			// Resolve relative to the importing file's directory
			target := filepath.Clean(filepath.Join(dir, imp))
			found := resolveJSPath(target, idx)
			if found != "" {
				resolved = append(resolved, found)
			}
		}
	}
	return resolved
}

func resolveJSPath(target string, idx *FileIndex) string {
	// Try exact path
	if idx.Exists(target) {
		return target
	}
	// Try with extensions
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".mts"} {
		if idx.Exists(target + ext) {
			return target + ext
		}
	}
	// Try index files
	for _, name := range []string{"/index.ts", "/index.tsx", "/index.js", "/index.jsx"} {
		if idx.Exists(target + name) {
			return target + name
		}
	}
	return ""
}

func resolveRubyImports(lines []string, filePath string, idx *FileIndex) []string {
	dir := filepath.Dir(filePath)
	seen := make(map[string]bool)
	var resolved []string

	for _, line := range lines {
		// require_relative — resolve relative to the current file's directory
		if m := rbRequireRelative.FindStringSubmatch(line); m != nil {
			imp := m[1]
			target := filepath.Clean(filepath.Join(dir, imp))
			if !strings.HasSuffix(target, ".rb") {
				target += ".rb"
			}
			if idx.Exists(target) && !seen[target] {
				seen[target] = true
				resolved = append(resolved, target)
			}
			continue
		}

		// require — try common Ruby source roots
		if m := rbRequire.FindStringSubmatch(line); m != nil {
			imp := m[1]
			// Skip gem-like requires: no "/" or "." and no local file match
			if !strings.Contains(imp, "/") && !strings.Contains(imp, ".") {
				found := false
				for _, root := range []string{"lib/", "app/", "src/"} {
					candidate := root + imp + ".rb"
					if idx.Exists(candidate) {
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}

			for _, root := range []string{"lib/", "app/", "src/", ""} {
				candidate := filepath.Clean(root + imp)
				if !strings.HasSuffix(candidate, ".rb") {
					candidate += ".rb"
				}
				if idx.Exists(candidate) && !seen[candidate] {
					seen[candidate] = true
					resolved = append(resolved, candidate)
					break
				}
			}
		}
	}
	return resolved
}

func resolveJavaImports(lines []string, filePath string, lang string, idx *FileIndex) []string {
	seen := make(map[string]bool)
	var resolved []string

	// Pick regex based on language
	re := javaImportRe
	if lang == "kotlin" {
		re = kotlinImportRe
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		imp := m[1]

		// Skip standard library imports
		if strings.HasPrefix(imp, "java.") || strings.HasPrefix(imp, "javax.") ||
			strings.HasPrefix(imp, "kotlin.") || strings.HasPrefix(imp, "kotlinx.") ||
			strings.HasPrefix(imp, "android.") {
			continue
		}

		// For static imports, take the class part.
		// If the last segment starts with lowercase, it's a member — strip it.
		parts := strings.Split(imp, ".")
		if len(parts) > 1 {
			last := parts[len(parts)-1]
			if len(last) > 0 && last[0] >= 'a' && last[0] <= 'z' {
				parts = parts[:len(parts)-1]
			}
		}

		// Convert dots to path separators: com.example.User → com/example/User
		classPath := strings.Join(parts, "/")

		// Source root prefixes to try (plus root-level with empty prefix)
		roots := []string{
			"src/main/java/",
			"src/main/kotlin/",
			"src/",
			"app/src/main/java/",
			"app/src/main/kotlin/",
			"",
		}
		exts := []string{".java", ".kt"}

		for _, root := range roots {
			for _, ext := range exts {
				candidate := root + classPath + ext
				if idx.Exists(candidate) && candidate != filePath && !seen[candidate] {
					seen[candidate] = true
					resolved = append(resolved, candidate)
				}
			}
		}
	}
	return resolved
}

// resolveCSharpImports finds using statements in a C# file and resolves them
// to file paths by matching namespace segments against the file index. C# has no
// strict namespace-to-file mapping, so we use directory-based heuristics.
func resolveCSharpImports(lines []string, filePath string, idx *FileIndex) []string {
	seen := make(map[string]bool)
	var resolved []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		m := csUsing.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ns := m[1]

		// Skip system/framework namespaces
		if strings.HasPrefix(ns, "System") || strings.HasPrefix(ns, "Microsoft") ||
			strings.HasPrefix(ns, "NuGet") {
			continue
		}

		// .NET namespaces map to directories in two forms:
		//   1. Dots as slashes:  Public.Common → Public/Common
		//   2. Dots kept as-is:  Public.Common → Public.Common (standard .NET project naming)
		nsSlashed := strings.ReplaceAll(ns, ".", "/")
		nsDotted := ns
		segments := strings.Split(ns, ".")

		// Build suffix patterns from the last 1–3 segments in both forms.
		// E.g., Public.Domain.Discount.Models → try:
		//   slash: "Models", "Discount/Models", "Domain/Discount/Models"
		//   dotted: "Models", "Discount.Models", "Domain.Discount.Models"
		var slashSuffixes, dottedSuffixes []string
		for i := len(segments) - 1; i >= 0 && len(segments)-i <= 3; i-- {
			slashSuffixes = append(slashSuffixes, strings.Join(segments[i:], "/"))
			dottedSuffixes = append(dottedSuffixes, strings.Join(segments[i:], "."))
		}

		// Strategy 1: direct directory match under the full namespace path.
		// Try both dotted (Public.Common) and slashed (Public/Common) forms,
		// with common root prefixes.
		foundDirect := false
		dirCandidates := []string{nsSlashed, nsDotted}
		for _, candidate := range dirCandidates {
			// Try bare, under src/, and with any intermediate directories.
			for _, prefix := range []string{"", "src/"} {
				files := idx.FilesInDir(prefix + candidate)
				for _, f := range files {
					if f.Lang == "csharp" && f.RelPath != filePath && !seen[f.RelPath] {
						seen[f.RelPath] = true
						resolved = append(resolved, f.RelPath)
						foundDirect = true
					}
				}
			}
		}

		// Strategy 2: scan all .cs files for directory paths ending with namespace segments.
		// Match against both slash-separated and dot-separated directory names.
		if !foundDirect {
			for _, f := range idx.All() {
				if f.Lang != "csharp" || f.RelPath == filePath || seen[f.RelPath] {
					continue
				}
				relDir := filepath.Dir(f.RelPath)
				lowerDir := strings.ToLower(filepath.ToSlash(relDir))

				matched := false
				// Try slash-separated suffixes (Discount/Models).
				for _, suffix := range slashSuffixes {
					lower := strings.ToLower(suffix)
					if strings.HasSuffix(lowerDir, "/"+lower) || lowerDir == lower {
						matched = true
						break
					}
				}
				// Try dotted suffixes (Public.Domain, DataLayer.Tests).
				if !matched {
					for _, suffix := range dottedSuffixes {
						lower := strings.ToLower(suffix)
						if strings.HasSuffix(lowerDir, "/"+lower) || lowerDir == lower {
							matched = true
							break
						}
					}
				}
				if matched {
					seen[f.RelPath] = true
					resolved = append(resolved, f.RelPath)
				}
			}
		}
	}
	return resolved
}

func resolveRustImports(lines []string, filePath string, idx *FileIndex) []string {
	dir := filepath.Dir(filePath)
	seen := make(map[string]bool)
	var resolved []string

	// Find the crate root (src/) relative to the project
	// Walk up from the file to find the enclosing src/ directory
	crateRoot := ""
	parts := strings.Split(filepath.ToSlash(filePath), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "src" {
			crateRoot = strings.Join(parts[:i+1], "/")
			break
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// mod declarations: mod child; -> {dir}/child.rs or {dir}/child/mod.rs
		if m := rsModDecl.FindStringSubmatch(trimmed); m != nil {
			child := m[1]
			// Skip common non-import mod keywords
			if child == "tests" || child == "test" {
				continue
			}
			for _, candidate := range []string{
				filepath.Join(dir, child+".rs"),
				filepath.Join(dir, child, "mod.rs"),
			} {
				if idx.Exists(candidate) && !seen[candidate] {
					seen[candidate] = true
					resolved = append(resolved, candidate)
					break
				}
			}
			continue
		}

		// use statements: use crate::a::b, use self::x, use super::x
		if m := rsUseStmt.FindStringSubmatch(trimmed); m != nil {
			imp := m[1]
			segments := strings.Split(imp, "::")

			if len(segments) < 2 {
				continue
			}

			switch segments[0] {
			case "crate":
				if crateRoot == "" {
					continue
				}
				// Strip "crate", convert remaining segments to path
				modParts := segments[1:]
				modPath := strings.Join(modParts, "/")

				// Try full path as file: src/a/b/c.rs
				// Try full path as dir module: src/a/b/c/mod.rs
				// Try parent file (item might be defined in parent module): src/a/b.rs
				candidates := []string{
					filepath.Join(crateRoot, modPath+".rs"),
					filepath.Join(crateRoot, modPath, "mod.rs"),
				}
				if len(modParts) > 1 {
					parentPath := strings.Join(modParts[:len(modParts)-1], "/")
					candidates = append(candidates, filepath.Join(crateRoot, parentPath+".rs"))
				}
				for _, candidate := range candidates {
					if idx.Exists(candidate) && !seen[candidate] && candidate != filePath {
						seen[candidate] = true
						resolved = append(resolved, candidate)
						break
					}
				}

			case "super":
				// use super::x -> go up one directory
				parentDir := filepath.Dir(dir)
				modParts := segments[1:]
				modPath := strings.Join(modParts, "/")

				candidates := []string{
					filepath.Join(parentDir, modPath+".rs"),
					filepath.Join(parentDir, modPath, "mod.rs"),
				}
				if len(modParts) > 1 {
					parentPath := strings.Join(modParts[:len(modParts)-1], "/")
					candidates = append(candidates, filepath.Join(parentDir, parentPath+".rs"))
				}
				for _, candidate := range candidates {
					if idx.Exists(candidate) && !seen[candidate] && candidate != filePath {
						seen[candidate] = true
						resolved = append(resolved, candidate)
						break
					}
				}

			case "self":
				// use self::x -> resolve in same directory
				modParts := segments[1:]
				modPath := strings.Join(modParts, "/")

				candidates := []string{
					filepath.Join(dir, modPath+".rs"),
					filepath.Join(dir, modPath, "mod.rs"),
				}
				if len(modParts) > 1 {
					parentPath := strings.Join(modParts[:len(modParts)-1], "/")
					candidates = append(candidates, filepath.Join(dir, parentPath+".rs"))
				}
				for _, candidate := range candidates {
					if idx.Exists(candidate) && !seen[candidate] && candidate != filePath {
						seen[candidate] = true
						resolved = append(resolved, candidate)
						break
					}
				}
			}
		}
	}
	return resolved
}

func resolvePyImports(lines []string, filePath string, idx *FileIndex) []string {
	dir := filepath.Dir(filePath)
	var resolved []string

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// from .module import something (relative)
		if m := pyImportFrom.FindStringSubmatch(line); m != nil {
			imp := m[1]
			if strings.HasPrefix(imp, ".") {
				target := resolvePyRelative(dir, imp)
				if target != "" && idx.Exists(target) {
					resolved = append(resolved, target)
				}
			}
			continue
		}

		// import module (could be relative in some contexts, usually absolute)
		// We only track relative imports for accuracy
	}
	return resolved
}

// resolvePHPImports resolves PHP use statements to local file paths.
// It parses PSR-4 namespace imports and maps them to files using composer.json
// autoload config when available, falling back to common directory conventions.
func resolvePHPImports(lines []string, filePath string, root string, idx *FileIndex) []string {
	seen := make(map[string]bool)
	var resolved []string

	// Parse composer.json PSR-4 autoload mappings if available
	psr4Map := parsePHPComposerPSR4(root)

	for _, line := range lines {
		line = strings.TrimSpace(line)

		m := phpUseStmt.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		fqcn := m[1]

		// Skip PHP built-in namespaces that won't resolve to local files
		if isPHPBuiltinNamespace(fqcn) {
			continue
		}

		// Convert backslashes to forward slashes for path resolution
		classPath := strings.ReplaceAll(fqcn, "\\", "/") + ".php"

		// Strategy 1: Try composer.json PSR-4 mappings
		for prefix, dirs := range psr4Map {
			nsPrefix := strings.ReplaceAll(prefix, "\\", "/")
			if strings.HasPrefix(classPath, nsPrefix) {
				remainder := strings.TrimPrefix(classPath, nsPrefix)
				for _, dir := range dirs {
					candidate := filepath.Clean(filepath.Join(dir, remainder))
					if idx.Exists(candidate) && candidate != filePath && !seen[candidate] {
						seen[candidate] = true
						resolved = append(resolved, candidate)
					}
				}
			}
		}

		// Strategy 2: Try direct path (namespace mirrors directory structure)
		if idx.Exists(classPath) && classPath != filePath && !seen[classPath] {
			seen[classPath] = true
			resolved = append(resolved, classPath)
		}

		// Strategy 3: Strip first namespace segment and try common root prefixes
		// e.g., App\Models\User → Models/User.php under src/, app/, lib/
		parts := strings.SplitN(fqcn, "\\", 2)
		if len(parts) == 2 {
			remainder := strings.ReplaceAll(parts[1], "\\", "/") + ".php"
			for _, prefix := range []string{"src/", "app/", "lib/", ""} {
				candidate := filepath.Clean(prefix + remainder)
				if idx.Exists(candidate) && candidate != filePath && !seen[candidate] {
					seen[candidate] = true
					resolved = append(resolved, candidate)
				}
			}
		}
	}
	return resolved
}

// parsePHPComposerPSR4 reads composer.json and extracts PSR-4 autoload mappings.
// Returns a map from namespace prefix to directory paths.
func parsePHPComposerPSR4(root string) map[string][]string {
	data, err := os.ReadFile(filepath.Join(root, "composer.json"))
	if err != nil {
		return nil
	}

	var composer struct {
		Autoload struct {
			PSR4 map[string]json.RawMessage `json:"psr-4"`
		} `json:"autoload"`
	}
	if err := json.Unmarshal(data, &composer); err != nil {
		return nil
	}

	result := make(map[string][]string)
	for ns, raw := range composer.Autoload.PSR4 {
		// PSR-4 values can be a string or array of strings
		var single string
		if err := json.Unmarshal(raw, &single); err == nil {
			result[ns] = []string{single}
			continue
		}
		var multiple []string
		if err := json.Unmarshal(raw, &multiple); err == nil {
			result[ns] = multiple
		}
	}
	return result
}

// isPHPBuiltinNamespace returns true for PHP standard/extension namespaces
// that won't resolve to local project files.
func isPHPBuiltinNamespace(fqcn string) bool {
	builtins := []string{
		"Psr\\",
		"Symfony\\",
		"Illuminate\\",
		"Doctrine\\",
		"PHPUnit\\",
		"GuzzleHttp\\",
		"Monolog\\",
		"Carbon\\",
		"Ramsey\\",
		"Faker\\",
		"League\\",
		"Composer\\",
	}
	for _, prefix := range builtins {
		if strings.HasPrefix(fqcn, prefix) {
			return true
		}
	}
	return false
}

// resolveDartImports resolves Dart import/export statements to local file paths.
// Dart imports use either:
//   - 'package:myapp/models/user.dart' → maps to lib/models/user.dart
//   - 'relative/path.dart' → relative to current file
//   - 'dart:core' → SDK, skipped
func resolveDartImports(lines []string, filePath string, root string, idx *FileIndex) []string {
	dir := filepath.Dir(filePath)
	pkgName := detectDartPackageName(root)

	seen := make(map[string]bool)
	var resolved []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		m := dartImportRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		imp := m[1]

		// Skip SDK imports
		if strings.HasPrefix(imp, "dart:") {
			continue
		}

		var target string
		if strings.HasPrefix(imp, "package:") {
			// package:myapp/models/user.dart → lib/models/user.dart
			pkgPath := strings.TrimPrefix(imp, "package:")
			slash := strings.IndexByte(pkgPath, '/')
			if slash < 0 {
				continue
			}
			pkg := pkgPath[:slash]
			rest := pkgPath[slash+1:]
			// Only resolve imports from our own package
			if pkgName != "" && pkg != pkgName {
				continue
			}
			target = filepath.Join("lib", rest)
		} else {
			// Relative import
			target = filepath.Clean(filepath.Join(dir, imp))
		}

		if target != "" && target != filePath && idx.Exists(target) && !seen[target] {
			seen[target] = true
			resolved = append(resolved, target)
		}
	}

	return resolved
}

// detectDartPackageName reads the package name from pubspec.yaml.
func detectDartPackageName(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "pubspec.yaml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		}
	}
	return ""
}

// resolveScalaImports resolves Scala import statements to local file paths.
// Scala imports look like: import com.example.models.User or import com.example.models._
// Resolution uses the same source root conventions as Java.
func resolveScalaImports(lines []string, filePath string, idx *FileIndex) []string {
	seen := make(map[string]bool)
	var resolved []string

	// Standard library prefixes to skip
	skipPrefixes := []string{"scala.", "java.", "javax.", "akka.", "cats.", "zio."}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		m := scalaImportRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		imp := m[1]

		// Skip standard library / common external packages
		skip := false
		for _, prefix := range skipPrefixes {
			if strings.HasPrefix(imp, prefix) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		// Convert dots to path separators
		classPath := strings.ReplaceAll(imp, ".", "/")

		// Try source roots with both .scala and .java extensions
		roots := []string{"src/main/scala/", "src/main/java/", "src/", "app/", ""}
		exts := []string{".scala", ".java"}

		for _, root := range roots {
			for _, ext := range exts {
				target := root + classPath + ext
				if target != filePath && idx.Exists(target) && !seen[target] {
					seen[target] = true
					resolved = append(resolved, target)
				}
			}
			// Also try as a directory (wildcard import: import com.example.models._)
			// Find all source files in the directory
			dirTarget := root + classPath
			for _, f := range idx.ByDir(dirTarget) {
				if f.RelPath != filePath && (f.Lang == "scala" || f.Lang == "java") &&
					f.Class == scan.ClassSource && !seen[f.RelPath] {
					seen[f.RelPath] = true
					resolved = append(resolved, f.RelPath)
				}
			}
		}
	}

	return resolved
}

func readAllLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

// resolveElixirImports finds module references in an Elixir file and resolves
// them to file paths. Elixir modules are referenced by name (e.g.,
// QuotePilot.Notifications.Providers.Twilio) and map to file paths by convention.
func resolveElixirImports(lines []string, filePath string, idx *FileIndex) []string {
	// Build module name → file path lookup from all .ex files.
	// Convention: lib/quote_pilot/notifications/sms.ex → QuotePilot.Notifications.Sms
	modToFile := buildElixirModuleMap(idx)

	// Find the module defined in this file so we don't self-reference.
	selfModule := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "defmodule ") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				selfModule = strings.TrimSuffix(parts[1], ",")
				break
			}
		}
	}

	seen := make(map[string]bool)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Skip comments and module doc strings
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		for _, m := range exModuleRef.FindAllString(line, -1) {
			if m == selfModule {
				continue
			}
			target, ok := modToFile[m]
			if !ok {
				continue
			}
			if target == filePath {
				continue
			}
			if !seen[target] {
				seen[target] = true
			}
		}
	}

	result := make([]string, 0, len(seen))
	for path := range seen {
		result = append(result, path)
	}
	return result
}

// buildElixirModuleMap creates a mapping from Elixir module names to file paths
// by reading the actual defmodule line from each .ex file.
func buildElixirModuleMap(idx *FileIndex) map[string]string {
	modMap := make(map[string]string)
	for _, f := range idx.All() {
		if f.Lang != "elixir" {
			continue
		}
		if !strings.HasPrefix(f.RelPath, "lib/") {
			continue
		}
		modName := readDefmodule(filepath.Join("lib", strings.TrimPrefix(f.RelPath, "lib/")))
		if modName != "" {
			modMap[modName] = f.RelPath
		}
	}
	return modMap
}

// readDefmodule reads the first defmodule declaration from an Elixir file.
// Returns the module name (e.g., "QuotePilot.Notifications.SMS") or "".
func readDefmodule(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "defmodule ") {
			// Extract module name: "defmodule QuotePilot.Notifications.SMS do"
			rest := strings.TrimPrefix(line, "defmodule ")
			// Module name ends at space or comma
			if idx := strings.IndexAny(rest, " ,"); idx > 0 {
				return rest[:idx]
			}
			return rest
		}
	}
	return ""
}

// swiftSystemFrameworks lists common Apple/system frameworks to skip during import resolution.
var swiftSystemFrameworks = map[string]bool{
	"Foundation": true, "UIKit": true, "SwiftUI": true, "Combine": true,
	"CoreData": true, "CoreGraphics": true, "CoreLocation": true, "CoreImage": true,
	"CoreText": true, "CoreFoundation": true, "CoreML": true, "CoreMotion": true,
	"CoreBluetooth": true, "CoreMedia": true, "CoreVideo": true, "CoreAudio": true,
	"AVFoundation": true, "ARKit": true, "AppKit": true, "Accelerate": true,
	"AuthenticationServices": true, "BackgroundTasks": true, "CallKit": true,
	"CarPlay": true, "CloudKit": true, "Contacts": true, "ContactsUI": true,
	"CryptoKit": true, "Darwin": true, "Dispatch": true, "EventKit": true,
	"GameKit": true, "GameplayKit": true, "HealthKit": true, "HomeKit": true,
	"MapKit": true, "MediaPlayer": true, "MessageUI": true, "Metal": true,
	"MetalKit": true, "MultipeerConnectivity": true, "NaturalLanguage": true,
	"Network": true, "NotificationCenter": true, "ObjectiveC": true,
	"PassKit": true, "PhotosUI": true, "Photos": true, "PushKit": true,
	"QuartzCore": true, "RealityKit": true, "ReplayKit": true, "SafariServices": true,
	"SceneKit": true, "Security": true, "SpriteKit": true, "StoreKit": true,
	"SystemConfiguration": true, "UserNotifications": true, "Vision": true,
	"WatchKit": true, "WebKit": true, "WidgetKit": true, "XCTest": true,
	"os": true, "Swift": true, "SwiftData": true, "Observation": true,
}

// resolveSwiftImports resolves Swift import statements to local source files.
// It maps cross-module dependencies in Swift Package Manager projects.
func resolveSwiftImports(lines []string, filePath string, idx *FileIndex) []string {
	// Collect imported module names
	var moduleNames []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		m := swiftImportRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		modName := m[1]
		if swiftSystemFrameworks[modName] {
			continue
		}
		moduleNames = append(moduleNames, modName)
	}
	if len(moduleNames) == 0 {
		return nil
	}

	// Build a map of local module/target names → source directories
	localTargets := buildSwiftTargetMap(idx)

	seen := make(map[string]bool)
	var resolved []string
	for _, modName := range moduleNames {
		dirs, ok := localTargets[modName]
		if !ok {
			continue
		}
		for _, dir := range dirs {
			for _, f := range idx.FilesInDir(dir) {
				if f.Lang == "swift" && f.Class == scan.ClassSource && f.RelPath != filePath && !seen[f.RelPath] {
					seen[f.RelPath] = true
					resolved = append(resolved, f.RelPath)
				}
			}
		}
	}
	return resolved
}

// buildSwiftTargetMap discovers local Swift package targets and maps their names
// to source directories. It looks for Package.swift to find .target(name:) definitions,
// falling back to a directory-based heuristic under Sources/.
func buildSwiftTargetMap(idx *FileIndex) map[string][]string {
	targets := make(map[string][]string)

	// Check if Package.swift exists in the index
	if idx.Exists("Package.swift") {
		targets = parseSwiftPackageTargets(idx)
	}

	// Fallback/supplement: look for directories under Sources/
	for _, f := range idx.All() {
		if f.Lang != "swift" {
			continue
		}
		if !strings.HasPrefix(f.RelPath, "Sources/") {
			continue
		}
		// Extract the module directory: Sources/<ModuleName>/...
		rest := strings.TrimPrefix(f.RelPath, "Sources/")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) < 2 {
			continue
		}
		modName := parts[0]
		modDir := "Sources/" + modName
		if _, ok := targets[modName]; !ok {
			targets[modName] = []string{modDir}
		}
	}

	return targets
}

// swiftTargetNameRe matches .target(name: "Foo" or .executableTarget(name: "Foo" patterns.
var swiftTargetNameRe = regexp.MustCompile(`\.(?:target|executableTarget|testTarget)\s*\(\s*name:\s*"([^"]+)"`)

// swiftTargetPathRe matches path: "some/path" within a target definition.
var swiftTargetPathRe = regexp.MustCompile(`path:\s*"([^"]+)"`)

// parseSwiftPackageTargets reads Package.swift and extracts target name → directory mappings.
func parseSwiftPackageTargets(idx *FileIndex) map[string][]string {
	targets := make(map[string][]string)

	// Read the Package.swift file
	lines, err := readAllLines("Package.swift")
	if err != nil {
		return targets
	}

	for i, line := range lines {
		m := swiftTargetNameRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		targetName := m[1]
		// Skip test targets
		if strings.Contains(line, ".testTarget") {
			continue
		}

		// Look for a path: override in the next few lines
		customPath := ""
		for j := i; j < len(lines) && j < i+5; j++ {
			if pm := swiftTargetPathRe.FindStringSubmatch(lines[j]); pm != nil {
				customPath = pm[1]
				break
			}
		}

		if customPath != "" {
			targets[targetName] = []string{customPath}
		} else {
			// Default SPM convention: Sources/<TargetName>
			targets[targetName] = []string{"Sources/" + targetName}
		}
	}
	return targets
}

func resolvePyRelative(dir, imp string) string {
	// Count leading dots
	dots := 0
	for _, c := range imp {
		if c == '.' {
			dots++
		} else {
			break
		}
	}
	module := imp[dots:]

	// Go up (dots-1) directories
	targetDir := dir
	for i := 1; i < dots; i++ {
		targetDir = filepath.Dir(targetDir)
	}

	if module == "" {
		return ""
	}

	// Convert dots to path separators
	relPath := strings.ReplaceAll(module, ".", "/")
	return filepath.Join(targetDir, relPath) + ".py"
}
