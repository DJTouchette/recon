package recon

import (
	"testing"

	"github.com/djtouchette/recon/internal/index"
)

func TestCallersResolution(t *testing.T) {
	// Definition of Target lives in pkg/lib/lib.go.
	symbols := []index.Symbol{
		{File: "pkg/lib/lib.go", Name: "Target", Kind: "function", Line: 10, Signature: "func Target()"},
		{File: "pkg/lib/lib.go", Name: "Other", Kind: "function", Line: 20},
	}

	// References to Target from several files:
	//  - importer.go imports the definition file  -> resolved (via import)
	//  - pkg/lib/sibling.go same dir               -> resolved (same dir)
	//  - unrelated.go neither imports nor same dir -> ambiguous
	references := []index.Reference{
		{Name: "Target", File: "cmd/importer.go", Line: 5},
		{Name: "Target", File: "pkg/lib/sibling.go", Line: 7},
		{Name: "Target", File: "unrelated/elsewhere.go", Line: 3},
		{Name: "Other", File: "cmd/importer.go", Line: 9},
	}

	imports := map[string][]string{
		"cmd/importer.go": {"pkg/lib/lib.go"},
		// unrelated/elsewhere.go imports something else
		"unrelated/elsewhere.go": {"pkg/other/x.go"},
	}

	r := &Recon{
		symbols:    index.NewSymbolIndexFromData(symbols),
		references: index.NewReferenceIndexFromData(references),
		deps:       index.NewDepGraphFromData(imports),
	}

	res := r.Callers("Target")

	if res.Name != "Target" {
		t.Errorf("Name = %q, want Target", res.Name)
	}
	if len(res.Definitions) != 1 {
		t.Fatalf("Definitions = %d, want 1: %+v", len(res.Definitions), res.Definitions)
	}
	if res.Definitions[0].File != "pkg/lib/lib.go" || res.Definitions[0].Line != 10 {
		t.Errorf("definition = %+v, want pkg/lib/lib.go:10", res.Definitions[0])
	}

	if len(res.References) != 3 {
		t.Fatalf("References = %d, want 3: %+v", len(res.References), res.References)
	}

	resolved := map[string]bool{}
	for _, ref := range res.References {
		resolved[ref.File] = ref.Resolved
	}
	if !resolved["cmd/importer.go"] {
		t.Errorf("cmd/importer.go should be resolved (imports definition file)")
	}
	if !resolved["pkg/lib/sibling.go"] {
		t.Errorf("pkg/lib/sibling.go should be resolved (same directory)")
	}
	if resolved["unrelated/elsewhere.go"] {
		t.Errorf("unrelated/elsewhere.go should be ambiguous (no import, different dir)")
	}
}
