package deployment

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
)

// A release or rebuild on a Docker host boots with the deployment's
// declared data class: without it, a standing service whose parameters
// moved read as replaceable, whatever the deployment held.
func TestADockerReleaseCarriesTheDeploymentsDataClass(t *testing.T) {
	t.Parallel()
	targets := NewTargets(nil, nil)
	env := NewEnvironment(targets, EnvironmentDependencies{})
	p := NewProviders(context.Background(), nil, ProviderRecords{Targets: targets}, NewHosts(targets, nil, HostSettings{}), env, ProviderDependencies{})
	for _, class := range []string{"production", "non-production", "synthetic"} {
		d, err := p.dockerDriver(context.Background(), &Target{Name: "local", Kind: KindDockerHost}, &Deployment{Name: "staging", DataClass: class})
		if err != nil {
			t.Fatal(err)
		}
		if got := d.(*docker.Driver).DataClass; got != class {
			t.Fatalf("a %s deployment's driver boots as %q", class, got)
		}
	}
}

// A landing reads the deployment's class; one it cannot read is unknown,
// never synthetic.
func TestALandingReadsTheDataClassAndUnknownIsProtected(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := createTestProject(t, db, "dc", "DC")
	s := New(context.Background(), db, Dependencies{})
	target, err := s.Targets.Put(context.Background(), &Target{Name: "local", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deployments.EnsureStanding(t.Context(), pid, "main", target.ID, Attributes{DataClass: "production"}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	if got := s.Deployments.DataClassOf(t.Context(), pid, "main"); got != "production" {
		t.Fatalf("main: %q", got)
	}
	for _, name := range []string{"missing", ""} {
		if got := s.Deployments.DataClassOf(t.Context(), pid, name); got != "" {
			t.Fatalf("%q reads as %q, not unknown", name, got)
		}
	}
}
