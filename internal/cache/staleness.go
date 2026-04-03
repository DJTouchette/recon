package cache

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/djtouchette/recon/internal/git"
)

// StaleReason describes why the cache is stale.
type StaleReason int

const (
	NotStale       StaleReason = iota
	NoCacheData                // no data in DB
	HeadChanged                // HEAD sha differs
	KeyFileChanged             // a key config file mtime changed
)

func (r StaleReason) String() string {
	switch r {
	case NotStale:
		return "not stale"
	case NoCacheData:
		return "no cache data"
	case HeadChanged:
		return "HEAD changed"
	case KeyFileChanged:
		return "key file changed"
	default:
		return "unknown"
	}
}

// NeedsRebuild indicates the cache should be fully rebuilt.
func (r StaleReason) NeedsRebuild() bool {
	return r == NoCacheData
}

// key config files to check mtimes for — cheap stat calls
var keyFiles = []string{
	"go.mod", "go.sum",
	"package.json", "package-lock.json", "yarn.lock",
	"Cargo.toml", "Cargo.lock",
	"pyproject.toml", "requirements.txt",
	"Gemfile", "Gemfile.lock",
	"mix.exs", "mix.lock",
	"pom.xml", "build.gradle",
	"tsconfig.json",
}

// CheckStaleness determines if the cached data is still valid.
func CheckStaleness(s *Store) StaleReason {
	if !s.HasData() {
		return NoCacheData
	}

	// HEAD check
	currentHead := git.GetHEAD(s.Root)
	storedHead, _ := s.GetMeta("head_sha")
	if currentHead != "" && storedHead != "" && currentHead != storedHead {
		return HeadChanged
	}

	// Key file mtime check — catches uncommitted changes to config files
	for _, kf := range keyFiles {
		storedStr, ok := s.GetMeta("mtime:" + kf)
		if !ok {
			continue
		}
		storedMtime, _ := strconv.ParseInt(storedStr, 10, 64)

		info, err := os.Stat(filepath.Join(s.Root, kf))
		if err != nil {
			// File was deleted — stale
			if storedMtime != 0 {
				return KeyFileChanged
			}
			continue
		}
		if info.ModTime().UnixNano() != storedMtime {
			return KeyFileChanged
		}
	}

	return NotStale
}

// SaveKeyFileMtimes stores the current mtimes of key config files.
func SaveKeyFileMtimes(s *Store) {
	for _, kf := range keyFiles {
		info, err := os.Stat(filepath.Join(s.Root, kf))
		if err != nil {
			s.SetMeta("mtime:"+kf, "0")
			continue
		}
		s.SetMeta("mtime:"+kf, strconv.FormatInt(info.ModTime().UnixNano(), 10))
	}
}
