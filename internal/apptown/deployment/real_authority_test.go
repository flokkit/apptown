package deployment

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids"
)

type realAuthoritySources struct {
	projectID ids.ID
	slug      string
}

func (s realAuthoritySources) Project(context.Context, ids.ID) (ReleaseProject, error) {
	return ReleaseProject{Application: Application{ID: s.projectID, Slug: s.slug}}, nil
}
func (s realAuthoritySources) CurrentContract(context.Context, string) (*appcontract.Contract, error) {
	return &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true}}}, nil
}
func (s realAuthoritySources) Contract(context.Context, ReleaseProject, string) (*appcontract.Contract, string, error) {
	c, _ := s.CurrentContract(context.Background(), s.slug)
	return c, "selected", nil
}

func realAuthorityFixture(t *testing.T) (*Service, *identity.Service, *database.DB, ids.ID) {
	t.Helper()
	db := testDB(t)
	if err := jobs.Migrate(context.Background(), db.Pool()); err != nil {
		t.Fatal(err)
	}
	pid := createTestProject(t, db, "game", "G")
	accessStore := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := accessStore.SeedAdministrator(context.Background(), "boss"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"owner", "outsider"} {
		if _, err := accessStore.EnsureUser(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	checked := access.Checked{Authority: accessStore, Projects: project.New(db, nil)}
	sources := realAuthoritySources{projectID: pid, slug: "game"}
	svc := New(context.Background(), db, Dependencies{Authority: checked, Configuration: ConfigurationDependencies{Authority: checked, Projects: sources, Contracts: sources}, Releases: ReleaseDependencies{Authority: checked, Still: accessStore, Sources: sources, Secrets: requestSecrets{}, Queue: doReleaseQueue{}}})
	return svc, accessStore, db, pid
}

func realCaller(name string) context.Context {
	return access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: name, Via: "session"}, Verified: true, Receipt: name})
}

func grantReal(t *testing.T, a *identity.Service, subject, role string, cap access.Capability, scope access.ScopeKind, id string) {
	t.Helper()
	if _, err := a.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: subject, Role: role, Capability: string(cap), Scope: scope, ScopeID: id, By: "fixture"}, ""); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentConfigurationRequiresInternalAuthority(t *testing.T) {
	t.Parallel()
	s, a, db, pid := realAuthorityFixture(t)
	target, err := s.Targets.Put(context.Background(), &Target{Name: "host", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	in := ConfigureDeployment{ProjectID: pid, Name: "test", Target: target.Name}
	if _, err := s.Configure(context.Background(), in); !errors.Is(err, access.ErrPrincipalRequired) {
		t.Fatalf("no verified caller: %v", err)
	}
	if _, err := s.Configure(realCaller("outsider"), in); err == nil {
		t.Fatal("outsider configured deployment")
	}
	grantReal(t, a, "owner", "", access.CapDeploymentBind, access.ScopeProject, pid.String())
	owner := realCaller("owner")
	t.Cleanup(db.FailNextStatement("INSERT INTO audit_log"))
	if result, err := s.Configure(owner, in); !errors.Is(err, database.ErrInjectedFailure) || result.Deployment != nil {
		t.Fatalf("receipt failure: %+v %v", result, err)
	}
	result, err := s.Configure(owner, in)
	if err != nil || result.Deployment == nil {
		t.Fatalf("authorized configuration: %+v %v", result, err)
	}
	wantN(t, "bound receipts by owner", receipts(t, db, "deployment.bound", byActor("owner")), 1)
	boss, err := identity.ActorIn(context.Background(), db, "boss")
	if err != nil || boss.Principal.IsZero() {
		t.Fatalf("fixture administrator: %v", err)
	}
	disabled := true
	if err := a.SaveUser(context.Background(), identity.AccountChange{Username: "owner", Disabled: &disabled}, access.Principal{ID: boss.Principal, Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	in.Name = "second"
	if _, err := s.Configure(owner, in); err == nil {
		t.Fatal("disabled user configured deployment")
	}
	wantN(t, "deployments", everyDeployment(t, db), 1)
}

func TestReleaseRequestChecksInternalAuthority(t *testing.T) {
	t.Parallel()
	s, a, db, pid := realAuthorityFixture(t)
	target, err := s.Targets.Put(context.Background(), &Target{Name: "request", Kind: "request-test", Origin: OriginBuiltIn, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.EnsureStanding(t.Context(), pid, "cloud", target.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "non-production"})
	if err != nil {
		t.Fatal(err)
	}
	in := DeploySelection{ProjectID: pid, DeploymentName: dep.Name}
	if _, err := s.RequestRelease(context.Background(), in); !errors.Is(err, access.ErrPrincipalRequired) {
		t.Fatalf("missing caller: %v", err)
	}
	if _, err := s.RequestRelease(realCaller("outsider"), in); err == nil {
		t.Fatal("outsider admitted")
	}
	grantReal(t, a, "owner", "", access.CapDeploymentDeploy, access.ScopeProject, pid.String())
	owner := realCaller("owner")
	if _, err := s.RequestRelease(owner, in); !errors.Is(err, ErrNoBundle) {
		t.Fatalf("permitted caller: %v", err)
	}
	dep, err = s.Deployments.EnsureStanding(owner, pid, "cloud", target.ID, Attributes{Feed: "promotion", Audience: "public", DataClass: "non-production"})
	if err != nil {
		t.Fatal(err)
	}
	grantReal(t, a, "owner", access.RoleReleaseManager, "", access.ScopeTarget, "*")
	revoked := 0
	for _, sc := range []struct {
		kind access.ScopeKind
		id   string
	}{{access.ScopeProject, "1"}, {access.ScopeTarget, dep.Name}, {access.ScopeTarget, "*"}} {
		grants, err := a.GrantsAt(context.Background(), sc.kind, sc.id)
		if err != nil {
			t.Fatal(err)
		}
		for _, grant := range grants {
			if grant.RoleName == access.RoleReleaseManager {
				if _, err := a.Revoke(owner, grant.ID, "boss"); err != nil {
					t.Fatal(err)
				}
				revoked++
			}
		}
	}
	if revoked != 1 {
		t.Fatalf("release-manager grants revoked: %d", revoked)
	}
	if _, err := s.RequestRelease(owner, in); err == nil || errors.Is(err, ErrNoBundle) {
		t.Fatalf("protected release lacked promotion check: %v", err)
	}
	grantReal(t, a, "owner", "", access.CapReleasePromote, access.ScopeTarget, dep.Name)
	if _, err := s.RequestRelease(owner, in); !errors.Is(err, ErrNoBundle) {
		t.Fatalf("promoter: %v", err)
	}
	boss, err := identity.ActorIn(context.Background(), db, "boss")
	if err != nil || boss.Principal.IsZero() {
		t.Fatalf("fixture administrator: %v", err)
	}
	disabled := true
	if err := a.SaveUser(context.Background(), identity.AccountChange{Username: "owner", Disabled: &disabled}, access.Principal{ID: boss.Principal, Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestRelease(owner, in); err == nil || errors.Is(err, ErrNoBundle) {
		t.Fatalf("disabled caller admitted: %v", err)
	}
	wantN(t, "releases", everyRelease(t, db), 0)
	wantN(t, "release jobs", queuedJobs(t, db, ReleaseJob, ""), 0)
}
