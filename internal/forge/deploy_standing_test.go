package forge

import (
	"context"
	"testing"

	"flokkit.io/forge/internal/forge/deployment"
)

// The boot reconciler leaves a record it cannot rebuild and does not panic —
// the failure mode is a log line, never a lost row.
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
	a.ensureDeploymentsOnce(context.Background()) // no repo, no container: must not panic
	if n := deploymentsMatching(t, a, func(deployment.ProjectDeployment) bool { return true }); n != 1 {
		t.Fatalf("the record must survive an unrebuildable environment: %d rows", n)
	}
}
