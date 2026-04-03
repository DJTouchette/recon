package git

import (
	"bufio"
	"bytes"
	"os/exec"
	"strings"
)

// Commit represents a parsed git commit with its changed files.
type Commit struct {
	Hash    string
	Author  string
	Date    string
	Message string
	Files   []string
}

// ParseLog runs git log and parses the output into commits.
// maxCommits limits how many commits to parse (0 = default 500).
func ParseLog(root string, maxCommits int) ([]Commit, error) {
	if maxCommits <= 0 {
		maxCommits = 500
	}

	cmd := exec.Command("git", "log",
		"--name-only",
		"--pretty=format:%H%n%an%n%ai%n%s",
		"--no-merges",
		"-n", itoa(maxCommits),
	)
	cmd.Dir = root

	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	return parseLogOutput(out), nil
}

func parseLogOutput(data []byte) []Commit {
	var commits []Commit
	scanner := bufio.NewScanner(bytes.NewReader(data))

	for scanner.Scan() {
		hash := scanner.Text()
		if hash == "" {
			continue
		}

		var c Commit
		c.Hash = hash

		if !scanner.Scan() {
			break
		}
		c.Author = scanner.Text()

		if !scanner.Scan() {
			break
		}
		c.Date = scanner.Text()

		if !scanner.Scan() {
			break
		}
		c.Message = scanner.Text()

		// Skip blank line between header and file list
		if scanner.Scan() && scanner.Text() == "" {
			// expected blank line, continue to files
		} else if scanner.Text() != "" {
			// No blank line — this line is already a file
			c.Files = append(c.Files, scanner.Text())
		}

		// Read file names until blank line or EOF
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				break
			}
			c.Files = append(c.Files, line)
		}

		if len(c.Files) > 0 {
			commits = append(commits, c)
		}
	}

	return commits
}

// GetHEAD returns the current HEAD sha.
func GetHEAD(root string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// IsGitRepo returns true if the directory is inside a git repository.
func IsGitRepo(root string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
