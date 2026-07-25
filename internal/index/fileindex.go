package index

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/djtouchette/recon/internal/scan"
)

// FileIndex provides fast lookups over scanned files.
type FileIndex struct {
	byPath  map[string]*scan.FileEntry
	byDir   map[string][]*scan.FileEntry
	byLang  map[string][]*scan.FileEntry
	byClass map[scan.FileClass][]*scan.FileEntry
	all     []*scan.FileEntry
}

// NewFileIndex builds an index from walk results.
func NewFileIndex(files []scan.FileEntry) *FileIndex {
	idx := &FileIndex{
		byPath:  make(map[string]*scan.FileEntry, len(files)),
		byDir:   make(map[string][]*scan.FileEntry),
		byLang:  make(map[string][]*scan.FileEntry),
		byClass: make(map[scan.FileClass][]*scan.FileEntry),
		all:     make([]*scan.FileEntry, len(files)),
	}

	for i := range files {
		f := &files[i]
		idx.all[i] = f
		idx.byPath[f.RelPath] = f

		dir := filepath.Dir(f.RelPath)
		if dir == "." {
			dir = ""
		}
		idx.byDir[dir] = append(idx.byDir[dir], f)

		if f.Lang != "" {
			idx.byLang[f.Lang] = append(idx.byLang[f.Lang], f)
		}
		idx.byClass[f.Class] = append(idx.byClass[f.Class], f)
	}

	return idx
}

func (idx *FileIndex) All() []*scan.FileEntry             { return idx.all }
func (idx *FileIndex) Len() int                           { return len(idx.all) }
func (idx *FileIndex) Get(relPath string) *scan.FileEntry { return idx.byPath[relPath] }

func (idx *FileIndex) ByDir(dir string) []*scan.FileEntry {
	return idx.byDir[dir]
}

func (idx *FileIndex) ByLang(lang string) []*scan.FileEntry {
	return idx.byLang[lang]
}

func (idx *FileIndex) ByClass(class scan.FileClass) []*scan.FileEntry {
	return idx.byClass[class]
}

// Languages returns languages with counts, most files first. Ties break on
// name: the counts come from ranging a map, so without a total order two runs
// over an unchanged repo report the same languages in a different order.
func (idx *FileIndex) Languages() []LangCount {
	var langs []LangCount
	total := 0
	for lang, files := range idx.byLang {
		count := 0
		for _, f := range files {
			if f.Class == scan.ClassSource || f.Class == scan.ClassTest {
				count++
			}
		}
		if count > 0 {
			langs = append(langs, LangCount{Name: lang, Count: count})
			total += count
		}
	}
	sort.Slice(langs, func(i, j int) bool {
		if langs[i].Count != langs[j].Count {
			return langs[i].Count > langs[j].Count
		}
		return langs[i].Name < langs[j].Name
	})
	for i := range langs {
		if total > 0 {
			langs[i].Percentage = float64(langs[i].Count) / float64(total) * 100
		}
	}
	return langs
}

type LangCount struct {
	Name       string
	Count      int
	Percentage float64
}

// TopDirs returns info about top-level directories.
func (idx *FileIndex) TopDirs() []DirInfo {
	// Count files per top-level directory
	dirFiles := make(map[string]int)
	dirLangs := make(map[string]map[string]bool)

	for _, f := range idx.all {
		parts := strings.SplitN(f.RelPath, "/", 2)
		var topDir string
		if len(parts) == 1 {
			topDir = "."
		} else {
			topDir = parts[0]
		}

		dirFiles[topDir]++
		if f.Lang != "" {
			if dirLangs[topDir] == nil {
				dirLangs[topDir] = make(map[string]bool)
			}
			dirLangs[topDir][f.Lang] = true
		}
	}

	var dirs []DirInfo
	for dir, count := range dirFiles {
		var langs []string
		for lang := range dirLangs[dir] {
			langs = append(langs, lang)
		}
		sort.Strings(langs)

		purpose := classifyDirPurpose(dir)
		dirs = append(dirs, DirInfo{
			Path:      dir,
			FileCount: count,
			Languages: langs,
			Purpose:   purpose,
		})
	}

	// Same map-iteration hazard as Languages: tie-break on path so the
	// structure section of an overview is stable run to run.
	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i].FileCount != dirs[j].FileCount {
			return dirs[i].FileCount > dirs[j].FileCount
		}
		return dirs[i].Path < dirs[j].Path
	})
	return dirs
}

