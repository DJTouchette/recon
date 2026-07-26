package cache

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
	_ "modernc.org/sqlite"
)

const (
	cacheDir  = ".recon"
	dbFile    = "recon.db"
	schemaVer = 7

	// analysisVer identifies the *logic* that produced the cached derived data,
	// as opposed to schemaVer, which identifies the shape of the tables holding
	// it. Bump it whenever a change alters what recon would compute for an
	// unchanged file: import resolution, test mapping, symbol extraction,
	// classification, metrics.
	//
	// Without it, a logic fix never reaches an existing cache. Refresh rescans
	// changed files only — correct for incremental work, and exactly wrong when
	// the analysis itself changed, because then every file's stored result is
	// stale while every file's mtime says otherwise. Observed on a 13k-file
	// repo: after C# usings began classifying third-party namespaces as
	// External, a cold build reported 11 unresolved imports and `recon refresh`
	// went on reporting 413 indefinitely. The caveat that fix existed to
	// silence stayed on screen, and nothing anywhere said the number was
	// produced by code that no longer exists.
	//
	// A mismatch drops and recreates the cache, same as a schema change. That
	// is affordable because the cache is fully rebuildable from the repo — 7.9s
	// cold on that same 13k-file repo — and the alternative is serving answers
	// from retired logic.
	//
	// 1: first tracked version. Covers the C# external-vs-unresolved
	//    classification, the .NET test-project mapping tiers, and the source
	//    content-scan issue reporting.
	// 2: C# file-based-app directives no longer break the parse, and a partial
	//    tree-sitter parse is supplemented by line patterns — both change the
	//    symbols and parse statuses stored for unchanged files.
	analysisVer = 2

	// busyTimeoutMS is how long a writer waits for a competing writer's lock
	// before giving up with SQLITE_BUSY. Several recon processes routinely run
	// against the same repo (editor plugin, CLI, agent tooling); without this
	// every concurrent write fails instantly.
	busyTimeoutMS = 10000
)

// connPragmas are applied to every pooled connection via the DSN. They must be
// set per-connection: database/sql opens connections lazily, so a single
// `db.Exec("PRAGMA ...")` after Open only configures whichever connection
// happened to serve it. All of these are connection state and cheap to repeat.
// journal_mode is not here on purpose — see enableWAL.
var connPragmas = []string{
	"busy_timeout(" + strconv.Itoa(busyTimeoutMS) + ")",
	"synchronous(NORMAL)",
	"cache_size(-64000)",
	"temp_store(MEMORY)",
	"mmap_size(268435456)",
}

// Snapshot holds all indexed data for save/load.
type Snapshot struct {
	Files   []scan.FileEntry
	Imports map[string][]string // source → targets
	// ImportStats is per-file import-resolution telemetry, keyed by rel_path.
	// It is not derivable from Imports: a file with no edges may have had every
	// specifier resolved to nothing (dropped edges) or none at all, and only
	// these counts tell the two apart. Present for files with zero edges too.
	ImportStats   map[string]index.ImportStats
	SourceToTest  map[string][]string // source → test paths
	TestToSource  map[string]string   // test → source path
	TestKinds     map[string]string   // test → kind
	CoChangePairs map[string]map[string]int
	Churn         map[string]int
	Symbols       []index.Symbol
	FileParses    []index.FileParse
	References    []index.Reference
	ContextDocs   []index.ContextDoc
	FileExtras    []index.FileExtra
	Metrics       []index.FileMetrics
	NearbyConfigs []index.NearbyConfig
	OwnerRules    []index.OwnerRule
}

// Store manages the SQLite cache database.
type Store struct {
	db       *sql.DB
	Root     string
	path     string
	cacheDir string // directory containing the DB file
}

// Open creates or opens the cache database in <root>/.recon/.
func Open(root string) (*Store, error) {
	return OpenAt(root, filepath.Join(root, cacheDir))
}

// OpenAt creates or opens the cache database in the given directory.
// This allows callers (e.g. rivet) to store the cache elsewhere.
func OpenAt(root, dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}

	writeCacheGitignore(dir)

	dbPath := filepath.Join(dir, dbFile)
	db, err := sql.Open("sqlite", buildDSN(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// sql.Open is lazy — force one connection now so a broken path or a bad
	// pragma surfaces here rather than halfway through an index build.
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open db %s: %w", dbPath, err)
	}
	enableWAL(db)

	s := &Store{db: db, Root: root, path: dbPath, cacheDir: dir}
	if err := s.ensureSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}

	return s, nil
}

// buildDSN appends the per-connection pragmas to the database path. The driver
// strips the query string from a non-`file:` DSN before opening, so a plain
// path stays a plain path (important on Windows); paths that themselves contain
// a query/fragment character need the URI form with escaping.
func buildDSN(dbPath string) string {
	params := make([]string, 0, len(connPragmas))
	for _, p := range connPragmas {
		params = append(params, "_pragma="+url.QueryEscape(p))
	}
	// Every transaction in this package is a write transaction. Taking the
	// write lock up front (BEGIN IMMEDIATE) means busy_timeout can retry it;
	// a deferred transaction that upgrades mid-way gets SQLITE_BUSY_SNAPSHOT,
	// which is *not* retried by busy_timeout.
	params = append(params, "_txlock=immediate")

	prefix := dbPath
	if strings.ContainsAny(dbPath, "?#") {
		r := strings.NewReplacer("?", "%3f", "#", "%23")
		prefix = "file:" + r.Replace(dbPath)
	}
	return prefix + "?" + strings.Join(params, "&")
}

