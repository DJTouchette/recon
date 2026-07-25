package git

import (
	"strings"
)

// RecentChanges returns commits since the given time spec (e.g. "7d", "2024-01-01").
// Paths are relative to root, as with ParseLog.
func RecentChanges(root string, since string) ([]Commit, error) {
	res, err := RecentChangesOpts(root, since)
	if err != nil {
		return nil, err
	}
	return res.Commits, nil
}

// RecentChangesOpts is RecentChanges plus the coverage of the window it read.
// The window is bounded by time only, not by a commit count.
func RecentChangesOpts(root string, since string) (*LogResult, error) {
	return ParseLogOpts(root, Options{
		MaxCommits: Unlimited,
		Since:      convertSince(since),
	})
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
