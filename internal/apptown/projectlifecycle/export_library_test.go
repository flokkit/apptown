package projectlifecycle

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// The library versions a move carries are exactly what the current
// revision selects, guidance and workflows by name and version, and the
// pack versions the project is placed on; an older revision's selection is
// history, not carried.
func TestAMoveCarriesTheCurrentSelectionsLibraryVersions(t *testing.T) {
	rev := func(n int, receipt adoption.Receipt) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"revision": n, "receipt_json": receipt})
		return b
	}
	old := adoption.EmptyReceipt()
	old.Guidance = []adoption.ReceiptBlock{{Name: "old-rule", Version: 1}}
	now := adoption.EmptyReceipt()
	now.Guidance = []adoption.ReceiptBlock{{Name: "house-style", Version: 3}}
	now.Workflows = []adoption.ReceiptWorkflow{{Name: "ship", Version: 2, System: true}}
	doc := adoption.ProjectExport{
		Current:   []json.RawMessage{json.RawMessage(`{"revision":2}`)},
		Revisions: []json.RawMessage{rev(1, old), rev(2, now)},
		Packs:     []json.RawMessage{json.RawMessage(`{"pack_id":"00000000-0004-7000-8000-000000000000","version":6}`)},
	}
	refs, err := libraryRefs(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := []wow.VersionRef{{Kind: "guidance", Name: "house-style", Version: 3}, {Kind: "workflow", Name: "ship", Version: 2}, {Kind: "pack", PackID: idstest.N(4), Version: 6}}
	if !slices.Equal(refs, want) {
		t.Fatalf("carried %+v, want %+v", refs, want)
	}
}
