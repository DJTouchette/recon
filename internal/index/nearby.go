package index

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/djtouchette/recon/internal/scan"
)

// NearbyConfig represents a config/operational file near a source directory.
type NearbyConfig struct {
	Dir        string `json:"dir"`
	ConfigType string `json:"config_type"`
	ConfigPath string `json:"config_path"`
}

// config types and their file patterns
var configPatterns = []struct {
	configType string
	names      []string
	prefixes   []string // match files starting with these
	suffixes   []string // match files ending with these
}{
	{
		configType: "package_manifest",
		names:      []string{"package.json", "go.mod", "Cargo.toml", "pyproject.toml", "setup.py", "mix.exs", "Gemfile", "pom.xml", "build.gradle", "build.gradle.kts"},
		suffixes:   []string{".csproj", ".fsproj"},
	},
	{
		configType: "dockerfile",
		names:      []string{"Dockerfile", "docker-compose.yml", "docker-compose.yaml"},
	},
	{
		configType: "ci",
		names:      []string{"Jenkinsfile", ".gitlab-ci.yml", "azure-pipelines.yml", "bitbucket-pipelines.yml"},
	},
	{
		configType: "makefile",
		names:      []string{"Makefile", "Rakefile", "Taskfile.yml", "justfile"},
	},
	{
		configType: "readme",
		names:      []string{"README.md", "README.rst", "README.txt", "README"},
	},
	{
		configType: "env",
		names:      []string{".env.example", ".env.sample", ".env.template"},
	},
	{
		configType: "lint",
		prefixes:   []string{".eslintrc", ".prettierrc"},
		names:      []string{".golangci.yml", ".golangci.yaml", ".rubocop.yml", ".pylintrc", "biome.json"},
	},
	{
		configType: "tsconfig",
		names:      []string{"tsconfig.json", "jsconfig.json"},
	},
}

// FindNearbyConfigs discovers operational config files for each directory containing source files.
func FindNearbyConfigs(root string, idx *FileIndex) []NearbyConfig {
	// Collect unique directories that have source files
	dirs := make(map[string]bool)
	for _, f := range idx.ByClass(scan.ClassSource) {
		dir := filepath.Dir(f.RelPath)
		if dir == "." {
			dir = ""
		}
		dirs[dir] = true
	}

	var results []NearbyConfig
	seen := make(map[string]bool) // dedup by dir+type

	for dir := range dirs {
		found := walkUpForConfigs(root, dir)
		for _, nc := range found {
			key := nc.Dir + "|" + nc.ConfigType
			if !seen[key] {
				seen[key] = true
				results = append(results, nc)
			}
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Dir != results[j].Dir {
			return results[i].Dir < results[j].Dir
		}
		return results[i].ConfigType < results[j].ConfigType
	})

	return results
}

// walkUpForConfigs walks from dir up to root, finding the nearest config of each type.
func walkUpForConfigs(root, startDir string) []NearbyConfig {
	found := make(map[string]NearbyConfig) // configType → nearest
	dir := startDir

	for {
		absDir := filepath.Join(root, dir)
		entries, err := os.ReadDir(absDir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					// Check for CI config dirs
					name := entry.Name()
					if name == ".github" {
						// Check for workflows
						wfDir := filepath.Join(absDir, ".github", "workflows")
						if wfEntries, err := os.ReadDir(wfDir); err == nil && len(wfEntries) > 0 {
							if _, ok := found["ci"]; !ok {
								var relPath string
								if dir == "" {
									relPath = ".github/workflows"
								} else {
									relPath = dir + "/.github/workflows"
								}
								found["ci"] = NearbyConfig{
									Dir:        startDir,
									ConfigType: "ci",
									ConfigPath: relPath,
								}
							}
						}
					}
					if name == ".circleci" {
						if _, ok := found["ci"]; !ok {
							var relPath string
							if dir == "" {
								relPath = ".circleci"
							} else {
								relPath = dir + "/.circleci"
							}
							found["ci"] = NearbyConfig{
								Dir:        startDir,
								ConfigType: "ci",
								ConfigPath: relPath,
							}
						}
					}
					continue
				}

				name := entry.Name()
				for _, cp := range configPatterns {
					if _, ok := found[cp.configType]; ok {
						continue // already found a nearer one
					}
					if matchesConfigPattern(name, cp.names, cp.prefixes, cp.suffixes) {
						var relPath string
						if dir == "" {
							relPath = name
						} else {
							relPath = dir + "/" + name
						}
						found[cp.configType] = NearbyConfig{
							Dir:        startDir,
							ConfigType: cp.configType,
							ConfigPath: relPath,
						}
					}
				}
			}
		}

		// Move up one directory
		if dir == "" {
			break
		}
		dir = filepath.Dir(dir)
		if dir == "." {
			dir = ""
		}
	}

	var result []NearbyConfig
	for _, nc := range found {
		result = append(result, nc)
	}
	return result
}

func matchesConfigPattern(name string, names, prefixes, suffixes []string) bool {
	for _, n := range names {
		if name == n {
			return true
		}
	}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	for _, s := range suffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// NearbyIndex provides fast lookup of nearby configs.
type NearbyIndex struct {
	byDir map[string][]NearbyConfig
}

// NewNearbyIndex creates a lookup from computed configs.
func NewNearbyIndex(configs []NearbyConfig) *NearbyIndex {
	ni := &NearbyIndex{byDir: make(map[string][]NearbyConfig)}
	for _, c := range configs {
		ni.byDir[c.Dir] = append(ni.byDir[c.Dir], c)
	}
	return ni
}

// ForDir returns nearby configs for a directory.
func (ni *NearbyIndex) ForDir(dir string) []NearbyConfig {
	if ni == nil {
		return nil
	}
	return ni.byDir[dir]
}

// ForFile returns nearby configs for a file's directory.
func (ni *NearbyIndex) ForFile(relPath string) []NearbyConfig {
	if ni == nil {
		return nil
	}
	dir := filepath.Dir(relPath)
	if dir == "." {
		dir = ""
	}
	return ni.byDir[dir]
}

// All returns all nearby configs.
func (ni *NearbyIndex) All() []NearbyConfig {
	if ni == nil {
		return nil
	}
	var all []NearbyConfig
	for _, configs := range ni.byDir {
		all = append(all, configs...)
	}
	return all
}
