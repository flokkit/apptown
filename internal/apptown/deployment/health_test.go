package deployment

import (
	"context"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Provider-managed deployments are outside this controller's responsibility;
// missing targets and broken Docker connections still need an actionable log.
func TestHealthControllerSelectsDockerTargetsWithoutProviderWarnings(t *testing.T) {
	// Serial because this test captures the process logger.
	db := testDB(t)
	s := New(context.Background(), db, assembled(db, testRuntime{Scope: docker.Scope{Client: docker.CLI{}}, BuiltInTarget: "local"}, Dependencies{}))
	builtin, err := s.Targets.Put(context.Background(), &Target{Name: "local", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	pid := createTestProject(t, db, "p", "P")
	targets := []*Target{builtin,
		{Name: "lan", Kind: KindPortainer, Origin: OriginAttached, Status: TargetReady},
		{Name: "cloud", Kind: KindDOApp, Origin: OriginAttached, Status: TargetReady},
		{Name: "broken-host", Kind: KindDockerHost, Origin: OriginAttached, Status: TargetReady},
	}
	for i, target := range targets {
		if i != 0 {
			target, err = s.Targets.Put(context.Background(), target)
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.Deployments.EnsureStanding(t.Context(), pid, target.Name, target.ID, Attributes{}.WithDefaults()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Deployments.ReserveHostPort(t.Context(), pid, target.Name, "container-"+target.Name, 41000+i); err != nil {
			t.Fatal(err)
		}
	}
	// Deliberately orphaned storage: a missing target must be diagnosed, never
	// interpreted as the built-in host. Only this owner's persistence fixture
	// can create a deployment that bypasses EnsureStanding's target check.
	storedDeploymentFixture(t, db, deploymentsql.EnsureStandingParams{ID: ids.New(time.Now()), ProjectID: pid, Name: "missing-target", TargetID: idstest.N(999999)})
	output, err := os.CreateTemp(t.TempDir(), "health-log")
	if err != nil {
		t.Fatal(err)
	}
	previous := log.Writer()
	log.SetOutput(output)
	t.Cleanup(func() { log.SetOutput(previous); _ = output.Close() })
	watched := s.hosts.Watched(context.Background(), s.Deployments)
	if len(watched) != 1 || watched[0].Name != builtin.Name || watched[0].Container != "container-"+builtin.Name {
		t.Fatalf("expected only the usable Docker deployment, got %+v", watched)
	}
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	logs := string(data)
	for _, name := range []string{"lan", "cloud"} {
		if strings.Contains(logs, "health controller: p/"+name+":") {
			t.Errorf("provider-managed deployment emitted a Docker warning: %s", logs)
		}
	}
	for _, name := range []string{"broken-host", "missing-target"} {
		if !strings.Contains(logs, "health controller: p/"+name+":") {
			t.Errorf("missing diagnostic for %s: %s", name, logs)
		}
	}
}
