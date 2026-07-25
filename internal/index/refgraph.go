package index

import (
	"embed"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/djtouchette/recon/internal/scan"
)

// Tree-sitter based call/reference extraction.
//
// This mirrors symbols_ts.go: each language with a refs query has
// queries/refs/<lang>.scm capturing the callee identifier as @name. Only the
// raw call sites are extracted and stored — resolution of a reference to its
// definition happens at query time (cheaply, only for the queried name) using
// the SymbolIndex + DepGraph. Real parsing means we never mistake a call-like
// token inside a string or comment for a real call.
//
// Languages without a refs query simply contribute no references; that's fine.

//go:embed queries/refs/*.scm
var refQueryFS embed.FS

var tsRefRegistry = map[string]*tsLang{}

func init() {
	for _, g := range tsGrammars {
		src, err := refQueryFS.ReadFile("queries/refs/" + g.refQueryFile())
		if err != nil {
			continue // no refs query for this language
		}
		l := tree_sitter.NewLanguage(g.langPtr)
		q, qerr := tree_sitter.NewQuery(l, string(src))
		if qerr != nil {
			// A refs query that fails to compile leaves that language without
			// references rather than crashing recon. TestRefQueriesCompile
			// guards against this in CI.
			continue
		}
		tsRefRegistry[g.lang] = &tsLang{lang: l, query: q}
	}
}

// hasTSRefs reports whether a tree-sitter refs grammar is registered for lang.
func hasTSRefs(lang string) bool {
	_, ok := tsRefRegistry[lang]
	return ok
}

// refGrammarCandidates returns the refs grammar keys to try for a file, in
// preference order, restricted to keys that actually have a refs query. It
// mirrors grammarCandidates so .tsx keeps the TSX grammar while .ts gets the
// real TypeScript one.
func refGrammarCandidates(lang, relPath string) []string {
	var out []string
	for _, key := range grammarCandidates(lang, relPath) {
		if hasTSRefs(key) {
			out = append(out, key)
		}
	}
	if len(out) == 0 && hasTSRefs(lang) {
		out = append(out, lang)
	}
	return out
}

// sortReferences orders references deterministically. Extraction is concurrent
// and appends in goroutine-arrival order, so without this the same repo scanned
// twice produced `callers` output in a different order each time.
func sortReferences(refs []Reference) {
	sort.Slice(refs, func(i, j int) bool {
		a, b := &refs[i], &refs[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Name < b.Name
	})
}

// Reference is a single raw call site: a callee name captured at a file:line.
// References are stored unresolved; resolution to a definition happens at query
// time.
type Reference struct {
	Name string `json:"name"`
	File string `json:"file"`
	Line int    `json:"line"`
}

// ReferenceIndex holds all extracted call sites, indexed by callee name.
type ReferenceIndex struct {
	byName map[string][]Reference
	all    []Reference
}

// NewReferenceIndex extracts references from all source and test files in the
// index, concurrently (mirroring NewSymbolIndex). Test files are included so
// callers in tests are discoverable.
func NewReferenceIndex(root string, idx *FileIndex) *ReferenceIndex {
	ri := &ReferenceIndex{byName: make(map[string][]Reference)}

	sources := idx.ByClass(scan.ClassSource)
	tests := idx.ByClass(scan.ClassTest)
	scripts := idx.ByClass(scan.ClassScript)
	allFiles := make([]*scan.FileEntry, 0, len(sources)+len(tests)+len(scripts))
	allFiles = append(allFiles, sources...)
	allFiles = append(allFiles, tests...)
	allFiles = append(allFiles, scripts...)

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0)*2)

	for _, f := range allFiles {
		if len(refGrammarCandidates(f.Lang, f.RelPath)) == 0 {
			continue
		}

		f := f
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			refs := extractFileReferences(root, f)
			if len(refs) == 0 {
				return
			}

			mu.Lock()
			ri.all = append(ri.all, refs...)
			mu.Unlock()
		}()
	}

	wg.Wait()

	sortReferences(ri.all)
	for i := range ri.all {
		ri.byName[ri.all[i].Name] = append(ri.byName[ri.all[i].Name], ri.all[i])
	}
	return ri
}

// NewReferenceIndexFromData creates a ReferenceIndex from pre-loaded data.
func NewReferenceIndexFromData(refs []Reference) *ReferenceIndex {
	ri := &ReferenceIndex{
		byName: make(map[string][]Reference),
		all:    refs,
	}
	// Cached rows come back in whatever order the store returns them; sort so a
	// cached read and a fresh scan agree.
	sortReferences(ri.all)
	for i := range ri.all {
		ri.byName[ri.all[i].Name] = append(ri.byName[ri.all[i].Name], ri.all[i])
	}
	return ri
}

// ScanFileReferences extracts references for specific files (incremental).
func ScanFileReferences(root string, files []*scan.FileEntry) []Reference {
	var all []Reference
	for _, f := range files {
		all = append(all, extractFileReferences(root, f)...)
	}
	sortReferences(all)
	return all
}

// ForName returns all references to the given callee name.
func (ri *ReferenceIndex) ForName(name string) []Reference {
	if ri == nil {
		return nil
	}
	return ri.byName[name]
}

// All returns every extracted reference.
func (ri *ReferenceIndex) All() []Reference {
	if ri == nil {
		return nil
	}
	return ri.all
}

// extractFileReferences reads and parses one file, returning its call sites.
func extractFileReferences(root string, f *scan.FileEntry) []Reference {
	candidates := refGrammarCandidates(f.Lang, f.RelPath)
	if len(candidates) == 0 {
		return nil
	}
	fullPath := filepath.Join(root, f.RelPath)
	// readSource normalises encoding and rejects binary content, so a UTF-16
	// file yields real references instead of silently yielding none.
	data, err := readSource(fullPath, 0)
	if err != nil {
		return nil
	}

	var best []Reference
	var bestClean bool
	for _, key := range candidates {
		refs, clean, ok := extractReferencesTS(data, f.RelPath, key)
		if !ok {
			continue
		}
		if best == nil || (clean && !bestClean) || (clean == bestClean && len(refs) > len(best)) {
			best, bestClean = refs, clean
		}
		if clean {
			break
		}
	}
	return best
}

// extractReferencesTS parses source with the refs grammar registered under key
// and returns its call sites. The first bool reports a clean parse; the second
// is false when no grammar is registered or the parse fails.
func extractReferencesTS(source []byte, relPath, key string) ([]Reference, bool, bool) {
	tl := tsRefRegistry[key]
	if tl == nil {
		return nil, false, false
	}

	p := tsParserPool.Get().(*tree_sitter.Parser)
	defer tsParserPool.Put(p)
	if err := p.SetLanguage(tl.lang); err != nil {
		return nil, false, false
	}

	tree := p.Parse(source, nil)
	if tree == nil {
		return nil, false, false
	}
	defer tree.Close()

	qc := tree_sitter.NewQueryCursor()
	defer qc.Close()

	root := tree.RootNode()
	clean := !root.HasError()
	names := tl.query.CaptureNames()
	matches := qc.Matches(tl.query, root, source)

	var refs []Reference
	for {
		m := matches.Next()
		if m == nil {
			break
		}
		for i := range m.Captures {
			c := &m.Captures[i]
			if names[c.Index] != "name" {
				continue
			}
			name := c.Node.Utf8Text(source)
			if name == "" || name == "_" {
				continue
			}
			refs = append(refs, Reference{
				Name: name,
				File: relPath,
				Line: int(c.Node.StartPosition().Row) + 1,
			})
		}
	}
	return refs, clean, true
}
