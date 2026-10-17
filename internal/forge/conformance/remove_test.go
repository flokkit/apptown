package conformance

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/conformance/internal/conformancesql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/stamp"

	"flokkit.io/forge/internal/database"
)

func TestRemoveProjectInKeepsOtherProjects(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	a := createTestProject(t, db, "a", "A", "")
	b := createTestProject(t, db, "b", "B", "")
	for _, pid := range []ids.ID{a, b} {
		if _, err := queries(db).InsertDisposition(context.Background(), conformancesql.InsertDispositionParams{ID: ids.New(stamp.Of(time.Now()).Time), ProjectID: pid, Status: "accepted", Justification: "why", By: "bls", CreatedAt: stamp.Of(time.Now()).Time}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error { return RemoveProjectIn(context.Background(), tx, a) }); err != nil {
		t.Fatal(err)
	}
	na, nb := projectDispositions(t, db, a), projectDispositions(t, db, b)
	if na != 0 || nb != 1 {
		t.Fatalf("a=%d b=%d", na, nb)
	}
}
