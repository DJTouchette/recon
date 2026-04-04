package index

import (
	"bufio"
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

	// Elixir module reference patterns
	exModuleRef = regexp.MustCompile(`\b([A-Z][A-Za-z0-9]*(?:\.[A-Z][A-Za-z0-9]*)+)`)
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
