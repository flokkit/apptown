package deployment

import (
	"context"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/audit"
)

func TestDeploymentSourceImageSettlesReleaseAndDeploymentTogether(t *testing.T) {
	t.Parallel()
	f, dep := deploymentOutcomeFixture(t)
	release, err := NewReleases(f.db).Start(context.Background(), ReleaseDraft{
		Source: ReleaseFromBundle, ProjectID: dep.ProjectID, DeploymentID: dep.ID,
		Deployment: dep.Name, Revision: "selected-revision", Actor: "alice",
	}, "prepared for source image settlement")
	if err != nil {
		t.Fatal(err)
	}
	// Source builds have no image digest at admission. Settlement records the
	// exact image that Docker built without changing the admitted inputs.
	at, _, err := f.outcomes.Admit(context.Background(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID,
		Key: fmt.Sprintf("release-%d", release.ID), Inputs: Inputs{ReleaseID: release.ID, Revision: "selected-revision"}})
	if err != nil {
		t.Fatal(err)
	}
	completion := deploymentCompletionFor(at, "source-built")
	completion.Observed.ImageDigest = "sha256:source-built"
	if _, err := f.outcomes.Complete(context.Background(), completion); err != nil {
		t.Fatal(err)
	}
	if d := storedDeployment(t, f.db, dep.ID); d.ImageDigest != "sha256:source-built" {
		t.Fatalf("deployment image: %q", d.ImageDigest)
	}
	if r := storedRelease(t, f.db, dep.ProjectID, release.ID); r.ExternalRef != "sha256:source-built" {
		t.Fatalf("release image: %q", r.ExternalRef)
	}
	wantN(t, "receipts naming the built image", receipts(t, f.db, "release.deployed", func(e audit.Entry) bool {
		return receiptPayload(t, e)["image"] == "sha256:source-built"
	}), 1)
}
