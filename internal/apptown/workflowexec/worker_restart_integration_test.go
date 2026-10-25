//go:build integration

package workflowexec

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// Complement the existing running-task recovery coverage: a real worker's
// already-finished result is collected after AppTown reconstructs its app.
func TestIntegrationAWorkerTaskFinishedDuringARestartIsCollected(t *testing.T) {
	ca := workertest.NewCA(t)
	worker := workertest.StartInContainer(t, ca)
	a, _, step := scriptTaskFixture(t, map[string]any{"run": "echo ran >> count.txt", "executor": "docker", "image": workertest.LoadedBusybox})
	driver := execution.WorkerTasks{URL: worker.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	a.tasks.Driver, a.tasks.Remote, a.tasks.Profile.Name, a.tasks.Backend = driver, driver, "worker", "worker at "+driver.URL
	a.tasks.AbandonAt = func(stage string) bool { return stage == "started" }
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if code, err := driver.Wait(ctx, execution.TaskRef{Key: taskKeyOf(step)}); err != nil || code != 0 {
		t.Fatalf("worker exit: code=%d err=%v", code, err)
	}
	b := restarted(t, a)
	awaitStep(t, b, step.ID, workflow.StatusPass)
	run, err := workflow.ReadRunIn(context.Background(), b.db, step.RunID)
	if err != nil {
		t.Fatal(err)
	}
	source := b.source(t, run)
	hub := requireLocation(t)(source.Repository(b.dir))
	tip := scm.Ref(ctx, hub, "refs/heads/"+source.Branch)
	if got := gittest.Run(t, hub, "show", tip+":count.txt"); got != "ran" {
		t.Fatalf("finished result lost or task rerun: %q", got)
	}
}
