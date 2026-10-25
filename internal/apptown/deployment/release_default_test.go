package deployment

import (
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func TestExplicitReleaseDefaultKeepsIdentityAndRequiresCurrentRevision(t *testing.T) {
	s, authority, in, target := configurationFixture(t)
	dep := configureForTest(t, s, in)
	empty, err := s.ReadReleaseDefault(t.Context(), in.ProjectID)
	if err != nil || !empty.DeploymentID.IsZero() || empty.Revision != 0 {
		t.Fatalf("implicit fallback: %+v %v", empty, err)
	}
	chosen, err := s.SetReleaseDefault(t.Context(), in.ProjectID, dep.ID, 0)
	if err != nil || chosen.DeploymentID != dep.ID || chosen.Revision != 1 {
		t.Fatalf("choice: %+v %v", chosen, err)
	}
	if _, err := s.SetReleaseDefault(t.Context(), in.ProjectID, ids.Nil, 0); err == nil {
		t.Fatal("stale default edit accepted")
	}
	if _, err := s.EditDeploymentLabel(t.Context(), in.ProjectID, dep.ID, dep.DisplayLabel, "Customer environment"); err != nil {
		t.Fatal(err)
	}
	after, err := s.ReadReleaseDefault(t.Context(), in.ProjectID)
	if err != nil || after != chosen {
		t.Fatalf("label changed default: %+v %v", after, err)
	}
	in.Feed = "landing"
	if _, err := s.Configure(t.Context(), in); err == nil {
		t.Fatal("default became landing-fed")
	}
	if err := s.Targets.Retire(t.Context(), target.Name, "alice"); err == nil {
		t.Fatal("default target retired without clearing selection")
	}
	foreign := createTestProject(t, s.db, "foreign-default", "Foreign")
	if _, err := s.SetReleaseDefault(t.Context(), foreign, dep.ID, 0); err == nil {
		t.Fatal("cross-project default accepted")
	}
	dbtest.FailNextStatement(t, s.db, "-- name: AppendReceipt")
	if _, err := s.SetReleaseDefault(t.Context(), in.ProjectID, ids.Nil, chosen.Revision); err == nil {
		t.Fatal("receipt failure hidden")
	}
	after, err = s.ReadReleaseDefault(t.Context(), in.ProjectID)
	if err != nil || after != chosen {
		t.Fatalf("default leaked from failed receipt: %+v %v", after, err)
	}
	authority.denial = errors.New("withdrawn")
	if _, err := s.ReadReleaseDefault(t.Context(), in.ProjectID); !errors.Is(err, authority.denial) {
		t.Fatalf("missing read authority refusal: %v", err)
	}
	if _, err := s.SetReleaseDefault(t.Context(), in.ProjectID, ids.Nil, chosen.Revision); !errors.Is(err, authority.denial) {
		t.Fatalf("missing authority refusal: %v", err)
	}
	authority.denial = nil
	cleared, err := s.SetReleaseDefault(t.Context(), in.ProjectID, ids.Nil, chosen.Revision)
	if err != nil || !cleared.DeploymentID.IsZero() || cleared.Revision != 2 {
		t.Fatalf("clear: %+v %v", cleared, err)
	}
	if _, err := s.Configure(t.Context(), in); err != nil {
		t.Fatalf("cleared choice blocked normal configuration: %v", err)
	}
}

func TestRemovalClearsDefaultOnlyWithCommittedSettlement(t *testing.T) {
	s, _, _, dep := removalFixture(t)
	chosen, err := s.SetReleaseDefault(t.Context(), dep.ProjectID, dep.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	settle := func() error {
		_, err := database.Transact(t.Context(), s.db, func(tx *database.Tx) ([]string, error) {
			return RemoveRecordedIn(t.Context(), tx, dep, false, RemovalSettlement{Target: "cloud", By: access.Principal{Name: "alice"}, At: time.Now()})
		})
		return err
	}
	dbtest.FailNextStatement(t, s.db, "-- name: AppendReceipt")
	if err := settle(); err == nil {
		t.Fatal("settlement receipt failure hidden")
	}
	current, err := s.ReadReleaseDefault(t.Context(), dep.ProjectID)
	if err != nil || current != chosen {
		t.Fatalf("failed settlement cleared default: %+v %v", current, err)
	}
	if err := settle(); err != nil {
		t.Fatal(err)
	}
	current, err = s.ReadReleaseDefault(t.Context(), dep.ProjectID)
	if err != nil || !current.DeploymentID.IsZero() || current.Revision != chosen.Revision+1 {
		t.Fatalf("committed settlement retained default: %+v %v", current, err)
	}
}
