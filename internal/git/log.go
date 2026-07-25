// Package git mines commit history for churn and co-change signal.
//
// # Wire format
//
// git log is asked for a machine-readable stream rather than the human
// oriented default, because the default is ambiguous:
//
//   - A commit with no file changes ("git commit --allow-empty", common for
//     CI and release markers) is followed by neither a blank line nor a file
//     list, so a line-oriented scanner walks straight into the next commit's
//     header and turns a SHA, an author name and a timestamp into "files".
//   - Without -z (or core.quotePath=false) git C-quotes any path with a byte
//     outside ASCII, so "src/café.go" arrives as "src/caf\303\251.go" and can
//     never be joined against a path produced by the filesystem walker.
//
// The stream therefore uses two ASCII control characters that cannot occur in
// a commit header and (in practice) not in a path either:
//
//	RS (0x1e) starts every commit record
//	US (0x1f) separates the header fields inside a record
//
// combined with -z, which makes git emit raw, NUL-terminated, unquoted paths.
// A record looks like:
//
//	\x1e <sha> \x1f <author> \x1f <date> \x1f <parents> \x1f <subject> \x1f \n <path>\0 <path>\0 \0
//
// The subject is the last header field, so a subject containing a stray
// separator can only corrupt the header/file boundary — which is detected,
// because git always writes "\n" (files follow) or NUL (no files) right after
// the final \x1f. Records whose first field is not a hex object name are
// dropped as fragments. Both cases are counted in Coverage rather than
// silently turned into filenames.
package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Tunables for how much history is mined. These are counts, not time windows:
// DefaultMaxCommits commits covers a week in a busy monorepo and a decade in a
// quiet library, so churn values are only comparable within a single repo.
// Callers that need comparability should pass an explicit Options.Since.
const (
	// DefaultMaxCommits is the size of the history window mined when a caller
	// does not ask for a specific one.
	DefaultMaxCommits = 500

	// Unlimited disables the commit-count window (Options.MaxCommits).
	Unlimited = -1
)

const (
	recordSep = "\x1e"
	fieldSep  = "\x1f"

	// logPretty must keep %s (the subject, the only free-form field) last so a
	// separator smuggled into it cannot shift the file list.
	logPretty = "--pretty=format:%x1e%H%x1f%an%x1f%ai%x1f%P%x1f%s%x1f"

	// headerFields is the number of \x1f-separated header fields plus the
	// trailing file blob.
	headerFields = 6
)

// Commit represents a parsed git commit with its changed files.
type Commit struct {
	Hash    string
	Author  string
	Date    string
	Message string
	// Files are relative to the root passed to ParseLog, not to the repository
	// top level, and never include paths outside that root.
	Files []string
	// IsRoot marks a commit with no parents. A root commit's "changes" are its
	// entire tree, which inflates churn; it is the reason a --depth 1 clone
	// reports every file as having changed exactly once.
	IsRoot bool
}

// Options controls how much history ParseLogOpts mines.
type Options struct {
	// MaxCommits caps the number of commits read. Zero means
	// DefaultMaxCommits; Unlimited (or any negative value) means no cap.
	MaxCommits int
	// Since is passed to git --since (already in git syntax). Optional.
	Since string
}

func (o Options) maxCommits() int {
	if o.MaxCommits == 0 {
		return DefaultMaxCommits
	}
	if o.MaxCommits < 0 {
		return Unlimited
	}
	return o.MaxCommits
}

// Coverage reports how much history was actually mined, so a caller can tell
// "mined 500 commits, this file never changed" apart from "I could mine
// nothing". A zero Coverage means no history was mined at all.
type Coverage struct {
	// Repo is true when the root is inside a git repository.
	Repo bool
	// Shallow is true for a shallow clone (CI's `--depth 1` default), where
	// the visible history is a fetch artifact rather than the real history.
	Shallow bool
	// Subdir is the repository-relative prefix of the root ("" at the top
	// level, e.g. "packages/api/" when pointed at one package of a monorepo).
	Subdir string
	// MaxCommits is the window that was requested (Unlimited if uncapped).
	MaxCommits int
	// CoChangeMaxFiles is the per-commit file threshold above which a commit
	// was excluded from co-change. Filled in by the co-change builder.
	CoChangeMaxFiles int

	// CommitsScanned is the number of commit records git returned.
	CommitsScanned int
	// CommitsWithFiles is the number of those commits that contributed at
	// least one file inside the root.
	CommitsWithFiles int
	// CommitsOversized counts commits excluded from co-change (but still
	// counted for churn) by CoChangeMaxFiles.
	CommitsOversized int
	// RootCommits counts parentless commits among those scanned.
	RootCommits int
	// MalformedRecords counts records that could not be parsed. Their files
	// are dropped rather than guessed; non-zero means signal was lost.
	MalformedRecords int
	// FilesOutsideRoot counts path entries discarded because they live outside
	// the root (normal when pointed at a subdirectory).
	FilesOutsideRoot int
	// WindowFull is true when the scan hit MaxCommits, i.e. older history
	// exists but was not mined.
	WindowFull bool
}

