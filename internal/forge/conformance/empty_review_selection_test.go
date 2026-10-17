package conformance

import (
	"testing"

	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestReviewKeepsExplicitEmptySelection(t *testing.T) {
	t.Parallel()
	db, _ := reviewFixture(t)
	service := newServiceWithAuthority(db, testAuthority{}, unavailableHeadSource{testCheckSource{}})
	ctx := caller("reviewer")
	for _, selection := range [][]ids.ID{{}, {idstest.N(999999), idstest.N(999999)}} {
		execution, err := service.BeginReview(ctx, ReviewRequest{Trigger: "manual", Projects: selection})
		if err != nil {
			t.Fatal(err)
		}
		projects, err := execution.Projects(ctx)
		if err != nil || len(projects) != 0 {
			t.Fatalf("selection %v expanded to estate: %+v %v", selection, projects, err)
		}
		if err := execution.Progress(ctx, 0, ""); err != nil {
			t.Fatal(err)
		}
		review, err := execution.Finish(ctx, Completion{}, ReviewDone)
		if err != nil || review.ProjectsTotal != 0 {
			t.Fatalf("selection %v counted projects: %+v %v", selection, review, err)
		}
		detail, err := service.Review(ctx, review.ID)
		if err != nil || len(detail.Verdicts) != 0 {
			t.Fatalf("selection %v recorded verdicts: %+v %v", selection, detail, err)
		}
	}
}
