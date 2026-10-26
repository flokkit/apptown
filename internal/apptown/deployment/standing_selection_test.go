package deployment

import (
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"testing"
)

func TestStandingConsentSelectionRequiresExactProjectEnvironmentAndTarget(t *testing.T) {
	s, _, in, target := configurationFixture(t)
	dep := configureForTest(t, s, in)
	got, err := ReadStandingConsentSelection(t.Context(), s.db, in.ProjectID, dep.ID, target.ID)
	if err != nil || got.Resource.ProjectID != in.ProjectID || got.PromotionResource.ID != dep.Name || got.PromotionRequired != dep.needsPromotion() {
		t.Fatalf("wrong selected resource: %+v %v", got, err)
	}
	if _, err = ReadStandingConsentSelection(t.Context(), s.db, idstest.N(999), dep.ID, target.ID); err == nil {
		t.Fatal("another project's environment supplied standing consent")
	}
	other := *target
	other.ID = ids.Nil
	other.Name = "other-standing-target"
	peer, err := s.Targets.Put(t.Context(), &other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ReadStandingConsentSelection(t.Context(), s.db, in.ProjectID, dep.ID, peer.ID); err == nil {
		t.Fatal("target substitution supplied standing consent")
	}
	if _, err = ReadStandingConsentSelection(t.Context(), s.db, in.ProjectID, idstest.N(999), target.ID); err == nil {
		t.Fatal("missing environment supplied standing consent")
	}
	dbtest.FailNextStatement(t, s.db, "-- name: DeploymentByID :one")
	if _, err = ReadStandingConsentSelection(t.Context(), s.db, in.ProjectID, dep.ID, target.ID); err == nil {
		t.Fatal("unavailable environment supplied standing consent")
	}
}
