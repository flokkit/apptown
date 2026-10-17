package forge

import (
	"context"
	"fmt"
	"testing"

	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/ids"
)

func deploymentReleaseFixture(t *testing.T) (*App, *deployment.Deployment, ids.ID, deployment.Attempt) {
	t.Helper()
	return deploymentReleaseImageFixture(t, "selected-image")
}
func deploymentReleaseImageFixture(t *testing.T, image string) (*App, *deployment.Deployment, ids.ID, deployment.Attempt) {
	t.Helper()
	a, env := deploymentOutcomeFixture(t)
	releaseID := deploymentReleasingFixture(t, a, env, "main", "selected-revision", "alice")
	at, _, err := (a.Deployment.Outcomes).Admit(context.Background(), deployment.Start{DeploymentID: env.ID, TargetID: env.TargetID, Key: fmt.Sprintf("release-%d", releaseID),
		Inputs: deployment.Inputs{ReleaseID: releaseID, Revision: "selected-revision", ImageDigest: image, ArtifactHash: "selected-artifact"}})
	if err != nil {
		t.Fatal(err)
	}
	return a, env, releaseID, at
}
