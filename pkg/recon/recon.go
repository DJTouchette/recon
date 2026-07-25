package recon

import (
	"fmt"
	"path/filepath"
	"sort"
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
	root        string
	store       *cache.Store
	idx         *index.FileIndex
	deps        *index.DepGraph
	tests       *index.TestMap
	symbols     *index.SymbolIndex
	references  *index.ReferenceIndex
	contextDocs *index.ContextDocIndex
	extras      map[string]*index.FileExtra
	metrics     *index.MetricsIndex
	nearby      *index.NearbyIndex
	ownership   *index.Ownership
	cochange    *gitpkg.CoChange
	isGit       bool

	// lastTestStatus records why the most recent Tests() call returned what it
	// did, so the CLI can say "no mapping rules for this file type" instead of
	// "no test files found" — a false negative dressed as a fact.
	lastTestStatus string

	// lastTestSubject is set when the most recent Tests() call was asked about a
	// test file, so the CLI can phrase the answer as "covers" rather than
	// "tested by".
	lastTestSubject string

	// lastTestQueryWasTest records that the query named a test file, so an empty
	// answer can say "this is a test with no single subject" rather than
	// "no tests found", which is a different claim entirely.
	lastTestQueryWasTest bool
}

// LastTestQueryWasTest reports whether the most recent Tests call named a test
// file rather than a source file.
func (r *Recon) LastTestQueryWasTest() bool { return r.lastTestQueryWasTest }

// LastTestSubject reports the source file the most recent Tests call resolved a
// test to, or "" when the query was not about a test file.
func (r *Recon) LastTestSubject() string { return r.lastTestSubject }

// LastTestStatus reports the outcome of the most recent Tests call:
// "mapped", "no_match", or "unsupported".
func (r *Recon) LastTestStatus() string { return r.lastTestStatus }

// Option configures Recon behaviour.
type Option func(*options)

type options struct {
	cacheDir string
}

// WithCacheDir stores the cache in the given directory instead of <root>/.recon/.
func WithCacheDir(dir string) Option {
	return func(o *options) { o.cacheDir = dir }
}

// New creates a Recon instance rooted at the given directory.
// It loads from cache when fresh, refreshes when HEAD changed, or rebuilds from scratch.
func New(root string, opts ...Option) (*Recon, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}

	var store *cache.Store
	if o.cacheDir != "" {
		absCache, _ := filepath.Abs(o.cacheDir)
		store, err = cache.OpenAt(absRoot, absCache)
	} else {
		store, err = cache.Open(absRoot)
	}
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

	if reason.NeedsRebuild() {
		return r, r.Rebuild()
	}

	// Everything else goes through Refresh, including cache.NotStale.
	//
	// CheckStaleness only compares HEAD and the mtimes of a handful of manifest
	// files, so it cannot see an ordinary source edit. Treating "not stale" as
	// "serve the cache untouched" meant a file the caller had just edited was
	// invisible until it was committed: recon would report symbols that had been
	// deleted, and omit ones that had been added, with no way to tell. For an
	// agent editing code that is the normal case, not an edge case.
	//
	// Refresh walks and diffs the tree itself, which is milliseconds on a repo
	// this size, and it re-mines git history only when HEAD actually moved. So
	// the cheap check now decides how much work Refresh does, not whether the
	// working tree is looked at.
	if err := r.Refresh(); err != nil {
		return r, r.Rebuild()
	}
	return r, nil
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
	// Detect (not DetectAll) so the result can say WHY a list is empty.
	// "frameworks: null" previously meant both "this project uses none" and
	// "no detector recognised these languages", and the raw manifest dump that
	// used to be reported as frameworks — including the project's own artifact
	// id — is now separated into dependencies.
	res := detect.Detect(r.idx, r.root)
	languages, frameworks, entrypoints := res.Languages, res.Frameworks, res.Entrypoints

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

	var issues []ManifestIssueInfo
	for _, mi := range res.ManifestIssues {
		affects := make([]string, 0, len(mi.Affects))
		for _, f := range mi.Affects {
			affects = append(affects, string(f))
		}
		issues = append(issues, ManifestIssueInfo{
			Manifest: mi.Manifest,
			Language: mi.Language,
			Reason:   mi.Reason,
			Affects:  affects,
		})
	}

	var deps []Dependency
	for _, d := range res.Dependencies {
		deps = append(deps, Dependency{
			Name:     d.Name,
			Version:  d.Version,
			Language: d.Language,
			Manifest: d.Manifest,
		})
	}

	return &Overview{
		Root:             r.root,
		Languages:        langs,
		Frameworks:       fws,
		Dependencies:     deps,
		Structure:        structure,
		Entrypoints:      eps,
		FileCount:        r.idx.Len(),
		TestCount:        len(r.idx.ByClass(scan.ClassTest)),
		FrameworkStatus:  string(res.FrameworkStatus),
		EntrypointStatus: string(res.EntrypointStatus),
		DependencyStatus: string(res.DependencyStatus),
		ImportCoverage:   r.ImportCoverage(),
		ManifestIssues:   issues,
	}, nil
}

