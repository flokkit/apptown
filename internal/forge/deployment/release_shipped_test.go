package deployment

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
	"flokkit.io/forge/internal/ids"
)

// What a change with no code of its own shipped is its newest released
// release's commit, compared with the newest earlier release released on the
// same deployment: not a failed release between them, not another
// deployment's, and not the change's own release that did not finish.
func TestShippedByComparesWithThePreviousReleaseOnTheDeployment(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ctx := context.Background()
	p := createTestProject(t, db, "shipped-by", "Shipped by")
	at := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	bundle := func(commit string) ids.ID {
		at = at.Add(time.Minute)
		return storedBundleFixture(t, db, deploymentsql.InsertBundleParams{ID: ids.New(at), ProjectID: p, ContractRevision: commit, CreatedAt: at})
	}
	release := func(number int, state, deployment, commit string) ids.ID {
		// sql-guarantee: TestShippedByComparesWithThePreviousReleaseOnTheDeployment needs released, failed and other-deployment releases in one history to test the read's selection; settling a release needs a deployment attempt per row, which is not what this read depends on.
		return mustInsert(t, db, `INSERT INTO releases(id,project_id,number,state,deployment,created_at,bundle_id) VALUES (?,?,?,?,?,'2026-10-21',?)`, p, number, state, deployment, bundle(commit))
	}
	change := shippedChangeAt(t, db, p, at)
	carry := func(rel ids.ID) {
		// sql-guarantee: as above; release membership is written by Start from the shipped set, which a fixed release history cannot choose.
		mustExec(t, db, `INSERT INTO release_changes(release_id,change_id) VALUES ($1,$2)`, rel, change)
	}
	store := NewReleases(db)

	if got, err := store.ShippedBy(ctx, p, change); err != nil || got != nil {
		t.Fatalf("a change no release carried: %+v %v", got, err)
	}
	release(1, Released.String(), "main", "c1")
	release(2, Released.String(), "staging", "s2") // another deployment
	release(3, ReleaseFailed.String(), "main", "f3")
	carry(release(4, Released.String(), "main", "c4"))
	carry(release(5, ReleaseFailed.String(), "main", "f5")) // the change's newest, failed
	got, err := store.ShippedBy(ctx, p, change)
	if err != nil {
		t.Fatal(err)
	}
	want := Shipped{Deployment: "main", Number: 4, Commit: "c4", Previous: 1, PreviousCommit: "c1"}
	if got == nil || *got != want {
		t.Fatalf("shipped: %+v, want %+v", got, want)
	}

	// The first release on its deployment has nothing earlier to compare.
	other := shippedChangeAt(t, db, p, at)
	// sql-guarantee: as above.
	mustExec(t, db, `INSERT INTO release_changes(release_id,change_id) VALUES ($1,$2)`, release(6, Released.String(), "preview", "p6"), other)
	got, err = store.ShippedBy(ctx, p, other)
	if want := (Shipped{Deployment: "preview", Number: 6, Commit: "p6"}); err != nil || got == nil || *got != want {
		t.Fatalf("first release: %+v %v, want %+v", got, err, want)
	}
}
