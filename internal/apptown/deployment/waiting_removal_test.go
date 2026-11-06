package deployment

import (
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
)

// A take-down that cannot be admitted must leave the approval and its history
// untouched; a committed admission cancels before the deployment disappears.
func TestWaitingReleaseCancellationSharesRemovalAdmission(t *testing.T) {
	s, in, _, _ := requestFixture(t)
	original, err := s.Deployments.ByName(t.Context(), in.ProjectID, in.DeploymentName)
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.EnsureStanding(t.Context(), in.ProjectID, "production", original.TargetID, Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	dep, err = s.SetFeed(t.Context(), in.ProjectID, dep.Name, FeedSettings{Revision: dep.FeedRevision, Feed: "manual", ApprovalRole: access.RoleReleaseManager})
	if err != nil {
		t.Fatal(err)
	}
	in.DeploymentName = dep.Name
	accepted, err := s.RequestRelease(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.Releases.Events(t.Context(), accepted.ID)
	if err != nil {
		t.Fatal(err)
	}
	veto := errors.New("removal admission refused")
	for _, commit := range []bool{false, true} {
		err := s.db.InTx(t.Context(), func(tx *database.Tx) error {
			selected, err := SelectRemovalIn(t.Context(), tx, dep.ProjectID, dep.Name, false)
			if err != nil {
				return err
			}
			if err := CancelWaitingForRemovalIn(t.Context(), tx, selected.ID, access.Principal{Name: "alice"}, s.now()); err != nil {
				return err
			}
			if !commit {
				return veto
			}
			return nil
		})
		if (!commit && !errors.Is(err, veto)) || (commit && err != nil) {
			t.Fatal(err)
		}
		rel, err := s.Releases.ByNumber(t.Context(), dep.ProjectID, accepted.Number)
		if err != nil {
			t.Fatal(err)
		}
		events, err := s.Releases.Events(t.Context(), rel.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !commit {
			if rel.State != releaseWaiting || len(events) != len(before) {
				t.Fatalf("refused removal changed approval: %+v %+v", rel, events)
			}
		} else if rel.State != releaseCancelled || len(events) != len(before)+1 {
			t.Fatalf("admitted removal did not cancel: %+v %+v", rel, events)
		}
	}
}