type DirInfo struct {
	Path      string
	FileCount int
	Languages []string
	Purpose   string
}

func classifyDirPurpose(dir string) string {
	ldir := strings.ToLower(dir)
	switch {
	case ldir == "." || ldir == "":
		return "root"
	case ldir == "src" || ldir == "lib" || ldir == "internal" || ldir == "pkg" || ldir == "app":
		return "source"
	case ldir == "cmd":
		return "entrypoint"
	case ldir == "test" || ldir == "tests" || ldir == "spec" || ldir == "specs" ||
		ldir == "playwright" || ldir == "cypress" || ldir == "e2e":
		return "test"
	case ldir == "docs" || ldir == "doc" || ldir == "documentation":
		return "docs"
	case ldir == "config" || ldir == "configs" || ldir == "configuration" || ldir == ".config":
		return "config"
	case ldir == "scripts" || ldir == "script" || ldir == "tools" || ldir == "tool":
		return "scripts"
	case strings.HasPrefix(ldir, "infra") || strings.HasPrefix(ldir, "deploy") ||
		ldir == "terraform" || ldir == "infrastructure" || ldir == "pipelines" ||
		ldir == "ci" || ldir == ".github" || ldir == ".azuredevops" || ldir == ".circleci":
		return "infra"
	case ldir == "vendor" || ldir == "third_party" || ldir == "external":
		return "vendor"
	case ldir == "build" || ldir == "dist" || ldir == "out" || ldir == "output":
		return "build"
	case ldir == "assets" || ldir == "static" || ldir == "public" || ldir == "media" || ldir == "images":
		return "assets"
	case strings.HasPrefix(ldir, "client") || ldir == "frontend" || ldir == "web" || ldir == "ui":
		return "frontend"
	case strings.HasPrefix(ldir, "backend") || ldir == "server" || ldir == "api":
		return "backend"
	case ldir == "support":
		return "support"
	case ldir == "dataconversion" || ldir == "data" || ldir == "migration" || ldir == "migrations":
		return "data"
	default:
		return "source"
	}
}

// Exists returns true if the path exists in the index.
func (idx *FileIndex) Exists(relPath string) bool {
	_, ok := idx.byPath[relPath]
	return ok
}

// FilesUnderDir returns every file at ANY depth under dir, including files in
// nested subdirectories.
//
// The recursion is the point — "tests under internal/orders" means the whole
// subtree — but it also means this is the wrong function for any question of
// the form "is this file a sibling of that one" / "are these in the same
// package": FilesUnderDir("internal") matches every file in the repo's
// internal tree, which makes a same-package signal fire on everything. Use
// FilesDirectlyIn for that.
//
// dir is a slash-separated relpath. "" and "." both mean the repo root and
// return every file.
func (idx *FileIndex) FilesUnderDir(dir string) []*scan.FileEntry {
	dir = normalizeDir(dir)
	if dir == "" {
		out := make([]*scan.FileEntry, len(idx.all))
		copy(out, idx.all)
		return out
	}
	prefix := dir + "/"
	var result []*scan.FileEntry
	for _, f := range idx.all {
		if strings.HasPrefix(f.RelPath, prefix) {
			result = append(result, f)
		}
	}
	return result
}

// FilesDirectlyIn returns only the files whose immediate parent is dir —
// siblings, not the subtree. This is the "same package / same directory"
// question.
//
// "" and "." both mean the repo root.
func (idx *FileIndex) FilesDirectlyIn(dir string) []*scan.FileEntry {
	return idx.byDir[normalizeDir(dir)]
}

// FilesInDir is the recursive form.
//
// Deprecated: the name does not say whether it recurses, and callers have read
// it both ways. Use FilesUnderDir (recursive) or FilesDirectlyIn (siblings
// only).
func (idx *FileIndex) FilesInDir(dirPrefix string) []*scan.FileEntry {
	return idx.FilesUnderDir(dirPrefix)
}

// normalizeDir maps the several spellings of "the repo root" onto the empty
// string used as the byDir key. Without this, FilesUnderDir(".") builds the
// prefix "./", which never matches a cleaned relpath, and a query about the
// whole repo silently returns nothing.
func normalizeDir(dir string) string {
	dir = filepath.ToSlash(dir)
	dir = strings.TrimSuffix(dir, "/")
	if dir == "." || dir == "/" {
		return ""
	}
	dir = strings.TrimPrefix(dir, "./")
	if dir == "." {
		return ""
	}
	return dir
}
