package deployment

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

// Release metadata belongs to Deployment. No workflow engine or AppTown App is
// needed to check selection, numbering, membership and malformed bundle input.
func TestWorkflowReleaseSelectsScopedMetadata(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"missing bundle", "matching bundle", "newest is another run", "another project", "malformed bundle"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			db := testDB(t)
			project := createTestProject(t, db, "metadata", "Metadata")
			change, err := workflow.NewCreator(db, func(context.Context, workflow.CreatedChange) error { return nil }).Create(context.Background(), workflow.ChangeDraft{ProjectID: project, Type: "feature", Title: "Shipped"}, "alice")
			if err != nil {
				t.Fatal(err)
			}
			if err := db.InTx(context.Background(), func(tx *database.Tx) error {
				_, err := workflow.TransitionChangeIn(context.Background(), tx, project, change.ID, workflow.ChangeMove{Action: workflow.ShipChange}, time.Now())
				return err
			}); err != nil {
				t.Fatal(err)
			}
			run := idstest.N(17) // opaque Work identity supplied to the participant
			deployment := storedDeploymentFixture(t, db, deploymentsql.EnsureStandingParams{ID: ids.New(time.Now()), ProjectID: project, Name: "main"})
			// sql-guarantee: TestWorkflowReleaseSelectsScopedMetadata needs one sparse planned number-seven row so MAX-based numbering yields eight independently of row count and state; CreateRelease only allocates numbers.
			mustInsert(t, db, `INSERT INTO releases(id,project_id,number,state,created_at) VALUES ($1,$2,7,'planned','2026-10-01')`, project)
			var bundle ids.ID
			if scenario != "missing bundle" {
				if scenario == "malformed bundle" {
					bundle = storedBundleFixture(t, db, deploymentsql.InsertBundleParams{ID: ids.New(time.Now()), ProjectID: project, ContractRevision: "tree", BuiltFromJson: []byte(`{"runId":"corrupt"}`)})
				} else {
					bundle = cutBundleFixture(t, db, project, "tree", BundleOrigin{RunID: idstest.N(17)})
				}
			}
			switch scenario {
			case "newest is another run":
				cutBundleFixture(t, db, project, "newer", BundleOrigin{RunID: idstest.N(18)})
				bundle = ids.Nil
			case "another project":
				other := createTestProject(t, db, "other", "Other")
				cutBundleFixture(t, db, other, "other", BundleOrigin{RunID: idstest.N(18)})
			}
			var receipt NumberedRelease
			err = db.InTx(context.Background(), func(tx *database.Tx) error {
				if err := lockReleaseProjectIn(tx, project); err != nil {
					return err
				}
				var err error
				receipt, err = CreateReleaseIn(context.Background(), tx, ReleaseDraft{At: time.Now(), Source: ReleaseFromWorkflow, ProjectID: project, RunID: run, ChangeID: change.ID, Deployment: "main"})
				return err
			})
			if scenario == "malformed bundle" {
				if err == nil {
					t.Fatal("malformed bundle became an empty link")
				}
				// sql-guarantee: no release of this run may exist at any number; the typed listing refuses the sparse planned #7, so only a raw count sees every row.
				dbtest.Count(t, db, 0, `SELECT COUNT(*) FROM releases WHERE run_id=$1`, run)
				// The one change is every change.
				wantN(t, "releases carrying the change", changeReleases(t, db, project, change.ID), 0)
				return
			}
			if err != nil || receipt.Number != 8 {
				t.Fatalf("release: %+v %v", receipt, err)
			}
			// The sparse #7 is in a state the typed listing refuses, so read #8 alone.
			r, err := NewReleases(db).ByNumber(context.Background(), project, receipt.Number)
			if err != nil {
				t.Fatal(err)
			}
			if r.ID != receipt.ID || r.State != Released || r.RunID != run || r.BundleID != bundle || r.DeploymentID != deployment {
				t.Fatalf("workflow release: %+v", r)
			}
			if rels := changeReleases(t, db, project, change.ID); len(rels) != 1 || rels[0].ID != receipt.ID {
				t.Fatalf("releases carrying the change: %+v", rels)
			}
		})
	}
}
