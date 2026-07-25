package cache

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
)

// --- helpers ---

// newStore opens a real on-disk cache in a temp dir.
func newStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// openSecond opens an independent Store on the same cache dir, simulating a
// second recon process: separate *sql.DB, separate connection pool, real
// cross-connection lock contention.
func openSecond(t *testing.T, s *Store) *Store {
	t.Helper()
	s2, err := OpenAt(s.Root, s.cacheDir)
	if err != nil {
		t.Fatalf("OpenAt (second store): %v", err)
	}
	t.Cleanup(func() { s2.Close() })
	return s2
}

func count(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func sym(file, name string, line int) index.Symbol {
	return index.Symbol{File: file, Name: name, Kind: "function", Line: line, Signature: "func " + name + "()"}
}

// --- Open / pragmas / gitignore ---

func TestOpenSetsBusyTimeoutOnEveryConnection(t *testing.T) {
	s := newStore(t)

	// Force several distinct pooled connections to exist simultaneously, then
	// check each one carries the pragma. A pragma issued once after sql.Open
	// only configures whichever connection served it — this is the regression.
	var conns []*sql.Conn
	for i := 0; i < 4; i++ {
		c, err := s.db.Conn(t.Context())
		if err != nil {
			t.Fatalf("Conn %d: %v", i, err)
		}
		conns = append(conns, c)
	}
	for i, c := range conns {
		var got int
		if err := c.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&got); err != nil {
			t.Fatalf("read busy_timeout on conn %d: %v", i, err)
		}
		if got != busyTimeoutMS {
			t.Errorf("conn %d: busy_timeout = %d, want %d", i, got, busyTimeoutMS)
		}
		c.Close()
	}
}

func TestOpenSetsJournalModeWAL(t *testing.T) {
	s := newStore(t)
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

func TestOpenWritesSelfIgnoringGitignore(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	data, err := os.ReadFile(filepath.Join(root, cacheDir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .recon/.gitignore: %v", err)
	}
	if string(data) != "*\n" {
		t.Errorf("gitignore = %q, want %q", data, "*\n")
	}
}

func TestOpenDoesNotClobberExistingGitignore(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, cacheDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	custom := "# hand written\n*\n!keep.txt\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(data) != custom {
		t.Errorf("gitignore was overwritten: %q", data)
	}
}

func TestBuildDSN(t *testing.T) {
	dsn := buildDSN("/tmp/x/recon.db")
	for _, want := range []string{
		"/tmp/x/recon.db?",
		"_pragma=busy_timeout",
		"_txlock=immediate",
	} {
		if !contains(dsn, want) {
			t.Errorf("DSN %q missing %q", dsn, want)
		}
	}
	// journal_mode must NOT be a DSN pragma: the initial conversion returns
	// SQLITE_BUSY without honouring busy_timeout, which made concurrent opens
	// of a fresh cache fail outright. enableWAL handles it once, with retries.
	if contains(dsn, "journal_mode") {
		t.Errorf("journal_mode must not be a per-connection DSN pragma: %q", dsn)
	}
	if contains(dsn, "file:") {
		t.Errorf("plain path should not be turned into a URI: %q", dsn)
	}

	// A path containing '?' would otherwise be truncated by the driver.
	q := buildDSN("/tmp/we?rd/recon.db")
	if !contains(q, "file:/tmp/we%3frd/recon.db?") {
		t.Errorf("path with '?' not escaped: %q", q)
	}
}

func TestOpenWithAwkwardPath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "cache dir #1")
	s, err := OpenAt(root, dir)
	if err != nil {
		t.Fatalf("OpenAt(%q): %v", dir, err)
	}
	defer s.Close()

	if err := s.SetMeta("k", "v"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, dbFile)); err != nil {
		t.Errorf("db not created at expected path: %v", err)
	}
}

// --- schema migration ---

func TestEnsureSchemaIsIdempotent(t *testing.T) {
	s := newStore(t)
	if err := s.SetMeta("head_sha", "abc"); err != nil {
		t.Fatal(err)
	}
	if err := s.ensureSchema(); err != nil {
		t.Fatalf("second ensureSchema: %v", err)
	}
	if v, ok := s.GetMeta("head_sha"); !ok || v != "abc" {
		t.Errorf("meta lost by a no-op migration: %q, %v", v, ok)
	}
}

