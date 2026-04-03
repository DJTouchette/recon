package scan

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// WalkResult holds the output of a directory walk.
type WalkResult struct {
	Files   []FileEntry
	RootDir string
}

// Walk performs a parallel filesystem walk rooted at root, respecting .gitignore files.
func Walk(root string) (*WalkResult, error) {
	root = filepath.Clean(root)

	// Load root-level ignore files
	rootMatcher := DefaultMatcher()
	if rules := ParseGitignoreFile(filepath.Join(root, ".gitignore"), ""); len(rules) > 0 {
		rootMatcher = rootMatcher.Child(rules)
	}
	if rules := ParseGitignoreFile(filepath.Join(root, ".reconignore"), ""); len(rules) > 0 {
		rootMatcher = rootMatcher.Child(rules)
	}

	var (
		mu    sync.Mutex
		files = make([]FileEntry, 0, 8192)
		wg    sync.WaitGroup
		sem   = make(chan struct{}, runtime.GOMAXPROCS(0)*2)
	)

	var walk func(dir, relDir string, matcher *Matcher)
	walk = func(dir, relDir string, matcher *Matcher) {
		defer wg.Done()

		sem <- struct{}{}
		entries, err := os.ReadDir(dir)
		<-sem

		if err != nil {
			return
		}

		// Check for .gitignore in this directory (skip root, already loaded)
		if relDir != "" {
			for _, entry := range entries {
				if entry.Name() == ".gitignore" {
					if rules := ParseGitignoreFile(filepath.Join(dir, ".gitignore"), relDir); len(rules) > 0 {
						matcher = matcher.Child(rules)
					}
					break
				}
			}
		}

		var localFiles []FileEntry

		for _, entry := range entries {
			name := entry.Name()

			// Skip dotfiles that are always irrelevant
			if name[0] == '.' && len(name) > 1 {
				if IsHardcodedIgnore(name) {
					continue
				}
			}

			// Fast path for hardcoded ignored dirs
			if entry.IsDir() && IsHardcodedIgnore(name) {
				continue
			}

			// Build relative path without filepath.Rel overhead
			var relPath string
			if relDir == "" {
				relPath = name
			} else {
				relPath = relDir + "/" + name
			}

			// Skip symlinks to avoid cycles
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}

			if matcher.Match(relPath, entry.IsDir()) {
				continue
			}

			if entry.IsDir() {
				wg.Add(1)
				go walk(filepath.Join(dir, name), relPath, matcher)
				continue
			}

			info, err := entry.Info()
			if err != nil {
				continue
			}

			localFiles = append(localFiles, FileEntry{
				RelPath: relPath,
				Size:    info.Size(),
				ModTime: info.ModTime().UnixNano(),
				Class:   Classify(relPath, name),
				Lang:    LangFromExt(name),
			})
		}

		if len(localFiles) > 0 {
			mu.Lock()
			files = append(files, localFiles...)
			mu.Unlock()
		}
	}

	wg.Add(1)
	go walk(root, "", rootMatcher)
	wg.Wait()

	return &WalkResult{Files: files, RootDir: root}, nil
}
