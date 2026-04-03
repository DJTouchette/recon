package scan

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		relPath string
		name    string
		want    FileClass
	}{
		{"src/main.go", "main.go", ClassSource},
		{"src/main_test.go", "main_test.go", ClassTest},
		{"src/app.test.ts", "app.test.ts", ClassTest},
		{"src/app.spec.tsx", "app.spec.tsx", ClassTest},
		{"test_main.py", "test_main.py", ClassTest},
		{"go.mod", "go.mod", ClassConfig},
		{"package.json", "package.json", ClassConfig},
		{"backend/Foo.csproj", "Foo.csproj", ClassConfig},
		{"README.md", "README.md", ClassDoc},
		{"docs/guide.txt", "guide.txt", ClassDoc},
		{"assets/logo.png", "logo.png", ClassAsset},
		{"vendor/lib.go", "lib.go", ClassVendor},
		{"generated/types.pb.go", "types.pb.go", ClassGenerated},
		{"src/utils.min.js", "utils.min.js", ClassGenerated},
		{"deploy.sh", "deploy.sh", ClassScript},
		{"scripts/run.ps1", "run.ps1", ClassScript},
		{"data.csv", "data.csv", ClassData},
		{"__tests__/foo.tsx", "foo.tsx", ClassTest},
		{"backend/Foo.Tests/BarTest.cs", "BarTest.cs", ClassTest},
		{"src/FooTests.cs", "FooTests.cs", ClassTest},
		{"src/app.ts", "app.ts", ClassSource},
	}

	for _, tt := range tests {
		t.Run(tt.relPath, func(t *testing.T) {
			got := Classify(tt.relPath, tt.name)
			if got != tt.want {
				t.Errorf("Classify(%q, %q) = %v, want %v", tt.relPath, tt.name, got, tt.want)
			}
		})
	}
}

func TestLangFromExt(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"main.go", "go"},
		{"app.ts", "typescript"},
		{"app.tsx", "typescript"},
		{"index.js", "javascript"},
		{"app.py", "python"},
		{"lib.rs", "rust"},
		{"Foo.cs", "csharp"},
		{"Main.java", "java"},
		{"app.rb", "ruby"},
		{"app.ex", "elixir"},
		{"unknown.xyz", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LangFromExt(tt.name)
			if got != tt.want {
				t.Errorf("LangFromExt(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}
