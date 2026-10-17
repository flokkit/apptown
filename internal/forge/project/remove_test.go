package project

import (
	"context"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/ids"
)

// The root row goes last with its receipt; a missing row is never a
// silent success; a failure after the delete rolls the delete back.
func TestRemoveInIsReceiptedAndNeverSilent(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return CreateAt(t.Context(), tx, time.Now(), Spec{Slug: "gone", Name: "Gone"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return RemoveIn(context.Background(), tx, id, "bls", map[string]any{"why": "test"}, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	receipts, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: "project.deleted", Subject: "gone", Limit: audit.MaxLimit})
	if err != nil || len(receipts) != 1 || receipts[0].Kind != "project.deleted" || receipts[0].Actor != "bls" {
		t.Fatalf("receipt: %v %+v", err, receipts)
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error { return RemoveIn(context.Background(), tx, id, "bls", nil, time.Now()) }); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("a second removal is not silent: %v", err)
	}
	id, err = database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return CreateAt(t.Context(), tx, time.Now(), Spec{Slug: "kept", Name: "Kept"})
	})
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("a later participant failed")
	err = db.InTx(context.Background(), func(tx *database.Tx) error {
		if err := RemoveIn(context.Background(), tx, id, "bls", nil, time.Now()); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("the failure surfaces: %v", err)
	}
	if kept, err := ByIDIn(context.Background(), db, id); err != nil || kept.ID != id {
		t.Fatalf("the delete rolled back with the transaction: %v %+v", err, kept)
	}
}
