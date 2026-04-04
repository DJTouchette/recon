package index

import (
	"path/filepath"
	"strings"

	"github.com/djtouchette/recon/internal/scan"
)

// TestMap maps source files to their test files and vice versa.
type TestMap struct {
	sourceToTest map[string][]string
	testToSource map[string]string
}

// NewTestMap builds test mappings from the file index.
func NewTestMap(idx *FileIndex) *TestMap {
	tm := &TestMap{
		sourceToTest: make(map[string][]string),
		testToSource: make(map[string]string),
	}

	tests := idx.ByClass(scan.ClassTest)
	sources := idx.ByClass(scan.ClassSource)

	// Build a map of source files by dir+basename for fast lookup
	sourceByKey := make(map[string]string, len(sources))
	for _, s := range sources {
		dir := filepath.Dir(s.RelPath)
		base := filepath.Base(s.RelPath)
		ext := filepath.Ext(base)
		name := strings.TrimSuffix(base, ext)
		key := dir + "/" + name + "|" + ext
		sourceByKey[key] = s.RelPath
	}

	for _, t := range tests {
		if src := findSourceForTest(t, sourceByKey, idx); src != "" {
			tm.sourceToTest[src] = append(tm.sourceToTest[src], t.RelPath)
			tm.testToSource[t.RelPath] = src
		}
	}

	return tm
}