// Related returns files related to the given path, ranked by relevance.
func (r *Recon) Related(path string, opts ...RelatedOption) ([]RelatedFile, error) {
	cfg := &relatedConfig{maxResults: 20}
	for _, o := range opts {
		o(cfg)
	}

	path = filepath.Clean(path)

	results := relate.FindRelated(path, r.idx, r.deps, r.tests, r.cochange, r.metrics, r.ownership, cfg.maxResults)

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

	ctx.Docs = toContextDocInfos(r.contextDocs.ForFile(path))
	ctx.ImportStats = r.ImportStatsFor(path)

	return ctx, nil
}

// Docs returns context docs extracted from rivet:context code comments and
// .context/ sidecar markdown files. Query forms: "" returns all docs,
// "file:<path>" returns docs attached to a file, "symbol:<name>" returns docs
// attached to that exact symbol, and a bare query matches symbol names and
// file paths case-insensitively. maxResults caps output (0 = default 50,
// -1 = unlimited).
func (r *Recon) Docs(query string, maxResults int) ([]ContextDocInfo, error) {
	if maxResults == 0 {
		maxResults = 50
	}

	var docs []index.ContextDoc
	switch {
	case query == "":
		docs = r.contextDocs.All()
	case strings.HasPrefix(query, "file:"):
		docs = r.contextDocs.ForFile(filepath.Clean(strings.TrimPrefix(query, "file:")))
	case strings.HasPrefix(query, "symbol:"):
		docs = r.contextDocs.ForSymbol(strings.TrimPrefix(query, "symbol:"))
	default:
		q := strings.ToLower(query)
		for _, d := range r.contextDocs.All() {
			if strings.Contains(strings.ToLower(d.Symbol), q) ||
				strings.Contains(strings.ToLower(d.File), q) {
				docs = append(docs, d)
			}
		}
	}

	if maxResults > 0 && len(docs) > maxResults {
		docs = docs[:maxResults]
	}
	return toContextDocInfos(docs), nil
}

func toContextDocInfos(docs []index.ContextDoc) []ContextDocInfo {
	if len(docs) == 0 {
		return nil
	}
	out := make([]ContextDocInfo, len(docs))
	for i, d := range docs {
		out[i] = ContextDocInfo{
			File:   d.File,
			Symbol: d.Symbol,
			Line:   d.Line,
			Source: d.Source,
			Origin: d.Origin,
			Body:   d.Body,
		}
	}
	return out
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

// Search performs a unified search across symbol names, file paths, and file previews.
func (r *Recon) Search(query string, maxResults int) ([]SearchResult, error) {
	if maxResults <= 0 {
		maxResults = 30
	}

	results := index.Search(query, r.root, r.idx, r.symbols, r.extras, maxResults)

	var out []SearchResult
	for _, sr := range results {
		res := SearchResult{
			Path:      sr.Path,
			Score:     sr.Score,
			MatchType: sr.MatchType,
			Context:   sr.Context,
		}
		if sr.Symbol != nil {
			res.Symbol = &SymbolInfo{
				File:      sr.Symbol.File,
				Name:      sr.Symbol.Name,
				Kind:      sr.Symbol.Kind,
				Line:      sr.Symbol.Line,
				Signature: sr.Symbol.Signature,
			}
		}
		out = append(out, res)
	}
	return out, nil
}

// Symbols returns symbols matching the query. If query is empty, returns all symbols.
// If query starts with "file:", returns symbols for that specific file.
// maxResults caps output (0 = default 30, -1 = unlimited).
func (r *Recon) Symbols(query string, maxResults int) ([]SymbolInfo, error) {
	if maxResults == 0 {
		maxResults = 30
	}

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
		if maxResults > 0 && len(out) >= maxResults {
			break
		}
		out = append(out, SymbolInfo{
			File:      s.File,
			Name:      s.Name,
			Kind:      s.Kind,
			Line:      s.Line,
			Signature: s.Signature,
			Extractor: s.Extractor,
		})
	}
	return out, nil
}

