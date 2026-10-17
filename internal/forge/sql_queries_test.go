package forge

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
)

// Forge's optional presentation lookups preserve absence. Project owns exact
// persisted fields, identifier precision and transaction isolation coverage.
func TestOptionalProjectLookupsPreserveAbsence(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	app := constructApp(t, db, Config{DataDir: t.TempDir()})
	if p := app.projectByID(context.Background(), idstest.N(999999)); p != nil {
		t.Fatalf("pointer lookup must preserve absence: %+v", p)
	}
	dbtest.FailNextStatement(t, db, "ProjectByID")
	if p := app.projectByID(context.Background(), idstest.N(999999)); p != nil {
		t.Fatalf("failed lookup must preserve absence: %+v", p)
	}

}

func TestOptionalGrantReadFailureFailsClosed(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	dbtest.FailNextStatement(t, db, "ActiveGrants")
	if got, err := identity.ActiveGrantsIn(context.Background(), db, access.Principal{Kind: "user", Name: "reader"}, time.Now()); err == nil || len(got) != 0 {
		t.Fatalf("failed authority read granted access: %+v %v", got, err)
	}
}

// The optional Forge presentation adapter treats an absent or unavailable
// current receipt as absent. Selection and decoding guarantees live in Adoption.
func TestOptionalCurrentAdoptionPreservesAbsence(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "receipt", Name: "Receipt"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := currentAdoption(db, id); got != nil {
		t.Fatalf("unconfigured project: %+v", got)
	}
	if got := currentAdoption(db, idstest.N(999999)); got != nil {
		t.Fatalf("unknown project: %+v", got)
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return adoption.RecordIn(t.Context(), tx, id, 1, adoption.AdoptionEdit, "created", adoption.EmptyReceipt(), "alice", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	if got := currentAdoption(db, id); got == nil || got.Revision != 1 {
		t.Fatalf("recorded revision: %+v", got)
	}
	dbtest.FailNextStatement(t, db, "-- name: CurrentAdoption :one")
	if got := currentAdoption(db, id); got != nil {
		t.Fatalf("unavailable revision: %+v", got)
	}
}
