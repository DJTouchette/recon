package recon

// Overview is the top-level repo summary.
type Overview struct {
	Root       string      `json:"root"`
	Languages  []Language  `json:"languages"`
	Frameworks []Framework `json:"frameworks"`

	// Dependencies is what a manifest actually declares. It used to be reported
	// as Frameworks, which meant a Maven project listed its own artifact id and
	// its build plugins as frameworks it used. Frameworks is now
	// evidence-backed only.
	Dependencies []Dependency `json:"dependencies,omitempty"`

	Structure   []DirectoryInfo `json:"structure"`
	Entrypoints []Entrypoint    `json:"entrypoints"`
	FileCount   int             `json:"file_count"`
	TestCount   int             `json:"test_count"`

	// Status fields distinguish "there are none" from "nothing here could tell".
	// An empty list on its own served as both answers, and the second one is
	// the one a reader needs to know about.
	FrameworkStatus  string `json:"framework_status,omitempty"`  // found | none_matched | unsupported
	EntrypointStatus string `json:"entrypoint_status,omitempty"` // found | none_matched | unsupported
}

// Dependency is a declared dependency read from a manifest.
type Dependency struct {
	Name     string `json:"name"`
	Version  string `json:"version,omitempty"`
	Language string `json:"language,omitempty"`
	Manifest string `json:"manifest,omitempty"`
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

	// Extractor names what produced this symbol: "tree-sitter" for a real
	// grammar, "regex" for the pattern fallback, "" when unknown. The two have
	// very different error profiles — a regex-derived symbol can come from a
	// language whose declarations were matched by shape rather than parsed —
	// and without this they were byte-identical in the output.
	Extractor string `json:"extractor,omitempty"`
}

// FileParseInfo reports how a file was read, including files that produced no
// symbols at all.
//
// This is the part that per-symbol provenance cannot express: an unsupported
// language, a failed parse, and a file that genuinely declares nothing all
// yield zero symbols, so without a per-file record they are indistinguishable.
// A file with no record at all means "not examined", never "parsed cleanly".
type FileParseInfo struct {
	File        string `json:"file"`
	Lang        string `json:"lang,omitempty"`
	Extractor   string `json:"extractor"` // tree-sitter | regex | none
	Status      string `json:"status"`    // ok | partial | unsupported | failed
	SymbolCount int    `json:"symbol_count"`
	Detail      string `json:"detail,omitempty"` // human-readable caveat
}

// CallersResult is the response for a "find callers/references" query.
type CallersResult struct {
	Name        string       `json:"name"`
	Definitions []SymbolInfo `json:"definitions"`
	References  []Reference  `json:"references"`
}

// Reference is a single resolved call site for a symbol.
type Reference struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Resolved bool   `json:"resolved"`
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
	Docs          []ContextDocInfo  `json:"docs,omitempty"`           // context docs attached to this file
}

// ContextDocInfo is a context doc extracted from a rivet:context code comment
// or a .context/ sidecar markdown file, attached to a file or symbol.
type ContextDocInfo struct {
	File   string `json:"file"`
	Symbol string `json:"symbol,omitempty"` // "" = file-level
	Line   int    `json:"line,omitempty"`   // marker line for comment docs
	Source string `json:"source"`           // "comment" or "sidecar"
	Origin string `json:"origin"`           // where the doc text lives
	Body   string `json:"body"`
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
	MatchType string `json:"type"`              // "definition", "reference", "comment", "test"
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
