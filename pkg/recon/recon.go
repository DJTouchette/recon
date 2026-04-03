package recon

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/djtouchette/recon/internal/cache"
	"github.com/djtouchette/recon/internal/detect"
	gitpkg "github.com/djtouchette/recon/internal/git"
	"github.com/djtouchette/recon/internal/index"
	"github.com/djtouchette/recon/internal/relate"
	"github.com/djtouchette/recon/internal/scan"
)

// Recon is the main entry point for repo intelligence.
type Recon struct {
	root      string
	store     *cache.Store
	idx       *index.FileIndex
	deps      *index.DepGraph
	tests     *index.TestMap
	symbols   *index.SymbolIndex
	extras    map[string]*index.FileExtra
	metrics   *index.MetricsIndex
	nearby    *index.NearbyIndex
	ownership *index.Ownership
	cochange  *gitpkg.CoChange
	isGit     bool
}

// New creates a Recon instance rooted at the given directory.
// It loads from cache when fresh, refreshes when HEAD changed, or rebuilds from scratch.
func New(root string) (*Recon, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}

	store, err := cache.Open(absRoot)
	if err != nil {
		// Can't open DB — rebuild without persistent cache
		r := &Recon{root: absRoot, isGit: gitpkg.IsGitRepo(absRoot)}
		return r, r.rebuildNoPersist()
	}

	r := &Recon{
		root:  absRoot,
		store: store,
		isGit: gitpkg.IsGitRepo(absRoot),
	}

	reason := cache.CheckStaleness(store)

	switch {
	case reason == cache.NotStale:
		// Cache is fresh — load from DB
		if err := r.loadFromCache(); err != nil {
			return r, r.Rebuild()
		}
		return r, nil

	case reason.NeedsRebuild():
		// No cache data — full rebuild
		return r, r.Rebuild()

	default:
		// HEAD or key file changed — refresh
		if err := r.Refresh(); err != nil {
			return r, r.Rebuild()
		}
		return r, nil
	}
}

// Close releases the database connection.
func (r *Recon) Close() error {
	if r.store != nil {
		return r.store.Close()
	}
	return nil
}

// Overview returns a structured summary of the repo.
func (r *Recon) Overview() (*Overview, error) {
	languages, frameworks, entrypoints := detect.DetectAll(r.idx, r.root)

	var langs []Language
	for _, l := range languages {
		langs = append(langs, Language{
			Name:       l.Name,
			FileCount:  l.FileCount,
			Percentage: l.Percentage,
			Extensions: l.Extensions,
		})
	}

	var fws []Framework
	for _, f := range frameworks {
		fws = append(fws, Framework{
			Name:     f.Name,
			Language: f.Language,
			Evidence: f.Evidence,
		})
	}

	var eps []Entrypoint
	for _, e := range entrypoints {
		eps = append(eps, Entrypoint{Path: e.Path, Kind: e.Kind})
	}

	dirs := r.idx.TopDirs()
	var structure []DirectoryInfo
	for _, d := range dirs {
		structure = append(structure, DirectoryInfo{
			Path:      d.Path,
			FileCount: d.FileCount,
			Languages: d.Languages,
			Purpose:   d.Purpose,
		})
	}

	return &Overview{
		Root:        r.root,
		Languages:   langs,
		Frameworks:  fws,
		Structure:   structure,
		Entrypoints: eps,
		FileCount:   r.idx.Len(),
		TestCount:   len(r.idx.ByClass(scan.ClassTest)),
	}, nil
}

// Related returns files related to the given path, ranked by relevance.
func (r *Recon) Related(path string, opts ...RelatedOption) ([]RelatedFile, error) {
	cfg := &relatedConfig{maxResults: 20}
	for _, o := range opts {
		o(cfg)
	}

	path = filepath.Clean(path)

	results := relate.FindRelated(path, r.idx, r.deps, r.tests, r.cochange, cfg.maxResults)

	var out []RelatedFile
	for _, rf := range results {
		out = append(out, RelatedFile{
			Path:    rf.Path,
			Score:   rf.Score,
			Signals: rf.Signals,
		})
	}
	return out, nil
}

