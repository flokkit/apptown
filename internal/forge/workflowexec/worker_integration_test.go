//go:build integration

package workflowexec

import (
	"context"
	"testing"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/tasks"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/worker/workertest"
)

// A forge configured with a worker runs a real script step there: the
// worker is apptown-worker in its own Docker-in-Docker container, sharing no
// files with the forge. The script's work is published to the change's
// branch, and the forge's workspace is brought to it.
func TestIntegrationAScriptStepRunsOnAWorkerInItsOwnContainer(t *testing.T) {
	ca := workertest.NewCA(t)
	w := workertest.StartInContainer(t, ca)
	a, _, step := scriptTaskFixture(t, map[string]any{"run": "echo made on the worker > made.txt", "executor": "docker", "image": "busybox:latest"})
	driver := execution.WorkerTasks{URL: w.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	a.tasks.Driver, a.tasks.Remote, a.tasks.Profile.Name, a.tasks.Backend = driver, driver, "worker", "worker at "+w.URL
	run, err := workflow.ReadRunIn(context.Background(), a.db, step.RunID)
	if err != nil {
		t.Fatal(err)
	}
	source := a.source(t, run)
	hub, ws := source.Repository(a.dir), source.Workspace(a.dir)
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	settled := awaitStep(t, a, step.ID, workflow.StatusPass)
	tip := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
	if got := gittest.Run(t, hub, "show", tip+":made.txt"); got != "made on the worker" {
		t.Fatalf("the published branch holds %q; evidence %s", got, settled.Evidence)
	}
	if got := gittest.Run(t, ws, "rev-parse", "HEAD"); got != tip {
		t.Fatalf("AppTown's workspace is at %s, want %s", got, tip)
	}
	attempt, err := tasks.New(a.db).Get(context.Background(), taskKeyOf(step))
	if err != nil || attempt.Backend != "worker at "+w.URL || attempt.Observed != tasks.Settled || attempt.Source == "" {
		t.Fatalf("the attempt: %+v %v", attempt, err)
	}
}
