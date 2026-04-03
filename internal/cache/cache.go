package cache

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/scan"
	_ "modernc.org/sqlite"
)

const (
	cacheDir   = ".recon"
	dbFile     = "recon.db"
	schemaVer  = 2
)

// Snapshot holds all indexed data for save/load.
type Snapshot struct {
	Files         []scan.FileEntry
	Imports       map[string][]string // source → targets
	SourceToTest  map[string][]string // source → test paths
	TestToSource  map[string]string   // test → source path
	TestKinds     map[string]string   // test → kind
	CoChangePairs map[string]map[string]int
	Churn         map[string]int
	Symbols       []index.Symbol
	FileExtras    []index.FileExtra
}

// Store manages the SQLite cache database.
type Store struct {
	db   *sql.DB
	Root string
	path string
}

// Open creates or opens the cache database.
func Open(root string) (*Store, error) {
	dir := filepath.Join(root, cacheDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}

	dbPath := filepath.Join(dir, dbFile)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// Performance tuning
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=-64000",
		"PRAGMA temp_store=MEMORY",
		"PRAGMA mmap_size=268435456",
	} {
		db.Exec(pragma)
	}

	s := &Store{db: db, Root: root, path: dbPath}
	if err := s.ensureSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}

	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *Store) ensureSchema() error {
	var versionStr string
	err := s.db.QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&versionStr)
	if err == nil {
		v, _ := strconv.Atoi(versionStr)
		if v == schemaVer {
			return nil
		}
	}

	// Drop and recreate all tables
	for _, table := range []string{"meta", "files", "imports", "tests", "cochange", "churn", "symbols", "file_extras"} {
		s.db.Exec("DROP TABLE IF EXISTS " + table)
	}

	_, err = s.db.Exec(schema)
	if err != nil {
		return err
	}

	_, err = s.db.Exec("INSERT INTO meta (key, value) VALUES ('schema_version', ?)", strconv.Itoa(schemaVer))
	return err
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
CREATE TABLE symbols (
	file_path TEXT NOT NULL,
	name TEXT NOT NULL,
	kind TEXT NOT NULL,
	line INTEGER NOT NULL DEFAULT 0,
	signature TEXT NOT NULL DEFAULT ''
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
CREATE INDEX idx_symbols_file ON symbols(file_path);
CREATE INDEX idx_symbols_name ON symbols(name);
CREATE INDEX idx_symbols_kind ON symbols(kind);
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
	for _, table := range []string{"files", "imports", "tests", "cochange", "churn", "symbols", "file_extras"} {
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
				importStmt.Exec(src, target)
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
			testStmt.Exec(testPath, sourcePath, kind)
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
				ccStmt.Exec(a, b, count)
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
			churnStmt.Exec(path, commits)
		}
	}

	// --- Symbols ---
	if len(snap.Symbols) > 0 {
		symStmt, err := tx.Prepare("INSERT INTO symbols (file_path, name, kind, line, signature) VALUES (?, ?, ?, ?, ?)")
		if err != nil {
			return fmt.Errorf("prepare symbols: %w", err)
		}
		defer symStmt.Close()

		for i := range snap.Symbols {
			s := &snap.Symbols[i]
			symStmt.Exec(s.File, s.Name, s.Kind, s.Line, s.Signature)
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
			extraStmt.Exec(e.RelPath, e.Preview, e.ContentHash)
		}
	}

	return tx.Commit()
}

// --- Full load ---

// LoadSnapshot reads all indexed data from the database.
func (s *Store) LoadSnapshot() (*Snapshot, error) {
	snap := &Snapshot{
		Imports:       make(map[string][]string),
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
	rows, err = s.db.Query("SELECT file_path, name, kind, line, signature FROM symbols")
	if err != nil {
		return nil, fmt.Errorf("query symbols: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var sym index.Symbol
		if err := rows.Scan(&sym.File, &sym.Name, &sym.Kind, &sym.Line, &sym.Signature); err != nil {
			return nil, fmt.Errorf("scan symbol row: %w", err)
		}
		snap.Symbols = append(snap.Symbols, sym)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("symbol rows: %w", err)
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
			delStmt.Exec(p)
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
			stmt.Exec(f.RelPath, dir, f.Lang, int(f.Class), f.Size, f.ModTime)
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

	// Delete imports for all changed/removed sources
	for _, src := range removedSources {
		delStmt.Exec(src)
	}
	for src := range newImports {
		delStmt.Exec(src)
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
				insStmt.Exec(src, target)
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

	tx.Exec("DELETE FROM tests")

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
			stmt.Exec(testPath, sourcePath, kind)
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

	tx.Exec("DELETE FROM cochange")
	tx.Exec("DELETE FROM churn")

	if len(pairs) > 0 {
		ccStmt, err := tx.Prepare("INSERT INTO cochange (file_a, file_b, count) VALUES (?, ?, ?)")
		if err != nil {
			return err
		}
		defer ccStmt.Close()
		for a, bs := range pairs {
			for b, count := range bs {
				ccStmt.Exec(a, b, count)
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
			churnStmt.Exec(path, commits)
		}
	}

	return tx.Commit()
}

// UpdateSymbols deletes old symbols for given files and inserts new ones.
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
		delStmt.Exec(f)
	}
	for _, f := range removedFiles {
		delStmt.Exec(f)
	}

	if len(symbols) > 0 {
		insStmt, err := tx.Prepare("INSERT INTO symbols (file_path, name, kind, line, signature) VALUES (?, ?, ?, ?, ?)")
		if err != nil {
			return err
		}
		defer insStmt.Close()
		for i := range symbols {
			s := &symbols[i]
			insStmt.Exec(s.File, s.Name, s.Kind, s.Line, s.Signature)
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
			delStmt.Exec(f)
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
			stmt.Exec(e.RelPath, e.Preview, e.ContentHash)
		}
	}

	return tx.Commit()
}

// Clear removes the cache database file.
func (s *Store) Clear() error {
	s.db.Close()
	return os.RemoveAll(filepath.Join(s.Root, cacheDir))
}
