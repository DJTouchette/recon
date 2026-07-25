package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// --- test repo helpers ---------------------------------------------------

type testRepo struct {
	t   *testing.T
	dir string
}

// newTestRepo creates a real, isolated git repository in a temp dir.
func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// Isolate the code under test from the developer's own git config
	// (core.quotePath, log.showSignature, ...).
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	r := &testRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q")
	r.git("config", "user.email", "test@example.com")
	r.git("config", "user.name", "Test User")
	r.git("config", "commit.gpgsign", "false")
	return r
}

func (r *testRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_AUTHOR_NAME=Test User",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *testRepo) write(rel, content string) {
	r.t.Helper()
	full := filepath.Join(r.dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// commit stages everything and commits with the given subject.
func (r *testRepo) commit(subject string) {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "-m", subject)
}

func subjects(commits []Commit) []string {
	var out []string
	for _, c := range commits {
		out = append(out, c.Message)
	}
	return out
}

func findCommit(t *testing.T, commits []Commit, subject string) Commit {
	t.Helper()
	for _, c := range commits {
		if c.Message == subject {
			return c
		}
	}
	t.Fatalf("commit %q not found, have %q", subject, subjects(commits))
	return Commit{}
}

// record formats a commit the way `git log -z` with logPretty does.
func record(hash, author, date, parents, subject string, files ...string) string {
	s := recordSep + hash + fieldSep + author + fieldSep + date + fieldSep +
		parents + fieldSep + subject + fieldSep
	if len(files) == 0 {
		return s + "\x00"
	}
	s += "\n"
	for _, f := range files {
		s += f + "\x00"
	}
	return s + "\x00"
}

// --- parser unit tests ---------------------------------------------------

func TestParseLogHappyPath(t *testing.T) {
	in := record("abc123def456", "John Doe", "2024-01-15 10:30:00 +0000", "aaa111", "Fix authentication bug",
		"src/auth/login.go", "src/auth/login_test.go") +
		record("def456abc789", "Jane Smith", "2024-01-14 09:00:00 +0000", "bbb222", "Add user profile endpoint",
			"src/api/profile.go", "src/api/routes.go", "src/models/user.go")

	res := parseLog([]byte(in), "")

	if len(res.commits) != 2 {
		t.Fatalf("expected 2 commits, got %d: %+v", len(res.commits), res.commits)
	}
	c := res.commits[0]
	if c.Hash != "abc123def456" {
		t.Errorf("hash = %q", c.Hash)
	}
	if c.Author != "John Doe" {
		t.Errorf("author = %q", c.Author)
	}
	if c.Date != "2024-01-15 10:30:00 +0000" {
		t.Errorf("date = %q", c.Date)
	}
	if c.Message != "Fix authentication bug" {
		t.Errorf("message = %q", c.Message)
	}
	if len(c.Files) != 2 {
		t.Errorf("files = %v, want 2", c.Files)
	}
	if c.IsRoot {
		t.Error("commit with a parent reported as root")
	}
	if len(res.commits[1].Files) != 3 {
		t.Errorf("files = %v, want 3", res.commits[1].Files)
	}
	if res.malformed != 0 {
		t.Errorf("malformed = %d, want 0", res.malformed)
	}
	if res.records != 2 {
		t.Errorf("records = %d, want 2", res.records)
	}
}

// Regression for the desync that turned a SHA, an author and a date into
// "files" when a commit changed nothing.
func TestParseLogEmptyCommitDoesNotDesync(t *testing.T) {
	in := record("aaaaaaa1111111", "Alice", "2024-01-03 00:00:00 +0000", "p1", "third", "a.txt") +
		record("bbbbbbb2222222", "CI Bot", "2024-01-02 00:00:00 +0000", "p2", "empty ci marker") +
		record("ccccccc3333333", "Alice", "2024-01-01 00:00:00 +0000", "", "first", "a.txt")

	res := parseLog([]byte(in), "")

	if res.records != 3 {
		t.Errorf("records = %d, want 3", res.records)
	}
	// The empty commit carries no signal and is not returned, but it must not
	// swallow the commit that follows it.
	if got := subjects(res.commits); len(got) != 2 || got[0] != "third" || got[1] != "first" {
		t.Fatalf("commits = %q, want [third first]", got)
	}
	for _, c := range res.commits {
		for _, f := range c.Files {
			if f != "a.txt" {
				t.Errorf("fabricated file %q in commit %q", f, c.Message)
			}
		}
	}
	if res.rootCommits != 1 {
		t.Errorf("rootCommits = %d, want 1", res.rootCommits)
	}
}

func TestParseLogEmptySubject(t *testing.T) {
	in := record("aaaaaaa1111111", "Alice", "2024-01-02 00:00:00 +0000", "p1", "", "a.txt") +
		record("bbbbbbb2222222", "Bob", "2024-01-01 00:00:00 +0000", "", "first", "b.txt")

	res := parseLog([]byte(in), "")

	if len(res.commits) != 2 {
		t.Fatalf("commits = %d, want 2: %+v", len(res.commits), res.commits)
	}
	if res.commits[0].Message != "" {
		t.Errorf("message = %q, want empty", res.commits[0].Message)
	}
	if got := res.commits[0].Files; len(got) != 1 || got[0] != "a.txt" {
		t.Errorf("files = %v, want [a.txt]", got)
	}
	if got := res.commits[1].Files; len(got) != 1 || got[0] != "b.txt" {
		t.Errorf("files = %v, want [b.txt]", got)
	}
}

// A separator smuggled into a subject must lose that record's file list, not
// invent paths out of the subject text.
func TestParseLogMalformedRecordsDropFilesInsteadOfInventingThem(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"field separator in subject", record("aaaaaaa1111111", "Alice", "d", "p", "subject with \x1f sneaky", "a.txt")},
		{"record separator in subject", record("aaaaaaa1111111", "Alice", "d", "p", "subject with \x1e sneaky", "a.txt")},
		{"truncated record", recordSep + "aaaaaaa1111111" + fieldSep + "Alice" + fieldSep + "date"},
		{"non-hex object name", record("not-a-sha-at-all", "Alice", "d", "p", "s", "a.txt")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := parseLog([]byte(tt.in), "")
			for _, c := range res.commits {
				for _, f := range c.Files {
					if f != "a.txt" {
						t.Errorf("fabricated file %q from malformed record", f)
					}
				}
			}
			if res.malformed == 0 {
				t.Errorf("malformed = 0, want >0 (corruption was not reported)")
			}
		})
	}
}

