package adoption

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption/internal/adoptionsql"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestCurrentMetadataNeverInfersOrRepairsAReceipt(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	missing := adoptionTestProject(t, db, "missing", "Missing")
	for _, pid := range []ids.ID{missing, idstest.N(999999)} {
		if got, err := CurrentIn(context.Background(), db, pid); got != nil || !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("missing aggregate %d: %+v %v", pid, got, err)
		}
	}
	noCurrent(t, db, missing) // the only project; an aggregate needs one
	if id, err := CurrentReceiptIDIn(context.Background(), db, missing); err != nil || !id.IsZero() {
		t.Fatalf("known unconfigured project: %d %v", id, err)
	}
	if _, err := CurrentReceiptIDIn(context.Background(), db, idstest.N(999999)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown project became known: %v", err)
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return InitializeIn(t.Context(), tx, missing, 17, "selected-pack", "review this")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := adoptionsql.New(db.SQL()).InsertAdoption(context.Background(), adoptionsql.InsertAdoptionParams{ID: ids.New(time.Now()),
		ProjectID: missing, Revision: 18, Kind: "target", Digest: "newer", Receipt: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := CurrentIn(context.Background(), db, missing); err != nil || got.Revision != 17 || got.Pack != "selected-pack" || got.Drift != "review this" {
		t.Fatalf("metadata changed to history: %+v %v", got, err)
	}
	if got, err := CurrentReceiptIn(t.Context(), db, missing); got != nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("dangling pointer guessed receipt: %+v %v", got, err)
	}
	brokenRow, err := adoptionsql.New(db.SQL()).InsertAdoption(context.Background(), adoptionsql.InsertAdoptionParams{ID: ids.New(time.Now()),
		ProjectID: missing, Revision: 17, Kind: AdoptionEdit.String(), Digest: "broken", Receipt: []byte(`{"resolver":"not-a-number"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	broken := brokenRow.ID
	if got, err := CurrentReceiptIn(t.Context(), db, missing); got != nil || err == nil || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("broken receipt became absence: %+v %v", got, err)
	}
	if got, err := CurrentIn(context.Background(), db, missing); err != nil || got.Revision != 17 || got.Pack != "selected-pack" || got.Drift != "review this" {
		t.Fatalf("metadata decoded history: %+v %v", got, err)
	}
	if id, err := CurrentReceiptIDIn(context.Background(), db, missing); err != nil || id != broken {
		t.Fatalf("identity observation decoded history: %d %v", id, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := CurrentIn(ctx, db, missing); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %+v %v", got, err)
	}
}

func TestCurrentMutationInitializationAndReceiptShareTransaction(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := adoptionTestProject(t, db, "first", "First")
	if err := db.InTx(context.Background(), func(tx *database.Tx) error { _, err := CurrentForUpdateIn(context.Background(), tx, pid); return err }); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("lock read initialized missing aggregate: %v", err)
	}
	noCurrent(t, db, pid) // the only project; an aggregate needs one
	s := New(db, Dependencies{Ways: wow.New(db, wow.Dependencies{})})
	dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
	if _, err := s.Advance(t.Context(), pid, AdoptionEdit, "first", "alice", func(tx *database.Tx, prior *RecordedRevision, now time.Time) (Receipt, error) {
		return s.UnionIn(t.Context(), tx, pid, prior)
	}); err == nil {
		t.Fatal("failed receipt committed initial aggregate")
	}
	noCurrent(t, db, pid)
	if revs := revisionsOf(t, db, pid); len(revs) != 0 {
		t.Fatalf("failed receipt recorded a revision: %+v", revs)
	}
	if got, err := s.Advance(t.Context(), pid, AdoptionEdit, "first", "alice", func(tx *database.Tx, prior *RecordedRevision, now time.Time) (Receipt, error) {
		return s.UnionIn(t.Context(), tx, pid, prior)
	}); err != nil || got.Revision != 1 {
		t.Fatalf("first initialized mutation: %+v %v", got, err)
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return InitializeIn(t.Context(), tx, pid, 999, "replacement", "replacement")
	}); err == nil {
		t.Fatal("duplicate initialization rewrote current state")
	}
	if got, err := s.Current(t.Context(), pid); err != nil || got.Revision != 1 || got.Pack != "" || got.Drift != "" {
		t.Fatalf("duplicate changed aggregate: %+v %v", got, err)
	}
}
