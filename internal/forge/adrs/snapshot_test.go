package adrs

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/ids"
)

func TestDetailKeepsDecisionAndSuccessorInOneSnapshot(t *testing.T) {
	t.Parallel()
	s, p := fixture(t)
	db := s.records.db
	old := create(t, s, p, "original")
	decide(t, s, old.ID, Accept)
	// Only the body projection blocks. Scope lookup and successor lookup do not
	// evaluate this stable view expression, so the first detail statement can be
	// held while another connection commits a new successor before the second read.
	completionExec(t, db, `ALTER TABLE adrs RENAME TO adr_snapshot_records`)
	completionExec(t, db, `CREATE FUNCTION read_decision_body(body text) RETURNS text LANGUAGE plpgsql STABLE AS $$ BEGIN PERFORM pg_advisory_xact_lock(913); RETURN body; END $$`)
	// sql-guarantee: DDL for the barrier view whose body projection waits on an advisory lock, so one detail read can be held mid-snapshot; not an assertion read
	completionExec(t, db, `CREATE VIEW adrs AS SELECT id,project_id,number,title,status,read_decision_body(body_md) AS body_md,tags,supersedes_id,change_id,created_at,updated_at FROM adr_snapshot_records`)
	gate, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gate.Rollback() }()
	// sql-guarantee: takes the advisory lock the barrier view waits on; a held lock, not a read
	if _, err := gate.Exec(`SELECT pg_advisory_xact_lock(913)`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		detail *Detail
		err    error
	}
	done := make(chan result, 1)
	go func() { d, err := s.Get(testCaller(), old.ID); done <- result{d, err} }()
	dbtest.WaitForLock(t, db, "SELECT a.id, a.project_id")
	// sql-guarantee: Change the backing row while the ADR view read waits; EditADR also writes the view's computed body column and cannot update this renamed backing table.
	if _, err := gate.Exec(`UPDATE adr_snapshot_records SET status='deprecated' WHERE id=?`, old.ID); err != nil {
		t.Fatal(err)
	}
	next := ids.New(time.Now())
	// sql-guarantee: Commit a successor behind the blocked view; InsertADR writes the computed body column and cannot insert through that view or address its renamed backing table.
	if _, err := gate.Exec(`INSERT INTO adr_snapshot_records(id,project_id,number,title,status,supersedes_id,created_at,updated_at) VALUES (?,?,2,'new successor','accepted',?,'2026-10-01','2026-10-01')`, next, p, old.ID); err != nil {
		t.Fatal(err)
	}
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.detail == nil || got.detail.ADR.Status != Accepted || !got.detail.SupersededByID.IsZero() || !got.detail.Options.CanSupersede {
			t.Fatalf("mixed detail: %+v %v", got.detail, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("detail did not release its read snapshot")
	}
	fresh, err := s.Get(testCaller(), old.ID)
	if err != nil || fresh.ADR.Status != Deprecated || fresh.SupersededByID != next || fresh.Options.CanSupersede {
		t.Fatalf("fresh snapshot missed committed facts: %+v %v", fresh, err)
	}
}
