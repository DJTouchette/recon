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
	// Statuses: found | none_matched | unsupported | incomplete.
	//
	// "incomplete" outranks the others, "found" included — a list assembled from
	// a partial reading of the repo is not the whole answer, so a non-empty list
	// can still be incomplete. ManifestIssues says what failed.
	FrameworkStatus  string `json:"framework_status,omitempty"`
	EntrypointStatus string `json:"entrypoint_status,omitempty"`
	DependencyStatus string `json:"dependency_status,omitempty"`

	// ImportCoverage says how complete the dependency graph actually is, per
	// language. An empty graph and an unresolvable one look the same without it.
	ImportCoverage []LangImportCoverage `json:"import_coverage,omitempty"`

	// ManifestIssues name manifests recon tried to read and could not. They are
	// why a status can be "incomplete": the list that was produced came from a
	// partial reading of the repo, which is not the same as being complete.
	ManifestIssues []ManifestIssueInfo `json:"manifest_issues,omitempty"`
}

// ManifestIssueInfo is a manifest recon could not use, and why.
type ManifestIssueInfo struct {
	Manifest string `json:"manifest"`
	Language string `json:"language,omitempty"`
	Reason   string `json:"reason"`
	// Affects names which lists are incomplete because of this. An unreadable
	// pom.xml costs frameworks and dependencies but no entrypoints, since JVM
	// entrypoints come from source — saying so keeps the caveat off lists that
	// really are complete.
	Affects []string `json:"affects,omitempty"`
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
	Path string `json:"path"`

	// Status is StatusIndexed when this path names a file recon actually
	// scanned, and StatusNotIndexed when it does not — a path outside the
	// repo, a typo, or a file excluded by the ignore rules. Every count below
	// is zero in that case, and the difference between "zero dependents" and
	// "recon has never seen this file" is the whole point of the field.
	Status string `json:"status"`

	Preview       string            `json:"preview,omitempty"`
	ContentHash   string            `json:"content_hash,omitempty"`
	Owners        []string          `json:"owners,omitempty"`
	FanIn         int               `json:"fan_in"`
	FanOut        int               `json:"fan_out"`
	Churn         int               `json:"churn"`
	HotspotScore  float64           `json:"hotspot_score"`
	NearbyConfigs map[string]string `json:"nearby_configs,omitempty"` // type → path
	Docs          []ContextDocInfo  `json:"docs,omitempty"`           // context docs attached to this file

	// ImportStats qualifies FanOut. Without it, "fan_out: 0" means both "this
	// file imports nothing local" and "recon could not resolve this file's
	// imports at all", which are very different facts to act on.
	ImportStats *ImportStatsInfo `json:"import_stats,omitempty"`
}

// Statuses for FileContext.Status.
const (
	StatusIndexed    = "indexed"
	StatusNotIndexed = "not_indexed"
)

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

// ImportStatsInfo is how one file's import specifiers resolved.
//
// Every specifier lands in exactly one bucket. External is an expected
// non-edge — stdlib and third-party imports have no in-repo target — while
// Unresolved counts edges recon knows it failed to resolve. A high Unresolved
// is the signal that this file's dependency picture is incomplete, which is
// otherwise invisible: a dropped edge looks exactly like an absent one.
type ImportStatsInfo struct {
	Lang            string   `json:"lang"`
	Extracted       int      `json:"extracted"`
	Resolved        int      `json:"resolved"`
	External        int      `json:"external"`
	Unresolved      int      `json:"unresolved"`
	UnresolvedSpecs []string `json:"unresolved_specs,omitempty"` // bounded sample
}

// LangImportCoverage aggregates import resolution over one language.
type LangImportCoverage struct {
	Lang       string `json:"lang"`
	Files      int    `json:"files"`
	Extracted  int    `json:"extracted"`
	Resolved   int    `json:"resolved"`
	External   int    `json:"external"`
	Unresolved int    `json:"unresolved"`
}
