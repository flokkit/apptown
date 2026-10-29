package adrs

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adrs/internal/adrssql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestQueriesShareScopeOrderingAndExactTagRules(t *testing.T) {
	t.Parallel()
	s, p := fixture(t)
	db := s.records.db
	other := projectFixture(t, db, "foreign", "Foreign")
	// sql-guarantee: Scoped Change identity for private ADR matching/locking checks; workflow imports adrs and its generated writer is internal, so no callable owner writer exists from this test package.
	change := completionInsert(t, db, `INSERT INTO changes(id,number,project_id,type,title,created_at) VALUES ($1,$2,$3,'feature','local','2026-10-09T00:00:00Z')`, fixtureNumber(), p)
	a := create(t, s, p, "one")
	b := create(t, s, p, "two")
	create(t, s, other, "foreign")
	if _, err := s.Edit(testCaller(), Edit{ID: a.ID, ChangeID: &change, Decision: Accept}); err != nil {
		t.Fatal(err)
	}
	records, err := s.List(testCaller(), Filter{ProjectID: p})
	if err != nil || len(records) != 2 || records[0].ID != b.ID || records[1].ID != a.ID || records[1].Tags != "data,storage" {
		t.Fatalf("ordered scoped records: %+v %v", records, err)
	}
	// A linked ADR names its change by number (the insert above numbers it
	// from 1000); an unlinked one names none.
	if records[1].ChangeID != change || records[1].ChangeNumber < 1000 || !records[0].ChangeID.IsZero() || records[0].ChangeNumber != 0 {
		t.Fatalf("change numbers: linked %d, unlinked %d", records[1].ChangeNumber, records[0].ChangeNumber)
	}
	if got, err := s.Get(testCaller(), a.ID); err != nil || got.ADR.ChangeNumber != records[1].ChangeNumber {
		t.Fatalf("one ADR's change number: %+v %v", got, err)
	}
	records, err = s.List(testCaller(), Filter{ProjectSlug: "decisions", State: Accepted, Tag: "data", Query: "one"})
	if err != nil || len(records) != 1 || records[0].ID != a.ID {
		t.Fatalf("filters: %+v %v", records, err)
	}
	records, err = s.List(testCaller(), Filter{Tag: "data' OR true --"})
	if err != nil || records == nil || len(records) != 0 {
		t.Fatalf("parameterized empty result: %+v %v", records, err)
	}
	if records, err := s.List(testCaller(), Filter{State: 255}); records != nil || !errors.Is(err, ErrState) {
		t.Fatalf("invalid state filter: %+v %v", records, err)
	}
	counts, err := s.Counts(testCaller(), []ids.ID{p, other, idstest.N(99999)})
	if err != nil || counts[p] != 2 || counts[other] != 1 || counts[idstest.N(99999)] != 0 {
		t.Fatalf("batch counts: %+v %v", counts, err)
	}
	summary, err := s.ForChange(testCaller(), p, change)
	if err != nil || len(summary) != 1 || summary[0].Status != Accepted || summary[0].ID != a.ID {
		t.Fatalf("Change summary: %+v %v", summary, err)
	}
	// A malformed historical foreign link still cannot satisfy this project's gate.
	if _, err := adrssql.New(db.SQL()).InsertADR(context.Background(), adrssql.InsertADRParams{ID: ids.New(time.Now()), ProjectID: other, Number: 9, Title: "foreign link", Status: Accepted.String(), ChangeID: change, Tags: "data,storage"}); err != nil {
		t.Fatal(err)
	}
	err = db.InTx(context.Background(), func(tx *database.Tx) error {
		if err := lockProject(testCaller(), tx, p); err != nil {
			return err
		}
		matches, err := MatchingIn(testCaller(), tx, MatchRequest{ProjectID: p, ChangeID: change, State: Accepted, Tags: []string{" data ", "storage"}})
		if err != nil || len(matches) != 1 || matches[0].ID != a.ID {
			t.Fatalf("matching ownership/tags: %+v %v", matches, err)
		}
		matches, err = MatchingIn(testCaller(), tx, MatchRequest{ProjectID: p, ChangeID: change, State: Accepted, Tags: []string{"dat"}})
		if err != nil || matches == nil || len(matches) != 0 {
			t.Fatalf("partial tag matched: %+v %v", matches, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestRequiredQueriesDoNotBecomeMissingOrEmpty(t *testing.T) {
	t.Parallel()
	s, p := fixture(t)
	v := create(t, s, p, "record")
	cases := []struct {
		query string
		read  func() error
	}{
		{"-- name: ADRScopeMeta :one", func() error {
			got, err := s.Get(testCaller(), v.ID)
			if got != nil {
				t.Fatal("scope failure returned a record")
			}
			return err
		}},
		{"SET TRANSACTION", func() error {
			got, err := s.Get(testCaller(), v.ID)
			if got != nil {
				t.Fatal("snapshot failure returned a record")
			}
			return err
		}},
		{"-- name: ADRByID :one", func() error {
			got, err := s.Get(testCaller(), v.ID)
			if got != nil {
				t.Fatal("point failure returned a record")
			}
			return err
		}},
		{"-- name: SuccessorID :one", func() error {
			got, err := s.Get(testCaller(), v.ID)
			if got != nil {
				t.Fatal("successor failure returned a partial detail")
			}
			return err
		}},
		{"-- name: ADRsMatching :many", func() error {
			got, err := s.List(testCaller(), Filter{ProjectID: p})
			if got != nil {
				t.Fatal("list failure returned rows")
			}
			return err
		}},
		{"-- name: ADRCountsByProject :many", func() error {
			got, err := s.Counts(testCaller(), []ids.ID{p})
			if got != nil {
				t.Fatal("count failure returned counts")
			}
			return err
		}},
		{"-- name: ADRsForChange :many", func() error {
			got, err := s.ForChange(testCaller(), p, idstest.N(1))
			if got != nil {
				t.Fatal("summary failure returned rows")
			}
			return err
		}},
	}
	for _, c := range cases {
		dbtest.FailNextStatement(t, s.records.db, c.query)
		if err := c.read(); !(errors.Is(err, database.ErrInjectedFailure) || err != nil && strings.Contains(err.Error(), "forge_injected_failure")) {
			t.Fatalf("required read %s: %v", c.query, err)
		}
	}
	if got, err := s.Get(testCaller(), idstest.N(99999)); got != nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing record: %+v %v", got, err)
	}
	if n, err := adrssql.New(s.records.db.SQL()).EditADR(context.Background(), adrssql.EditADRParams{ID: v.ID, ProjectID: p, OldStatus: v.Status.String(), Status: "unexpected", Title: v.Title, BodyMd: v.BodyMD, Tags: v.Tags, ChangeID: v.ChangeID}); err != nil || n != 1 {
		t.Fatalf("invalid stored state: %d %v", n, err)
	}
	if got, err := s.Get(testCaller(), v.ID); got != nil || !errors.Is(err, ErrState) {
		t.Fatalf("invalid record escaped: %+v %v", got, err)
	}
	if got, err := s.List(testCaller(), Filter{ProjectID: p}); got != nil || !errors.Is(err, ErrState) {
		t.Fatalf("invalid list escaped: %+v %v", got, err)
	}
}
func TestCancellationReleasesProjectAdmission(t *testing.T) {
	t.Parallel()
	s, p := fixture(t)
	gate, err := s.records.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gate.Rollback() }()
	if err := lockProject(testCaller(), gate, p); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(testCaller())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := s.Create(ctx, Create{ProjectID: p, Title: "cancelled"}); result <- err }()
	dbtest.WaitForLock(t, s.records.db, "FROM projects WHERE id")
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled admission: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled command kept its transaction")
	}
	counts, err := adrssql.New(s.records.db.SQL()).ADRCountsByProject(context.Background(), []ids.ID{p})
	if err != nil || len(counts) != 0 {
		t.Fatalf("cancelled admission left records: %+v %v", counts, err)
	}
	wantCount(t, "receipts", len(ledger(t, s.records.db, "")), 0)
}
func TestProjectRemovalRefusesSuppressedDeletion(t *testing.T) {
	t.Parallel()
	s, p := fixture(t)
	db := s.records.db
	a := create(t, s, p, "one")
	create(t, s, p, "two")
	completionExec(t, db, `CREATE FUNCTION veto_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.number=1 THEN RETURN NULL; END IF; RETURN OLD; END $$`)
	completionExec(t, db, `CREATE TRIGGER veto_delete BEFORE DELETE ON adrs FOR EACH ROW EXECUTE FUNCTION veto_delete()`)
	before := snapshot(t, db)
	remove := func() error {
		return db.InTx(context.Background(), func(tx *database.Tx) error {
			if err := lockProject(testCaller(), tx, p); err != nil {
				return err
			}
			return RemoveProjectIn(testCaller(), tx, p)
		})
	}
	if err := remove(); !errors.Is(err, ErrConflict) {
		t.Fatalf("partial deletion succeeded: %v", err)
	}
	if after := snapshot(t, db); after != before {
		t.Fatal("failed participant left a partial deletion")
	}
	completionExec(t, db, `DROP TRIGGER veto_delete ON adrs`)
	if err := remove(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(testCaller(), a.ID); got != nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("removed record: %+v %v", got, err)
	}
}
