package httpapi

import (
	"context"
	"testing"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/ids"
)

// receiptsOfKind is every ledger receipt of exactly one kind, read through
// the audit owner. The read is bounded, so it refuses to count at the bound.
func receiptsOfKind(t *testing.T, q database.Queryer, kind string) []audit.Entry {
	t.Helper()
	all, err := audit.Recent(context.Background(), q, audit.Filter{KindPrefix: kind, Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == audit.MaxLimit {
		t.Fatalf("%d or more %q receipts: a bounded read cannot count them", audit.MaxLimit, kind)
	}
	var out []audit.Entry
	for _, e := range all {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// importedBlocks counts a project's guidance imports of one block name,
// read through the adoption owner.
func importedBlocks(t *testing.T, a *adoption.Service, projectID ids.ID, name string) int {
	t.Helper()
	imports, err := a.Imports(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, im := range imports {
		if im.Name == name {
			n++
		}
	}
	return n
}