func TestSchemaMigrationRecreatesOnVersionMismatch(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSnapshot(&Snapshot{Files: []scan.FileEntry{{RelPath: "a.go", Lang: "go"}}}); err != nil {
		t.Fatal(err)
	}
	if !s.HasData() {
		t.Fatal("expected data before migration")
	}
	// Pretend the DB was written by an older recon.
	if err := s.SetMeta("schema_version", "1"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(root)
	if err != nil {
		t.Fatalf("reopen after version downgrade: %v", err)
	}
	defer s2.Close()

	if v, _ := s2.GetMeta("schema_version"); v != strconv.Itoa(schemaVer) {
		t.Errorf("schema_version = %q, want %d", v, schemaVer)
	}
	if s2.HasData() {
		t.Error("stale-schema data survived migration; it must be dropped and rebuilt")
	}
	// All tables exist and are usable again.
	if err := s2.SaveSnapshot(&Snapshot{Symbols: []index.Symbol{sym("a.go", "F", 1)}}); err != nil {
		t.Errorf("post-migration write failed: %v", err)
	}
}

// A logic change leaves the table shape alone, so schema_version cannot catch
// it. Without a separate analysis_version the stored results of retired code
// are served indefinitely: refresh only rescans changed files, and after a
// resolver fix every file's stored result is stale while every file's mtime
// says otherwise.
func TestAnalysisVersionMismatchDropsDerivedData(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSnapshot(&Snapshot{Files: []scan.FileEntry{{RelPath: "a.cs", Lang: "csharp"}}}); err != nil {
		t.Fatal(err)
	}
	if !s.HasData() {
		t.Fatal("expected data before migration")
	}
	// Schema shape is current; only the analysis logic has moved on.
	if v, _ := s.GetMeta("schema_version"); v != strconv.Itoa(schemaVer) {
		t.Fatalf("precondition: schema_version = %q, want %d", v, schemaVer)
	}
	if err := s.SetMeta("analysis_version", strconv.Itoa(analysisVer-1)); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(root)
	if err != nil {
		t.Fatalf("reopen after analysis-version bump: %v", err)
	}
	defer s2.Close()

	if s2.HasData() {
		t.Error("data computed by superseded analysis logic survived; it must be dropped and recomputed")
	}
	if v, _ := s2.GetMeta("analysis_version"); v != strconv.Itoa(analysisVer) {
		t.Errorf("analysis_version = %q, want %d", v, analysisVer)
	}
	if err := s2.SaveSnapshot(&Snapshot{Symbols: []index.Symbol{sym("a.cs", "F", 1)}}); err != nil {
		t.Errorf("post-migration write failed: %v", err)
	}
}

// A cache written before analysis_version existed has no such row. Absent must
// mean rebuild, not "current" — otherwise every cache predating the mechanism
// permanently escapes it.
func TestMissingAnalysisVersionIsTreatedAsStale(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSnapshot(&Snapshot{Files: []scan.FileEntry{{RelPath: "a.go", Lang: "go"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DELETE FROM meta WHERE key='analysis_version'"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	if s2.HasData() {
		t.Error("cache with no analysis_version was trusted; absent must mean rebuild")
	}
}

// Reopening an unchanged cache must not rebuild it — the whole point of the
// cache is that the common path is cheap.
func TestMatchingVersionsKeepData(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSnapshot(&Snapshot{Files: []scan.FileEntry{{RelPath: "a.go", Lang: "go"}}}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	if !s2.HasData() {
		t.Error("an up-to-date cache was dropped; reopening must be a no-op")
	}
}

func TestSchemaMigrationFromGarbageVersion(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta("schema_version", "not-a-number"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(root)
	if err != nil {
		t.Fatalf("reopen with unparseable version: %v", err)
	}
	defer s2.Close()
	if v, _ := s2.GetMeta("schema_version"); v != strconv.Itoa(schemaVer) {
		t.Errorf("schema_version = %q, want %d", v, schemaVer)
	}
}

func TestConcurrentOpenMigratesOnce(t *testing.T) {
	root := t.TempDir()
	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := Open(root)
			if err != nil {
				errs[i] = err
				return
			}
			defer s.Close()
			errs[i] = s.SetMeta(fmt.Sprintf("k%d", i), "v")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("opener %d: %v", i, err)
		}
	}

	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := count(t, s, "SELECT COUNT(*) FROM meta WHERE key='schema_version'"); got != 1 {
		t.Errorf("schema_version rows = %d, want 1", got)
	}
}

// --- snapshot round-trip ---

func TestSaveLoadSnapshotRoundTrip(t *testing.T) {
	s := newStore(t)
	snap := &Snapshot{
		Files: []scan.FileEntry{
			{RelPath: "a/b.go", Lang: "go", Class: scan.ClassSource, Size: 12, ModTime: 99},
			{RelPath: "top.go", Lang: "go", Class: scan.ClassSource, Size: 3, ModTime: 7},
		},
		Imports:       map[string][]string{"a/b.go": {"top.go"}},
		TestToSource:  map[string]string{"a/b_test.go": "a/b.go"},
		TestKinds:     map[string]string{"a/b_test.go": "unit"},
		CoChangePairs: map[string]map[string]int{"a/b.go": {"top.go": 4}},
		Churn:         map[string]int{"a/b.go": 9},
		Symbols: []index.Symbol{
			{File: "a/b.go", Name: "F", Kind: "function", Line: 3, Signature: "func F()", Extractor: index.ExtractorTreeSitter},
			{File: "a/b.go", Name: "G", Kind: "function", Line: 9, Signature: "func G()", Extractor: index.ExtractorRegex},
		},
		FileParses: []index.FileParse{
			{RelPath: "a/b.go", Lang: "go", Extractor: index.ExtractorTreeSitter, Status: index.ParseOK, SymbolCount: 2},
			{RelPath: "top.go", Lang: "go", Extractor: index.ExtractorNone, Status: index.ParseUnsupported, SymbolCount: 0, Detail: "no grammar"},
		},
		ImportStats: map[string]index.ImportStats{
			"a/b.go": {Lang: "go", Extracted: 3, Resolved: 1, External: 2},
			// The interesting row: every specifier dropped, no edges, so it
			// appears in no other table.
			"top.go": {Lang: "go", Extracted: 2, Unresolved: 2, UnresolvedSpecs: []string{"example.com/gone", "example.com/also-gone"}},
		},
		References:    []index.Reference{{Name: "F", File: "top.go", Line: 2}},
		ContextDocs:   []index.ContextDoc{{File: "a/b.go", Symbol: "F", Line: 1, Source: "comment", Origin: "a/b.go", Body: "does F"}},
		FileExtras:    []index.FileExtra{{RelPath: "a/b.go", Preview: "package a", ContentHash: "deadbeef"}},
		Metrics:       []index.FileMetrics{{RelPath: "a/b.go", FanIn: 1, FanOut: 2, Churn: 9, HotspotScore: 1.5}},
		NearbyConfigs: []index.NearbyConfig{{Dir: "a", ConfigType: "go.mod", ConfigPath: "go.mod"}},
		OwnerRules:    []index.OwnerRule{{Priority: 2, Pattern: "*.go", Owners: []string{"@me", "@you"}}},
	}
	if err := s.SaveSnapshot(snap); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	got, err := s.LoadSnapshot()
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}

	if len(got.Files) != 2 {
		t.Errorf("Files = %d, want 2", len(got.Files))
	}
	if len(got.Imports["a/b.go"]) != 1 || got.Imports["a/b.go"][0] != "top.go" {
		t.Errorf("Imports = %v", got.Imports)
	}
	if got.TestToSource["a/b_test.go"] != "a/b.go" || got.TestKinds["a/b_test.go"] != "unit" {
		t.Errorf("tests = %v / %v", got.TestToSource, got.TestKinds)
	}
	if len(got.SourceToTest["a/b.go"]) != 1 {
		t.Errorf("SourceToTest = %v", got.SourceToTest)
	}
	if got.CoChangePairs["a/b.go"]["top.go"] != 4 || got.Churn["a/b.go"] != 9 {
		t.Errorf("cochange/churn = %v / %v", got.CoChangePairs, got.Churn)
	}
	if len(got.Symbols) != 2 || len(got.References) != 1 || len(got.ContextDocs) != 1 ||
		len(got.FileExtras) != 1 || len(got.Metrics) != 1 || len(got.NearbyConfigs) != 1 {
		t.Errorf("row counts wrong: %+v", got)
	}
	if len(got.OwnerRules) != 1 || len(got.OwnerRules[0].Owners) != 2 {
		t.Errorf("OwnerRules = %+v", got.OwnerRules)
	}

	// Per-symbol provenance survives the cache: a regex-derived symbol must not
	// come back looking tree-sitter-clean on the second run.
	byName := map[string]index.Symbol{}
	for _, sm := range got.Symbols {
		byName[sm.Name] = sm
	}
	if byName["F"].Extractor != index.ExtractorTreeSitter {
		t.Errorf("symbol F extractor = %q, want %q", byName["F"].Extractor, index.ExtractorTreeSitter)
	}
	if byName["G"].Extractor != index.ExtractorRegex {
		t.Errorf("symbol G extractor = %q, want %q", byName["G"].Extractor, index.ExtractorRegex)
	}

	// Parse provenance must survive the cache — otherwise trust markers exist
	// only on the very first (uncached) run, and the absence of a warning on
	// every later run reads as an all-clear.
	if len(got.FileParses) != 2 {
		t.Fatalf("FileParses = %d, want 2", len(got.FileParses))
	}
	byPath := map[string]index.FileParse{}
	for _, p := range got.FileParses {
		byPath[p.RelPath] = p
	}
	if p := byPath["a/b.go"]; p.Extractor != index.ExtractorTreeSitter || p.Status != index.ParseOK || p.SymbolCount != 2 {
		t.Errorf("a/b.go parse = %+v", p)
	}
	// The zero-symbol row is the one that matters: it is the only record that
	// "top.go produced nothing" was a missing extractor, not an empty file.
	if p := byPath["top.go"]; p.Status != index.ParseUnsupported || p.SymbolCount != 0 || p.Detail != "no grammar" {
		t.Errorf("top.go parse = %+v", p)
	}

	// Import telemetry must survive the cache for the same reason: it is computed
	// at scan time, and every run after the first is served from here.
	if len(got.ImportStats) != 2 {
		t.Fatalf("ImportStats = %d, want 2: %+v", len(got.ImportStats), got.ImportStats)
	}
	if st := got.ImportStats["a/b.go"]; st.Lang != "go" || st.Extracted != 3 || st.Resolved != 1 || st.External != 2 || st.Unresolved != 0 {
		t.Errorf("a/b.go import stats = %+v", st)
	}
	// top.go has no import edges at all, so this row is the only thing that can
	// distinguish "imports nothing" from "recon dropped both of its imports".
	st := got.ImportStats["top.go"]
	if st.Extracted != 2 || st.Unresolved != 2 || st.Resolved != 0 {
		t.Errorf("top.go import stats = %+v", st)
	}
	if len(st.UnresolvedSpecs) != 2 || st.UnresolvedSpecs[0] != "example.com/gone" || st.UnresolvedSpecs[1] != "example.com/also-gone" {
		t.Errorf("unresolved specs = %v, want both in order", st.UnresolvedSpecs)
	}
	if _, ok := got.Imports["top.go"]; ok {
		t.Error("test premise broken: top.go should have no import edges")
	}
}

func TestFileParseUpsertAndRemoval(t *testing.T) {
	s := newStore(t)

	if err := s.UpdateFileParses([]index.FileParse{
		{RelPath: "a.ts", Lang: "typescript", Extractor: index.ExtractorTreeSitter, Status: index.ParsePartial, SymbolCount: 2, Detail: "legacy cast"},
		{RelPath: "b.cs", Lang: "csharp", Extractor: index.ExtractorTreeSitter, Status: index.ParseFailed, SymbolCount: 0, Detail: "utf-16"},
		{RelPath: "c.clj", Lang: "clojure", Extractor: index.ExtractorNone, Status: index.ParseUnsupported},
	}, nil); err != nil {
		t.Fatal(err)
	}

	parses, err := s.GetFileParses()
	if err != nil {
		t.Fatalf("GetFileParses: %v", err)
	}
	if len(parses) != 3 {
		t.Fatalf("parses = %d, want 3", len(parses))
	}
	if parses["a.ts"].Status != index.ParsePartial || parses["a.ts"].SymbolCount != 2 {
		t.Errorf("a.ts = %+v", parses["a.ts"])
	}
	if parses["b.cs"].Detail != "utf-16" {
		t.Errorf("b.cs = %+v", parses["b.cs"])
	}

	// Re-parse: rel_path is the primary key, so this replaces in place. A
	// repeated write can never accumulate duplicate rows here.
	for i := 0; i < 3; i++ {
		if err := s.UpdateFileParses([]index.FileParse{
			{RelPath: "a.ts", Lang: "typescript", Extractor: index.ExtractorTreeSitter, Status: index.ParseOK, SymbolCount: 8},
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := count(t, s, "SELECT COUNT(*) FROM file_parse"); got != 3 {
		t.Errorf("file_parse rows = %d, want 3", got)
	}
	parses, _ = s.GetFileParses()
	if parses["a.ts"].Status != index.ParseOK || parses["a.ts"].SymbolCount != 8 {
		t.Errorf("re-parse did not replace: %+v", parses["a.ts"])
	}

	// Deleted files must not keep a stale parse row.
	if err := s.UpdateFileParses(nil, []string{"b.cs"}); err != nil {
		t.Fatal(err)
	}
	parses, _ = s.GetFileParses()
	if _, ok := parses["b.cs"]; ok {
		t.Error("parse row survived file removal")
	}

	// No-op call is cheap and harmless.
	if err := s.UpdateFileParses(nil, nil); err != nil {
		t.Errorf("empty UpdateFileParses: %v", err)
	}
}

func TestGetFileParsesEmpty(t *testing.T) {
	s := newStore(t)
	parses, err := s.GetFileParses()
	if err != nil {
		t.Fatalf("GetFileParses on empty cache: %v", err)
	}
	if len(parses) != 0 {
		t.Errorf("parses = %d, want 0", len(parses))
	}
}

func TestImportStatsUpsertAndRemoval(t *testing.T) {
	s := newStore(t)

	if err := s.UpdateImportStats(map[string]index.ImportStats{
		"a.ts":  {Lang: "typescript", Extracted: 4, Resolved: 3, External: 1},
		"b.ex":  {Lang: "elixir", Extracted: 2, Unresolved: 2, UnresolvedSpecs: []string{"MyApp.Nowhere", "MyApp.AlsoGone"}},
		"c.jl":  {Lang: "julia", Extracted: 1, External: 1},
		"d.sh":  {Lang: "shell"}, // extracted nothing at all
		"e.kt":  {Lang: "kotlin", Extracted: 3, Resolved: 1, External: 1, Unresolved: 1, UnresolvedSpecs: []string{"com.ex.Missing"}},
		"f.rho": {Lang: "unknown"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	stats, err := s.GetImportStats()
	if err != nil {
		t.Fatalf("GetImportStats: %v", err)
	}
	if len(stats) != 6 {
		t.Fatalf("stats = %d, want 6", len(stats))
	}
	if got := stats["a.ts"]; got.Resolved != 3 || got.External != 1 || got.Unresolved != 0 {
		t.Errorf("a.ts = %+v", got)
	}
	if got := stats["b.ex"]; len(got.UnresolvedSpecs) != 2 || got.UnresolvedSpecs[0] != "MyApp.Nowhere" {
		t.Errorf("b.ex specs = %v", got.UnresolvedSpecs)
	}
	// A file whose extractor produced nothing still gets a row: an absent row and
	// a zeroed row mean different things to the reader.
	if got, ok := stats["d.sh"]; !ok || got.Lang != "shell" || got.Extracted != 0 {
		t.Errorf("d.sh = %+v, present=%v", got, ok)
	}
	// No specs stored means no specs read back — not an empty-string artefact.
	if got := stats["c.jl"]; got.UnresolvedSpecs != nil {
		t.Errorf("c.jl specs = %v, want nil", got.UnresolvedSpecs)
	}

	// Re-scan: rel_path is the primary key, so this replaces in place. Repeated
	// writes cannot accumulate duplicate rows the way keyless tables can.
	for i := 0; i < 3; i++ {
		if err := s.UpdateImportStats(map[string]index.ImportStats{
			"b.ex": {Lang: "elixir", Extracted: 2, Resolved: 2},
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := count(t, s, "SELECT COUNT(*) FROM import_stats"); got != 6 {
		t.Errorf("import_stats rows = %d, want 6", got)
	}
	stats, _ = s.GetImportStats()
	if got := stats["b.ex"]; got.Resolved != 2 || got.Unresolved != 0 || len(got.UnresolvedSpecs) != 0 {
		t.Errorf("re-scan did not replace: %+v", got)
	}

	// Deleted files must not keep a stale row claiming their imports resolved.
	if err := s.UpdateImportStats(nil, []string{"c.jl", "e.kt"}); err != nil {
		t.Fatal(err)
	}
	stats, _ = s.GetImportStats()
	if _, ok := stats["c.jl"]; ok {
		t.Error("import_stats row survived file removal")
	}
	if _, ok := stats["e.kt"]; ok {
		t.Error("import_stats row survived file removal")
	}
	if len(stats) != 4 {
		t.Errorf("stats after removal = %d, want 4", len(stats))
	}

	// A file both rewritten and listed as removed in the same call must end up
	// with the fresh row: the delete runs first, exactly as in UpdateFileParses.
	if err := s.UpdateImportStats(map[string]index.ImportStats{
		"a.ts": {Lang: "typescript", Extracted: 9, Resolved: 9},
	}, []string{"a.ts"}); err != nil {
		t.Fatal(err)
	}
	stats, _ = s.GetImportStats()
	if got := stats["a.ts"]; got.Resolved != 9 {
		t.Errorf("a.ts after delete+insert = %+v, want the fresh row", got)
	}

	// No-op call is cheap and harmless.
	if err := s.UpdateImportStats(nil, nil); err != nil {
		t.Errorf("empty UpdateImportStats: %v", err)
	}
}

func TestGetImportStatsEmpty(t *testing.T) {
	s := newStore(t)
	stats, err := s.GetImportStats()
	if err != nil {
		t.Fatalf("GetImportStats on empty cache: %v", err)
	}
	if len(stats) != 0 {
		t.Errorf("stats = %d, want 0", len(stats))
	}
}

// TestUnresolvedSpecsAreBoundedAndJSON pins the storage decision: the sample
// lives in one JSON TEXT column, and the write is bounded regardless of what the
// producer hands over, so no single row can grow without limit.
func TestUnresolvedSpecsAreBoundedAndJSON(t *testing.T) {
	s := newStore(t)

	many := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		many = append(many, fmt.Sprintf("pkg/spec%02d", i))
	}
	if err := s.UpdateImportStats(map[string]index.ImportStats{
		"big.go": {Lang: "go", Extracted: 50, Unresolved: 50, UnresolvedSpecs: many},
	}, nil); err != nil {
		t.Fatal(err)
	}

	stats, _ := s.GetImportStats()
	got := stats["big.go"]
	if len(got.UnresolvedSpecs) != maxStoredUnresolvedSpecs {
		t.Errorf("stored specs = %d, want %d", len(got.UnresolvedSpecs), maxStoredUnresolvedSpecs)
	}
	// Truncation keeps the head, and the count is untouched: the sample shrinks,
	// the number that drives the warning does not.
	if got.UnresolvedSpecs[0] != "pkg/spec00" {
		t.Errorf("first spec = %q", got.UnresolvedSpecs[0])
	}
	if got.Unresolved != 50 {
		t.Errorf("unresolved count = %d, want 50 — the count must not be truncated with the sample", got.Unresolved)
	}

	// It really is JSON on disk, not a delimiter-joined string that a specifier
	// containing the delimiter could corrupt.
	var raw string
	if err := s.db.QueryRow("SELECT unresolved_specs FROM import_stats WHERE rel_path='big.go'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var decoded []string
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unresolved_specs is not valid JSON (%q): %v", raw, err)
	}

	// Specifiers with quotes, commas and spaces survive verbatim.
	odd := []string{`weird,"spec" one`, "with space", `back\slash`}
	if err := s.UpdateImportStats(map[string]index.ImportStats{
		"odd.go": {Lang: "go", Extracted: 3, Unresolved: 3, UnresolvedSpecs: odd},
	}, nil); err != nil {
		t.Fatal(err)
	}
	stats, _ = s.GetImportStats()
	for i, want := range odd {
		if stats["odd.go"].UnresolvedSpecs[i] != want {
			t.Errorf("spec %d = %q, want %q", i, stats["odd.go"].UnresolvedSpecs[i], want)
		}
	}

	// A corrupt sample loses the breadcrumbs but must not fail the load: the
	// counts in the same row are the signal, and refusing to serve the cache
	// would be a far worse outcome than a missing example.
	if _, err := s.db.Exec("UPDATE import_stats SET unresolved_specs='{not json' WHERE rel_path='odd.go'"); err != nil {
		t.Fatal(err)
	}
	stats, err := s.GetImportStats()
	if err != nil {
		t.Fatalf("GetImportStats with a corrupt sample: %v", err)
	}
	if got := stats["odd.go"]; got.Unresolved != 3 || got.UnresolvedSpecs != nil {
		t.Errorf("corrupt sample = %+v, want counts kept and specs dropped", got)
	}
	snap, err := s.LoadSnapshot()
	if err != nil {
		t.Fatalf("LoadSnapshot with a corrupt sample: %v", err)
	}
	if snap.ImportStats["odd.go"].Unresolved != 3 {
		t.Errorf("snapshot lost the counts: %+v", snap.ImportStats["odd.go"])
	}
}

// TestImportStatsSurviveConcurrentWriters is the primary-key guarantee: the
// keyless symbols table is what let concurrent runs duplicate rows, so this
// table has rel_path as its PRIMARY KEY and cannot.
func TestImportStatsSurviveConcurrentWriters(t *testing.T) {
	s := newStore(t)

	stats := make(map[string]index.ImportStats, 20)
	for i := 0; i < 20; i++ {
		stats[fmt.Sprintf("f%d.go", i)] = index.ImportStats{
			Lang: "go", Extracted: 2, Resolved: 1, Unresolved: 1,
			UnresolvedSpecs: []string{"example.com/missing"},
		}
	}

	const writers = 8
	stores := make([]*Store, writers)
	stores[0] = s
	for i := 1; i < writers; i++ {
		stores[i] = openSecond(t, s)
	}

	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = stores[i].UpdateImportStats(stats, nil)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d: %v", i, err)
		}
	}
	if got := count(t, s, "SELECT COUNT(*) FROM import_stats"); got != 20 {
		t.Errorf("import_stats rows = %d, want 20 — concurrent writers duplicated rows", got)
	}
}

// TestImportStatsDroppedOnSchemaBump guards the recreate-on-mismatch upgrade
// path for the new table: a cache written by an older recon has no import_stats
// table at all, so the schema must be rebuilt rather than queried into an error.
func TestImportStatsDroppedOnSchemaBump(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateImportStats(map[string]index.ImportStats{
		"a.go": {Lang: "go", Extracted: 1, Unresolved: 1, UnresolvedSpecs: []string{"gone"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	// Simulate the previous schema version, the one with no import_stats table.
	if err := s.SetMeta("schema_version", strconv.Itoa(schemaVer-1)); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(root)
	if err != nil {
		t.Fatalf("reopen across the schema bump: %v", err)
	}
	defer s2.Close()
	if v, _ := s2.GetMeta("schema_version"); v != strconv.Itoa(schemaVer) {
		t.Errorf("schema_version = %q, want %d", v, schemaVer)
	}
	// Table exists and is empty; a stale row would be telemetry for a scan that
	// this build's resolvers never performed.
	stats, err := s2.GetImportStats()
	if err != nil {
		t.Fatalf("GetImportStats after bump: %v", err)
	}
	if len(stats) != 0 {
		t.Errorf("stats survived the schema bump: %+v", stats)
	}
	if err := s2.UpdateImportStats(map[string]index.ImportStats{
		"a.go": {Lang: "go", Extracted: 1, Resolved: 1},
	}, nil); err != nil {
		t.Errorf("post-bump write failed: %v", err)
	}
}

func TestSaveSnapshotReplacesPreviousData(t *testing.T) {
	s := newStore(t)
	first := &Snapshot{Symbols: []index.Symbol{sym("a.go", "Old", 1)}}
	if err := s.SaveSnapshot(first); err != nil {
		t.Fatal(err)
	}
	second := &Snapshot{Symbols: []index.Symbol{sym("a.go", "New", 1)}}
	if err := s.SaveSnapshot(second); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM symbols"); got != 1 {
		t.Errorf("symbols = %d, want 1 (rebuild must not append)", got)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM symbols WHERE name='Old'"); got != 0 {
		t.Error("stale symbol survived a full rebuild")
	}
}

// --- delete-then-insert cycles ---

func TestUpdateSymbolsReplacesPerFile(t *testing.T) {
	s := newStore(t)
	if err := s.UpdateSymbols([]index.Symbol{
		sym("a.go", "A1", 1), sym("a.go", "A2", 2), sym("b.go", "B1", 1),
	}, nil); err != nil {
		t.Fatal(err)
	}

	// Re-index a.go with a different symbol set; b.go must be untouched.
	if err := s.UpdateSymbols([]index.Symbol{sym("a.go", "A3", 5)}, nil); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM symbols WHERE file_path='a.go'"); got != 1 {
		t.Errorf("a.go symbols = %d, want 1", got)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM symbols WHERE file_path='b.go'"); got != 1 {
		t.Errorf("b.go symbols = %d, want 1 (untouched file was disturbed)", got)
	}

	// Deleted files lose their symbols.
	if err := s.UpdateSymbols(nil, []string{"b.go"}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM symbols WHERE file_path='b.go'"); got != 0 {
		t.Errorf("b.go symbols after removal = %d, want 0", got)
	}
}

func TestUpdateSymbolsIsIdempotent(t *testing.T) {
	s := newStore(t)
	syms := []index.Symbol{sym("a.go", "F", 1), sym("a.go", "G", 2)}
	for i := 0; i < 5; i++ {
		if err := s.UpdateSymbols(syms, nil); err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
	}
	if got := count(t, s, "SELECT COUNT(*) FROM symbols"); got != 2 {
		t.Errorf("symbols after 5 identical updates = %d, want 2", got)
	}
}

func TestSameNameTwiceInFileIsKept(t *testing.T) {
	// The uniqueness constraint must not collapse legitimate repeats: the same
	// name can be declared more than once in a file at different lines.
	s := newStore(t)
	if err := s.UpdateSymbols([]index.Symbol{
		sym("a.go", "New", 10),
		sym("a.go", "New", 42),
		{File: "a.go", Name: "New", Kind: "method", Line: 10, Signature: "method"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM symbols WHERE name='New'"); got != 3 {
		t.Errorf("distinct declarations of 'New' = %d, want 3", got)
	}
}

func TestSymbolUniqueKeyRejectsDuplicateRows(t *testing.T) {
	s := newStore(t)
	// Two identical keys with different signatures: last write wins, one row.
	if err := s.UpdateSymbols([]index.Symbol{
		{File: "a.go", Name: "F", Kind: "function", Line: 1, Signature: "old"},
		{File: "a.go", Name: "F", Kind: "function", Line: 1, Signature: "new"},
	}, nil); err != nil {
		t.Fatalf("duplicate key must upsert, not fail: %v", err)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM symbols"); got != 1 {
		t.Errorf("symbols = %d, want 1", got)
	}
	var sig string
	if err := s.db.QueryRow("SELECT signature FROM symbols").Scan(&sig); err != nil {
		t.Fatal(err)
	}
	if sig != "new" {
		t.Errorf("signature = %q, want the later write %q", sig, "new")
	}

	// And a raw append is now impossible even if some future code path tries.
	_, err := s.db.Exec("INSERT INTO symbols (file_path, name, kind, line, signature) VALUES ('a.go','F','function',1,'sneaky')")
	if err == nil {
		t.Error("plain INSERT of a duplicate symbol key succeeded; constraint is not enforced")
	}
}

func TestUpdateReferencesKeepsRepeatsOnSameLine(t *testing.T) {
	// f(g(x)) + f(y): two real call sites sharing (name, file, line). They must
	// both survive — this is why references_ has no uniqueness constraint.
	s := newStore(t)
	refs := []index.Reference{
		{Name: "f", File: "a.go", Line: 7},
		{Name: "f", File: "a.go", Line: 7},
	}
	if err := s.UpdateReferences(refs, nil); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM references_"); got != 2 {
		t.Errorf("references = %d, want 2 (same-line repeats are distinct call sites)", got)
	}

	// But re-indexing the file replaces rather than appends.
	if err := s.UpdateReferences(refs, nil); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM references_"); got != 2 {
		t.Errorf("references after re-index = %d, want 2", got)
	}

	if err := s.UpdateReferences(nil, []string{"a.go"}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM references_"); got != 0 {
		t.Errorf("references after removal = %d, want 0", got)
	}
}

func TestUpdateImportsReplacesPerSource(t *testing.T) {
	s := newStore(t)
	if err := s.UpdateImports(map[string][]string{"a.go": {"x.go", "y.go"}, "b.go": {"x.go"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateImports(map[string][]string{"a.go": {"z.go"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM imports WHERE source_path='a.go'"); got != 1 {
		t.Errorf("a.go imports = %d, want 1", got)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM imports WHERE source_path='b.go'"); got != 1 {
		t.Errorf("b.go imports = %d, want 1", got)
	}
	if err := s.UpdateImports(nil, []string{"b.go"}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM imports"); got != 1 {
		t.Errorf("imports after removal = %d, want 1", got)
	}
}

func TestUpdateContextDocsReplacesByOrigin(t *testing.T) {
	s := newStore(t)
	docs := []index.ContextDoc{
		{File: "a.go", Symbol: "F", Source: "comment", Origin: "a.go", Body: "v1"},
		{File: "a.go", Symbol: "F", Source: "sidecar", Origin: ".context/a.md", Body: "side"},
	}
	if err := s.UpdateContextDocs(docs, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateContextDocs([]index.ContextDoc{
		{File: "a.go", Symbol: "F", Source: "comment", Origin: "a.go", Body: "v2"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM context_docs"); got != 2 {
		t.Errorf("context_docs = %d, want 2 (one per origin)", got)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM context_docs WHERE body='v1'"); got != 0 {
		t.Error("superseded doc survived re-index")
	}
	if err := s.UpdateContextDocs(nil, []string{".context/a.md"}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM context_docs"); got != 1 {
		t.Errorf("context_docs after origin removal = %d, want 1", got)
	}
}

func TestUpdateFilesAndSignatures(t *testing.T) {
	s := newStore(t)
	if err := s.UpdateFiles([]scan.FileEntry{
		{RelPath: "a.go", Lang: "go", Size: 10, ModTime: 100},
		{RelPath: "dir/b.go", Lang: "go", Size: 20, ModTime: 200},
	}, nil); err != nil {
		t.Fatal(err)
	}

	sigs, err := s.GetFileSignatures()
	if err != nil {
		t.Fatalf("GetFileSignatures: %v", err)
	}
	if len(sigs) != 2 {
		t.Fatalf("signatures = %d, want 2", len(sigs))
	}
	if sigs["a.go"] != (FileSig{ModTime: 100, Size: 10}) {
		t.Errorf("a.go sig = %+v", sigs["a.go"])
	}
	if sigs["dir/b.go"] != (FileSig{ModTime: 200, Size: 20}) {
		t.Errorf("dir/b.go sig = %+v", sigs["dir/b.go"])
	}

	// Same mtime, different size — the case mtime-only staleness misses.
	if err := s.UpdateFiles([]scan.FileEntry{{RelPath: "a.go", Lang: "go", Size: 999, ModTime: 100}}, nil); err != nil {
		t.Fatal(err)
	}
	sigs, _ = s.GetFileSignatures()
	if sigs["a.go"].Size != 999 {
		t.Errorf("size not updated: %+v", sigs["a.go"])
	}
	if got := count(t, s, "SELECT COUNT(*) FROM files"); got != 2 {
		t.Errorf("files = %d, want 2 (upsert must not duplicate)", got)
	}

	mtimes, err := s.GetFileMtimes()
	if err != nil {
		t.Fatal(err)
	}
	if mtimes["a.go"] != 100 {
		t.Errorf("GetFileMtimes = %v", mtimes)
	}

	if err := s.UpdateFiles(nil, []string{"a.go"}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM files"); got != 1 {
		t.Errorf("files after removal = %d, want 1", got)
	}
	// dir column is populated for nested paths (used by directory queries).
	var dir string
	if err := s.db.QueryRow("SELECT dir FROM files WHERE rel_path='dir/b.go'").Scan(&dir); err != nil {
		t.Fatal(err)
	}
	if dir != "dir" {
		t.Errorf("dir = %q, want %q", dir, "dir")
	}
}

func TestSaveTestsAndCoChangeReplaceWholesale(t *testing.T) {
	s := newStore(t)
	if err := s.SaveTests(nil, map[string]string{"a_test.go": "a.go"}, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTests(nil, map[string]string{"b_test.go": "b.go"}, map[string]string{"b_test.go": "e2e"}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM tests"); got != 1 {
		t.Errorf("tests = %d, want 1", got)
	}
	var kind string
	if err := s.db.QueryRow("SELECT kind FROM tests").Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "e2e" {
		t.Errorf("kind = %q, want e2e", kind)
	}
	// Missing kind defaults to unit.
	if err := s.SaveTests(nil, map[string]string{"c_test.go": "c.go"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT kind FROM tests").Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "unit" {
		t.Errorf("default kind = %q, want unit", kind)
	}

	if err := s.SaveCoChange(map[string]map[string]int{"a": {"b": 1}}, map[string]int{"a": 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCoChange(map[string]map[string]int{"c": {"d": 2}}, map[string]int{"c": 4}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM cochange"); got != 1 {
		t.Errorf("cochange = %d, want 1", got)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM churn"); got != 1 {
		t.Errorf("churn = %d, want 1", got)
	}
}

func TestUpdateFileExtras(t *testing.T) {
	s := newStore(t)
	if err := s.UpdateFileExtras([]index.FileExtra{{RelPath: "a.go", Preview: "p1", ContentHash: "h1"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateFileExtras([]index.FileExtra{{RelPath: "a.go", Preview: "p2", ContentHash: "h2"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM file_extras"); got != 1 {
		t.Errorf("file_extras = %d, want 1", got)
	}
	var hash string
	if err := s.db.QueryRow("SELECT content_hash FROM file_extras").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != "h2" {
		t.Errorf("content_hash = %q, want h2", hash)
	}
	if err := s.UpdateFileExtras(nil, []string{"a.go"}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM file_extras"); got != 0 {
		t.Errorf("file_extras after removal = %d", got)
	}
}

// --- error propagation ---

// blockWrites holds an exclusive write transaction on the cache from an
// independent connection, the way a second recon process would.
func blockWrites(t *testing.T, s *Store) func() {
	t.Helper()
	blocker, err := sql.Open("sqlite", buildDSN(s.path))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := blocker.Begin() // BEGIN IMMEDIATE — takes the write lock
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT OR REPLACE INTO meta (key, value) VALUES ('lock','held')"); err != nil {
		t.Fatal(err)
	}
	return func() {
		tx.Rollback()
		blocker.Close()
	}
}

// impatientStore points at the same DB with a 1ms busy timeout, so contention
// surfaces immediately instead of after the production 10s wait.
func impatientStore(t *testing.T, s *Store) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", s.path+"?_pragma=busy_timeout(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Store{db: db, Root: s.Root, path: s.path, cacheDir: s.cacheDir}
}

func TestWriteErrorsAreReportedNotSwallowed(t *testing.T) {
	s := newStore(t)
	// Pre-existing data that a swallowed DELETE would leave behind.
	if err := s.UpdateSymbols([]index.Symbol{sym("a.go", "Old", 1)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateReferences([]index.Reference{{Name: "r", File: "a.go", Line: 1}}, nil); err != nil {
		t.Fatal(err)
	}

	impatient := impatientStore(t, s)
	unblock := blockWrites(t, s)
	defer unblock()

	cases := []struct {
		name string
		call func() error
	}{
		{"UpdateSymbols", func() error {
			return impatient.UpdateSymbols([]index.Symbol{sym("a.go", "New", 2)}, nil)
		}},
		{"UpdateReferences", func() error {
			return impatient.UpdateReferences([]index.Reference{{Name: "r2", File: "a.go", Line: 2}}, nil)
		}},
		{"UpdateImports", func() error {
			return impatient.UpdateImports(map[string][]string{"a.go": {"b.go"}}, nil)
		}},
		{"UpdateFiles", func() error {
			return impatient.UpdateFiles([]scan.FileEntry{{RelPath: "a.go"}}, []string{"z.go"})
		}},
		{"UpdateContextDocs", func() error {
			return impatient.UpdateContextDocs([]index.ContextDoc{{File: "a.go", Origin: "a.go", Source: "comment"}}, nil)
		}},
		{"UpdateFileExtras", func() error {
			return impatient.UpdateFileExtras([]index.FileExtra{{RelPath: "a.go"}}, []string{"z.go"})
		}},
		{"SaveTests", func() error {
			return impatient.SaveTests(nil, map[string]string{"a_test.go": "a.go"}, nil)
		}},
		{"SaveCoChange", func() error {
			return impatient.SaveCoChange(map[string]map[string]int{"a": {"b": 1}}, map[string]int{"a": 1})
		}},
		{"UpdateFileParses", func() error {
			return impatient.UpdateFileParses([]index.FileParse{{RelPath: "a.go", Status: index.ParseOK}}, nil)
		}},
		{"UpdateImportStats", func() error {
			return impatient.UpdateImportStats(map[string]index.ImportStats{"a.go": {Lang: "go", Extracted: 1}}, nil)
		}},
		{"SaveSnapshot", func() error {
			return impatient.SaveSnapshot(&Snapshot{Symbols: []index.Symbol{sym("a.go", "New", 3)}})
		}},
		{"SetMeta", func() error { return impatient.SetMeta("k", "v") }},
	}
	for _, tc := range cases {
		if err := tc.call(); err == nil {
			t.Errorf("%s: returned nil while the database was locked by another writer", tc.name)
		}
	}

	// Nothing leaked past the failed transactions.
	unblock()
	if got := count(t, s, "SELECT COUNT(*) FROM symbols"); got != 1 {
		t.Errorf("symbols = %d, want 1 — a failed update mutated the cache", got)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM symbols WHERE name='Old'"); got != 1 {
		t.Error("original symbol lost to a failed update")
	}
	if got := count(t, s, "SELECT COUNT(*) FROM references_"); got != 1 {
		t.Errorf("references = %d, want 1", got)
	}
}

func TestSaveKeyFileMtimesReportsFailure(t *testing.T) {
	s := newStore(t)
	if err := SaveKeyFileMtimes(s); err != nil {
		t.Fatalf("healthy store: %v", err)
	}
	impatient := impatientStore(t, s)
	unblock := blockWrites(t, s)
	defer unblock()
	if err := SaveKeyFileMtimes(impatient); err == nil {
		t.Error("SaveKeyFileMtimes returned nil while the database was locked")
	}
}

// --- concurrency (the audited corruption) ---

// TestConcurrentUpdateSymbolsDoesNotDuplicate is the regression test for the
// audited defect: with no busy_timeout and a discarded DELETE error, concurrent
// writers appended a second copy of every symbol and every process still
// reported success.
func TestConcurrentUpdateSymbolsDoesNotDuplicate(t *testing.T) {
	s := newStore(t)

	const writers = 8
	const files = 20
	const perFile = 5

	var syms []index.Symbol
	for f := 0; f < files; f++ {
		for i := 0; i < perFile; i++ {
			syms = append(syms, sym(fmt.Sprintf("f%d.go", f), fmt.Sprintf("S%d", i), i+1))
		}
	}

	stores := make([]*Store, writers)
	stores[0] = s
	for i := 1; i < writers; i++ {
		stores[i] = openSecond(t, s)
	}

	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = stores[i].UpdateSymbols(syms, nil)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d: %v", i, err)
		}
	}
	want := files * perFile
	if got := count(t, s, "SELECT COUNT(*) FROM symbols"); got != want {
		t.Errorf("symbols = %d, want %d — concurrent writers corrupted the cache", got, want)
	}
}

func TestConcurrentMixedWritersAndReaders(t *testing.T) {
	s := newStore(t)
	if err := s.SaveSnapshot(&Snapshot{
		Files:      []scan.FileEntry{{RelPath: "f0.go", Lang: "go"}},
		Symbols:    []index.Symbol{sym("f0.go", "S", 1)},
		References: []index.Reference{{Name: "S", File: "f0.go", Line: 1}},
	}); err != nil {
		t.Fatal(err)
	}

	const n = 6
	errs := make(chan error, n*3)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		other := openSecond(t, s)
		wg.Add(3)
		go func(i int) {
			defer wg.Done()
			errs <- other.UpdateSymbols([]index.Symbol{sym("f0.go", "S", 1)}, nil)
		}(i)
		go func(i int) {
			defer wg.Done()
			errs <- other.UpdateReferences([]index.Reference{{Name: "S", File: "f0.go", Line: 1}}, nil)
		}(i)
		go func(i int) {
			defer wg.Done()
			_, err := other.LoadSnapshot()
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent op: %v", err)
		}
	}
	if got := count(t, s, "SELECT COUNT(*) FROM symbols"); got != 1 {
		t.Errorf("symbols = %d, want 1", got)
	}
	if got := count(t, s, "SELECT COUNT(*) FROM references_"); got != 1 {
		t.Errorf("references = %d, want 1", got)
	}
}

// --- misc ---

func TestMetaAndHasData(t *testing.T) {
	s := newStore(t)
	if _, ok := s.GetMeta("nope"); ok {
		t.Error("GetMeta reported a missing key as present")
	}
	if err := s.SetMeta("k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta("k", "v2"); err != nil {
		t.Fatal(err)
	}
	if v, ok := s.GetMeta("k"); !ok || v != "v2" {
		t.Errorf("GetMeta = %q, %v", v, ok)
	}
	if s.HasData() {
		t.Error("HasData true with no files")
	}
	if err := s.UpdateFiles([]scan.FileEntry{{RelPath: "a.go"}}, nil); err != nil {
		t.Fatal(err)
	}
	if !s.HasData() {
		t.Error("HasData false with files present")
	}
}

func TestClearRemovesCacheDir(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, cacheDir)); !os.IsNotExist(err) {
		t.Errorf("cache dir still present after Clear: %v", err)
	}
	// Reopening rebuilds cleanly.
	s2, err := Open(root)
	if err != nil {
		t.Fatalf("reopen after Clear: %v", err)
	}
	s2.Close()
}

func TestLoadSnapshotOnEmptyDB(t *testing.T) {
	s := newStore(t)
	snap, err := s.LoadSnapshot()
	if err != nil {
		t.Fatalf("LoadSnapshot on empty cache: %v", err)
	}
	if len(snap.Files) != 0 || len(snap.Symbols) != 0 {
		t.Errorf("expected empty snapshot, got %+v", snap)
	}
	if snap.Imports == nil || snap.Churn == nil || snap.CoChangePairs == nil || snap.ImportStats == nil {
		t.Error("maps must be initialised even when empty")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