// Callers finds all call sites referencing the given symbol name and resolves
// them against the symbol definitions. Definitions are the symbols exactly
// named `name`; references are all call sites for that name. A reference is
// marked Resolved when its file plausibly reaches a definition — either it
// imports a definition file, or it sits in the same directory as one — which
// filters out unrelated calls that merely share the name.
func (r *Recon) Callers(name string) CallersResult {
	result := CallersResult{Name: name}
	if name == "" {
		return result
	}

	// Definitions: symbols exactly named `name`.
	defFiles := make(map[string]bool)
	defDirs := make(map[string]bool)
	if r.symbols != nil {
		for _, s := range r.symbols.Exact(name) {
			result.Definitions = append(result.Definitions, SymbolInfo{
				File:      s.File,
				Name:      s.Name,
				Kind:      s.Kind,
				Line:      s.Line,
				Signature: s.Signature,
			})
			defFiles[s.File] = true
			defDirs[filepath.Dir(s.File)] = true
		}
	}

	// References: all call sites for `name`, resolved against definition files.
	if r.references != nil {
		for _, ref := range r.references.ForName(name) {
			result.References = append(result.References, Reference{
				File:     ref.File,
				Line:     ref.Line,
				Resolved: r.referenceResolves(ref.File, defFiles, defDirs),
			})
		}
	}

	return result
}

// referenceResolves reports whether a referencing file plausibly reaches one of
// the definition files: it is itself a definition file, imports one, or shares a
// directory with one.
func (r *Recon) referenceResolves(file string, defFiles, defDirs map[string]bool) bool {
	if len(defFiles) == 0 {
		return false
	}
	if defFiles[file] {
		return true
	}
	if defDirs[filepath.Dir(file)] {
		return true
	}
	if r.deps != nil {
		for _, imp := range r.deps.ImportsOf(file) {
			if defFiles[imp] {
				return true
			}
		}
	}
	return false
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
// maxResults caps output (0 = default 20, -1 = unlimited).
func (r *Recon) Tests(path string, maxResults int) ([]TestFile, error) {
	if maxResults == 0 {
		maxResults = 20
	}
	path = filepath.Clean(path)

	testPaths, status := r.tests.LookupTests(path)

	// Asked about a test file, answer with what it covers. LookupSource has
	// always known this; nothing called it, so pointing at a test returned
	// "No test files found." — a confident no to a question that was never
	// asked. Which direction the caller meant is unambiguous from the file's
	// own class, so there is no reason to make them phrase it differently.
	if len(testPaths) == 0 {
		if f := r.idx.Get(path); f != nil && f.Class == scan.ClassTest {
			r.lastTestQueryWasTest = true
			if src, st := r.tests.LookupSource(path); src != "" {
				r.lastTestStatus = string(st)
				r.lastTestSubject = src
				return []TestFile{{
					Path:    src,
					Kind:    index.ClassifyTestKind(path),
					ForFile: path,
				}}, nil
			} else {
				// A fixture or an end-to-end test genuinely has no single
				// subject. Say that, rather than reporting it as untested.
				r.lastTestStatus = string(st)
				r.lastTestSubject = ""
				return nil, nil
			}
		}
	}

	// If path is a directory, find tests for all source files in it. A
	// directory is not itself a mappable file, so its own status says nothing.
	if len(testPaths) == 0 {
		dirFiles := r.idx.FilesUnderDir(path)
		for _, f := range dirFiles {
			if f.Class == scan.ClassSource {
				found, st := r.tests.LookupTests(f.RelPath)
				testPaths = append(testPaths, found...)
				// A directory counts as supported if any file inside it is.
				if st != index.TestMapUnsupported {
					status = st
				}
			}
		}
		if len(dirFiles) > 0 && len(testPaths) > 0 {
			status = index.TestMapMapped
		}
	}
	r.lastTestStatus = string(status)

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
		if maxResults > 0 && len(out) >= maxResults {
			break
		}
	}
	return out, nil
}

