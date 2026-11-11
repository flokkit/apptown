package apptown

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
)

// A repair of a record it cannot rebuild leaves the record and does not
// panic — the failure mode is a log line, never a lost row.
func TestEnsureDeploymentsLeavesUnrebuildableRecord(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	pid := createReadTestProject(t, a, "ghost")
	target, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: "local", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Deployments.EnsureStanding(t.Context(), pid, "main", target.ID, deployment.Attributes{}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Deployments.ReserveHostPort(t.Context(), pid, "main", "apptown-env-ghost-main", 59999); err != nil {
		t.Fatal(err)
	}
	dep, err := a.Deployment.Deployments.ByName(t.Context(), pid, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Jobs().Enqueue(context.Background(), "rebuild", dep.ProjectID.String()+"/"+dep.Name, map[string]any{"attempt": dep.ServingAttemptID}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	runQueuedJobForTest(t, a, "rebuild", pid.String()+"/main") // no repo, no container: must not panic
	if n := deploymentsMatching(t, a, func(deployment.ProjectDeployment) bool { return true }); n != 1 {
		t.Fatalf("the record must survive an unrebuildable environment: %d rows", n)
	}
}
