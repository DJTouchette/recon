package recon

// Overview is the top-level repo summary.
type Overview struct {
	Root        string          `json:"root"`
	Languages   []Language      `json:"languages"`
	Frameworks  []Framework     `json:"frameworks"`
	Structure   []DirectoryInfo `json:"structure"`
	Entrypoints []Entrypoint    `json:"entrypoints"`
	FileCount   int             `json:"file_count"`
	TestCount   int             `json:"test_count"`
}

type Language struct {
	Name       string   `json:"name"`
	FileCount  int      `json:"file_count"`
	Percentage float64  `json:"percentage"`
	Extensions []string `json:"extensions"`
}

type Framework struct {
	Name     string `json:"name"`
	Language string `json:"language"`
	Evidence string `json:"evidence"`
}

type DirectoryInfo struct {
	Path      string   `json:"path"`
	FileCount int      `json:"file_count"`
	Languages []string `json:"languages"`
	Purpose   string   `json:"purpose"`
}

type Entrypoint struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type RelatedFile struct {
	Path    string   `json:"path"`
	Score   float64  `json:"score"`
	Signals []string `json:"signals"`
}

type TestFile struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	ForFile string `json:"for_file"`
}

type ChangeSet struct {
	Hash    string   `json:"hash"`
	Author  string   `json:"author"`
	Date    string   `json:"date"`
	Message string   `json:"message"`
	Files   []string `json:"files"`
	Areas   []string `json:"areas"`
}

type SymbolInfo struct {
	File      string `json:"file"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Line      int    `json:"line"`
	Signature string `json:"signature"`
}

type FileDetail struct {
	Path        string `json:"path"`
	Preview     string `json:"preview,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
}

type FileContext struct {
	Path          string            `json:"path"`
	Preview       string            `json:"preview,omitempty"`
	ContentHash   string            `json:"content_hash,omitempty"`
	Owners        []string          `json:"owners,omitempty"`
	FanIn         int               `json:"fan_in"`
	FanOut        int               `json:"fan_out"`
	Churn         int               `json:"churn"`
	HotspotScore  float64           `json:"hotspot_score"`
	NearbyConfigs map[string]string `json:"nearby_configs,omitempty"` // type → path
}

type HotspotInfo struct {
	Path         string  `json:"path"`
	FanIn        int     `json:"fan_in"`
	FanOut       int     `json:"fan_out"`
	Churn        int     `json:"churn"`
	HotspotScore float64 `json:"hotspot_score"`
}

type SearchResult struct {
	Path      string      `json:"path"`
	Score     float64     `json:"score"`
	MatchType string      `json:"match_type"` // "symbol", "file_path", "preview"
	Context   string      `json:"context"`
	Symbol    *SymbolInfo `json:"symbol,omitempty"`
}

// GrepSummary is a quick overview before detailed results.
type GrepSummary struct {
	Files       int `json:"files"`
	Total       int `json:"total"`
	Definitions int `json:"definitions"`
	References  int `json:"references"`
	Tests       int `json:"tests"`
	Comments    int `json:"comments"`
	Truncated   int `json:"truncated,omitempty"` // files not shown due to cap
}

// GrepResult is the top-level grep response.
type GrepResult struct {
	Summary GrepSummary      `json:"summary"`
	Files   []GrepFileResult `json:"files"`
}

// GrepFileResult groups grep matches by file with shared metrics.
type GrepFileResult struct {
	Path         string     `json:"path"`
	FanIn        int        `json:"fan_in"`
	HotspotScore float64    `json:"hotspot_score"`
	Matches      []GrepLine `json:"matches"`
}

// GrepLine is a single matched line within a file.
type GrepLine struct {
	Line      int    `json:"line"`
	Text      string `json:"text"`
	MatchType string `json:"type"` // "definition", "reference", "comment", "test"
	Similar   int    `json:"similar,omitempty"` // count of additional lines with identical text
}

// CoChangePair represents a file that frequently co-changes with another file.
type CoChangePair struct {
	File  string `json:"file"`
	Count int    `json:"count"`
}

type RelatedOption func(*relatedConfig)

type relatedConfig struct {
	maxResults int
}

func WithMaxResults(n int) RelatedOption {
	return func(c *relatedConfig) {
		c.maxResults = n
	}
}
