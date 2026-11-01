package attention

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"testing"
)

type checkEvidence struct {
	standing []conformance.ProjectStanding
	calls    int
	selected []ids.ID
}

func (s *checkEvidence) CurrentView(_ context.Context, projects []ids.ID) ([]conformance.ProjectStanding, error) {
	s.calls++
	s.selected = projects
	return s.standing, nil
}
func TestAppChecksOnlyRaiseConfirmedProblemsFromOneBatch(t *testing.T) {
	t.Parallel()
	owner := &checkEvidence{standing: []conformance.ProjectStanding{
		{ProjectID: idstest.N(1), Name: "Calendar", Slug: "calendar", Lanes: conformance.Lanes{Findings: 2, Broken: 1, Flagged: 4, Unassessed: 1}},
		{ProjectID: idstest.N(2), Name: "Unrun", Slug: "unrun", Lanes: conformance.Lanes{Unassessed: 8}},
	}}
	got, err := (Checks{Conformance: owner}).Snapshot(t.Context(), []ids.ID{idstest.N(1), idstest.N(2)})
	if err != nil || !got.Known || len(got.Items) != 1 || got.Items[0].Count != 3 || got.Items[0].Unknown || len(got.Coverage) != 0 || owner.calls != 1 || len(owner.selected) != 2 {
		t.Fatalf("snapshot: %+v, owner=%+v, %v", got, owner, err)
	}
}