// ChurnTrustworthy reports whether the mined history is deep enough for churn
// and hotspot scores to mean anything. When it is false, a caller should
// report "churn unavailable" rather than "no churn".
func (c Coverage) ChurnTrustworthy() bool {
	if !c.Repo || c.CommitsWithFiles == 0 {
		return false
	}
	if c.Shallow {
		return false
	}
	// Nothing but root commits means every file looks equally changed.
	return c.CommitsWithFiles > c.RootCommits
}

// Reason explains why churn is not trustworthy. It is empty when it is.
func (c Coverage) Reason() string {
	switch {
	case !c.Repo:
		return "not a git repository"
	case c.Shallow && c.CommitsScanned <= 1:
		return "shallow clone (depth 1): history is a single root commit, churn is unavailable"
	case c.Shallow:
		return "shallow clone: only " + strconv.Itoa(c.CommitsScanned) + " commits fetched, churn is not comparable"
	case c.CommitsWithFiles == 0 && c.Subdir != "":
		return "no commits touched " + c.Subdir + " within the mined window"
	case c.CommitsScanned == 0:
		return "no commits in the mined window"
	case c.CommitsWithFiles == 0:
		return "no commits touched any tracked file within the mined window"
	case c.CommitsWithFiles <= c.RootCommits:
		return "history contains only root commits, churn is unavailable"
	}
	return ""
}

// ParseLog runs git log and parses the output into commits.
// maxCommits limits how many commits to parse (0 = DefaultMaxCommits).
func ParseLog(root string, maxCommits int) ([]Commit, error) {
	res, err := ParseLogOpts(root, Options{MaxCommits: maxCommits})
	if err != nil {
		return nil, err
	}
	return res.Commits, nil
}

// LogResult is the output of ParseLogOpts: the commits plus how much history
// they represent.
type LogResult struct {
	Commits  []Commit
	Coverage Coverage
}

