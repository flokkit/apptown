package deployment

import (
	"context"
	"testing"
)

// A release admitted before promoteAfter was removed keeps that key in its
// recorded attributes. The key carried no decision, so the release still
// executes against its unchanged deployment.
func TestAReleaseAdmittedWithAPromoteAfterKeyStillExecutes(t *testing.T) {
	t.Parallel()
	w := newDigestWorld(t, "", nil)
	b := w.publish(t)
	if _, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "prod", w.lab.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	accepted, err := w.s.RequestRelease(context.Background(), DeploySelection{ProjectID: w.project, DeploymentName: "prod", BundleID: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: the stored attributes of a request admitted by an older AppTown, which the current API cannot write.
	if _, err := w.db.SQL().ExecContext(context.Background(), `UPDATE release_requests SET attributes_json='{"feed":"promotion","audience":"team","data":"synthetic","promoteAfter":"main"}' WHERE release_id=$1`, accepted.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.s.ExecuteRelease(context.Background(), accepted.ID); err != nil {
		t.Fatal(err)
	}
	rel, err := w.s.Releases.ByNumber(context.Background(), w.project, accepted.Number)
	if err != nil || rel.State != Released {
		t.Fatalf("the admitted release: %+v %v", rel, err)
	}
}