func TestToRootRelative(t *testing.T) {
	tests := []struct {
		path, prefix, want string
		ok                 bool
	}{
		{"src/a.go", "", "src/a.go", true},
		{"src/a.go", "src/", "a.go", true},
		{"src/deep/a.go", "src/", "deep/a.go", true},
		{"docs/a.md", "src/", "", false},
		{"srcx/a.go", "src/", "", false},
		{"src/", "src/", "", false},
		{"", "", "", false},
		{"src/café.go", "src/", "café.go", true},
		{"a file with spaces.txt", "", "a file with spaces.txt", true},
	}
	for _, tt := range tests {
		got, ok := toRootRelative(tt.path, tt.prefix)
		if got != tt.want || ok != tt.ok {
			t.Errorf("toRootRelative(%q, %q) = (%q, %v), want (%q, %v)",
				tt.path, tt.prefix, got, ok, tt.want, tt.ok)
		}
	}
}

func TestIsObjectName(t *testing.T) {
	valid := []string{"abc1234", strings.Repeat("a", 40), strings.Repeat("F", 64)}
	invalid := []string{"", "abc123", "Test User", "2024-01-15 10:30:00 +0000", "src/a.go", strings.Repeat("a", 65)}
	for _, s := range valid {
		if !isObjectName(s) {
			t.Errorf("isObjectName(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if isObjectName(s) {
			t.Errorf("isObjectName(%q) = true, want false", s)
		}
	}
}

// --- real repository tests -----------------------------------------------

// Defect 1, against a real repo: an empty commit in the middle of history.
func TestParseLogRealRepoEmptyCommits(t *testing.T) {
	r := newTestRepo(t)
	r.write("a.txt", "one\n")
	r.commit("first")
	r.git("commit", "-q", "--allow-empty", "-m", "empty ci marker")
	r.git("commit", "-q", "--allow-empty", "--allow-empty-message", "-m", "")
	r.write("b.txt", "two\n")
	r.commit("second")

	res, err := ParseLogOpts(r.dir, Options{})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{"a.txt": true, "b.txt": true}
	for _, c := range res.Commits {
		for _, f := range c.Files {
			if !want[f] {
				t.Errorf("fabricated file %q in commit %q (%s)", f, c.Message, c.Hash)
			}
		}
	}
	// The commit after the empty ones must survive.
	first := findCommit(t, res.Commits, "first")
	if len(first.Files) != 1 || first.Files[0] != "a.txt" {
		t.Errorf("first.Files = %v, want [a.txt]", first.Files)
	}
	if !first.IsRoot {
		t.Error("initial commit not flagged as root")
	}
	if res.Coverage.CommitsScanned != 4 {
		t.Errorf("CommitsScanned = %d, want 4", res.Coverage.CommitsScanned)
	}
	if res.Coverage.CommitsWithFiles != 2 {
		t.Errorf("CommitsWithFiles = %d, want 2", res.Coverage.CommitsWithFiles)
	}
	if res.Coverage.MalformedRecords != 0 {
		t.Errorf("MalformedRecords = %d, want 0", res.Coverage.MalformedRecords)
	}

	cc := NewCoChange(res.Commits)
	for f := range cc.AllChurn() {
		if !want[f] {
			t.Errorf("churn keyed on non-file %q", f)
		}
	}
}

// Defect 2: non-ASCII paths must arrive raw so they join with walker paths.
func TestParseLogRealRepoUnicodeAndSpaces(t *testing.T) {
	r := newTestRepo(t)
	paths := []string{
		"src/café.go",
		"src/日本語.md",
		"src/a file with spaces.txt",
		"src/plain.go",
	}
	for _, p := range paths {
		r.write(p, "x\n")
	}
	r.commit("unicode")

	res, err := ParseLogOpts(r.dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Commits) != 1 {
		t.Fatalf("commits = %d, want 1", len(res.Commits))
	}

	got := append([]string(nil), res.Commits[0].Files...)
	sort.Strings(got)
	want := append([]string(nil), paths...)
	sort.Strings(want)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("files = %q, want %q", got, want)
	}
	for _, f := range got {
		if strings.Contains(f, `\`) || strings.HasPrefix(f, `"`) {
			t.Errorf("path %q is still C-quoted", f)
		}
		// The joinable property that actually matters: the path git reports
		// must exist on disk under the same name.
		if _, err := os.Stat(filepath.Join(r.dir, f)); err != nil {
			t.Errorf("path %q from git does not resolve on disk: %v", f, err)
		}
	}
}

