package git

import (
	"os/exec"
	"strings"
)

// RecentChanges returns commits since the given time spec (e.g. "7d", "2024-01-01").
func RecentChanges(root string, since string) ([]Commit, error) {
	// Convert shorthand like "7d" to git-compatible format
	gitSince := convertSince(since)

	cmd := exec.Command("git", "log",
		"--name-only",
		"--pretty=format:%H%n%an%n%ai%n%s",
		"--no-merges",
		"--since="+gitSince,
	)
	cmd.Dir = root

	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	return parseLogOutput(out), nil
}

func convertSince(since string) string {
	if since == "" {
		return "7 days ago"
	}

	// Handle shorthand formats: 7d, 2w, 1m, etc.
	if len(since) >= 2 {
		num := since[:len(since)-1]
		unit := since[len(since)-1]

		allDigits := true
		for _, c := range num {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}

		if allDigits {
			switch unit {
			case 'd':
				return num + " days ago"
			case 'w':
				return num + " weeks ago"
			case 'm':
				return num + " months ago"
			case 'y':
				return num + " years ago"
			}
		}
	}

	// If it looks like an ISO date, pass through
	if strings.Contains(since, "-") {
		return since
	}

	return since
}
