package adoption

import (
	"context"
	"errors"

	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func adoptionVerifiedContext(user string) context.Context {
	return access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: user, Via: "token"}, Verified: true, Receipt: user})
}
func policyCaller(id *access.Principal, receipt string) context.Context {
	var pr access.Principal
	if id != nil {
		pr = *id
	}
	return access.WithCaller(context.Background(), access.Caller{Principal: pr, Verified: id != nil, Receipt: receipt})
}
func TestAdoptionAuthoritySeparatesIdentityAttributionAndCapabilities(t *testing.T) {
	t.Parallel()
	authority, auth := adoptionPolicyFixture(t)
	selection := authorization{Operation: OperationSelect, ProjectID: idstest.N(1), Actor: "owner"}
	pack := authorization{Operation: OperationApplyPack, ProjectID: idstest.N(1), Actor: "owner"}
	ctx := adoptionVerifiedContext("owner")
	if err := authority.require(ctx, pack); err != nil {
		t.Fatalf("existing pack selector refused: %v", err)
	}
	// Adopting a selection is choosing the project's way of working: a
	// maintainer holds that right, and ordinary metadata configuration alone
	// does not carry it.
	if err := authority.require(ctx, selection); err != nil {
		t.Fatalf("maintainer refused selecting the way of working: %v", err)
	}
	apptownd := selection
	apptownd.Actor = "boss"
	if err := authority.require(ctx, apptownd); err == nil {
		t.Fatal("receipt name replaced verified identity")
	}
	if err := authority.require(context.Background(), selection); !errors.Is(err, access.ErrPrincipalRequired) {
		t.Fatalf("bare actor established identity: %v", err)
	}
	unverified := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "owner"}, Receipt: "owner"})
	if err := authority.require(unverified, selection); !errors.Is(err, access.ErrPrincipalRequired) {
		t.Fatalf("unverified header after bootstrap: %v", err)
	}
	outsider := authorization{Operation: OperationSelect, ProjectID: idstest.N(1), Actor: "outsider"}
	if err := authority.require(adoptionVerifiedContext("outsider"), outsider); err == nil {
		t.Fatal("outsider configured project")
	}
	policyGrant(t, auth, "user", "outsider", "", string(access.CapProjectConfigure), access.ScopeProject, idstest.N(1).String(), "fixture", "boss", "")
	if err := authority.require(adoptionVerifiedContext("outsider"), outsider); err == nil {
		t.Fatal("ordinary configuration admitted selecting the way of working")
	}
	selection.Weakens = true
	if err := authority.require(ctx, selection); err == nil {
		t.Fatal("project configuration granted standards weakening")
	}
	policyGrant(t, auth, "user", "owner", "", string(access.CapStandardsPacks), access.ScopeInstance, "", "fixture", "boss", "")
	if err := authority.require(ctx, selection); err != nil {
		t.Fatalf("explicit standards authority refused: %v", err)
	}
	grants, err := auth.ActiveGrants(ctx, access.Principal{Kind: "user", Name: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range grants {
		if g.Capability == access.CapStandardsPacks {
			if _, err := auth.Revoke(ctx, g.ID, "boss"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := authority.require(ctx, selection); err == nil {
		t.Fatal("revoked standards authority stayed cached")
	}
	selection.Weakens = false
	boss, err := identity.ActorIn(context.Background(), authority.db, "boss")
	if err != nil || boss.Principal.IsZero() {
		t.Fatalf("fixture administrator: %v", err)
	}
	disabled := true
	if err := auth.SaveUser(context.Background(), identity.AccountChange{Username: "owner", Disabled: &disabled}, access.Principal{ID: boss.Principal, Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	if err := authority.require(ctx, selection); err == nil {
		t.Fatal("disabled owner configured project")
	}
}

func TestAdoptionAuthorityBoundsSystemAndMCPContexts(t *testing.T) {
	t.Parallel()
	authority, _ := adoptionPolicyFixture(t)
	permission := authorization{Operation: OperationApplyPack, ProjectID: idstest.N(1), Actor: "owner via mcp/reviewer"}
	ctx := policyCaller(&access.Principal{Kind: "user", Name: "owner", Via: "token"}, permission.Actor)
	if err := authority.require(ctx, permission); err != nil {
		t.Fatalf("verified MCP attribution refused: %v", err)
	}
	permission.Actor = "owner via mcp/apptownd"
	if err := authority.require(ctx, permission); err == nil {
		t.Fatal("MCP changed its server-derived receipt label")
	}
	permission.Actor = "owner"
	if err := authority.require(policyCaller(nil, "owner"), permission); err == nil {
		t.Fatal("MCP receipt name established identity")
	}
	permission = authorization{Operation: OperationSelect, ProjectID: idstest.N(1), Actor: "apptown-system", Weakens: true}
	if err := authority.require(context.Background(), permission); !errors.Is(err, access.ErrPrincipalRequired) {
		t.Fatalf("system actor string granted permission: %v", err)
	}
	system := WithSystemAuthority(context.Background(), idstest.N(1))
	if err := authority.require(system, permission); err != nil {
		t.Fatalf("explicit completion scope refused: %v", err)
	}
	permission.ProjectID = idstest.N(2)
	if err := authority.require(system, permission); err == nil {
		t.Fatal("system completion escaped project")
	}
	permission.ProjectID = idstest.N(1)
	cancelled, cancel := context.WithCancel(system)
	cancel()
	if err := authority.require(cancelled, permission); !errors.Is(err, context.Canceled) {
		t.Fatalf("system scope ignored cancellation: %v", err)
	}
}

func TestAdoptionCannotUseBootstrapAsAuthority(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := New(db, Dependencies{})
	c := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "first-user"}, Receipt: "first-user"})
	if err := s.require(c, authorization{Operation: OperationSelect, ProjectID: idstest.N(1), Actor: "first-user"}); !errors.Is(err, access.ErrPrincipalRequired) {
		t.Fatalf("unverified setup caller: %v", err)
	}
}

type policyProjects struct{}

func (policyProjects) AuthorityResource(_ context.Context, id ids.ID) (access.Resource, error) {
	return access.Resource{ProjectID: id, ProjectName: "Project"}, nil
}
func adoptionPolicyFixture(t *testing.T) (*Service, *identity.Service) {
	t.Helper()
	db := testDB(t)
	auth := identity.New(db, identity.Config{})
	if err := auth.SeedAdministrator(context.Background(), "boss"); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"owner", "outsider", "boss"} {
		if _, err := auth.EnsureUser(context.Background(), user); err != nil {
			t.Fatal(err)
		}
	}
	policyGrant(t, auth, "user", "owner", access.RoleProjectMaintainer, "", access.ScopeProject, idstest.N(1).String(), "fixture", "boss", "")
	return New(db, Dependencies{Authority: access.Checked{Authority: auth, Projects: policyProjects{}}}), auth
}
func policyGrant(t *testing.T, s *identity.Service, kind, user, role, cap string, scope access.ScopeKind, id, origin, by, reason string) {
	t.Helper()
	if _, err := s.PutGrant(context.Background(), identity.Spec{SubjectType: kind, Subject: user, Role: role, Capability: cap, Scope: scope, ScopeID: id, Origin: origin, By: by, Reason: reason}); err != nil {
		t.Fatal(err)
	}
}