// ParseLogOpts mines history under root and reports its own coverage.
// Paths are translated to be relative to root; anything outside it is dropped.
func ParseLogOpts(root string, opts Options) (*LogResult, error) {
	info, err := repoInfo(root)
	if err != nil {
		return nil, err
	}

	max := opts.maxCommits()

	cmd := exec.Command("git", logArgs(max, opts.Since, info.prefix != "")...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	pr := parseLog(out, info.prefix)

	return &LogResult{
		Commits: pr.commits,
		Coverage: Coverage{
			Repo:             true,
			Shallow:          info.shallow,
			Subdir:           info.prefix,
			MaxCommits:       max,
			CommitsScanned:   pr.records,
			CommitsWithFiles: len(pr.commits),
			RootCommits:      pr.rootCommits,
			MalformedRecords: pr.malformed,
			FilesOutsideRoot: pr.filesOutside,
			WindowFull:       max > 0 && pr.records >= max,
		},
	}, nil
}

func logArgs(maxCommits int, since string, limitToDir bool) []string {
	args := []string{
		// Belt and braces: -z already suppresses path quoting, but an explicit
		// core.quotePath=false means a future format change cannot silently
		// reintroduce octal-escaped paths.
		"-c", "core.quotePath=false",
		"log",
		"-z",
		"--name-only",
		"--no-merges",
		logPretty,
	}
	if maxCommits > 0 {
		args = append(args, "-n", strconv.Itoa(maxCommits))
	}
	if since != "" {
		args = append(args, "--since="+since)
	}
	if limitToDir {
		// Running against a subdirectory: spend the commit window on commits
		// that actually touch it instead of the whole monorepo.
		args = append(args, "--", ".")
	}
	return args
}

type parseResult struct {
	commits      []Commit
	records      int
	malformed    int
	rootCommits  int
	filesOutside int
}

// parseLog decodes the RS/US/NUL stream. prefix is the repository-relative
// path of the root ("" or "sub/dir/"); paths outside it are dropped.
func parseLog(data []byte, prefix string) parseResult {
	var res parseResult

	for _, rec := range bytes.Split(data, []byte(recordSep)) {
		if len(bytes.Trim(rec, "\x00\n")) == 0 {
			continue
		}

		fields := bytes.SplitN(rec, []byte(fieldSep), headerFields)
		if len(fields) < headerFields {
			res.records++
			res.malformed++
			continue
		}

		hash := string(fields[0])
		if !isObjectName(hash) {
			// A fragment: something injected a record separator upstream.
			// Drop it rather than inventing a commit.
			res.records++
			res.malformed++
			continue
		}

		res.records++

		c := Commit{
			Hash:    hash,
			Author:  string(fields[1]),
			Date:    string(fields[2]),
			IsRoot:  len(bytes.TrimSpace(fields[3])) == 0,
			Message: string(fields[4]),
		}
		if c.IsRoot {
			res.rootCommits++
		}

		names, ok := splitNames(fields[5])
		if !ok {
			// The header/file boundary is not where git puts it, so whatever
			// follows is not a reliable file list.
			res.malformed++
			continue
		}

		for _, n := range names {
			rel, inside := toRootRelative(n, prefix)
			if !inside {
				res.filesOutside++
				continue
			}
			c.Files = append(c.Files, rel)
		}

		// Commits with no in-root file changes carry no churn or co-change
		// signal; they are counted in Coverage instead of returned.
		if len(c.Files) > 0 {
			res.commits = append(res.commits, c)
		}
	}

	return res
}

// splitNames decodes the NUL-separated file blob that follows the header.
// git writes "\n" before a non-empty list and nothing (or the record's own NUL
// terminator) when the commit changed no files; anything else means the record
// was corrupted and the caller must not treat it as paths.
func splitNames(blob []byte) ([]string, bool) {
	switch {
	case len(blob) == 0:
		return nil, true
	case blob[0] == '\n':
		blob = blob[1:]
	case blob[0] == 0:
		// Empty commit: only the -z record terminator.
		if len(bytes.Trim(blob, "\x00")) != 0 {
			return nil, false
		}
		return nil, true
	default:
		return nil, false
	}

	var names []string
	for _, n := range bytes.Split(blob, []byte{0}) {
		if len(n) == 0 {
			continue
		}
		names = append(names, string(n))
	}
	return names, true
}

// toRootRelative rebases a repository-root-relative path onto the scan root.
// prefix is "" (root is the repository top level) or "sub/dir/".
func toRootRelative(path, prefix string) (string, bool) {
	if path == "" {
		return "", false
	}
	if prefix == "" {
		return path, true
	}
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	rel := path[len(prefix):]
	if rel == "" {
		return "", false
	}
	return rel, true
}

func isObjectName(s string) bool {
	// Abbreviated (7) through SHA-256 (64).
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

type repoMeta struct {
	prefix  string // repository-relative path of root, "" or "sub/dir/"
	shallow bool
}

// repoInfo asks git where the root sits inside the repository and whether the
// clone is shallow, in a single process.
func repoInfo(root string) (repoMeta, error) {
	cmd := exec.Command("git", "rev-parse", "--show-prefix", "--is-shallow-repository")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return repoMeta{}, err
	}

	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	var meta repoMeta
	if len(lines) > 0 {
		meta.prefix = strings.TrimSpace(lines[0])
	}
	if len(lines) > 1 {
		switch strings.TrimSpace(lines[1]) {
		case "true":
			meta.shallow = true
		case "false":
			meta.shallow = false
		default:
			// git < 2.15 does not know --is-shallow-repository.
			meta.shallow = hasShallowFile(root)
		}
	} else {
		meta.shallow = hasShallowFile(root)
	}

	if meta.prefix != "" && !strings.HasSuffix(meta.prefix, "/") {
		meta.prefix += "/"
	}
	return meta, nil
}

func hasShallowFile(root string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-common-dir")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return false
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	_, statErr := os.Stat(filepath.Join(dir, "shallow"))
	return statErr == nil
}

// IsShallow reports whether root lives in a shallow clone, where history is
// truncated by the fetch and churn cannot be trusted.
func IsShallow(root string) bool {
	info, err := repoInfo(root)
	if err != nil {
		return false
	}
	return info.shallow
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
