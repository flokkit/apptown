package usage

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestLedgerRecordsRollsUpAndKeepsCursors(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	l := New(db)
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "u", Name: "U"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var u Totals
	u.Add(100, 50, 10, 5, 165, 0.0025)
	if u.CostMicros != 2500 {
		t.Fatalf("cost in micro-dollars: %d", u.CostMicros)
	}
	if err := l.Record(context.Background(), Entry{Kind: "step", ProjectID: pid, RunID: idstest.N(7), RootRunID: idstest.N(7), StepID: idstest.N(9), WorkflowName: "quick-change", WorkflowVersion: 3, Provider: "openrouter", Model: "m"}, u, ""); err != nil {
		t.Fatal(err)
	}
	if err := l.Record(context.Background(), Entry{Kind: "console", ProjectID: pid, SessionID: "s1", Actor: "alice", Provider: "local", Model: "n"}, Totals{}, ""); err != nil {
		t.Fatalf("a complete zero is never recorded, and is no error: %v", err)
	}
	var c Totals
	c.Add(20, 20, 0, 0, 40, 0)
	if err := l.Record(context.Background(), Entry{Kind: "console", ProjectID: pid, SessionID: "s1", Actor: "alice", Provider: "local", Model: "n"}, c, ""); err != nil {
		t.Fatal(err)
	}
	total, cost, err := l.RunTotal(context.Background(), idstest.N(7))
	if err != nil || total != 165 || cost != 2500 {
		t.Fatalf("run total: %d %d %v", total, cost, err)
	}
	rep, err := l.Summary(context.Background(), 30, ids.Nil, "")
	if err != nil || rep.Totals.Total != 205 || len(rep.ByKind) != 2 || len(rep.ByProject) != 1 || rep.ByProject[0].Key != "u" || len(rep.ByDay) != 1 {
		t.Fatalf("instance rollup: %+v %v", rep, err)
	}
	if rep.Totals.Incomplete != 0 {
		t.Fatalf("nothing unknown was spent: %+v", rep.Totals)
	}
	rep, err = l.Summary(context.Background(), 30, pid, "quick-change")
	if err != nil || rep.Totals.Total != 165 || len(rep.ByWorkflow) != 1 || rep.ByWorkflow[0].Key != "quick-change" {
		t.Fatalf("project and workflow rollup: %+v %v", rep, err)
	}
	if off, err := l.Cursor(context.Background(), "/x.jsonl"); err != nil || off != 0 {
		t.Fatalf("a file never read starts at zero: %d %v", off, err)
	}
	if err := l.SetCursor(context.Background(), "/x.jsonl", 512); err != nil {
		t.Fatal(err)
	}
	if err := l.SetCursor(context.Background(), "/x.jsonl", 1024); err != nil {
		t.Fatal(err)
	}
	if off, _ := l.Cursor(context.Background(), "/x.jsonl"); off != 1024 {
		t.Fatalf("cursor: %d", off)
	}
}

// An observation recorded once is counted once however often it is replayed,
// while distinct observations of the same step each count, and a key-less
// call is refused rather than silently appending.
func TestLedgerRecordOnceCountsEachObservationOnce(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	l := New(db)
	if _, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "o", Name: "O"})
	}); err != nil {
		t.Fatal(err)
	}
	e := Entry{Kind: "step", ProjectID: idstest.N(1), RunID: idstest.N(3), RootRunID: idstest.N(3), StepID: idstest.N(4), WorkflowName: "w", WorkflowVersion: 1, Provider: "p", Model: "m"}
	var u Totals
	u.Add(10, 5, 0, 0, 15, 0.001)
	for range 2 {
		if err := l.RecordOnce(context.Background(), "step:4:a1:first", e, u, ""); err != nil {
			t.Fatal(err)
		}
	}
	if total, cost, err := l.RunTotal(context.Background(), idstest.N(3)); err != nil || total != 15 || cost != 1000 {
		t.Fatalf("a replayed observation counts once: %d %d %v", total, cost, err)
	}
	if err := l.RecordOnce(context.Background(), "step:4:a1:first-r2", e, u, ""); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := l.Record(context.Background(), e, u, ""); err != nil {
			t.Fatal(err)
		}
	}
	if total, _, err := l.RunTotal(context.Background(), idstest.N(3)); err != nil || total != 60 {
		t.Fatalf("distinct observations and unkeyed records each count: %d %v", total, err)
	}
	if err := l.RecordOnce(context.Background(), "", e, u, ""); err == nil {
		t.Fatal("an observation without a key was accepted")
	}
	if rep, err := l.Summary(context.Background(), 30, idstest.N(1), "w"); err != nil || rep.Totals.Total != 60 {
		t.Fatalf("the rollup agrees: %+v %v", rep.Totals, err)
	}
}