// CaseMode controls how grep handles letter case. See the Case* constants.
type CaseMode = index.CaseMode

const (
	// CaseSmart matches case-sensitively iff the pattern contains an
	// uppercase letter (like ripgrep's --smart-case). The default.
	CaseSmart = index.CaseSmart
	// CaseInsensitive always ignores case.
	CaseInsensitive = index.CaseInsensitive
	// CaseSensitive always respects case.
	CaseSensitive = index.CaseSensitive
)

// GrepOptions configures grep behavior.
type GrepOptions struct {
	MaxFiles   int      // max files to return (default 20)
	TypeFilter string   // filter by match type: "definition", "reference", "test", "comment", or ""
	CaseMode   CaseMode // case handling (default CaseSmart)
}

// Grep searches file content for a pattern and returns results grouped by file.
// The pattern is a Go regular expression; patterns without regex
// metacharacters use a faster literal scan. Matching is smart-case by default
// (case-sensitive iff the pattern has an uppercase letter); override with
// GrepOptions.CaseMode.
// Each match is classified as definition, reference, comment, or test.
// Duplicate text within a file is collapsed with a Similar count.
func (r *Recon) Grep(pattern string, opts GrepOptions) (*GrepResult, error) {
	if pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 20
	}

	raw, err := index.Grep(pattern, r.root, r.idx, r.symbols, r.metrics, opts.CaseMode)
	if err != nil {
		return nil, err
	}

	// Filter by type if requested.
	if opts.TypeFilter != "" {
		var filtered []index.GrepMatch
		for _, m := range raw {
			if m.MatchType == opts.TypeFilter {
				filtered = append(filtered, m)
			}
		}
		raw = filtered
	}

	// Build summary from ALL matches (before file cap).
	summary := GrepSummary{}
	fileMap := make(map[string]*GrepFileResult)
	var order []string

	for _, m := range raw {
		// Count for summary.
		summary.Total++
		switch m.MatchType {
		case "definition":
			summary.Definitions++
		case "reference":
			summary.References++
		case "test":
			summary.Tests++
		case "comment":
			summary.Comments++
		}

		fr, ok := fileMap[m.Path]
		if !ok {
			fr = &GrepFileResult{
				Path:         m.Path,
				FanIn:        m.FanIn,
				HotspotScore: m.HotspotScore,
			}
			fileMap[m.Path] = fr
			order = append(order, m.Path)
		}
		fr.Matches = append(fr.Matches, GrepLine{
			Line:      m.Line,
			Text:      m.Text,
			MatchType: m.MatchType,
		})
	}

	summary.Files = len(order)

	// Collapse duplicate text within each file.
	for _, fr := range fileMap {
		fr.Matches = collapseMatches(fr.Matches)
	}

	// Cap files.
	var files []GrepFileResult
	for _, path := range order {
		if len(files) >= opts.MaxFiles {
			break
		}
		files = append(files, *fileMap[path])
	}
	if len(order) > opts.MaxFiles {
		summary.Truncated = len(order) - opts.MaxFiles
	}

	return &GrepResult{Summary: summary, Files: files}, nil
}

