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

type RelatedOption func(*relatedConfig)

type relatedConfig struct {
	maxResults int
}

func WithMaxResults(n int) RelatedOption {
	return func(c *relatedConfig) {
		c.maxResults = n
	}
}