// Context returns the full operational context for a file: preview, hash, owners, metrics, nearby configs.
func (r *Recon) Context(path string) (*FileContext, error) {
	path = filepath.Clean(path)
	ctx := &FileContext{Path: path}

	if e, ok := r.extras[path]; ok {
		ctx.Preview = e.Preview
		ctx.ContentHash = e.ContentHash
	}

	if m := r.metrics.Get(path); m != nil {
		ctx.FanIn = m.FanIn
		ctx.FanOut = m.FanOut
		ctx.Churn = m.Churn
		ctx.HotspotScore = m.HotspotScore
	}

	ctx.Owners = r.ownership.OwnersOf(path)

	configs := r.nearby.ForFile(path)
	if len(configs) > 0 {
		ctx.NearbyConfigs = make(map[string]string, len(configs))
		for _, c := range configs {
			ctx.NearbyConfigs[c.ConfigType] = c.ConfigPath
		}
	}

	return ctx, nil
}

// Hotspots returns the top N files ranked by hotspot score (fan-in * churn).
func (r *Recon) Hotspots(n int) ([]HotspotInfo, error) {
	spots := r.metrics.Hotspots(n)
	var out []HotspotInfo
	for _, m := range spots {
		out = append(out, HotspotInfo{
			Path:         m.RelPath,
			FanIn:        m.FanIn,
			FanOut:       m.FanOut,
			Churn:        m.Churn,
			HotspotScore: m.HotspotScore,
		})
	}
	return out, nil
}

// Symbols returns symbols matching the query. If query is empty, returns all symbols.
// If query starts with "file:", returns symbols for that specific file.
func (r *Recon) Symbols(query string) ([]SymbolInfo, error) {
	var syms []index.Symbol

	if strings.HasPrefix(query, "file:") {
		filePath := filepath.Clean(strings.TrimPrefix(query, "file:"))
		syms = r.symbols.ForFile(filePath)
	} else if query == "" {
		syms = r.symbols.All()
	} else {
		syms = r.symbols.Search(query)
	}

	var out []SymbolInfo
	for _, s := range syms {
		out = append(out, SymbolInfo{
			File:      s.File,
			Name:      s.Name,
			Kind:      s.Kind,
			Line:      s.Line,
			Signature: s.Signature,
		})
	}
	return out, nil
}

// FileDetail returns preview and content hash for a file.
func (r *Recon) FileDetail(path string) (*FileDetail, error) {
	path = filepath.Clean(path)
	if e, ok := r.extras[path]; ok {
		return &FileDetail{
			Path:        path,
			Preview:     e.Preview,
			ContentHash: e.ContentHash,
		}, nil
	}
	return &FileDetail{Path: path}, nil
}

// Tests returns test files relevant to the given path.
func (r *Recon) Tests(path string) ([]TestFile, error) {
	path = filepath.Clean(path)

	testPaths := r.tests.TestsFor(path)

	// If path is a directory, find tests for all source files in it
	if len(testPaths) == 0 {
		for _, f := range r.idx.FilesInDir(path) {
			if f.Class == scan.ClassSource {
				testPaths = append(testPaths, r.tests.TestsFor(f.RelPath)...)
			}
		}
	}

	var out []TestFile
	seen := make(map[string]bool)
	for _, tp := range testPaths {
		if seen[tp] {
			continue
		}
		seen[tp] = true
		out = append(out, TestFile{
			Path:    tp,
			Kind:    index.ClassifyTestKind(tp),
			ForFile: r.tests.SourceFor(tp),
		})
	}
	return out, nil
}

// RecentChanges returns a summary of recent git activity.
func (r *Recon) RecentChanges(since string) ([]ChangeSet, error) {
	if !r.isGit {
		return nil, fmt.Errorf("not a git repository")
	}

	commits, err := gitpkg.RecentChanges(r.root, since)
	if err != nil {
		return nil, err
	}

	var out []ChangeSet
	for _, c := range commits {
		out = append(out, ChangeSet{
			Hash:    c.Hash,
			Author:  c.Author,
			Date:    c.Date,
			Message: c.Message,
			Files:   c.Files,
			Areas:   gitpkg.AreasFromFiles(c.Files),
		})
	}
	return out, nil
}

// Rebuild does a full rescan from scratch and persists to cache.
func (r *Recon) Rebuild() error {
	// Walk the filesystem
	walkResult, err := scan.Walk(r.root)
	if err != nil {
		return fmt.Errorf("walk: %w", err)
	}

	// Build all in-memory indexes
	r.idx = index.NewFileIndex(walkResult.Files)
	r.tests = index.NewTestMap(r.idx)
	r.deps = index.NewDepGraph(r.root, r.idx)
	r.symbols = index.NewSymbolIndex(r.root, r.idx)
	r.buildExtrasMap(index.ExtractFileExtras(r.root, r.idx))
	r.nearby = index.NewNearbyIndex(index.FindNearbyConfigs(r.root, r.idx))
	r.ownership = index.ParseCodeowners(r.root)

	if r.isGit {
		commits, err := gitpkg.ParseLog(r.root, 500)
		if err == nil && len(commits) > 0 {
			r.cochange = gitpkg.NewCoChange(commits)
		}
	}

	r.metrics = index.NewMetricsIndex(index.ComputeMetrics(r.deps, r.cochange))

	// Persist to SQLite
	if r.store != nil {
		snap := r.toSnapshot(walkResult.Files)
		if err := r.store.SaveSnapshot(snap); err != nil {
			return fmt.Errorf("save snapshot: %w", err)
		}
		r.saveMeta()
	}

	return nil
}