// Defect 3: pointed at a subdirectory, paths must be root-relative.
func TestParseLogRealRepoSubdirRoot(t *testing.T) {
	r := newTestRepo(t)
	r.write("src/a.go", "a\n")
	r.write("src/nested/b.go", "b\n")
	r.write("docs/readme.md", "d\n")
	r.commit("init")
	r.write("docs/readme.md", "d2\n")
	r.commit("docs only")
	r.write("src/a.go", "a2\n")
	r.commit("src only")

	sub := filepath.Join(r.dir, "src")
	res, err := ParseLogOpts(sub, Options{})
	if err != nil {
		t.Fatal(err)
	}

	if res.Coverage.Subdir != "src/" {
		t.Errorf("Subdir = %q, want %q", res.Coverage.Subdir, "src/")
	}
	for _, c := range res.Commits {
		for _, f := range c.Files {
			if strings.HasPrefix(f, "src/") || strings.HasPrefix(f, "docs/") {
				t.Errorf("path %q is not relative to the scan root", f)
			}
			if _, err := os.Stat(filepath.Join(sub, f)); err != nil {
				t.Errorf("path %q does not resolve under root: %v", f, err)
			}
		}
	}
	if got := subjects(res.Commits); len(got) != 2 {
		t.Errorf("commits = %q, want the 2 that touched src/", got)
	}

	cc := NewCoChange(res.Commits)
	churn := cc.AllChurn()
	if churn["a.go"] != 2 {
		t.Errorf("churn[a.go] = %d, want 2 (%v)", churn["a.go"], churn)
	}
	if _, bad := churn["readme.md"]; bad {
		t.Error("file outside root leaked into churn")
	}
}

// A package with no history of its own must report "nothing mined" rather than
// looking like a repository where nothing ever changes.
func TestParseLogSubdirWithoutHistory(t *testing.T) {
	r := newTestRepo(t)
	r.write("docs/readme.md", "d\n")
	r.commit("docs only")

	fresh := filepath.Join(r.dir, "packages", "new")
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := ParseLogOpts(fresh, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Commits) != 0 {
		t.Errorf("commits = %d, want 0", len(res.Commits))
	}
	if res.Coverage.Subdir != "packages/new/" {
		t.Errorf("Subdir = %q", res.Coverage.Subdir)
	}
	if res.Coverage.ChurnTrustworthy() {
		t.Error("ChurnTrustworthy = true with no mined commits")
	}
	if want := "no commits touched packages/new/ within the mined window"; res.Coverage.Reason() != want {
		t.Errorf("Reason = %q, want %q", res.Coverage.Reason(), want)
	}
}

