package deployment

import (
	"github.com/flokkit/apptown/internal/ids"
	"testing"
)

func TestFollowingMainSelectsOnlyStandingMainFeeds(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	target := p.target(t, IntranetTargetName)
	for _, feed := range []string{"main", "manual", "promotion"} {
		dep, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, feed, target.ID, Attributes{Feed: feed, Audience: "organisation", DataClass: "non-production"})
		if err != nil {
			t.Fatal(err)
		}
		if feed == "main" {
			if _, err := p.s.EditDeploymentLabel(t.Context(), p.project, dep.ID, dep.DisplayLabel, "Staff app"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := p.s.Deployments.RecordPreview(t.Context(), p.project, ids.New(p.s.now()), target.ID, "preview", 8080, "http://localhost:8080", nil); err != nil {
		t.Fatal(err)
	}
	rows, err := p.s.Deployments.FollowingMain(t.Context(), p.project)
	if err != nil || len(rows) != 1 || rows[0].Name != "main" || rows[0].DisplayLabel != "Staff app" || rows[0].Audience != "organisation" || rows[0].TargetLabel != target.DisplayLabel {
		t.Fatalf("destinations %+v: %v", rows, err)
	}
}
