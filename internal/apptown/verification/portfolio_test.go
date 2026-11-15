package verification

import (
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/ids"
)

func TestStandingReachKeepsEveryFollowsMainDeploymentWithoutReadingGit(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	projects := project.New(f.DB, nil)
	// A repository-backed record without any on-disk repository: reach must
	// not inspect the contract or main's scan observations.
	if err := projects.SetDefaultBranch(t.Context(), f.ProjectID, "main"); err != nil {
		t.Fatal(err)
	}
	directory := identity.New(f.DB, identity.Config{Projects: project.NewCatalogue(f.DB)})
	person, err := directory.EnsurePerson(t.Context(), "bls")
	if err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: person.PrincipalID, Kind: "user", Name: "bls", Via: "session"}, Verified: true})
	service := deployment.New(t.Context(), f.DB, deployment.Dependencies{Configuration: deployment.ConfigurationDependencies{Authority: access.Checked{Authority: identity.Scoped(directory, "deployment"), Projects: projects}}})
	target, err := service.Targets.Put(t.Context(), &deployment.Target{Name: "reach", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	want := map[ids.ID]ids.ID{}
	for _, name := range []string{"automatic", "protected", "person", "manual"} {
		at := deployment.Attributes{Feed: "main"}.WithDefaults()
		if name == "protected" {
			at.DataClass = "production"
		}
		if name == "manual" {
			at.Feed = "manual"
		}
		dep, err := service.Deployments.EnsureStanding(t.Context(), f.ProjectID, name, target.ID, at)
		if err != nil {
			t.Fatal(err)
		}
		if name == "person" {
			if _, err := service.SetRoutineUpdates(ctx, f.ProjectID, name, deployment.RoutineUpdateSettings{Revision: dep.RoutineUpdatesRevision, RoutineUpdates: deployment.RoutineUpdates{Mode: "person"}}); err != nil {
				t.Fatal(err)
			}
		}
		if at.Feed == "main" {
			want[dep.ID] = target.ID
		}
	}
	portfolio := Portfolio{Releases: service, DataDir: t.TempDir()}
	reach, err := portfolio.StandingReach(t.Context(), f.DB, f.ProjectID)
	if err != nil || !reach.Repository || len(reach.FollowsMain) != len(want) {
		t.Fatalf("reach: %+v %v", reach, err)
	}
	for _, dep := range reach.FollowsMain {
		if target, ok := want[dep.DeploymentID]; !ok || dep.TargetID != target {
			t.Fatalf("lost exact deployment/target scope: %+v", dep)
		}
		delete(want, dep.DeploymentID)
	}
	if len(want) != 0 {
		t.Fatalf("missing deployments: %v", want)
	}
	if err := projects.SetDefaultBranch(t.Context(), f.ProjectID, ""); err != nil {
		t.Fatal(err)
	}
	reach, err = portfolio.StandingReach(t.Context(), f.DB, f.ProjectID)
	if err != nil || reach.Repository {
		t.Fatalf("no repository: %+v %v", reach, err)
	}
}
