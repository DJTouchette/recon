package scan

import (
	"testing"
)

func TestParseGitignore(t *testing.T) {
	tests := []struct {
		name    string
		content string
		path    string
		isDir   bool
		want    bool
	}{
		{"simple file", "*.log", "debug.log", false, true},
		{"simple file no match", "*.log", "debug.txt", false, false},
		{"directory pattern", "build/", "build", true, true},
		{"directory pattern file", "build/", "build", false, false},
		{"nested file", "*.log", "src/debug.log", false, true},
		{"anchored pattern", "/build", "build", false, true},
		{"anchored no match deep", "/build", "src/build", false, false},
		{"comment", "# comment\n*.log", "test.log", false, true},
		{"empty line", "\n*.log\n", "test.log", false, true},
		{"negation", "*.log\n!important.log", "important.log", false, false},
		{"negation other", "*.log\n!important.log", "debug.log", false, true},
		{"double star", "**/logs", "logs", false, true},
		{"double star nested", "**/logs", "src/logs", false, true},
		{"double star deep", "**/logs", "src/deep/logs", false, true},
		{"path pattern", "src/build", "src/build", false, true},
		{"path pattern no match", "src/build", "lib/build", false, false},
		{"wildcard", "doc/*.txt", "doc/notes.txt", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := ParseGitignore([]byte(tt.content), "")
			m := DefaultMatcher().Child(rules)
			got := m.Match(tt.path, tt.isDir)
			if got != tt.want {
				t.Errorf("Match(%q, isDir=%v) = %v, want %v", tt.path, tt.isDir, got, tt.want)
			}
		})
	}
}

func TestMatcherChild(t *testing.T) {
	parent := DefaultMatcher().Child(ParseGitignore([]byte("*.log"), ""))
	child := parent.Child(ParseGitignore([]byte("!important.log"), "src"))

	// Parent should ignore all .log
	if !parent.Match("debug.log", false) {
		t.Error("parent should match debug.log")
	}

	// Child should un-ignore important.log under src/
	if child.Match("src/important.log", false) {
		t.Error("child should not match src/important.log (negated)")
	}

	// Child should still ignore other .log files
	if !child.Match("src/debug.log", false) {
		t.Error("child should match src/debug.log")
	}
}

func TestHardcodedIgnore(t *testing.T) {
	if !IsHardcodedIgnore(".git") {
		t.Error("should ignore .git")
	}
	if !IsHardcodedIgnore("node_modules") {
		t.Error("should ignore node_modules")
	}
	if IsHardcodedIgnore("src") {
		t.Error("should not ignore src")
	}
}