// Refresh does an incremental update — walks the tree, diffs mtimes, only re-scans changed files.
func (r *Recon) Refresh() error {
	if r.store == nil {
		return r.Rebuild()
	}

	// Walk the current file tree
	walkResult, err := scan.Walk(r.root)
	if err != nil {
		return fmt.Errorf("walk: %w", err)
	}

	// Get stored mtimes from DB
	storedMtimes, err := r.store.GetFileMtimes()
	if err != nil {
		return r.Rebuild()
	}

	// Diff: find added, changed, removed
	var upsert []scan.FileEntry
	var remove []string
	var changedSourceFiles []*scan.FileEntry
	currentPaths := make(map[string]bool, len(walkResult.Files))

	for i := range walkResult.Files {
		f := &walkResult.Files[i]
		currentPaths[f.RelPath] = true
		storedMtime, exists := storedMtimes[f.RelPath]
		if !exists || f.ModTime != storedMtime {
			upsert = append(upsert, *f)
			if f.Class == scan.ClassSource {
				changedSourceFiles = append(changedSourceFiles, f)
			}
		}
	}
	for path := range storedMtimes {
		if !currentPaths[path] {
			remove = append(remove, path)
		}
	}

	// Build new file index from the current walk (always up-to-date)
	r.idx = index.NewFileIndex(walkResult.Files)

	// Update files in DB
	if len(upsert) > 0 || len(remove) > 0 {
		if err := r.store.UpdateFiles(upsert, remove); err != nil {
			return r.Rebuild()
		}
	}

	// Re-scan imports only for changed/added source files
	changedPaths := make([]string, 0, len(changedSourceFiles)+len(remove))
	for _, f := range changedSourceFiles {
		changedPaths = append(changedPaths, f.RelPath)
	}
	changedPaths = append(changedPaths, remove...)

	if len(changedSourceFiles) > 0 {
		newImports := index.ScanFileImports(r.root, changedSourceFiles, r.idx)
		if err := r.store.UpdateImports(newImports, changedPaths); err != nil {
			return r.Rebuild()
		}
	} else if len(remove) > 0 {
		if err := r.store.UpdateImports(nil, remove); err != nil {
			return r.Rebuild()
		}
	}

	// Re-derive test map from current file index (cheap, no I/O)
	r.tests = index.NewTestMap(r.idx)
	testKinds := make(map[string]string)
	for testPath := range r.tests.TestToSourceMap() {
		testKinds[testPath] = index.ClassifyTestKind(testPath)
	}
	r.store.SaveTests(r.tests.AllMappings(), r.tests.TestToSourceMap(), testKinds)

	// Re-extract symbols and extras for changed/added source files
	if len(changedSourceFiles) > 0 {
		newSymbols := index.ScanFileSymbols(r.root, changedSourceFiles)
		r.store.UpdateSymbols(newSymbols, changedPaths)
		newExtras := index.ExtractFileExtrasForPaths(r.root, changedSourceFiles)
		r.store.UpdateFileExtras(newExtras, changedPaths)
	} else if len(remove) > 0 {
		r.store.UpdateSymbols(nil, remove)
		r.store.UpdateFileExtras(nil, remove)
	}

	// Re-parse git if HEAD changed
	storedHead, _ := r.store.GetMeta("head_sha")
	currentHead := gitpkg.GetHEAD(r.root)
	if r.isGit && currentHead != storedHead {
		commits, err := gitpkg.ParseLog(r.root, 500)
		if err == nil && len(commits) > 0 {
			r.cochange = gitpkg.NewCoChange(commits)
			r.store.SaveCoChange(r.cochange.AllPairs(), r.cochange.AllChurn())
		}
	}

	// Load full snapshot from DB (all updates applied)
	snap, err := r.store.LoadSnapshot()
	if err != nil {
		return r.Rebuild()
	}

	r.deps = index.NewDepGraphFromData(snap.Imports)
	r.symbols = index.NewSymbolIndexFromData(snap.Symbols)
	r.buildExtrasMap(snap.FileExtras)
	r.metrics = index.NewMetricsIndex(snap.Metrics)
	r.nearby = index.NewNearbyIndex(snap.NearbyConfigs)
	r.ownership = index.NewOwnershipFromData(snap.OwnerRules)
	if r.cochange == nil {
		r.cochange = gitpkg.NewCoChangeFromData(snap.CoChangePairs, snap.Churn)
	}

	// Update meta
	r.saveMeta()

	return nil
}

