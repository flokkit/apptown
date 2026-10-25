package deployment

import (
	"context"
	"errors"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
)

func TestPromotionPolicyIsSeparateFromDataAndRequiresRevisionAndReceipt(t *testing.T) {
	s, authority, in, _ := configurationFixture(t)
	dep := configureForTest(t, s, in)
	changed, err := s.SetPromotionPolicy(t.Context(), in.ProjectID, dep.ID, dep.PolicyRevision, true)
	if err != nil || !changed.RequirePromotion || !changed.needsPromotion() || changed.Protected() != dep.Protected() || changed.DataClass != dep.DataClass {
		t.Fatalf("policy/data separation: %+v %v", changed, err)
	}
	if _, err := s.SetPromotionPolicy(t.Context(), in.ProjectID, dep.ID, dep.PolicyRevision, false); err == nil {
		t.Fatal("stale policy accepted")
	}
	dbtest.FailNextStatement(t, s.db, "-- name: AppendReceipt")
	if _, err := s.SetPromotionPolicy(t.Context(), in.ProjectID, dep.ID, changed.PolicyRevision, false); err == nil {
		t.Fatal("policy receipt failure hidden")
	}
	after, err := s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil || !after.RequirePromotion || after.PolicyRevision != changed.PolicyRevision {
		t.Fatalf("failed policy edit leaked: %+v %v", after, err)
	}
	authority.denial = errors.New("withdrawn")
	if _, err := s.SetPromotionPolicy(t.Context(), in.ProjectID, dep.ID, changed.PolicyRevision, false); !errors.Is(err, authority.denial) {
		t.Fatalf("policy authority: %v", err)
	}
}

func TestReleaseSetupRecordsChoiceAtomicallyWithoutStartingRelease(t *testing.T) {
	s, _, in, target := configurationFixture(t)
	setup := ReleaseSetup{TargetID: target.ID, DeploymentName: "new-release", TargetRevision: &target.SettingsRevision, RequirePromotion: true}
	dbtest.FailNextStatement(t, s.db, "-- name: AppendReceipt")
	if _, err := s.SetupReleaseDefault(t.Context(), in.ProjectID, setup); err == nil {
		t.Fatal("setup receipt failure hidden")
	}
	if _, err := s.Deployments.ByName(t.Context(), in.ProjectID, setup.DeploymentName); err == nil {
		t.Fatal("failed setup left deployment")
	}
	selected, err := s.SetupReleaseDefault(t.Context(), in.ProjectID, setup)
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.ByID(t.Context(), selected.DeploymentID)
	if err != nil || dep.DesiredGeneration != 0 || !dep.RequirePromotion {
		t.Fatalf("setup released or lost policy: %+v %v", dep, err)
	}
	if _, err := s.SetupReleaseDefault(t.Context(), in.ProjectID, setup); err == nil {
		t.Fatal("stale setup accepted")
	}
}

type policyAuthority struct {
	denied access.Capability
	seen   []access.Permission
}

func (a *policyAuthority) Require(_ context.Context, p access.Permission) (access.Caller, error) {
	a.seen = append(a.seen, p)
	if p.Capability == a.denied {
		return access.Caller{}, access.ErrAuthorityUnavailable
	}
	return access.Caller{Principal: access.Principal{ID: idstest.N(501), Kind: "user", Name: "policy-actor", Credential: "header:policy-actor"}, Verified: true}, nil
}
func TestPromotionPolicyCannotBeRelaxedWithBindingPermissionAlone(t *testing.T) {
	s, _, in, _ := configurationFixture(t)
	dep := configureForTest(t, s, in)
	auth := &policyAuthority{denied: access.CapReleasePromote}
	s.configuration.Authority = auth
	if _, err := s.SetPromotionPolicy(t.Context(), in.ProjectID, dep.ID, dep.PolicyRevision, false); err == nil {
		t.Fatal("binder relaxed promotion policy without promotion permission")
	}
	if len(auth.seen) != 2 || auth.seen[1].Resource.ProjectID != in.ProjectID || auth.seen[1].Resource.ID != dep.Name {
		t.Fatalf("promotion scope: %+v", auth.seen)
	}
	after, err := s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil || after.PolicyRevision != dep.PolicyRevision {
		t.Fatalf("denied policy edit persisted: %+v %v", after, err)
	}
}

func (a *policyAuthority) RequireStill(ctx context.Context, _ access.Principal, p access.Permission) error {
	_, err := a.Require(ctx, p)
	return err
}

func TestReleaseAdmissionCapturesLockedPromotionPolicyAndExecutionKeepsBothGates(t *testing.T) {
	s, in, _, sources := requestFixture(t)
	auth := &policyAuthority{}
	s.configuration.Authority = auth
	s.release.Authority = auth
	s.release.Still = auth
	dep, err := s.Deployments.ByName(t.Context(), in.ProjectID, in.DeploymentName)
	if err != nil {
		t.Fatal(err)
	}
	sources.before = func() {
		sources.before = nil
		if _, err := s.SetPromotionPolicy(t.Context(), in.ProjectID, dep.ID, dep.PolicyRevision, true); err != nil {
			t.Fatal(err)
		}
	}
	accepted, err := s.RequestRelease(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := deploymentsql.New(s.db.SQL()).RequestedReleaseForUpdate(t.Context(), accepted.ID)
	if err != nil || !recorded.ReleaseRequest.RequirePromotion {
		t.Fatalf("locked policy not captured: %+v %v", recorded.ReleaseRequest, err)
	}
	current, err := s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	current, err = s.SetPromotionPolicy(t.Context(), in.ProjectID, dep.ID, current.PolicyRevision, false)
	if err != nil {
		t.Fatal(err)
	}
	auth.denied = access.CapReleasePromote
	principal := access.Principal{ID: idstest.N(501), Kind: "user", Name: "policy-actor", Credential: "header:policy-actor"}
	if err := s.requireReleaseAuthority(t.Context(), principal, dep.ID, true); err == nil {
		t.Fatal("relaxed current policy erased admitted promotion gate")
	}
	auth.denied = ""
	if _, err := s.SetPromotionPolicy(t.Context(), in.ProjectID, dep.ID, current.PolicyRevision, true); err != nil {
		t.Fatal(err)
	}
	auth.denied = access.CapReleasePromote
	if err := s.requireReleaseAuthority(t.Context(), principal, dep.ID, false); err == nil {
		t.Fatal("admitted unprotected policy bypassed current promotion gate")
	}
}

func TestReleaseAdmissionRefusesPromotionWithdrawnBeforeLockedPolicyCheck(t *testing.T) {
	s, in, _, sources := requestFixture(t)
	auth := &policyAuthority{}
	s.configuration.Authority = auth
	s.release.Authority = auth
	dep, err := s.Deployments.ByName(t.Context(), in.ProjectID, in.DeploymentName)
	if err != nil {
		t.Fatal(err)
	}
	sources.before = func() {
		sources.before = nil
		if _, err := s.SetPromotionPolicy(t.Context(), in.ProjectID, dep.ID, dep.PolicyRevision, true); err != nil {
			t.Fatal(err)
		}
		auth.denied = access.CapReleasePromote
	}
	got, err := s.RequestRelease(t.Context(), in)
	if !errors.Is(err, access.ErrAuthorityUnavailable) || !got.ID.IsZero() {
		t.Fatalf("locked admission accepted withdrawn promotion: %+v %v", got, err)
	}
	wantN(t, "releases", everyRelease(t, s.db), 0)
}
