package apptown

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/model"
)

// Model slots & catalog read paths (models.go): the matrix the operator
// reads before changing a cell — slots with gloss, fallbacks, consumers and
// spend; the profile columns; and the per-binding model catalog. Resolution
// itself is pinned in model/selection_test.go; these cover the read/aggregate surface.

func TestSeedModelSlotsInsertsDefault(t *testing.T) {
	t.Parallel()
	a := noModelApp(t, testDB(t))
	// No default model configured → nothing seeded.
	a.SeedModelSlots(t.Context())
	unseeded, err := a.Models.Cells(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(unseeded["default"]); n != 0 {
		t.Fatalf("no -agent-model means no seeded default, got %d rows", n)
	}
	// sql-guarantee: no default row in any scope; the instance cells read only project 0 and the overrides read only rows whose project still exists.
	completionCount(t, a.DB, 0, `SELECT COUNT(*) FROM model_slots WHERE slot='default'`)
	// With one set, insert-if-absent lands exactly one row.
	a = newApp(t, a.DB, Config{AgentModel: "prov/base"})

	a.SeedModelSlots(t.Context())
	a.SeedModelSlots(t.Context()) // idempotent: INSERT OR IGNORE
	cells, err := a.Models.Cells(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pattern := cells["default"][model.StandardProfile]
	if pattern != "prov/base" {
		t.Fatalf("seeded default pattern: %q", pattern)
	}
}
