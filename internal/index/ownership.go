package index

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// OwnerRule is a single CODEOWNERS pattern → owners mapping.
type OwnerRule struct {
	Priority int      `json:"priority"` // line number (higher = takes precedence)
	Pattern  string   `json:"pattern"`
	Owners   []string `json:"owners"`
}

// Ownership holds parsed CODEOWNERS rules and resolves file ownership.
type Ownership struct {
	rules    []OwnerRule
	compiled []compiledOwnerRule
}

type compiledOwnerRule struct {
	rule     OwnerRule
	matchFn  func(string) bool
}

// ParseCodeowners finds and parses the CODEOWNERS file from standard locations.
func ParseCodeowners(root string) *Ownership {
	paths := []string{
		filepath.Join(root, ".github", "CODEOWNERS"),
		filepath.Join(root, "CODEOWNERS"),
		filepath.Join(root, "docs", "CODEOWNERS"),
	}

	for _, p := range paths {
		if rules := parseCodeownersFile(p); len(rules) > 0 {
			return newOwnership(rules)
		}
	}

	return &Ownership{}
}

// NewOwnershipFromData creates an Ownership from stored rules.
func NewOwnershipFromData(rules []OwnerRule) *Ownership {
	return newOwnership(rules)
}

func newOwnership(rules []OwnerRule) *Ownership {
	o := &Ownership{rules: rules}
	o.compiled = make([]compiledOwnerRule, len(rules))
	for i, r := range rules {
		o.compiled[i] = compiledOwnerRule{
			rule:    r,
			matchFn: compileOwnerPattern(r.Pattern),
		}
	}
	return o
}

func parseCodeownersFile(path string) []OwnerRule {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	var rules []OwnerRule
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		pattern := fields[0]
		owners := fields[1:]

		rules = append(rules, OwnerRule{
			Priority: lineNum,
			Pattern:  pattern,
			Owners:   owners,
		})
	}

	return rules
}

// OwnersOf returns the owners for a given file path.
// Returns nil if no rule matches.
func (o *Ownership) OwnersOf(relPath string) []string {
	if o == nil || len(o.compiled) == 0 {
		return nil
	}

	// Last matching rule wins (iterate in reverse)
	for i := len(o.compiled) - 1; i >= 0; i-- {
		if o.compiled[i].matchFn(relPath) {
			return o.compiled[i].rule.Owners
		}
	}
	return nil
}

// Rules returns all parsed rules.
func (o *Ownership) Rules() []OwnerRule {
	if o == nil {
		return nil
	}
	return o.rules
}

// HasRules returns true if CODEOWNERS was found and parsed.
func (o *Ownership) HasRules() bool {
	return o != nil && len(o.rules) > 0
}

// compileOwnerPattern converts a CODEOWNERS pattern to a match function.
// CODEOWNERS patterns follow gitignore-like rules:
// - * matches anything except /
// - ** matches everything including /
// - /pattern means anchored to root
// - pattern/ means only directories
// - plain pattern matches anywhere in the path
func compileOwnerPattern(pattern string) func(string) bool {
	// Handle leading /
	anchored := false
	if strings.HasPrefix(pattern, "/") {
		anchored = true
		pattern = pattern[1:]
	}

	// Handle trailing /
	dirOnly := false
	if strings.HasSuffix(pattern, "/") {
		dirOnly = true
		pattern = strings.TrimSuffix(pattern, "/")
	}

	// Simple cases for speed
	if pattern == "*" {
		return func(string) bool { return true }
	}

	// Convert to a matching function
	return func(path string) bool {
		if dirOnly {
			// Only match directory prefixes
			if !strings.HasPrefix(path, pattern+"/") && path != pattern {
				return false
			}
			return true
		}

		if anchored {
			return matchGlob(pattern, path)
		}

		// Unanchored: try matching against full path and against basename
		if matchGlob(pattern, path) {
			return true
		}
		// Also try matching against just the file name
		base := filepath.Base(path)
		if matchGlob(pattern, base) {
			return true
		}
		// Try matching against path suffixes
		parts := strings.Split(path, "/")
		for i := range parts {
			suffix := strings.Join(parts[i:], "/")
			if matchGlob(pattern, suffix) {
				return true
			}
		}
		return false
	}
}

// matchGlob does simple glob matching supporting * and **.
func matchGlob(pattern, str string) bool {
	return doMatch(pattern, str)
}

func doMatch(pattern, str string) bool {
	for len(pattern) > 0 {
		switch {
		case len(pattern) >= 2 && pattern[0] == '*' && pattern[1] == '*':
			// ** matches everything
			rest := pattern[2:]
			if len(rest) > 0 && rest[0] == '/' {
				rest = rest[1:]
			}
			if len(rest) == 0 {
				return true
			}
			// Try matching rest against every suffix of str
			for i := 0; i <= len(str); i++ {
				if doMatch(rest, str[i:]) {
					return true
				}
			}
			return false

		case pattern[0] == '*':
			// * matches anything except /
			rest := pattern[1:]
			if len(rest) == 0 {
				// * at end matches rest of segment
				return !strings.Contains(str, "/")
			}
			for i := 0; i <= len(str); i++ {
				if i > 0 && str[i-1] == '/' {
					break // * doesn't cross /
				}
				if doMatch(rest, str[i:]) {
					return true
				}
			}
			return false

		case pattern[0] == '?':
			if len(str) == 0 || str[0] == '/' {
				return false
			}
			pattern = pattern[1:]
			str = str[1:]

		default:
			if len(str) == 0 || pattern[0] != str[0] {
				return false
			}
			pattern = pattern[1:]
			str = str[1:]
		}
	}

	return len(str) == 0
}