func findSourceForTest(test *scan.FileEntry, sourceByKey map[string]string, idx *FileIndex) string {
	dir := filepath.Dir(test.RelPath)
	base := filepath.Base(test.RelPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	lext := strings.ToLower(ext)

	// Language-specific test naming conventions
	var candidates []string

	switch lext {
	case ".go":
		// foo_test.go → foo.go
		if strings.HasSuffix(name, "_test") {
			srcName := strings.TrimSuffix(name, "_test")
			candidates = append(candidates, dir+"/"+srcName+ext)
		}

	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".mts":
		// foo.test.ts → foo.ts, foo.spec.tsx → foo.tsx
		srcName := name
		srcName = strings.TrimSuffix(srcName, ".test")
		srcName = strings.TrimSuffix(srcName, ".spec")
		if srcName != name {
			// Try same directory
			candidates = append(candidates, dir+"/"+srcName+ext)
			// Try without test directory (__tests__/foo.test.ts → foo.ts)
			parentDir := filepath.Dir(dir)
			candidates = append(candidates, parentDir+"/"+srcName+ext)
			// Try various extensions
			for _, tryExt := range []string{".ts", ".tsx", ".js", ".jsx"} {
				if tryExt != ext {
					candidates = append(candidates, dir+"/"+srcName+tryExt)
					candidates = append(candidates, parentDir+"/"+srcName+tryExt)
				}
			}
		}

	case ".py":
		// test_foo.py → foo.py; foo_test.py → foo.py
		srcName := name
		if strings.HasPrefix(srcName, "test_") {
			srcName = strings.TrimPrefix(srcName, "test_")
		} else if strings.HasSuffix(srcName, "_test") {
			srcName = strings.TrimSuffix(srcName, "_test")
		}
		if srcName != name {
			candidates = append(candidates, dir+"/"+srcName+ext)
			// Python tests often in a parallel tests/ directory
			testlessDir := strings.Replace(dir, "tests/", "", 1)
			testlessDir = strings.Replace(testlessDir, "test/", "", 1)
			if testlessDir != dir {
				candidates = append(candidates, testlessDir+"/"+srcName+ext)
			}
		}

	case ".rb":
		// foo_spec.rb → foo.rb; foo_test.rb → foo.rb
		srcName := name
		srcName = strings.TrimSuffix(srcName, "_spec")
		srcName = strings.TrimSuffix(srcName, "_test")
		if srcName != name {
			candidates = append(candidates, dir+"/"+srcName+ext)
			speclessDir := strings.Replace(dir, "spec/", "", 1)
			if speclessDir != dir {
				candidates = append(candidates, speclessDir+"/"+srcName+ext)
			}
		}

	case ".exs":
		// foo_test.exs → foo.ex
		if strings.HasSuffix(name, "_test") {
			srcName := strings.TrimSuffix(name, "_test")
			candidates = append(candidates, dir+"/"+srcName+".ex")
			// Elixir tests in test/ mirror lib/
			testlessDir := strings.Replace(dir, "test/", "lib/", 1)
			if testlessDir != dir {
				candidates = append(candidates, testlessDir+"/"+srcName+".ex")
			}
		}

	case ".cs":
		// FooTests.cs → Foo.cs; FooTest.cs → Foo.cs
		srcName := name
		srcName = strings.TrimSuffix(srcName, "Tests")
		srcName = strings.TrimSuffix(srcName, "Test")
		if srcName != name {
			candidates = append(candidates, dir+"/"+srcName+ext)
			// C# tests often in parallel project dirs
			// e.g., MyApp.Tests/FooTests.cs → MyApp/Foo.cs
			// Also handle test/ → src/ directory swap common in .NET
			parts := strings.Split(dir, "/")
			for i, p := range parts {
				testSuffixes := []string{".Tests", ".Test", ".IntegrationTests", ".UnitTests"}
				for _, suffix := range testSuffixes {
					if strings.HasSuffix(p, suffix) {
						srcDir := strings.TrimSuffix(p, suffix)
						newParts := make([]string, len(parts))
						copy(newParts, parts)
						newParts[i] = srcDir
						joined := strings.Join(newParts, "/")
						candidates = append(candidates, joined+"/"+srcName+ext)
						// Also try test/ → src/ swap
						swapped := strings.Replace(joined, "/test/", "/src/", 1)
						if swapped != joined {
							candidates = append(candidates, swapped+"/"+srcName+ext)
						}
					}
				}
				// Also try plain test/ → src/ swap without .Tests suffix
				if p == "test" || p == "tests" {
					newParts := make([]string, len(parts))
					copy(newParts, parts)
					newParts[i] = "src"
					candidates = append(candidates, strings.Join(newParts, "/")+"/"+srcName+ext)
				}
			}
		}

	case ".java":
		// FooTest.java → Foo.java
		srcName := name
		srcName = strings.TrimSuffix(srcName, "Test")
		srcName = strings.TrimSuffix(srcName, "Tests")
		srcName = strings.TrimSuffix(srcName, "IT")
		if srcName != name {
			candidates = append(candidates, dir+"/"+srcName+ext)
			// Java: src/test/java/... → src/main/java/...
			testlessDir := strings.Replace(dir, "src/test/", "src/main/", 1)
			if testlessDir != dir {
				candidates = append(candidates, testlessDir+"/"+srcName+ext)
			}
		}

	case ".rs":
		// foo_test.rs → foo.rs (rare but exists)
		if strings.HasSuffix(name, "_test") {
			srcName := strings.TrimSuffix(name, "_test")
			candidates = append(candidates, dir+"/"+srcName+ext)
		}
		// Files in tests/ directory → try matching by name against src/ files
		if strings.Contains(dir, "tests") {
			srcDir := strings.Replace(dir, "tests", "src", 1)
			candidates = append(candidates, srcDir+"/"+name+ext)
		}
		// src/foo/tests.rs → src/foo/mod.rs or src/foo.rs
		if name == "tests" {
			candidates = append(candidates, dir+"/mod.rs")
			parentDir := filepath.Dir(dir)
			dirBase := filepath.Base(dir)
			candidates = append(candidates, parentDir+"/"+dirBase+".rs")
		}

	case ".kt", ".kts":
		// FooTest.kt → Foo.kt (same as Java pattern)
		srcName := name
		srcName = strings.TrimSuffix(srcName, "Tests")
		srcName = strings.TrimSuffix(srcName, "Test")
		if srcName != name {
			candidates = append(candidates, dir+"/"+srcName+ext)
			// src/test/kotlin/... → src/main/kotlin/...
			testlessDir := strings.Replace(dir, "src/test/kotlin", "src/main/kotlin", 1)
			if testlessDir != dir {
				candidates = append(candidates, testlessDir+"/"+srcName+ext)
			}
			// Also try .java extension (mixed Kotlin/Java projects)
			candidates = append(candidates, dir+"/"+srcName+".java")
			if testlessDir != dir {
				candidates = append(candidates, testlessDir+"/"+srcName+".java")
			}
		}

	case ".php":
		// FooTest.php → Foo.php
		srcName := name
		srcName = strings.TrimSuffix(srcName, "Test")
		if srcName != name {
			candidates = append(candidates, dir+"/"+srcName+ext)
			// tests/... → src/... (PSR-4 convention)
			testlessDir := strings.Replace(dir, "tests/", "src/", 1)
			if testlessDir != dir {
				candidates = append(candidates, testlessDir+"/"+srcName+ext)
			}
			// tests/Unit/FooTest.php → src/Foo.php or app/Foo.php
			unitlessDir := strings.Replace(dir, "tests/Unit/", "src/", 1)
			if unitlessDir != dir {
				candidates = append(candidates, unitlessDir+"/"+srcName+ext)
			}
			appDir := strings.Replace(dir, "tests/Unit/", "app/", 1)
			if appDir != dir {
				candidates = append(candidates, appDir+"/"+srcName+ext)
			}
			appDir2 := strings.Replace(dir, "tests/", "app/", 1)
			if appDir2 != dir {
				candidates = append(candidates, appDir2+"/"+srcName+ext)
			}
		}

	case ".swift":
		// FooTests.swift → Foo.swift; FooTest.swift → Foo.swift
		srcName := name
		srcName = strings.TrimSuffix(srcName, "Tests")
		srcName = strings.TrimSuffix(srcName, "Test")
		if srcName != name {
			candidates = append(candidates, dir+"/"+srcName+ext)
			// Tests/ModuleTests/FooTests.swift → Sources/Module/Foo.swift
			if strings.HasPrefix(dir, "Tests/") {
				testModDir := strings.TrimPrefix(dir, "Tests/")
				// Strip "Tests" suffix from module test directory name
				testModDir = strings.TrimSuffix(testModDir, "Tests")
				testModDir = strings.TrimSuffix(testModDir, "Test")
				srcDir := "Sources/" + testModDir
				candidates = append(candidates, srcDir+"/"+srcName+ext)
			}
		}

	case ".dart":
		// foo_test.dart → foo.dart
		if strings.HasSuffix(name, "_test") {
			srcName := strings.TrimSuffix(name, "_test")
			candidates = append(candidates, dir+"/"+srcName+ext)
			// test/models/user_test.dart → lib/models/user.dart
			testlessDir := strings.Replace(dir, "test/", "lib/", 1)
			if testlessDir != dir {
				candidates = append(candidates, testlessDir+"/"+srcName+ext)
			}
			// Also try: test/user_test.dart → lib/src/user.dart
			testlessDir2 := strings.Replace(dir, "test", "lib/src", 1)
			if testlessDir2 != dir {
				candidates = append(candidates, testlessDir2+"/"+srcName+ext)
			}
		}

	case ".scala":
		// FooSpec.scala → Foo.scala; FooTest.scala → Foo.scala; FooSuite.scala → Foo.scala
		srcName := name
		srcName = strings.TrimSuffix(srcName, "Spec")
		srcName = strings.TrimSuffix(srcName, "Test")
		srcName = strings.TrimSuffix(srcName, "Tests")
		srcName = strings.TrimSuffix(srcName, "Suite")
		if srcName != name {
			candidates = append(candidates, dir+"/"+srcName+ext)
			// src/test/scala/... → src/main/scala/...
			testlessDir := strings.Replace(dir, "src/test/", "src/main/", 1)
			if testlessDir != dir {
				candidates = append(candidates, testlessDir+"/"+srcName+ext)
			}
			// Also try .java extension (mixed projects)
			candidates = append(candidates, dir+"/"+srcName+".java")
			if testlessDir != dir {
				candidates = append(candidates, testlessDir+"/"+srcName+".java")
			}
		}
	}

	// Check candidates against the index
	for _, c := range candidates {
		c = filepath.Clean(c)
		if idx.Exists(c) {
			return c
		}
	}

	return ""
}

// NewTestMapFromData creates a TestMap from pre-computed mappings.
func NewTestMapFromData(sourceToTest map[string][]string, testToSource map[string]string) *TestMap {
	return &TestMap{
		sourceToTest: sourceToTest,
		testToSource: testToSource,
	}
}

// TestsFor returns test files for a given source file path.
func (tm *TestMap) TestsFor(srcPath string) []string {
	return tm.sourceToTest[srcPath]
}

// SourceFor returns the source file for a given test file.
func (tm *TestMap) SourceFor(testPath string) string {
	return tm.testToSource[testPath]
}

// AllMappings returns the full source→test map.
func (tm *TestMap) AllMappings() map[string][]string {
	return tm.sourceToTest
}

// TestToSourceMap returns the full test→source map.
func (tm *TestMap) TestToSourceMap() map[string]string {
	return tm.testToSource
}

// ClassifyTestKind guesses the test kind from the path.
func ClassifyTestKind(relPath string) string {
	lpath := strings.ToLower(relPath)
	if strings.Contains(lpath, "e2e") || strings.Contains(lpath, "playwright") ||
		strings.Contains(lpath, "cypress") || strings.Contains(lpath, "selenium") {
		return "e2e"
	}
	if strings.Contains(lpath, "integration") || strings.Contains(lpath, "integ") {
		return "integration"
	}
	return "unit"
}