// An observation whose transcript could not be read whole is stored as
// incomplete with the totals that survived, even when none did, so it never
// reads as complete or as "nothing spent"; and the first record of a key
// stands, so a replay that reads the output whole cannot make it complete.
func TestLedgerRecordOnceKeepsAnIncompleteObservationIncomplete(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	l := New(db)
	if _, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "i", Name: "I"})
	}); err != nil {
		t.Fatal(err)
	}
	e := Entry{Kind: "step", ProjectID: idstest.N(1), RunID: idstest.N(3), RootRunID: idstest.N(3), StepID: idstest.N(4), WorkflowName: "w", WorkflowVersion: 1}
	var partial, whole Totals
	partial.Add(10, 5, 0, 0, 15, 0)
	whole.Add(100, 50, 0, 0, 150, 0)
	if err := l.RecordOnce(context.Background(), "step:4:a1:x", e, partial, "a record did not parse"); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordOnce(context.Background(), "step:4:a1:x", e, whole, ""); err != nil {
		t.Fatal(err)
	}
	got, found, err := l.Observed(context.Background(), "step:4:a1:x")
	if err != nil || !found || got.Incomplete != "a record did not parse" || got.Totals.Total != 15 || got.Totals.Input != 10 {
		t.Fatalf("a replay changed an incomplete observation: %+v %t %v", got, found, err)
	}
	if err := l.RecordOnce(context.Background(), "step:4:a1:nothing", e, Totals{}, "the last record has no newline"); err != nil {
		t.Fatal(err)
	}
	if got, found, err := l.Observed(context.Background(), "step:4:a1:nothing"); err != nil || !found || got.Incomplete == "" || got.Totals.Total != 0 {
		t.Fatalf("an incomplete observation with no surviving usage: %+v %t %v", got, found, err)
	}
	if err := l.RecordOnce(context.Background(), "step:4:a1:complete", e, whole, ""); err != nil {
		t.Fatal(err)
	}
	if got, found, err := l.Observed(context.Background(), "step:4:a1:complete"); err != nil || !found || got.Incomplete != "" || got.Totals.Total != 150 {
		t.Fatalf("a complete observation: %+v %t %v", got, found, err)
	}
	if _, found, err := l.Observed(context.Background(), "step:4:a1:never"); err != nil || found {
		t.Fatalf("an unrecorded key: %t %v", found, err)
	}
	if err := l.Record(context.Background(), e, whole, ""); err != nil {
		t.Fatal(err)
	}
	if _, found, err := l.Observed(context.Background(), ""); err != nil || found {
		t.Fatalf("the empty key names no observation: %t %v", found, err)
	}
}

// Spend that is not wholly known is recorded, not dropped: a call that ended
// after it was sent, or answered with no usage, is a row marked incomplete
// even with nothing counted, and the totals over it say "at least".
func TestLedgerRecordsUnknownSpendAsIncomplete(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	l := New(db)
	if _, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "u", Name: "U"})
	}); err != nil {
		t.Fatal(err)
	}
	e := Entry{Kind: "narrative", ProjectID: idstest.N(1), Actor: "apptown-system", Provider: "openrouter", Model: "m"}
	if err := l.Record(context.Background(), e, Totals{}, "the provider reported no usage"); err != nil {
		t.Fatal(err)
	}
	if err := l.Record(context.Background(), e, Totals{Input: 10, Output: 5, Total: 15}, "the provider reported tokens with no price"); err != nil {
		t.Fatal(err)
	}
	if err := l.Record(context.Background(), e, Totals{CostMicros: 40}, ""); err != nil {
		t.Fatal(err)
	}
	rep, err := l.Summary(context.Background(), 30, idstest.N(1), "")
	if err != nil || rep.Totals.Incomplete != 2 || rep.Totals.Total != 15 || rep.Totals.CostMicros != 40 {
		t.Fatalf("unknown spend kept and counted, priced spend with no tokens kept: %+v %v", rep.Totals, err)
	}
}
