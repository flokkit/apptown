package remediation

import (
	"context"
	"testing"

	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestConformanceRemediationMandateCannotChangeProjectOrObservation(t *testing.T) {
	t.Parallel()
	f, outcomes, item, _ := outcomeFixture(t)
	f.moveChange(t, item, workflow.ShipChange)
	item = f.outcome(t, item, Outcome{Kind: "landed"})
	checks := conformance.New(f.db, reconcileActor{}, attemptChecks{}, nil)
	service := New(f.db, Dependencies{Work: f.work, Caller: f.ctx, Checks: checks})
	reviews := func() int {
		t.Helper()
		page, err := checks.Reviews(f.ctx, 0) // zero is uncapped: every review
		if err != nil {
			t.Fatal(err)
		}
		return len(page)
	}
	reviewsBefore := reviews()
	apptownd := item
	apptownd.ProjectID = idstest.N(999999)
	if _, err := checks.BeginReview(conformance.WithVerification(context.Background(), service.VerificationScope(apptownd)), conformance.ReviewRequest{Projects: []ids.ID{apptownd.ProjectID}}); err == nil {
		t.Fatal("mandate moved to another project")
	}
	ctx := conformance.WithVerification(context.Background(), service.VerificationScope(item))
	if _, err := checks.BeginReview(ctx, conformance.ReviewRequest{}); err == nil {
		t.Fatal("project mandate became fleet authority")
	}
	if _, err := checks.BeginReview(ctx, conformance.ReviewRequest{Projects: []ids.ID{item.ProjectID, idstest.N(999999)}}); err == nil {
		t.Fatal("project mandate expanded selection")
	}
	if n := reviews(); n != reviewsBefore {
		t.Fatalf("refused mandates opened reviews: %d want %d", n, reviewsBefore)
	}
	admitted, err := checks.BeginReview(ctx, conformance.ReviewRequest{Projects: []ids.ID{item.ProjectID}})
	if err != nil {
		t.Fatal(err)
	}
	if current, err := outcomes.Record(ctx, item, Outcome{Kind: "verified", Verdict: conformance.DoesNotConform, ReviewID: admitted.Review().ID, Reason: "still fails"}); err != nil || current == nil {
		t.Fatalf("invalidate observation: %+v %v", current, err)
	}
	if _, err := checks.BeginReview(ctx, conformance.ReviewRequest{Projects: []ids.ID{item.ProjectID}}); err == nil {
		t.Fatal("stale observation admitted new work")
	}
	finished, err := admitted.Finish(context.Background(), conformance.Completion{}, conformance.ReviewError)
	if err != nil || finished.Actor != "apptown-system" {
		t.Fatalf("admitted work lost authority: %+v %v", finished, err)
	}
	if n := reviews(); n != reviewsBefore+1 {
		t.Fatalf("reviews: %d want %d", n, reviewsBefore+1)
	}
}