// collapseMatches deduplicates lines with identical text within a file.
// Keeps the first occurrence and sets Similar to the count of duplicates.
func collapseMatches(matches []GrepLine) []GrepLine {
	if len(matches) <= 1 {
		return matches
	}

	type key struct {
		text      string
		matchType string
	}
	seen := make(map[key]int) // key → index in result
	var result []GrepLine

	for _, m := range matches {
		k := key{text: m.Text, matchType: m.MatchType}
		if idx, ok := seen[k]; ok {
			result[idx].Similar++
		} else {
			seen[k] = len(result)
			result = append(result, m)
		}
	}

	return result
}

// ImportedBy returns files that import the given file (reverse dependency edge).
func (r *Recon) ImportedBy(path string) []string {
	if r.deps == nil {
		return nil
	}
	return r.deps.ImportedBy(filepath.Clean(path))
}

// ImportsOf returns files imported by the given file.
func (r *Recon) ImportsOf(path string) []string {
	if r.deps == nil {
		return nil
	}
	return r.deps.ImportsOf(filepath.Clean(path))
}

// CoChangedWith returns files that frequently co-change with the given file.
func (r *Recon) CoChangedWith(path string, minCount int) []CoChangePair {
	if r.cochange == nil {
		return nil
	}
	internal := r.cochange.CoChangedWith(filepath.Clean(path), minCount)
	out := make([]CoChangePair, len(internal))
	for i, p := range internal {
		out[i] = CoChangePair{File: p.File, Count: p.Count}
	}
	return out
}