// Defect 5: a shallow clone must produce churn AND be flagged as untrustworthy.
func TestParseLogRealRepoShallowClone(t *testing.T) {
	r := newTestRepo(t)
	for i := 1; i <= 3; i++ {
		r.write(fmt.Sprintf("f%d.go", i), "x\n")
		r.commit(fmt.Sprintf("commit %d", i))
	}

	dest := filepath.Join(t.TempDir(), "shallow")
	clone := exec.Command("git", "-c", "protocol.file.allow=always",
		"clone", "-q", "--depth", "1", "file://"+r.dir, dest)
	clone.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Skipf("shallow clone unavailable: %v\n%s", err, out)
	}

	cc, err := Mine(dest, Options{}, CoChangeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cov := cc.Coverage()

	if !cov.Shallow {
		t.Error("Coverage.Shallow = false for a --depth 1 clone")
	}
	if cov.ChurnTrustworthy() {
		t.Error("ChurnTrustworthy = true for a --depth 1 clone")
	}
	if cov.Reason() == "" {
		t.Error("Reason is empty although churn is untrustworthy")
	}
	if cov.RootCommits != 1 || cov.CommitsScanned != 1 {
		t.Errorf("scanned = %d, root = %d, want 1/1", cov.CommitsScanned, cov.RootCommits)
	}
	// The signal itself is still produced (defect 4 no longer eats it), it is
	// just flat: every file changed exactly once.
	if len(cc.AllChurn()) != 3 {
		t.Errorf("churn entries = %d, want 3: %v", len(cc.AllChurn()), cc.AllChurn())
	}

	if IsShallow(r.dir) {
		t.Error("IsShallow = true for a normal repo")
	}
	if !IsShallow(dest) {
		t.Error("IsShallow = false for a shallow clone")
	}
}

func TestCoverageWindowAndTrust(t *testing.T) {
	r := newTestRepo(t)
	for i := 1; i <= 5; i++ {
		r.write("a.go", fmt.Sprintf("%d\n", i))
		r.commit(fmt.Sprintf("commit %d", i))
	}

	res, err := ParseLogOpts(r.dir, Options{MaxCommits: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Commits) != 2 {
		t.Fatalf("commits = %d, want 2", len(res.Commits))
	}
	if !res.Coverage.WindowFull {
		t.Error("WindowFull = false although the window was filled")
	}
	if res.Coverage.MaxCommits != 2 {
		t.Errorf("MaxCommits = %d, want 2", res.Coverage.MaxCommits)
	}
	if !res.Coverage.ChurnTrustworthy() {
		t.Errorf("ChurnTrustworthy = false for a normal repo: %s", res.Coverage.Reason())
	}
	if res.Coverage.Reason() != "" {
		t.Errorf("Reason = %q, want empty", res.Coverage.Reason())
	}

	full, err := ParseLogOpts(r.dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if full.Coverage.MaxCommits != DefaultMaxCommits {
		t.Errorf("MaxCommits = %d, want %d", full.Coverage.MaxCommits, DefaultMaxCommits)
	}
	if full.Coverage.WindowFull {
		t.Error("WindowFull = true although history is shorter than the window")
	}
	if len(full.Commits) != 5 {
		t.Errorf("commits = %d, want 5", len(full.Commits))
	}

	// Backwards-compatible wrapper still works.
	commits, err := ParseLog(r.dir, 0)
	if err != nil || len(commits) != 5 {
		t.Errorf("ParseLog = %d commits, %v; want 5, nil", len(commits), err)
	}
}

func TestParseLogNotARepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	if _, err := ParseLogOpts(dir, Options{}); err == nil {
		t.Error("expected an error outside a git repository")
	}
	var zero Coverage
	if zero.ChurnTrustworthy() {
		t.Error("zero Coverage reports trustworthy churn")
	}
	if zero.Reason() != "not a git repository" {
		t.Errorf("Reason = %q", zero.Reason())
	}
}

func TestRecentChangesRealRepo(t *testing.T) {
	r := newTestRepo(t)
	r.write("src/a.go", "a\n")
	r.write("src/café.go", "c\n")
	r.commit("recent work")
	r.git("commit", "-q", "--allow-empty", "-m", "empty marker")

	commits, err := RecentChanges(r.dir, "7d")
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 {
		t.Fatalf("commits = %q, want 1", subjects(commits))
	}
	got := append([]string(nil), commits[0].Files...)
	sort.Strings(got)
	want := []string{"src/a.go", "src/café.go"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("files = %q, want %q", got, want)
	}

	res, err := RecentChangesOpts(filepath.Join(r.dir, "src"), "7d")
	if err != nil {
		t.Fatal(err)
	}
	if res.Coverage.Subdir != "src/" {
		t.Errorf("Subdir = %q", res.Coverage.Subdir)
	}
	if got := res.Commits[0].Files; len(got) != 2 || strings.HasPrefix(got[0], "src/") {
		t.Errorf("files = %q, want them relative to src/", got)
	}
}

func TestConvertSince(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"7d", "7 days ago"},
		{"2w", "2 weeks ago"},
		{"1m", "1 months ago"},
		{"", "7 days ago"},
		{"2024-01-01", "2024-01-01"},
	}

	for _, tt := range tests {
		got := convertSince(tt.input)
		if got != tt.want {
			t.Errorf("convertSince(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