// --- internal helpers ---

// loadFromCache loads all data from the SQLite cache into memory.
func (r *Recon) loadFromCache() error {
	snap, err := r.store.LoadSnapshot()
	if err != nil {
		return err
	}

	r.idx = index.NewFileIndex(snap.Files)
	r.deps = index.NewDepGraphFromData(snap.Imports)
	r.tests = index.NewTestMapFromData(snap.SourceToTest, snap.TestToSource)
	r.cochange = gitpkg.NewCoChangeFromData(snap.CoChangePairs, snap.Churn)
	r.symbols = index.NewSymbolIndexFromData(snap.Symbols)
	r.buildExtrasMap(snap.FileExtras)
	r.metrics = index.NewMetricsIndex(snap.Metrics)
	r.nearby = index.NewNearbyIndex(snap.NearbyConfigs)
	r.ownership = index.NewOwnershipFromData(snap.OwnerRules)

	return nil
}

// rebuildNoPersist does a full rebuild without saving to cache (fallback when DB fails).
func (r *Recon) rebuildNoPersist() error {
	walkResult, err := scan.Walk(r.root)
	if err != nil {
		return fmt.Errorf("walk: %w", err)
	}

	r.idx = index.NewFileIndex(walkResult.Files)
	r.tests = index.NewTestMap(r.idx)
	r.deps = index.NewDepGraph(r.root, r.idx)
	r.symbols = index.NewSymbolIndex(r.root, r.idx)
	r.buildExtrasMap(index.ExtractFileExtras(r.root, r.idx))
	r.nearby = index.NewNearbyIndex(index.FindNearbyConfigs(r.root, r.idx))
	r.ownership = index.ParseCodeowners(r.root)

	if r.isGit {
		commits, err := gitpkg.ParseLog(r.root, 500)
		if err == nil && len(commits) > 0 {
			r.cochange = gitpkg.NewCoChange(commits)
		}
	}
	r.metrics = index.NewMetricsIndex(index.ComputeMetrics(r.deps, r.cochange))
	return nil
}

// toSnapshot extracts all in-memory data into a Snapshot for persistence.
func (r *Recon) toSnapshot(files []scan.FileEntry) *cache.Snapshot {
	snap := &cache.Snapshot{
		Files:         files,
		Imports:       r.deps.AllImports(),
		SourceToTest:  r.tests.AllMappings(),
		TestToSource:  r.tests.TestToSourceMap(),
		TestKinds:     make(map[string]string),
		CoChangePairs: nil,
		Churn:         nil,
		Symbols:       r.symbols.All(),
		Metrics:       r.metrics.All(),
		NearbyConfigs: r.nearby.All(),
		OwnerRules:    r.ownership.Rules(),
	}

	// Build file extras list from map
	for _, e := range r.extras {
		snap.FileExtras = append(snap.FileExtras, *e)
	}

	for testPath := range snap.TestToSource {
		snap.TestKinds[testPath] = index.ClassifyTestKind(testPath)
	}

	if r.cochange != nil {
		snap.CoChangePairs = r.cochange.AllPairs()
		snap.Churn = r.cochange.AllChurn()
	}

	return snap
}

// buildExtrasMap converts a slice of FileExtra into the lookup map.
func (r *Recon) buildExtrasMap(extras []index.FileExtra) {
	r.extras = make(map[string]*index.FileExtra, len(extras))
	for i := range extras {
		r.extras[extras[i].RelPath] = &extras[i]
	}
}

// saveMeta writes HEAD sha, file count, scan time, and key file mtimes.
func (r *Recon) saveMeta() {
	if r.store == nil {
		return
	}
	r.store.SetMeta("head_sha", gitpkg.GetHEAD(r.root))
	r.store.SetMeta("file_count", strconv.Itoa(r.idx.Len()))
	r.store.SetMeta("scan_time", time.Now().Format(time.RFC3339))
	cache.SaveKeyFileMtimes(r.store)
}
