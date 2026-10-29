package deployment

import (
	"context"
	"errors"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"testing"
)

type policyAuthority struct {
	denied access.Capability
}

func (a *policyAuthority) Require(_ context.Context, p access.Permission) (access.Caller, error) {
	if p.Capability == a.denied {
		return access.Caller{}, access.ErrAuthorityUnavailable
	}
	return access.Caller{Principal: access.Principal{ID: idstest.N(501), Kind: "user", Name: "policy-actor", Credential: "header:policy-actor"}, Verified: true}, nil
}
func (a *policyAuthority) RequireStill(ctx context.Context, _ access.Principal, p access.Permission) error {
	_, err := a.Require(ctx, p)
	return err
}

// Promotion permission follows classification. Admission records whether the
// locked deployment was protected, and execution requires promotion when
// either the admitted record or the current classification asks for it.
func TestReleaseAdmissionCapturesLockedProtectionAndExecutionKeepsBothGates(t *testing.T) {
	s, in, _, _ := requestFixture(t)
	auth := &policyAuthority{}
	s.configuration.Authority = auth
	s.release.Authority = auth
	s.release.Still = auth
	dep, err := s.Deployments.ByName(t.Context(), in.ProjectID, in.DeploymentName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deployments.EnsureStanding(t.Context(), in.ProjectID, dep.Name, dep.TargetID, Attributes{Feed: "promotion", Audience: "team", DataClass: "production"}); err != nil {
		t.Fatal(err)
	}
	accepted, err := s.RequestRelease(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := deploymentsql.New(s.db.SQL()).RequestedReleaseForUpdate(t.Context(), accepted.ID)
	if err != nil || !recorded.ReleaseRequest.RequirePromotion {
		t.Fatalf("locked protection not captured: %+v %v", recorded.ReleaseRequest, err)
	}
	unprotected, err := s.Deployments.EnsureStanding(t.Context(), in.ProjectID, "other", dep.TargetID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	principal := access.Principal{ID: idstest.N(501), Kind: "user", Name: "policy-actor", Credential: "header:policy-actor"}
	auth.denied = access.CapReleasePromote
	if err := s.requireReleaseAuthority(t.Context(), principal, unprotected.ID, false); err != nil {
		t.Fatalf("unprotected deployment needed promotion permission: %v", err)
	}
	if err := s.requireReleaseAuthority(t.Context(), principal, unprotected.ID, true); err == nil {
		t.Fatal("current classification erased the admitted promotion gate")
	}
	if err := s.requireReleaseAuthority(t.Context(), principal, dep.ID, false); err == nil {
		t.Fatal("admitted unprotected record bypassed the current promotion gate")
	}
}

func TestReleaseAdmissionRefusesPromotionWithdrawnBeforeLockedProtectionCheck(t *testing.T) {
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
		if _, err := s.Deployments.EnsureStanding(t.Context(), in.ProjectID, dep.Name, dep.TargetID, Attributes{Feed: "promotion", Audience: "team", DataClass: "production"}); err != nil {
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
