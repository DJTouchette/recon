package scan

import (
	"bufio"
	"bytes"
	"os"
	"regexp"
	"strings"
)

// Matcher checks paths against gitignore-style rules.
// It forms a linked list: each directory's .gitignore creates a child matcher.
type Matcher struct {
	rules  []rule
	parent *Matcher
}

type rule struct {
	negate   bool
	dirOnly  bool
	anchored bool
	regex    *regexp.Regexp
}

// hardcoded directories to always skip — checked before gitignore for speed.
var hardcodedIgnoreDirs = map[string]bool{
	".git":           true,
	".recon":         true,
	"node_modules":   true,
	"__pycache__":    true,
	".tox":           true,
	".mypy_cache":    true,
	".pytest_cache":  true,
	".next":          true,
	".nuxt":          true,
	".svelte-kit":    true,
	".angular":       true,
	".turbo":         true,
	".vercel":        true,
	".terraform":     true,
	".gradle":        true,
	".idea":          true,
	".vs":            true,
	".vscode":        true,
}

// IsHardcodedIgnore returns true for directory names that are always skipped.
func IsHardcodedIgnore(name string) bool {
	return hardcodedIgnoreDirs[name]
}

// DefaultMatcher returns a matcher with built-in ignore rules.
func DefaultMatcher() *Matcher {
	return &Matcher{}
}

// Child creates a new matcher that inherits from this one and adds rules.
func (m *Matcher) Child(rules []rule) *Matcher {
	if len(rules) == 0 {
		return m
	}
	return &Matcher{rules: rules, parent: m}
}

// Match returns true if the path should be ignored.
// relPath is relative to repo root using forward slashes.
// baseDir is the directory of the .gitignore relative to repo root.
func (m *Matcher) Match(relPath string, isDir bool) bool {
	for cur := m; cur != nil; cur = cur.parent {
		for i := len(cur.rules) - 1; i >= 0; i-- {
			r := &cur.rules[i]
			if r.dirOnly && !isDir {
				continue
			}
			if r.anchored {
				if r.regex.MatchString(relPath) {
					return !r.negate
				}
			} else {
				// Match against basename
				base := relPath
				if idx := strings.LastIndex(relPath, "/"); idx >= 0 {
					base = relPath[idx+1:]
				}
				if r.regex.MatchString(base) {
					return !r.negate
				}
			}
		}
	}
	return false
}

// ParseGitignoreFile reads a .gitignore file and returns compiled rules.
// baseDir is the directory containing the .gitignore, relative to repo root.
func ParseGitignoreFile(path, baseDir string) []rule {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return ParseGitignore(data, baseDir)
}

// ParseGitignore parses gitignore content into rules.
func ParseGitignore(data []byte, baseDir string) []rule {
	var rules []rule
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimRight(line, " \t")
		if line == "" || line[0] == '#' {
			continue
		}

		r := compileRule(line, baseDir)
		if r.regex != nil {
			rules = append(rules, r)
		}
	}
	return rules
}

func compileRule(pattern, baseDir string) rule {
	var r rule

	// Handle negation
	if strings.HasPrefix(pattern, "!") {
		r.negate = true
		pattern = pattern[1:]
	}

	// Handle trailing slash (directory only)
	if strings.HasSuffix(pattern, "/") {
		r.dirOnly = true
		pattern = strings.TrimSuffix(pattern, "/")
	}

	// Handle leading slash (anchored to gitignore dir)
	if strings.HasPrefix(pattern, "/") {
		r.anchored = true
		pattern = pattern[1:]
	} else if strings.Contains(pattern, "/") {
		// Patterns with / in the middle are also anchored
		r.anchored = true
	}

	// If anchored, prepend baseDir so we match against full relPath
	if r.anchored && baseDir != "" && baseDir != "." {
		pattern = baseDir + "/" + pattern
	}

	regex := patternToRegex(pattern)
	if regex == nil {
		return r
	}
	r.regex = regex
	return r
}

func patternToRegex(pattern string) *regexp.Regexp {
	var buf strings.Builder
	buf.WriteString("^")

	i := 0
	for i < len(pattern) {
		c := pattern[i]
		switch {
		case c == '*' && i+1 < len(pattern) && pattern[i+1] == '*':
			if i+2 < len(pattern) && pattern[i+2] == '/' {
				// **/ matches zero or more directories
				buf.WriteString("(?:.+/)?")
				i += 3
			} else {
				// ** at end matches everything
				buf.WriteString(".*")
				i += 2
			}
		case c == '*':
			buf.WriteString("[^/]*")
			i++
		case c == '?':
			buf.WriteString("[^/]")
			i++
		case c == '[':
			// Character class
			j := i + 1
			if j < len(pattern) && pattern[j] == '!' {
				buf.WriteString("[^")
				j++
			} else {
				buf.WriteByte('[')
			}
			for j < len(pattern) && pattern[j] != ']' {
				if pattern[j] == '\\' && j+1 < len(pattern) {
					buf.WriteByte(pattern[j])
					buf.WriteByte(pattern[j+1])
					j += 2
				} else {
					buf.WriteByte(pattern[j])
					j++
				}
			}
			if j < len(pattern) {
				buf.WriteByte(']')
				j++
			}
			i = j
		case c == '.' || c == '(' || c == ')' || c == '{' || c == '}' ||
			c == '+' || c == '^' || c == '$' || c == '|' || c == '\\':
			buf.WriteByte('\\')
			buf.WriteByte(c)
			i++
		default:
			buf.WriteByte(c)
			i++
		}
	}

	buf.WriteString("$")
	re, err := regexp.Compile(buf.String())
	if err != nil {
		return nil
	}
	return re
}
