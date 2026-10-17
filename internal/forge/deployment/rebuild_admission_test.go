package deployment

import (
	"context"
	"encoding/json"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
)

func TestDockerRebuildAdmissionAndAuditAreAtomic(t *testing.T) {
	t.Parallel()
	f, dep := deploymentOutcomeFixture(t)
	store := f.outcomes
	payload, err := json.Marshal(docker.SourceInputs{Version: 1, Revision: "selected-revision"})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(payload)
	inputs := Inputs{Revision: "selected-revision", ArtifactHash: deploy.ConfigHash(raw), DockerSource: raw}
	first, _, err := store.Admit(context.Background(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "initial", Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	completion := deploymentCompletionFor(first, "initial")
	completion.Observed.ImageDigest = "sha256:initial"
	if _, err := store.Complete(context.Background(), completion); err != nil {
		t.Fatal(err)
	}
	inputs.Rebuild = true
	start := Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "rebuild", Inputs: inputs, BaseGeneration: &first.Generation}
	at, _, err := store.Admit(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	wrong := deploy.DockerIdentity{Deployment: dep.Label(), Generation: at.Generation, Artifact: "wrong"}
	if err := store.Checkpoint(context.Background(), at)(wrong.Checkpoint("container")); err == nil {
		t.Fatal("checkpoint changed admitted artifact")
	}
	wrong.Artifact = inputs.ArtifactHash
	if err := store.Checkpoint(context.Background(), at)(wrong.Checkpoint("container")); err != nil {
		t.Fatal(err)
	}
	completion = deploymentCompletionFor(at, "rebuilt")
	completion.Observed.ImageDigest = "sha256:rebuilt"
	dbtest.FailNextStatement(t, f.db, "INSERT INTO audit_log")
	if _, err := store.Complete(context.Background(), completion); err == nil {
		t.Fatal("audit failure must roll back rebuild outcome")
	}
	if d := storedDeployment(t, f.db, dep.ID); d.ObservedGeneration != first.Generation {
		t.Fatalf("rolled-back rebuild moved the deployment to generation %d", d.ObservedGeneration)
	}
	if a := storedAttempt(t, f.db, at.ID); a.Status != AttemptRunning.String() {
		t.Fatalf("rolled-back rebuild attempt: %s", a.Status)
	}
	for i := 0; i < 2; i++ {
		if _, err := store.Complete(context.Background(), completion); err != nil {
			t.Fatal(err)
		}
	}
	wantN(t, "rebuilt receipts", receipts(t, f.db, "deployment.rebuilt"), 1)
	if d := storedDeployment(t, f.db, dep.ID); d.ImageDigest != "sha256:rebuilt" {
		t.Fatalf("rebuilt image: %q", d.ImageDigest)
	}
	start.Key = "stale-rebuild"
	if _, _, err := store.Admit(context.Background(), start); err == nil {
		t.Fatal("stale rebuild superseded newer generation")
	}
	wantCount(t, "attempts", attemptsWhere(t, f.db, func(a deploymentsql.DeploymentAttempt) bool { return a.DeploymentID == dep.ID }), 2)
}

// An attempt's outcome records what its execution did: a caller that
// stopped as the execution finished still leaves the outcome recorded, not
// an attempt left running for a restart to observe.
//
// Kills: the outcome written on the stopped caller.
func TestAnAttemptsOutcomeIsRecordedThoughTheCallerStopped(t *testing.T) {
	t.Parallel()
	f, dep := deploymentOutcomeFixture(t)
	store := f.outcomes
	payload, err := json.Marshal(docker.SourceInputs{Version: 1, Revision: "selected-revision"})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(payload)
	at, _, err := store.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "stopped",
		Inputs: Inputs{Revision: "selected-revision", ArtifactHash: deploy.ConfigHash(raw), DockerSource: raw}})
	if err != nil {
		t.Fatal(err)
	}
	stopped, stop := context.WithCancel(t.Context())
	stop()
	completion := deploymentCompletionFor(at, "stopped")
	completion.Observed.ImageDigest = "sha256:stopped"
	if _, err := store.Complete(stopped, completion); err != nil {
		t.Fatalf("an outcome after the caller stopped: %v", err)
	}
	if a := storedAttempt(t, f.db, at.ID); a.Status == AttemptRunning.String() {
		t.Fatalf("the attempt is left running: %s", a.Status)
	}
}