// IsTestFile returns true if the given path is classified as a test file.
func (r *Recon) IsTestFile(path string) bool {
	f := r.idx.Get(filepath.Clean(path))
	if f == nil {
		return false
	}
	return f.Class == scan.ClassTest
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
	r.references = index.NewReferenceIndex(r.root, r.idx)
	r.contextDocs = index.NewContextDocIndex(r.root, r.idx, r.symbols)
	r.buildExtrasMap(index.ExtractFileExtras(r.root, r.idx))
	r.nearby = index.NewNearbyIndex(index.FindNearbyConfigs(r.root, r.idx))
	r.ownership = index.ParseCodeowners(r.root)

	if r.isGit {
		// Mine carries a Coverage record describing what history was actually
		// available — shallow clone, subdirectory root, window exhausted. That
		// is what lets a caller distinguish "nothing changed" from "I could not
		// read any history", which previously both surfaced as an empty churn
		// map and a null hotspots list.
		if cc, err := gitpkg.Mine(r.root, gitpkg.Options{}, gitpkg.CoChangeOptions{}); err == nil {
			r.cochange = cc
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

	// Compare mtime AND size. Size was already stored and never read, and
	// mtime alone misses every workflow that preserves it while changing
	// content: tar -x, rsync -t, cp -p, Docker layer and CI workspace restores.
	// In those cases recon reported symbols from the previous content, and the
	// size it already had on disk would have caught it.
	storedSigs, err := r.store.GetFileSignatures()
	if err != nil {
		return r.Rebuild()
	}

	// Diff: find added, changed, removed
	var upsert []scan.FileEntry
	var remove []string
	var changedSourceFiles []*scan.FileEntry
	// References are extracted from both source and test files, so refreshing
	// them requires tracking changed test files too.
	var changedRefFiles []*scan.FileEntry
	currentPaths := make(map[string]bool, len(walkResult.Files))

	for i := range walkResult.Files {
		f := &walkResult.Files[i]
		currentPaths[f.RelPath] = true
		sig, exists := storedSigs[f.RelPath]
		if !exists || f.ModTime != sig.ModTime || f.Size != sig.Size {
			upsert = append(upsert, *f)
			if f.Class == scan.ClassSource || f.Class == scan.ClassScript {
				changedSourceFiles = append(changedSourceFiles, f)
			}
			if f.Class == scan.ClassSource || f.Class == scan.ClassTest || f.Class == scan.ClassScript {
				changedRefFiles = append(changedRefFiles, f)
			}
		}
	}
	for path := range storedSigs {
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

	//
	// Telemetry is written alongside the edges it describes. Scanning without it
	// meant a refresh wrote new edges over old unresolved counts, so a file
	// whose imports had started resolving cleanly kept reporting a stale caveat.
	if len(changedSourceFiles) > 0 {
		newImports, newStats := index.ScanFileImportsWithStats(r.root, changedSourceFiles, r.idx)
		if err := r.store.UpdateImports(newImports, changedPaths); err != nil {
			return r.Rebuild()
		}
		if err := r.store.UpdateImportStats(newStats, changedPaths); err != nil {
			return r.Rebuild()
		}
	} else if len(remove) > 0 {
		if err := r.store.UpdateImports(nil, remove); err != nil {
			return r.Rebuild()
		}
		if err := r.store.UpdateImportStats(nil, remove); err != nil {
			return r.Rebuild()
		}
	}

	// Re-derive test map from current file index (cheap, no I/O)
	r.tests = index.NewTestMap(r.idx)
	testKinds := make(map[string]string)
	for testPath := range r.tests.TestToSourceMap() {
		testKinds[testPath] = index.ClassifyTestKind(testPath)
	}
	// Every store write below is checked, and any failure falls back to a full
	// rebuild.
	//
	// These returns used to be discarded, which is what turned a transient
	// SQLite lock into permanent corruption: a busy DELETE was swallowed, the
	// matching INSERTs ran anyway and appended duplicate rows, and then
	// saveMeta() at the end of this function stamped the new HEAD so the cache
	// reported itself current forever after. The store now surfaces those
	// errors; dropping them here would put the bug straight back.
	if err := r.store.SaveTests(r.tests.AllMappings(), r.tests.TestToSourceMap(), testKinds); err != nil {
		return r.Rebuild()
	}

	// Re-extract symbols and extras for changed/added source files
	var newSymbols []index.Symbol
	if len(changedSourceFiles) > 0 {
		// One pass yields both the symbols and the per-file parse record, so a
		// file that parsed badly — or whose language has no extractor at all —
		// still leaves a trace even though it contributes zero symbols.
		var newParses []index.FileParse
		newSymbols, newParses = index.ScanFileParses(r.root, changedSourceFiles)
		if err := r.store.UpdateSymbols(newSymbols, changedPaths); err != nil {
			return r.Rebuild()
		}
		if err := r.store.UpdateFileParses(newParses, changedPaths); err != nil {
			return r.Rebuild()
		}
		newExtras := index.ExtractFileExtrasForPaths(r.root, changedSourceFiles)
		if err := r.store.UpdateFileExtras(newExtras, changedPaths); err != nil {
			return r.Rebuild()
		}
	} else if len(remove) > 0 {
		if err := r.store.UpdateSymbols(nil, remove); err != nil {
			return r.Rebuild()
		}
		if err := r.store.UpdateFileParses(nil, remove); err != nil {
			return r.Rebuild()
		}
		if err := r.store.UpdateFileExtras(nil, remove); err != nil {
			return r.Rebuild()
		}
	}

	// Re-extract references for changed/added source+test files
	if len(changedRefFiles) > 0 {
		refChangedPaths := make([]string, 0, len(changedRefFiles)+len(remove))
		for _, f := range changedRefFiles {
			refChangedPaths = append(refChangedPaths, f.RelPath)
		}
		refChangedPaths = append(refChangedPaths, remove...)
		newRefs := index.ScanFileReferences(r.root, changedRefFiles)
		if err := r.store.UpdateReferences(newRefs, refChangedPaths); err != nil {
			return r.Rebuild()
		}
	} else if len(remove) > 0 {
		if err := r.store.UpdateReferences(nil, remove); err != nil {
			return r.Rebuild()
		}
	}

	// Re-extract context docs for any changed file: comment docs from code
	// files, sidecar docs from .context/*.md. ScanFileContextDocs filters out
	// non-candidates itself; positional symbol attachment only needs symbols
	// from the changed files, which is exactly what newSymbols holds.
	if len(upsert) > 0 || len(remove) > 0 {
		ctxFiles := make([]*scan.FileEntry, len(upsert))
		ctxOrigins := make([]string, 0, len(upsert)+len(remove))
		for i := range upsert {
			ctxFiles[i] = &upsert[i]
			ctxOrigins = append(ctxOrigins, upsert[i].RelPath)
		}
		ctxOrigins = append(ctxOrigins, remove...)
		newDocs := index.ScanFileContextDocs(r.root, ctxFiles, index.NewSymbolIndexFromData(newSymbols), r.idx)
		if err := r.store.UpdateContextDocs(newDocs, ctxOrigins); err != nil {
			return r.Rebuild()
		}
	}

	// Re-parse git if HEAD changed
	storedHead, _ := r.store.GetMeta("head_sha")
	currentHead := gitpkg.GetHEAD(r.root)
	if r.isGit && currentHead != storedHead {
		if cc, err := gitpkg.Mine(r.root, gitpkg.Options{}, gitpkg.CoChangeOptions{}); err == nil {
			// Mine returns a non-nil empty CoChange where the old len(commits)>0
			// guard left r.cochange nil, so an unreadable history now overwrites
			// the previous churn rather than silently keeping stale numbers.
			r.cochange = cc
			if err := r.store.SaveCoChange(r.cochange.AllPairs(), r.cochange.AllChurn()); err != nil {
				return r.Rebuild()
			}
		}
	}

	// Load full snapshot from DB (all updates applied)
	snap, err := r.store.LoadSnapshot()
	if err != nil {
		return r.Rebuild()
	}

	// FromCache, not FromData: the telemetry has to survive the round trip or
	// it only ever describes the run that built the cache.
	r.deps = index.NewDepGraphFromCache(snap.Imports, snap.ImportStats)
	// FromCache, not FromData: the parse records have to come back with the
	// symbols or every cached read reports "clean" for files that failed to
	// parse — and recon serves from cache almost always.
	r.symbols = index.NewSymbolIndexFromCache(snap.Symbols, snap.FileParses)
	r.references = index.NewReferenceIndexFromData(snap.References)
	r.contextDocs = index.NewContextDocIndexFromData(snap.ContextDocs)
	r.buildExtrasMap(snap.FileExtras)
	if r.cochange == nil {
		r.cochange = gitpkg.NewCoChangeFromData(snap.CoChangePairs, snap.Churn)
	}

	// Metrics, nearby configs and CODEOWNERS are derived from the whole index,
	// not from any one file, so they cannot be updated per-changed-file the way
	// symbols and imports are. They used to be written by Rebuild alone, which
	// left them frozen at the last full rebuild: a file added since had fan-in 0
	// and hotspot 0 forever, a deleted file kept its row, and a CODEOWNERS file
	// added after the first build never took effect at all. Recomputing them
	// here is cheap — they are pure functions of the index plus a couple of file
	// reads — and it is the only way `hotspots` stops decaying between rebuilds.
	//
	// These are deliberately NOT written back to the cache. Since every New()
	// now goes through Refresh, the persisted copies of these three tables are
	// written by Rebuild and never read again — recomputing in memory is what
	// makes them correct, and adding a write path would only create a second
	// source of truth to keep in sync.
	r.nearby = index.NewNearbyIndex(index.FindNearbyConfigs(r.root, r.idx))
	r.ownership = index.ParseCodeowners(r.root)
	r.metrics = index.NewMetricsIndex(index.ComputeMetrics(r.deps, r.cochange))

	// Update meta
	r.saveMeta()

	return nil
}

// --- internal helpers ---

// There is deliberately no cache-only load path any more.
//
// One used to exist and was taken whenever CheckStaleness said "not stale",
// which is how an uncommitted edit stayed invisible: the tree was never looked
// at. Every New() now goes through Refresh, which walks and diffs first and
// only then reads the cache for the parts that did not change. If a fast path
// is reintroduced, it must still diff the working tree.

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
	r.references = index.NewReferenceIndex(r.root, r.idx)
	r.contextDocs = index.NewContextDocIndex(r.root, r.idx, r.symbols)
	r.buildExtrasMap(index.ExtractFileExtras(r.root, r.idx))
	r.nearby = index.NewNearbyIndex(index.FindNearbyConfigs(r.root, r.idx))
	r.ownership = index.ParseCodeowners(r.root)

	if r.isGit {
		if cc, err := gitpkg.Mine(r.root, gitpkg.Options{}, gitpkg.CoChangeOptions{}); err == nil {
			r.cochange = cc
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
		ImportStats:   r.deps.AllImportStats(),
		// Per-file parse status. Without this the trust signal would exist only
		// in memory on the run that built it: a file whose language has no
		// extractor, or whose parse failed, produces zero symbol rows, so there
		// is nothing else in the snapshot from which "I could not read this
		// file" could be reconstructed. Refresh updates these per changed file
		// but can never backfill, so the full rebuild is what populates them.
		FileParses:    r.symbols.Files(),
		References:    r.references.All(),
		ContextDocs:   r.contextDocs.All(),
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

// ParseCoverage reports every source file recon could not fully read: an
// unsupported language, a failed or partial parse, or — critically — a file it
// never examined at all.
//
// This exists because the interesting failures are invisible in the symbol list
// itself. An unsupported language, a broken parse and a file that genuinely
// declares nothing all produce zero symbols, so "no symbols found" was the same
// answer for "there are none" and "I could not read this". A caller that wants
// to trust an empty result should check here first.
//
// A file with no parse record is reported as status "unknown", never as clean:
// records are written per examined file, so an absent one means the file was
// never accounted for, which is exactly the case that must not read as "fine".
func (r *Recon) ParseCoverage() []FileParseInfo {
	if r.symbols == nil || r.idx == nil {
		return nil
	}

	out := make([]FileParseInfo, 0)
	for _, fp := range r.symbols.Incomplete() {
		out = append(out, FileParseInfo{
			File:        fp.RelPath,
			Lang:        fp.Lang,
			Extractor:   fp.Extractor,
			Status:      fp.Status,
			SymbolCount: fp.SymbolCount,
			Detail:      fp.Detail,
		})
	}

	// Files the index knows about but which have no parse record at all. This
	// is the gap the cache can produce: parse rows are written per examined
	// file, so anything missed drops out of Incomplete() silently rather than
	// surfacing as a caveat.
	recorded := make(map[string]bool)
	for _, fp := range r.symbols.Files() {
		recorded[fp.RelPath] = true
	}
	for _, f := range r.idx.All() {
		if f.Class != scan.ClassSource && f.Class != scan.ClassTest && f.Class != scan.ClassScript {
			continue
		}
		if recorded[f.RelPath] {
			continue
		}
		out = append(out, FileParseInfo{
			File:      f.RelPath,
			Lang:      f.Lang,
			Extractor: "none",
			Status:    "unknown",
			Detail:    "no parse record — this file was never examined",
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

// ImportCoverage reports, per language, how many import specifiers recon
// extracted and how many it could actually resolve to a file in this repo.
//
// This is what makes an empty dependency answer interpretable. "fan_in: 0" had
// two very different meanings — nothing imports this file, or recon does not
// understand this language's import style — and they were indistinguishable.
// Unresolved is the number that matters: those are edges recon knows it dropped.
// External is expected and fine, being stdlib and third-party imports that
// correctly have no in-repo target.
func (r *Recon) ImportCoverage() []LangImportCoverage {
	if r.deps == nil {
		return nil
	}
	cov := r.deps.ImportCoverage()
	out := make([]LangImportCoverage, 0, len(cov))
	for _, c := range cov {
		out = append(out, LangImportCoverage{
			Lang:       c.Lang,
			Files:      c.Files,
			Extracted:  c.Extracted,
			Resolved:   c.Resolved,
			External:   c.External,
			Unresolved: c.Unresolved,
		})
	}
	return out
}

// ImportStatsFor returns the import resolution record for a single file, or nil
// when the file had no import specifiers at all.
func (r *Recon) ImportStatsFor(path string) *ImportStatsInfo {
	if r.deps == nil {
		return nil
	}
	st, ok := r.deps.ImportStatsOf(filepath.Clean(path))
	if !ok {
		return nil
	}
	return &ImportStatsInfo{
		Lang:            st.Lang,
		Extracted:       st.Extracted,
		Resolved:        st.Resolved,
		External:        st.External,
		Unresolved:      st.Unresolved,
		UnresolvedSpecs: st.UnresolvedSpecs,
	}
}
