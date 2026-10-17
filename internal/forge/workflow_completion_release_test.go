package forge

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

// The release a landing records names who approved the landing, so the
// release history and the departures board can say who answered for it.
func TestLandingReleaseRecordsWhoApprovedIt(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ctx := context.Background()
	var pid ids.ID
	receipt, err := database.Transact(ctx, db, func(tx *database.Tx) (workflow.ReleaseReceipt, error) {
		var err error
		pid, err = project.CreateAt(ctx, tx, time.Now(), project.Spec{Slug: "landed"})
		if err != nil {
			return workflow.ReleaseReceipt{}, err
		}
		ch, err := workflow.CreateChangeIn(ctx, tx, workflow.ChangeDraft{ProjectID: pid, Title: "heading"}, "bls", time.Now())
		if err != nil {
			return workflow.ReleaseReceipt{}, err
		}
		if _, err = workflow.TransitionChangeIn(ctx, tx, pid, ch.Change.ID, workflow.ChangeMove{Action: workflow.ShipChange}, time.Now()); err != nil {
			return workflow.ReleaseReceipt{}, err
		}
		return workflowCompletionEffects{}.RecordCompletionIn(ctx, tx, workflow.CompletedRun{ProjectID: pid, ChangeID: ch.Change.ID, Release: true, ApprovedBy: "bls"})
	})
	if err != nil {
		t.Fatal(err)
	}
	releases := deployment.NewReleases(db)
	recent, err := releases.Recent(ctx, []ids.ID{pid}, 5)
	if err != nil || len(recent) != 1 || recent[0].Number != receipt.Number || recent[0].ApprovedBy != "bls" {
		t.Fatalf("release: %+v %v", recent, err)
	}
}
