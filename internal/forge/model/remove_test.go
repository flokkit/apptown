package model

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/model/internal/modelsql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestRemoveProjectInKeepsInstanceAndOthers(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	slots := New(db)
	for _, pid := range []ids.ID{ids.Nil, idstest.N(1), idstest.N(2)} {
		if err := slots.Set(context.Background(), "fast", pid, "", "standard", "x", "tester"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		if err := RemoveProjectIn(context.Background(), tx, ids.Nil); err != nil {
			return err
		}
		return RemoveProjectIn(context.Background(), tx, idstest.N(1))
	}); err != nil {
		t.Fatal(err)
	}
	stored := func(pid ids.ID, profile string) bool {
		_, err := queries(db).ModelSlotCell(context.Background(), modelsql.ModelSlotCellParams{Slot: "fast", ProjectID: pid, Profile: profile})
		if errors.Is(err, sql.ErrNoRows) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		return true
	}
	if n0, n1, n2 := stored(ids.Nil, StandardProfile), stored(idstest.N(1), ""), stored(idstest.N(2), ""); !n0 || n1 || !n2 {
		t.Fatalf("instance=%v a=%v b=%v", n0, n1, n2)
	}
	if instance, err := queries(db).ModelSlotsInstance(context.Background()); err != nil || len(instance) != 1 {
		t.Fatalf("instance rows: %+v %v", instance, err)
	}
}
