package conformance

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/conformance/internal/conformancesql"
	"flokkit.io/forge/internal/ids"
)

// Test observations through the conformance store's own reads and the audit
// ledger, so a renamed column breaks compilation instead of an assertion.

// heldRecords is the store's own count of every review record it keeps.
func heldRecords(t *testing.T, q database.Queryer) conformancesql.ReviewRecordCountsRow {
	t.Helper()
	held, err := queries(q).ReviewRecordCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return held
}

// conformanceReceipts counts the ledger lines of exactly one kind. The read
// is bounded, so it refuses to answer when the bound is reached.
func conformanceReceipts(t *testing.T, q database.Queryer, kind string) int {
	t.Helper()
	all, err := audit.Recent(context.Background(), q, audit.Filter{KindPrefix: kind, Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == audit.MaxLimit {
		t.Fatalf("%d or more %q receipts: a bounded read cannot count them", audit.MaxLimit, kind)
	}
	n := 0
	for _, e := range all {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func wantHeld(t *testing.T, what string, got int64, want int) {
	t.Helper()
	if got != int64(want) {
		t.Fatalf("%s: %d want %d", what, got, want)
	}
}

func wantReceipts(t *testing.T, q database.Queryer, kind string, want int) {
	t.Helper()
	if got := conformanceReceipts(t, q, kind); got != want {
		t.Fatalf("%s receipts: %d want %d", kind, got, want)
	}
}

// projectDispositions counts every disposition recorded for one project,
// expired or not.
func projectDispositions(t *testing.T, q database.Queryer, projectID ids.ID) int {
	t.Helper()
	all, err := queries(q).DispositionsForProjects(context.Background(), conformancesql.DispositionsForProjectsParams{Projects: []ids.ID{projectID}, Active: false, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return len(all)
}
