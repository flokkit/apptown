package deployment

import "testing"

func TestReviewConfirmedFollowsMainReleasesToPortainer(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	bundle := p.publish(t)
	target := p.target(t, IntranetTargetName)
	dep, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, "automatic-lan", target.ID, Attributes{Feed: "main", Audience: "organisation", DataClass: "non-production"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.s.ConfirmFeed(t.Context(), p.project, dep.Name, dep.FeedRevision); err != nil {
		t.Fatal(err)
	}
	if err := p.s.RequestLandingReleases(t.Context(), p.project, bundle.BuiltFrom.RunID, bundle.BuiltFrom.ChangeID, "approver"); err != nil {
		t.Fatal(err)
	}
	requests, err := p.s.Releases.ForProject(t.Context(), p.project, 50)
	if err != nil || len(requests) != 1 {
		t.Fatalf("requests: %+v %v", requests, err)
	}
	if err := p.s.ExecuteRelease(t.Context(), requests[0].ID); err != nil {
		t.Fatalf("confirmed follows-main release: %v", err)
	}
	released, err := p.s.Releases.ByNumber(t.Context(), p.project, requests[0].Number)
	if err != nil || released.State != Released {
		t.Fatalf("release: %+v %v", released, err)
	}
}
