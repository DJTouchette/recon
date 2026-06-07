package index

import (
	"embed"
	"os"
	"path/filepath"
	"runtime"
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
		src, err := refQueryFS.ReadFile("queries/refs/" + g.lang + ".scm")
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
		if !hasTSRefs(f.Lang) {
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
			for i := range refs {
				ri.byName[refs[i].Name] = append(ri.byName[refs[i].Name], refs[i])
			}
			mu.Unlock()
		}()
	}

	wg.Wait()
	return ri
}

// NewReferenceIndexFromData creates a ReferenceIndex from pre-loaded data.
func NewReferenceIndexFromData(refs []Reference) *ReferenceIndex {
	ri := &ReferenceIndex{
		byName: make(map[string][]Reference),
		all:    refs,
	}
	for i := range refs {
		ri.byName[refs[i].Name] = append(ri.byName[refs[i].Name], refs[i])
	}
	return ri
}

// ScanFileReferences extracts references for specific files (incremental).
func ScanFileReferences(root string, files []*scan.FileEntry) []Reference {
	var all []Reference
	for _, f := range files {
		if !hasTSRefs(f.Lang) {
			continue
		}
		all = append(all, extractFileReferences(root, f)...)
	}
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
	if !hasTSRefs(f.Lang) {
		return nil
	}
	fullPath := filepath.Join(root, f.RelPath)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil
	}
	refs, _ := extractReferencesTS(data, f.RelPath, f.Lang)
	return refs
}

// extractReferencesTS parses source with the registered refs grammar for lang
// and returns its call sites. The bool is false when no grammar is registered
// or the parse fails.
func extractReferencesTS(source []byte, relPath, lang string) ([]Reference, bool) {
	tl := tsRefRegistry[lang]
	if tl == nil {
		return nil, false
	}

	p := tsParserPool.Get().(*tree_sitter.Parser)
	defer tsParserPool.Put(p)
	if err := p.SetLanguage(tl.lang); err != nil {
		return nil, false
	}

	tree := p.Parse(source, nil)
	if tree == nil {
		return nil, false
	}
	defer tree.Close()

	qc := tree_sitter.NewQueryCursor()
	defer qc.Close()

	names := tl.query.CaptureNames()
	matches := qc.Matches(tl.query, tree.RootNode(), source)

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
	return refs, true
}
