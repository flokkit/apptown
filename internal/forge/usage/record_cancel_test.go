package usage

import (
	"context"
	"testing"

	"flokkit.io/forge/internal/ids/idstest"
)

// Spend is recorded after the call that spent it: a caller that stopped
// in the meantime still gets its observation, its once-only observation,
// its cursor and its receipt's close written.
//
// Kills: any of those writes run on the caller's context (refused as
// cancelled, and the spend is lost).
func TestSpendIsRecordedThoughTheCallerStopped(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	l, r := New(db), NewRequests(db)
	receipt, err := r.Begin(context.Background(), Request{Kind: "step", Actor: "system", ProjectID: idstest.N(1), Provider: "p", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	e := Entry{Kind: "step", ProjectID: idstest.N(1), Provider: "p", Model: "m"}
	if err := l.Record(stopped, e, Totals{Total: 10}, ""); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := l.RecordOnce(stopped, "once", e, Totals{Total: 5}, ""); err != nil {
		t.Fatalf("record once: %v", err)
	}
	if err := l.SetCursor(stopped, "/records/a", 42); err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if err := r.Finish(stopped, receipt, 200, "ok", "{}"); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if rep, err := l.Summary(context.Background(), 1, idstest.N(1), ""); err != nil || rep.Totals.Total != 15 {
		t.Fatalf("recorded spend %+v: %v", rep.Totals, err)
	}
	if offset, err := l.Cursor(context.Background(), "/records/a"); err != nil || offset != 42 {
		t.Fatalf("cursor %d: %v", offset, err)
	}
}