// enableWAL switches the database to write-ahead logging, which is what lets
// readers run while a writer holds the lock.
//
// It is deliberately not one of the DSN pragmas. journal_mode is persisted in
// the database file, so it only has to succeed once — and the initial
// conversion needs exclusive access and returns SQLITE_BUSY *immediately*
// rather than honouring busy_timeout. Applying it on every connection therefore
// made a pack of processes opening a fresh cache at the same instant fail to
// open at all. Whoever wins the race sets the mode for everyone, so losing it is
// harmless: retry briefly, and if the mode is already wal (the overwhelmingly
// common case after the first ever open) return at once.
//
// Best-effort by design: a database that stays in rollback-journal mode is
// slower and serialises readers against writers, but it is still correct, and
// failing the open would be a worse outcome than a slow cache.
func enableWAL(db *sql.DB) {
	for i := 0; i < 50; i++ {
		var mode string
		if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err == nil && strings.EqualFold(mode, "wal") {
			return
		}
		if err := db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err == nil && strings.EqualFold(mode, "wal") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// writeCacheGitignore drops a self-ignoring .gitignore into the cache dir so a
// `git add -A` in the repo never sweeps up the multi-MB SQLite file. Advisory
// only: an existing file is left alone (the user may have edited it) and a
// write failure must not stop us from serving an already-built cache.
func writeCacheGitignore(dir string) {
	p := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(p); err == nil {
		return
	}
	_ = os.WriteFile(p, []byte("*\n"), 0o644)
}

// Close closes the database.
func (s *Store) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// allTables lists every table the schema owns, in drop order.
var allTables = []string{"meta", "files", "imports", "tests", "cochange", "churn", "symbols", "file_parse", "import_stats", "references_", "file_extras", "file_metrics", "nearby_configs", "codeowners", "context_docs"}

// dataTables is allTables minus meta — the tables a rebuild clears.
var dataTables = allTables[1:]

// rowQuerier is satisfied by both *sql.DB and *sql.Tx.
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// schemaUpToDate reports whether the cached data was produced by this build —
// both the table shape (schema_version) and the analysis logic that filled them
// (analysis_version). A missing meta table (fresh DB) or an unreadable or
// absent version means "no", which is also how a cache written before
// analysis_version existed is treated: rebuild rather than trust it.
func (s *Store) schemaUpToDate(q rowQuerier) bool {
	return storedVersion(q, "schema_version") == schemaVer &&
		storedVersion(q, "analysis_version") == analysisVer
}

// storedVersion reads an integer meta value, returning -1 when it is absent or
// unparseable — a sentinel no valid version equals, so unreadable is never
// mistaken for current.
func storedVersion(q rowQuerier, key string) int {
	var raw string
	if err := q.QueryRow("SELECT value FROM meta WHERE key=?", key).Scan(&raw); err != nil {
		return -1
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return v
}

// ensureSchema migrates the database to the current schema version, destroying
// any older data (the cache is fully rebuildable from the repo, so migration is
// drop-and-recreate). The whole migration runs in one transaction: SQLite DDL
// is transactional, so a concurrent process or a crash never observes a
// half-dropped schema.
func (s *Store) ensureSchema() error {
	if s.schemaUpToDate(s.db) {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin schema tx: %w", err)
	}
	defer tx.Rollback()

	// Re-check under the write lock: another process may have migrated while
	// we were waiting for it.
	if s.schemaUpToDate(tx) {
		return nil // deferred Rollback releases the lock
	}

	for _, table := range allTables {
		if _, err := tx.Exec("DROP TABLE IF EXISTS " + table); err != nil {
			return fmt.Errorf("drop %s: %w", table, err)
		}
	}

	if _, err := tx.Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	if _, err := tx.Exec("INSERT INTO meta (key, value) VALUES ('schema_version', ?)", strconv.Itoa(schemaVer)); err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	if _, err := tx.Exec("INSERT INTO meta (key, value) VALUES ('analysis_version', ?)", strconv.Itoa(analysisVer)); err != nil {
		return fmt.Errorf("record analysis version: %w", err)
	}

	return tx.Commit()
}

const schema = `
CREATE TABLE meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE files (
	rel_path TEXT PRIMARY KEY,
	dir TEXT NOT NULL,
	lang TEXT NOT NULL DEFAULT '',
	class INTEGER NOT NULL DEFAULT 0,
	size INTEGER NOT NULL DEFAULT 0,
	mtime INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE imports (
	source_path TEXT NOT NULL,
	target_path TEXT NOT NULL,
	PRIMARY KEY (source_path, target_path)
);
CREATE TABLE tests (
	test_path TEXT PRIMARY KEY,
	source_path TEXT NOT NULL,
	kind TEXT NOT NULL DEFAULT 'unit'
);
CREATE TABLE cochange (
	file_a TEXT NOT NULL,
	file_b TEXT NOT NULL,
	count INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (file_a, file_b)
);
CREATE TABLE churn (
	file_path TEXT PRIMARY KEY,
	commits INTEGER NOT NULL DEFAULT 0
);
-- extractor is per-symbol provenance (tree-sitter vs regex); '' means unknown.
-- It is NOT the parse-status signal — see file_parse below, which is the only
-- structure that can represent a file that produced no symbols at all.
CREATE TABLE symbols (
	file_path TEXT NOT NULL,
	name TEXT NOT NULL,
	kind TEXT NOT NULL,
	line INTEGER NOT NULL DEFAULT 0,
	signature TEXT NOT NULL DEFAULT '',
	extractor TEXT NOT NULL DEFAULT ''
);
-- Extraction provenance is a property of the FILE, not of a symbol. The three
-- states worth warning about — unsupported language, failed parse, truncated
-- parse — all yield zero symbol rows, so a per-symbol column is structurally
-- unable to represent them and a cached run would read as "clean" for exactly
-- the files that are broken. Hence a row per candidate file, including the ones
-- that produced nothing.
CREATE TABLE file_parse (
	rel_path TEXT PRIMARY KEY,
	lang TEXT NOT NULL DEFAULT '',
	extractor TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT '',
	symbol_count INTEGER NOT NULL DEFAULT 0,
	detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_file_parse_status ON file_parse(status);
-- Import resolution has the same shape of blind spot as symbol extraction, and
-- the same fix. A bare fan_in of 0 cannot distinguish "nothing imports this"
-- from "recon does not understand this language's import syntax", so every
-- specifier an extractor produced is bucketed here: resolved (made an edge),
-- external (stdlib/third-party, correctly no edge), unresolved (a real edge was
-- dropped). Rows exist for files whose imports produced zero edges — those are
-- precisely the files the signal is for, so an edge table cannot carry it.
--
-- rel_path is the PRIMARY KEY. symbols has no key on its file column, which is
-- why concurrent runs there could duplicate rows; an upsert here cannot.
CREATE TABLE import_stats (
	rel_path TEXT PRIMARY KEY,
	lang TEXT NOT NULL DEFAULT '',
	extracted INTEGER NOT NULL DEFAULT 0,
	resolved INTEGER NOT NULL DEFAULT 0,
	external INTEGER NOT NULL DEFAULT 0,
	unresolved INTEGER NOT NULL DEFAULT 0,
	-- JSON array of the bounded specifier sample (see encodeUnresolvedSpecs).
	unresolved_specs TEXT NOT NULL DEFAULT ''
);
-- Serves "which files dropped edges", the query that drives the trust warning.
CREATE INDEX idx_import_stats_unresolved ON import_stats(unresolved);
-- No uniqueness constraint here, deliberately: (name, file_path, line) is not
-- a key. Two calls to the same function on one line -- f(g(x)) + f(y) -- are
-- two distinct, legitimate references that share all three columns, and
-- reference counts are user-visible. Duplicate protection for this table comes
-- from the delete-then-insert cycle being correctly error-checked, not from a
-- constraint that would silently under-count real call sites.
CREATE TABLE references_ (
	name TEXT NOT NULL,
	file_path TEXT NOT NULL,
	line INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE file_extras (
	rel_path TEXT PRIMARY KEY,
	preview TEXT NOT NULL DEFAULT '',
	content_hash TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_files_dir ON files(dir);
CREATE INDEX idx_files_lang ON files(lang);
CREATE INDEX idx_files_class ON files(class);
CREATE INDEX idx_imports_target ON imports(target_path);
CREATE INDEX idx_tests_source ON tests(source_path);
CREATE INDEX idx_cochange_b ON cochange(file_b);
CREATE TABLE file_metrics (
	rel_path TEXT PRIMARY KEY,
	fan_in INTEGER NOT NULL DEFAULT 0,
	fan_out INTEGER NOT NULL DEFAULT 0,
	churn INTEGER NOT NULL DEFAULT 0,
	hotspot_score REAL NOT NULL DEFAULT 0
);
CREATE TABLE nearby_configs (
	dir TEXT NOT NULL,
	config_type TEXT NOT NULL,
	config_path TEXT NOT NULL,
	PRIMARY KEY (dir, config_type)
);
CREATE TABLE codeowners (
	priority INTEGER NOT NULL,
	pattern TEXT NOT NULL,
	owners TEXT NOT NULL
);
CREATE TABLE context_docs (
	file_path TEXT NOT NULL,
	symbol TEXT NOT NULL DEFAULT '',
	line INTEGER NOT NULL DEFAULT 0,
	source TEXT NOT NULL,
	origin_path TEXT NOT NULL,
	body TEXT NOT NULL
);
CREATE INDEX idx_ctxdocs_file ON context_docs(file_path);
CREATE INDEX idx_ctxdocs_symbol ON context_docs(symbol);
CREATE INDEX idx_ctxdocs_origin ON context_docs(origin_path);
-- (file_path, name, kind, line) is a genuine key for a symbol definition: the
-- same name may appear many times in a file (overloads, a method and a field,
-- re-declarations in sibling scopes) but not twice at the same line with the
-- same kind. Making it UNIQUE turns "duplicate symbol row" from silent cache
-- corruption into an impossibility, and inserts use OR REPLACE so a re-index of
-- an identical definition refreshes the signature instead of failing. Also
-- serves file_path lookups as the leftmost prefix, so no separate
-- idx_symbols_file is needed.
CREATE UNIQUE INDEX idx_symbols_key ON symbols(file_path, name, kind, line);
CREATE INDEX idx_symbols_name ON symbols(name);
CREATE INDEX idx_symbols_kind ON symbols(kind);
CREATE INDEX idx_references_name ON references_(name);
CREATE INDEX idx_metrics_hotspot ON file_metrics(hotspot_score);
CREATE INDEX idx_nearby_dir ON nearby_configs(dir);
`

// --- Meta operations ---

// GetMeta returns a meta value by key.
func (s *Store) GetMeta(key string) (string, bool) {
	var val string
	err := s.db.QueryRow("SELECT value FROM meta WHERE key=?", key).Scan(&val)
	if err != nil {
		return "", false
	}
	return val, true
}

// SetMeta sets a meta key/value pair.
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec("INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)", key, value)
	return err
}

// HasData returns true if the DB has file data.
func (s *Store) HasData() bool {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM files").Scan(&count)
	return err == nil && count > 0
}

// --- Full save (rebuild) ---

// SaveSnapshot writes all indexed data to the database in a single transaction.
func (s *Store) SaveSnapshot(snap *Snapshot) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Clear data tables (not meta)
	for _, table := range dataTables {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}

	// --- Files ---
	fileStmt, err := tx.Prepare("INSERT INTO files (rel_path, dir, lang, class, size, mtime) VALUES (?, ?, ?, ?, ?, ?)")
	if err != nil {
		return fmt.Errorf("prepare files: %w", err)
	}
	defer fileStmt.Close()

	for i := range snap.Files {
		f := &snap.Files[i]
		dir := filepath.Dir(f.RelPath)
		if dir == "." {
			dir = ""
		}
		if _, err := fileStmt.Exec(f.RelPath, dir, f.Lang, int(f.Class), f.Size, f.ModTime); err != nil {
			return fmt.Errorf("insert file %s: %w", f.RelPath, err)
		}
	}

	// --- Imports ---
	if len(snap.Imports) > 0 {
		importStmt, err := tx.Prepare("INSERT INTO imports (source_path, target_path) VALUES (?, ?)")
		if err != nil {
			return fmt.Errorf("prepare imports: %w", err)
		}
		defer importStmt.Close()

		for src, targets := range snap.Imports {
			for _, target := range targets {
				if _, err := importStmt.Exec(src, target); err != nil {
					return fmt.Errorf("insert import %s→%s: %w", src, target, err)
				}
			}
		}
	}

	// --- Tests ---
	if len(snap.TestToSource) > 0 {
		testStmt, err := tx.Prepare("INSERT INTO tests (test_path, source_path, kind) VALUES (?, ?, ?)")
		if err != nil {
			return fmt.Errorf("prepare tests: %w", err)
		}
		defer testStmt.Close()

		for testPath, sourcePath := range snap.TestToSource {
			kind := snap.TestKinds[testPath]
			if kind == "" {
				kind = "unit"
			}
			if _, err := testStmt.Exec(testPath, sourcePath, kind); err != nil {
				return fmt.Errorf("insert test %s: %w", testPath, err)
			}
		}
	}

	// --- CoChange ---
	if len(snap.CoChangePairs) > 0 {
		ccStmt, err := tx.Prepare("INSERT INTO cochange (file_a, file_b, count) VALUES (?, ?, ?)")
		if err != nil {
			return fmt.Errorf("prepare cochange: %w", err)
		}
		defer ccStmt.Close()

		for a, bs := range snap.CoChangePairs {
			for b, count := range bs {
				if _, err := ccStmt.Exec(a, b, count); err != nil {
					return fmt.Errorf("insert cochange %s/%s: %w", a, b, err)
				}
			}
		}
	}

	// --- Churn ---
	if len(snap.Churn) > 0 {
		churnStmt, err := tx.Prepare("INSERT INTO churn (file_path, commits) VALUES (?, ?)")
		if err != nil {
			return fmt.Errorf("prepare churn: %w", err)
		}
		defer churnStmt.Close()

		for path, commits := range snap.Churn {
			if _, err := churnStmt.Exec(path, commits); err != nil {
				return fmt.Errorf("insert churn %s: %w", path, err)
			}
		}
	}

	// --- Symbols ---
	if len(snap.Symbols) > 0 {
		symStmt, err := tx.Prepare(insertSymbolSQL)
		if err != nil {
			return fmt.Errorf("prepare symbols: %w", err)
		}
		defer symStmt.Close()

		for i := range snap.Symbols {
			sym := &snap.Symbols[i]
			if _, err := symStmt.Exec(sym.File, sym.Name, sym.Kind, sym.Line, sym.Signature, sym.Extractor); err != nil {
				return fmt.Errorf("insert symbol %s:%s: %w", sym.File, sym.Name, err)
			}
		}
	}

	// --- File parses ---
	if len(snap.FileParses) > 0 {
		fpStmt, err := tx.Prepare(insertFileParseSQL)
		if err != nil {
			return fmt.Errorf("prepare file_parse: %w", err)
		}
		defer fpStmt.Close()

		for i := range snap.FileParses {
			p := &snap.FileParses[i]
			if _, err := fpStmt.Exec(p.RelPath, p.Lang, p.Extractor, p.Status, p.SymbolCount, p.Detail); err != nil {
				return fmt.Errorf("insert file_parse %s: %w", p.RelPath, err)
			}
		}
	}

	// --- Import stats ---
	if len(snap.ImportStats) > 0 {
		isStmt, err := tx.Prepare(insertImportStatsSQL)
		if err != nil {
			return fmt.Errorf("prepare import_stats: %w", err)
		}
		defer isStmt.Close()

		for path, st := range snap.ImportStats {
			if err := execImportStats(isStmt, path, st); err != nil {
				return err
			}
		}
	}

	// --- References ---
	if len(snap.References) > 0 {
		refStmt, err := tx.Prepare("INSERT INTO references_ (name, file_path, line) VALUES (?, ?, ?)")
		if err != nil {
			return fmt.Errorf("prepare references: %w", err)
		}
		defer refStmt.Close()

		for i := range snap.References {
			r := &snap.References[i]
			if _, err := refStmt.Exec(r.Name, r.File, r.Line); err != nil {
				return fmt.Errorf("insert reference %s:%s: %w", r.File, r.Name, err)
			}
		}
	}

	// --- Context Docs ---
	if len(snap.ContextDocs) > 0 {
		cdStmt, err := tx.Prepare("INSERT INTO context_docs (file_path, symbol, line, source, origin_path, body) VALUES (?, ?, ?, ?, ?, ?)")
		if err != nil {
			return fmt.Errorf("prepare context_docs: %w", err)
		}
		defer cdStmt.Close()

		for i := range snap.ContextDocs {
			d := &snap.ContextDocs[i]
			if _, err := cdStmt.Exec(d.File, d.Symbol, d.Line, d.Source, d.Origin, d.Body); err != nil {
				return fmt.Errorf("insert context_doc %s: %w", d.Origin, err)
			}
		}
	}

	// --- File Extras ---
	if len(snap.FileExtras) > 0 {
		extraStmt, err := tx.Prepare("INSERT INTO file_extras (rel_path, preview, content_hash) VALUES (?, ?, ?)")
		if err != nil {
			return fmt.Errorf("prepare file_extras: %w", err)
		}
		defer extraStmt.Close()

		for i := range snap.FileExtras {
			e := &snap.FileExtras[i]
			if _, err := extraStmt.Exec(e.RelPath, e.Preview, e.ContentHash); err != nil {
				return fmt.Errorf("insert file_extra %s: %w", e.RelPath, err)
			}
		}
	}

	// --- File Metrics ---
	if len(snap.Metrics) > 0 {
		metStmt, err := tx.Prepare("INSERT INTO file_metrics (rel_path, fan_in, fan_out, churn, hotspot_score) VALUES (?, ?, ?, ?, ?)")
		if err != nil {
			return fmt.Errorf("prepare file_metrics: %w", err)
		}
		defer metStmt.Close()
		for i := range snap.Metrics {
			m := &snap.Metrics[i]
			if _, err := metStmt.Exec(m.RelPath, m.FanIn, m.FanOut, m.Churn, m.HotspotScore); err != nil {
				return fmt.Errorf("insert file_metrics %s: %w", m.RelPath, err)
			}
		}
	}

	// --- Nearby Configs ---
	if len(snap.NearbyConfigs) > 0 {
		ncStmt, err := tx.Prepare("INSERT INTO nearby_configs (dir, config_type, config_path) VALUES (?, ?, ?)")
		if err != nil {
			return fmt.Errorf("prepare nearby_configs: %w", err)
		}
		defer ncStmt.Close()
		for i := range snap.NearbyConfigs {
			nc := &snap.NearbyConfigs[i]
			if _, err := ncStmt.Exec(nc.Dir, nc.ConfigType, nc.ConfigPath); err != nil {
				return fmt.Errorf("insert nearby_config %s/%s: %w", nc.Dir, nc.ConfigType, err)
			}
		}
	}

	// --- CODEOWNERS ---
	if len(snap.OwnerRules) > 0 {
		coStmt, err := tx.Prepare("INSERT INTO codeowners (priority, pattern, owners) VALUES (?, ?, ?)")
		if err != nil {
			return fmt.Errorf("prepare codeowners: %w", err)
		}
		defer coStmt.Close()
		for i := range snap.OwnerRules {
			r := &snap.OwnerRules[i]
			if _, err := coStmt.Exec(r.Priority, r.Pattern, strings.Join(r.Owners, " ")); err != nil {
				return fmt.Errorf("insert codeowner %s: %w", r.Pattern, err)
			}
		}
	}

	return tx.Commit()
}

// --- Full load ---

// LoadSnapshot reads all indexed data from the database.
func (s *Store) LoadSnapshot() (*Snapshot, error) {
	snap := &Snapshot{
		Imports:       make(map[string][]string),
		ImportStats:   make(map[string]index.ImportStats),
		SourceToTest:  make(map[string][]string),
		TestToSource:  make(map[string]string),
		TestKinds:     make(map[string]string),
		CoChangePairs: make(map[string]map[string]int),
		Churn:         make(map[string]int),
	}

	// --- Files ---
	rows, err := s.db.Query("SELECT rel_path, lang, class, size, mtime FROM files")
	if err != nil {
		return nil, fmt.Errorf("query files: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var f scan.FileEntry
		var class int
		if err := rows.Scan(&f.RelPath, &f.Lang, &class, &f.Size, &f.ModTime); err != nil {
			return nil, fmt.Errorf("scan file row: %w", err)
		}
		f.Class = scan.FileClass(class)
		snap.Files = append(snap.Files, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("file rows: %w", err)
	}

	// --- Imports ---
	rows, err = s.db.Query("SELECT source_path, target_path FROM imports")
	if err != nil {
		return nil, fmt.Errorf("query imports: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var src, target string
		if err := rows.Scan(&src, &target); err != nil {
			return nil, fmt.Errorf("scan import row: %w", err)
		}
		snap.Imports[src] = append(snap.Imports[src], target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("import rows: %w", err)
	}

	// --- Tests ---
	rows, err = s.db.Query("SELECT test_path, source_path, kind FROM tests")
	if err != nil {
		return nil, fmt.Errorf("query tests: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var testPath, sourcePath, kind string
		if err := rows.Scan(&testPath, &sourcePath, &kind); err != nil {
			return nil, fmt.Errorf("scan test row: %w", err)
		}
		snap.SourceToTest[sourcePath] = append(snap.SourceToTest[sourcePath], testPath)
		snap.TestToSource[testPath] = sourcePath
		snap.TestKinds[testPath] = kind
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("test rows: %w", err)
	}

	// --- CoChange ---
	rows, err = s.db.Query("SELECT file_a, file_b, count FROM cochange")
	if err != nil {
		return nil, fmt.Errorf("query cochange: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var a, b string
		var count int
		if err := rows.Scan(&a, &b, &count); err != nil {
			return nil, fmt.Errorf("scan cochange row: %w", err)
		}
		if snap.CoChangePairs[a] == nil {
			snap.CoChangePairs[a] = make(map[string]int)
		}
		snap.CoChangePairs[a][b] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cochange rows: %w", err)
	}

	// --- Churn ---
	rows, err = s.db.Query("SELECT file_path, commits FROM churn")
	if err != nil {
		return nil, fmt.Errorf("query churn: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var path string
		var commits int
		if err := rows.Scan(&path, &commits); err != nil {
			return nil, fmt.Errorf("scan churn row: %w", err)
		}
		snap.Churn[path] = commits
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("churn rows: %w", err)
	}

	// --- Symbols ---
	rows, err = s.db.Query("SELECT file_path, name, kind, line, signature, extractor FROM symbols")
	if err != nil {
		return nil, fmt.Errorf("query symbols: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var sym index.Symbol
		if err := rows.Scan(&sym.File, &sym.Name, &sym.Kind, &sym.Line, &sym.Signature, &sym.Extractor); err != nil {
			return nil, fmt.Errorf("scan symbol row: %w", err)
		}
		snap.Symbols = append(snap.Symbols, sym)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("symbol rows: %w", err)
	}

	// --- File parses ---
	rows, err = s.db.Query("SELECT " + fileParseCols + " FROM file_parse")
	if err != nil {
		return nil, fmt.Errorf("query file_parse: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		p, err := scanFileParse(rows)
		if err != nil {
			return nil, err
		}
		snap.FileParses = append(snap.FileParses, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("file_parse rows: %w", err)
	}

	// --- Import stats ---
	rows, err = s.db.Query("SELECT " + importStatsCols + " FROM import_stats")
	if err != nil {
		return nil, fmt.Errorf("query import_stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		path, st, err := scanImportStats(rows)
		if err != nil {
			return nil, err
		}
		snap.ImportStats[path] = st
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("import_stats rows: %w", err)
	}

	// --- References ---
	rows, err = s.db.Query("SELECT name, file_path, line FROM references_")
	if err != nil {
		return nil, fmt.Errorf("query references: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var ref index.Reference
		if err := rows.Scan(&ref.Name, &ref.File, &ref.Line); err != nil {
			return nil, fmt.Errorf("scan reference row: %w", err)
		}
		snap.References = append(snap.References, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reference rows: %w", err)
	}

	// --- Context Docs ---
	rows, err = s.db.Query("SELECT file_path, symbol, line, source, origin_path, body FROM context_docs")
	if err != nil {
		return nil, fmt.Errorf("query context_docs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var d index.ContextDoc
		if err := rows.Scan(&d.File, &d.Symbol, &d.Line, &d.Source, &d.Origin, &d.Body); err != nil {
			return nil, fmt.Errorf("scan context_doc row: %w", err)
		}
		snap.ContextDocs = append(snap.ContextDocs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("context_doc rows: %w", err)
	}

	// --- File Extras ---
	rows, err = s.db.Query("SELECT rel_path, preview, content_hash FROM file_extras")
	if err != nil {
		return nil, fmt.Errorf("query file_extras: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var e index.FileExtra
		if err := rows.Scan(&e.RelPath, &e.Preview, &e.ContentHash); err != nil {
			return nil, fmt.Errorf("scan file_extra row: %w", err)
		}
		snap.FileExtras = append(snap.FileExtras, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("file_extra rows: %w", err)
	}

	// --- File Metrics ---
	rows, err = s.db.Query("SELECT rel_path, fan_in, fan_out, churn, hotspot_score FROM file_metrics")
	if err != nil {
		return nil, fmt.Errorf("query file_metrics: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var m index.FileMetrics
		if err := rows.Scan(&m.RelPath, &m.FanIn, &m.FanOut, &m.Churn, &m.HotspotScore); err != nil {
			return nil, fmt.Errorf("scan file_metrics row: %w", err)
		}
		snap.Metrics = append(snap.Metrics, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("file_metrics rows: %w", err)
	}

	// --- Nearby Configs ---
	rows, err = s.db.Query("SELECT dir, config_type, config_path FROM nearby_configs")
	if err != nil {
		return nil, fmt.Errorf("query nearby_configs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var nc index.NearbyConfig
		if err := rows.Scan(&nc.Dir, &nc.ConfigType, &nc.ConfigPath); err != nil {
			return nil, fmt.Errorf("scan nearby_configs row: %w", err)
		}
		snap.NearbyConfigs = append(snap.NearbyConfigs, nc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("nearby_configs rows: %w", err)
	}

	// --- CODEOWNERS ---
	rows, err = s.db.Query("SELECT priority, pattern, owners FROM codeowners ORDER BY priority")
	if err != nil {
		return nil, fmt.Errorf("query codeowners: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var r index.OwnerRule
		var ownersStr string
		if err := rows.Scan(&r.Priority, &r.Pattern, &ownersStr); err != nil {
			return nil, fmt.Errorf("scan codeowners row: %w", err)
		}
		r.Owners = strings.Fields(ownersStr)
		snap.OwnerRules = append(snap.OwnerRules, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("codeowners rows: %w", err)
	}

	return snap, nil
}

// --- Incremental operations ---

// GetFileMtimes returns all stored file paths and their mtimes for refresh diffing.
func (s *Store) GetFileMtimes() (map[string]int64, error) {
	rows, err := s.db.Query("SELECT rel_path, mtime FROM files")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	mtimes := make(map[string]int64, 8192)
	for rows.Next() {
		var path string
		var mtime int64
		if err := rows.Scan(&path, &mtime); err != nil {
			return nil, err
		}
		mtimes[path] = mtime
	}
	return mtimes, rows.Err()
}

// FileSig is the cheap change-detection signature for a cached file.
//
// mtime alone is not enough: `tar -x`, `rsync -t`, `cp -p` and Docker/CI
// workspace restores all reproduce the recorded mtime while the bytes differ,
// which makes recon serve symbols for content that is no longer on disk. Size
// catches those, and it costs nothing — the same os.Stat already provides it
// and the column has always been stored.
type FileSig struct {
	ModTime int64
	Size    int64
}

// GetFileSignatures returns the stored mtime and size for every cached file,
// for refresh diffing against the working tree.
func (s *Store) GetFileSignatures() (map[string]FileSig, error) {
	rows, err := s.db.Query("SELECT rel_path, mtime, size FROM files")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sigs := make(map[string]FileSig, 8192)
	for rows.Next() {
		var path string
		var sig FileSig
		if err := rows.Scan(&path, &sig.ModTime, &sig.Size); err != nil {
			return nil, err
		}
		sigs[path] = sig
	}
	return sigs, rows.Err()
}

// UpdateFiles upserts changed/added files and removes deleted files.
func (s *Store) UpdateFiles(upsert []scan.FileEntry, remove []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if len(remove) > 0 {
		delStmt, err := tx.Prepare("DELETE FROM files WHERE rel_path=?")
		if err != nil {
			return err
		}
		defer delStmt.Close()
		for _, p := range remove {
			if _, err := delStmt.Exec(p); err != nil {
				return fmt.Errorf("delete file %s: %w", p, err)
			}
		}
	}

	if len(upsert) > 0 {
		stmt, err := tx.Prepare("INSERT OR REPLACE INTO files (rel_path, dir, lang, class, size, mtime) VALUES (?, ?, ?, ?, ?, ?)")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i := range upsert {
			f := &upsert[i]
			dir := filepath.Dir(f.RelPath)
			if dir == "." {
				dir = ""
			}
			if _, err := stmt.Exec(f.RelPath, dir, f.Lang, int(f.Class), f.Size, f.ModTime); err != nil {
				return fmt.Errorf("upsert file %s: %w", f.RelPath, err)
			}
		}
	}

	return tx.Commit()
}

// UpdateImports deletes old imports for the given source files and inserts new ones.
func (s *Store) UpdateImports(newImports map[string][]string, removedSources []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	delStmt, err := tx.Prepare("DELETE FROM imports WHERE source_path=?")
	if err != nil {
		return err
	}
	defer delStmt.Close()

	// Delete imports for all changed/removed sources. A swallowed failure here
	// leaves the old rows in place and the inserts below pile new ones on top.
	for _, src := range removedSources {
		if _, err := delStmt.Exec(src); err != nil {
			return fmt.Errorf("delete imports for %s: %w", src, err)
		}
	}
	for src := range newImports {
		if _, err := delStmt.Exec(src); err != nil {
			return fmt.Errorf("delete imports for %s: %w", src, err)
		}
	}

	// Insert new imports
	if len(newImports) > 0 {
		insStmt, err := tx.Prepare("INSERT INTO imports (source_path, target_path) VALUES (?, ?)")
		if err != nil {
			return err
		}
		defer insStmt.Close()
		for src, targets := range newImports {
			for _, target := range targets {
				if _, err := insStmt.Exec(src, target); err != nil {
					return fmt.Errorf("insert import %s→%s: %w", src, target, err)
				}
			}
		}
	}

	return tx.Commit()
}

// SaveTests replaces all test mappings.
func (s *Store) SaveTests(sourceToTest map[string][]string, testToSource map[string]string, testKinds map[string]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM tests"); err != nil {
		return fmt.Errorf("clear tests: %w", err)
	}

	if len(testToSource) > 0 {
		stmt, err := tx.Prepare("INSERT INTO tests (test_path, source_path, kind) VALUES (?, ?, ?)")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for testPath, sourcePath := range testToSource {
			kind := testKinds[testPath]
			if kind == "" {
				kind = "unit"
			}
			if _, err := stmt.Exec(testPath, sourcePath, kind); err != nil {
				return fmt.Errorf("insert test %s: %w", testPath, err)
			}
		}
	}

	return tx.Commit()
}

// SaveCoChange replaces all co-change and churn data.
func (s *Store) SaveCoChange(pairs map[string]map[string]int, churn map[string]int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM cochange"); err != nil {
		return fmt.Errorf("clear cochange: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM churn"); err != nil {
		return fmt.Errorf("clear churn: %w", err)
	}

	if len(pairs) > 0 {
		ccStmt, err := tx.Prepare("INSERT INTO cochange (file_a, file_b, count) VALUES (?, ?, ?)")
		if err != nil {
			return err
		}
		defer ccStmt.Close()
		for a, bs := range pairs {
			for b, count := range bs {
				if _, err := ccStmt.Exec(a, b, count); err != nil {
					return fmt.Errorf("insert cochange %s/%s: %w", a, b, err)
				}
			}
		}
	}

	if len(churn) > 0 {
		churnStmt, err := tx.Prepare("INSERT INTO churn (file_path, commits) VALUES (?, ?)")
		if err != nil {
			return err
		}
		defer churnStmt.Close()
		for path, commits := range churn {
			if _, err := churnStmt.Exec(path, commits); err != nil {
				return fmt.Errorf("insert churn %s: %w", path, err)
			}
		}
	}

	return tx.Commit()
}

// insertSymbolSQL upserts on the (file_path, name, kind, line) unique key: a
// re-indexed identical definition refreshes its signature instead of failing.
const insertSymbolSQL = "INSERT OR REPLACE INTO symbols (file_path, name, kind, line, signature, extractor) VALUES (?, ?, ?, ?, ?, ?)"

// UpdateSymbols deletes old symbols for given files and inserts new ones.
//
// The delete and the insert must succeed or fail together. Historically the
// delete's error was discarded, so a busy database dropped the DELETE on the
// floor and the INSERTs appended a second copy of every symbol in the file —
// silently, with a zero exit code.
func (s *Store) UpdateSymbols(symbols []index.Symbol, removedFiles []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	delStmt, err := tx.Prepare("DELETE FROM symbols WHERE file_path=?")
	if err != nil {
		return err
	}
	defer delStmt.Close()

	// Collect files that have new symbols
	changedFiles := make(map[string]bool)
	for i := range symbols {
		changedFiles[symbols[i].File] = true
	}
	for f := range changedFiles {
		if _, err := delStmt.Exec(f); err != nil {
			return fmt.Errorf("delete symbols for %s: %w", f, err)
		}
	}
	for _, f := range removedFiles {
		if _, err := delStmt.Exec(f); err != nil {
			return fmt.Errorf("delete symbols for %s: %w", f, err)
		}
	}

	if len(symbols) > 0 {
		insStmt, err := tx.Prepare(insertSymbolSQL)
		if err != nil {
			return err
		}
		defer insStmt.Close()
		for i := range symbols {
			sym := &symbols[i]
			if _, err := insStmt.Exec(sym.File, sym.Name, sym.Kind, sym.Line, sym.Signature, sym.Extractor); err != nil {
				return fmt.Errorf("insert symbol %s:%s: %w", sym.File, sym.Name, err)
			}
		}
	}

	return tx.Commit()
}

const (
	insertFileParseSQL = "INSERT OR REPLACE INTO file_parse (rel_path, lang, extractor, status, symbol_count, detail) VALUES (?, ?, ?, ?, ?, ?)"
	fileParseCols      = "rel_path, lang, extractor, status, symbol_count, detail"
)

func scanFileParse(rows *sql.Rows) (index.FileParse, error) {
	var p index.FileParse
	if err := rows.Scan(&p.RelPath, &p.Lang, &p.Extractor, &p.Status, &p.SymbolCount, &p.Detail); err != nil {
		return p, fmt.Errorf("scan file_parse row: %w", err)
	}
	return p, nil
}

// UpdateFileParses upserts per-file parse results and drops the rows for
// deleted files.
//
// This is the incremental counterpart to the file_parse rows written by
// SaveSnapshot, and it must be called from the refresh path as well as the
// rebuild path. file_metrics, nearby_configs and codeowners are all populated
// only on full rebuild and are therefore frozen at the last rebuild; parse
// status must not become the fourth such table, because a stale "ok" is read as
// an all-clear for a file that no longer parses.
//
// rel_path is the primary key, so a re-index replaces in place: unlike symbols
// and references_, this table cannot accumulate duplicates even if a caller
// double-writes.
func (s *Store) UpdateFileParses(parses []index.FileParse, removedFiles []string) error {
	if len(parses) == 0 && len(removedFiles) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if len(removedFiles) > 0 {
		delStmt, err := tx.Prepare("DELETE FROM file_parse WHERE rel_path=?")
		if err != nil {
			return err
		}
		defer delStmt.Close()
		for _, f := range removedFiles {
			if _, err := delStmt.Exec(f); err != nil {
				return fmt.Errorf("delete file_parse for %s: %w", f, err)
			}
		}
	}

	if len(parses) > 0 {
		insStmt, err := tx.Prepare(insertFileParseSQL)
		if err != nil {
			return err
		}
		defer insStmt.Close()
		for i := range parses {
			p := &parses[i]
			if _, err := insStmt.Exec(p.RelPath, p.Lang, p.Extractor, p.Status, p.SymbolCount, p.Detail); err != nil {
				return fmt.Errorf("upsert file_parse %s: %w", p.RelPath, err)
			}
		}
	}

	return tx.Commit()
}

// GetFileParses returns the stored parse result for every indexed file, keyed
// by path. Files that produced no symbols are present too — that is the point:
// "no symbols" must be distinguishable from "not parsed".
func (s *Store) GetFileParses() (map[string]index.FileParse, error) {
	rows, err := s.db.Query("SELECT " + fileParseCols + " FROM file_parse")
	if err != nil {
		return nil, fmt.Errorf("query file_parse: %w", err)
	}
	defer rows.Close()

	out := make(map[string]index.FileParse, 4096)
	for rows.Next() {
		p, err := scanFileParse(rows)
		if err != nil {
			return nil, err
		}
		out[p.RelPath] = p
	}
	return out, rows.Err()
}

const (
	insertImportStatsSQL = "INSERT OR REPLACE INTO import_stats (rel_path, lang, extracted, resolved, external, unresolved, unresolved_specs) VALUES (?, ?, ?, ?, ?, ?, ?)"
	importStatsCols      = "rel_path, lang, extracted, resolved, external, unresolved, unresolved_specs"

	// maxStoredUnresolvedSpecs bounds the sample actually written, independent of
	// whatever bound the producer applied. The samples are diagnostic breadcrumbs
	// for a human, not data anything computes on, so a defect or a future change
	// upstream must not be able to grow one row into an unbounded blob. Matches
	// the current producer's cap, so in practice nothing is ever dropped here.
	maxStoredUnresolvedSpecs = 20
)

// encodeUnresolvedSpecs renders the specifier sample as a JSON array.
//
// JSON in one TEXT column rather than a child table: the samples are a bounded,
// ordered, read-as-a-whole blob with no independent identity, so a second table
// would buy a join and a second delete path for nothing. Empty stays the empty
// string so the common case (nothing unresolved) costs no bytes and no parse.
func encodeUnresolvedSpecs(specs []string) (string, error) {
	if len(specs) == 0 {
		return "", nil
	}
	if len(specs) > maxStoredUnresolvedSpecs {
		specs = specs[:maxStoredUnresolvedSpecs]
	}
	b, err := json.Marshal(specs)
	if err != nil {
		return "", fmt.Errorf("encode unresolved specs: %w", err)
	}
	return string(b), nil
}

// decodeUnresolvedSpecs is the inverse. Malformed JSON is not fatal: the counts
// in the same row are the load-bearing signal, and losing a diagnostic sample is
// a far better outcome than refusing to load the cache.
func decodeUnresolvedSpecs(raw string) []string {
	if raw == "" {
		return nil
	}
	var specs []string
	if err := json.Unmarshal([]byte(raw), &specs); err != nil {
		return nil
	}
	return specs
}

// execImportStats writes one row through an already-prepared statement.
func execImportStats(stmt *sql.Stmt, path string, st index.ImportStats) error {
	specs, err := encodeUnresolvedSpecs(st.UnresolvedSpecs)
	if err != nil {
		return fmt.Errorf("import_stats %s: %w", path, err)
	}
	if _, err := stmt.Exec(path, st.Lang, st.Extracted, st.Resolved, st.External, st.Unresolved, specs); err != nil {
		return fmt.Errorf("upsert import_stats %s: %w", path, err)
	}
	return nil
}

func scanImportStats(rows *sql.Rows) (string, index.ImportStats, error) {
	var path, specs string
	var st index.ImportStats
	if err := rows.Scan(&path, &st.Lang, &st.Extracted, &st.Resolved, &st.External, &st.Unresolved, &specs); err != nil {
		return "", st, fmt.Errorf("scan import_stats row: %w", err)
	}
	st.UnresolvedSpecs = decodeUnresolvedSpecs(specs)
	return path, st, nil
}

// UpdateImportStats upserts per-file import telemetry and drops the rows for
// deleted files.
//
// This is the incremental counterpart to the import_stats rows written by
// SaveSnapshot, and it must be called from the refresh path too. Every run after
// the first is served from cache, so telemetry that only the full-rebuild path
// writes is telemetry nobody ever sees — the same way parse status was invisible
// before file_parse got its own incremental writer.
//
// removedFiles must be the same list passed to UpdateSymbols/UpdateFileParses;
// otherwise a deleted file keeps a row claiming its imports were resolved.
//
// rel_path is the primary key, so a re-scan replaces in place and a repeated
// write cannot accumulate duplicates.
func (s *Store) UpdateImportStats(stats map[string]index.ImportStats, removedFiles []string) error {
	if len(stats) == 0 && len(removedFiles) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if len(removedFiles) > 0 {
		delStmt, err := tx.Prepare("DELETE FROM import_stats WHERE rel_path=?")
		if err != nil {
			return err
		}
		defer delStmt.Close()
		for _, f := range removedFiles {
			if _, err := delStmt.Exec(f); err != nil {
				return fmt.Errorf("delete import_stats for %s: %w", f, err)
			}
		}
	}

	if len(stats) > 0 {
		insStmt, err := tx.Prepare(insertImportStatsSQL)
		if err != nil {
			return err
		}
		defer insStmt.Close()
		for path, st := range stats {
			if err := execImportStats(insStmt, path, st); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

// GetImportStats returns the stored import telemetry for every file that had
// import specifiers extracted, keyed by path. Files whose specifiers produced no
// edges are present too — that is the point: "imports nothing" must be
// distinguishable from "imports were not understood".
func (s *Store) GetImportStats() (map[string]index.ImportStats, error) {
	rows, err := s.db.Query("SELECT " + importStatsCols + " FROM import_stats")
	if err != nil {
		return nil, fmt.Errorf("query import_stats: %w", err)
	}
	defer rows.Close()

	out := make(map[string]index.ImportStats, 4096)
	for rows.Next() {
		path, st, err := scanImportStats(rows)
		if err != nil {
			return nil, err
		}
		out[path] = st
	}
	return out, rows.Err()
}

// UpdateReferences deletes old references for given files and inserts new ones.
func (s *Store) UpdateReferences(refs []index.Reference, removedFiles []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	delStmt, err := tx.Prepare("DELETE FROM references_ WHERE file_path=?")
	if err != nil {
		return err
	}
	defer delStmt.Close()

	// Collect files that have new references.
	changedFiles := make(map[string]bool)
	for i := range refs {
		changedFiles[refs[i].File] = true
	}
	for f := range changedFiles {
		if _, err := delStmt.Exec(f); err != nil {
			return fmt.Errorf("delete references for %s: %w", f, err)
		}
	}
	for _, f := range removedFiles {
		if _, err := delStmt.Exec(f); err != nil {
			return fmt.Errorf("delete references for %s: %w", f, err)
		}
	}

	if len(refs) > 0 {
		insStmt, err := tx.Prepare("INSERT INTO references_ (name, file_path, line) VALUES (?, ?, ?)")
		if err != nil {
			return err
		}
		defer insStmt.Close()
		for i := range refs {
			r := &refs[i]
			if _, err := insStmt.Exec(r.Name, r.File, r.Line); err != nil {
				return fmt.Errorf("insert reference %s:%s: %w", r.File, r.Name, err)
			}
		}
	}

	return tx.Commit()
}

// UpdateContextDocs deletes old context docs whose origin is one of the
// changed/removed files and inserts the freshly extracted ones. Docs are keyed
// by origin_path (the file the doc text lives in — a source file for comment
// docs, a .context/*.md file for sidecars) so editing either kind of file
// replaces exactly its own docs.
func (s *Store) UpdateContextDocs(docs []index.ContextDoc, changedOrigins []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	delStmt, err := tx.Prepare("DELETE FROM context_docs WHERE origin_path=?")
	if err != nil {
		return err
	}
	defer delStmt.Close()

	origins := make(map[string]bool)
	for i := range docs {
		origins[docs[i].Origin] = true
	}
	for _, o := range changedOrigins {
		origins[o] = true
	}
	for o := range origins {
		if _, err := delStmt.Exec(o); err != nil {
			return fmt.Errorf("delete context_docs for %s: %w", o, err)
		}
	}

	if len(docs) > 0 {
		insStmt, err := tx.Prepare("INSERT INTO context_docs (file_path, symbol, line, source, origin_path, body) VALUES (?, ?, ?, ?, ?, ?)")
		if err != nil {
			return err
		}
		defer insStmt.Close()
		for i := range docs {
			d := &docs[i]
			if _, err := insStmt.Exec(d.File, d.Symbol, d.Line, d.Source, d.Origin, d.Body); err != nil {
				return fmt.Errorf("insert context_doc %s: %w", d.Origin, err)
			}
		}
	}

	return tx.Commit()
}

// UpdateFileExtras upserts file extras and removes deleted files.
func (s *Store) UpdateFileExtras(extras []index.FileExtra, removedFiles []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if len(removedFiles) > 0 {
		delStmt, err := tx.Prepare("DELETE FROM file_extras WHERE rel_path=?")
		if err != nil {
			return err
		}
		defer delStmt.Close()
		for _, f := range removedFiles {
			if _, err := delStmt.Exec(f); err != nil {
				return fmt.Errorf("delete file_extras for %s: %w", f, err)
			}
		}
	}

	if len(extras) > 0 {
		stmt, err := tx.Prepare("INSERT OR REPLACE INTO file_extras (rel_path, preview, content_hash) VALUES (?, ?, ?)")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i := range extras {
			e := &extras[i]
			if _, err := stmt.Exec(e.RelPath, e.Preview, e.ContentHash); err != nil {
				return fmt.Errorf("upsert file_extra %s: %w", e.RelPath, err)
			}
		}
	}

	return tx.Commit()
}

// Clear closes the store and removes the cache directory.
func (s *Store) Clear() error {
	closeErr := s.db.Close()
	return errors.Join(closeErr, os.RemoveAll(s.cacheDir))
}
