//go:build integration

package forge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/tasks"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/poll"
)

// taskRecoveryFixture admits one Docker script step on a repository-backed
// change. The script appends a line to runs.txt each time it runs, so a test
// can count executions.
func taskRecoveryFixture(t *testing.T, script string) (*App, *workflow.Step, string) {
	t.Helper()
	a := testApp(t)
	a.tasks.Poll = 50 * time.Millisecond
	projectID := plainTestProject(t, a.DB, "task-recovery", "Task Recovery")
	p := a.projectByID(context.Background(), projectID)
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
		{Name: "build", Steps: []schema.Step{{Name: "make", Uses: "script", With: map[string]any{"run": "echo run >> /work/runs.txt; " + script, "executor": "docker", "image": "busybox:latest"}}}},
	}}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "work", Version: 1}, Spec: spec})
	a.lifecycle.SelectLane(t.Context(), projectID, "work", true, "fixture")
	ctx := a.asForge(context.Background(), projectID, "fixture")
	ch, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: projectID, Type: "feature", Title: "task recovery"})
	if err == nil {
		ch, err = a.Work.Begin(ctx, ch.ID)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
		t.Fatal(err)
	}
	a.Work.Stop()
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "work"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1 {
		t.Fatalf("script step: %+v %v", snapshot, err)
	}
	// The run works in a clone of its own.
	return a, snapshot.Steps[0], workflowSourceForTest(t, a, run).Workspace(a.cfg.dataDir)
}

// crashAt runs the step with AppTown "dying" at stage: the runner returns
// without collecting, as a process that exited there would.
func crashAt(t *testing.T, a *App, step *workflow.Step, stage string) {
	t.Helper()
	a.tasks.AbandonAt = func(s string) bool { return s == stage }
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), step.ID); err != nil {
		t.Fatal(err)
	}
	a.tasks.AbandonAt = nil
}

// restart runs the next process's startup on a new App: database repair,
// then resuming recorded tasks.
func restart(t *testing.T, a *App) {
	t.Helper()
	restarted(t, a)
}

func runsIn(t *testing.T, ws string) int {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(ws, "runs.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(body), "run\n")
}

func onlyAttempt(t *testing.T, a *App, step *workflow.Step) tasks.Attempt {
	t.Helper()
	attempts, err := tasks.New(a.DB).Unsettled(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range attempts {
		if attempt.Owner.StepID == step.ID {
			return attempt
		}
	}
	t.Fatalf("step %d has no unsettled task", step.ID)
	return tasks.Attempt{}
}

func TestIntegrationADockerScriptStepRunsAsARecordedTask(t *testing.T) {
	a, step, ws := taskRecoveryFixture(t, "echo made > /work/made.txt")
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), step.ID); err != nil {
		t.Fatal(err)
	}
	awaitStep(t, a, step.ID, workflow.StatusPass)
	if runs := runsIn(t, ws); runs != 1 {
		t.Fatalf("the task ran %d times", runs)
	}
	if left, err := tasks.New(a.DB).Unsettled(context.Background()); err != nil || len(left) != 0 {
		t.Fatalf("unsettled after the step passed: %+v %v", left, err)
	}
}

func TestIntegrationARestartAdoptsARunningTaskInsteadOfRunningItAgain(t *testing.T) {
	a, step, ws := taskRecoveryFixture(t, "sleep 2")
	crashAt(t, a, step, "started")
	restart(t, a)
	awaitStep(t, a, step.ID, workflow.StatusPass)
	if runs := runsIn(t, ws); runs != 1 {
		t.Fatalf("the task ran %d times across the restart", runs)
	}
}

func TestIntegrationAFinishedTaskIsCollectedAfterARestart(t *testing.T) {
	a, step, ws := taskRecoveryFixture(t, "exit 0")
	crashAt(t, a, step, "started")
	attempt := onlyAttempt(t, a, step)
	var obs execution.TaskObservation
	var err error
	if !poll.Until(context.Background(), 30*time.Second, 50*time.Millisecond, func() bool {
		obs, err = execution.DockerTasks{}.Observe(context.Background(), execution.TaskRef{Key: attempt.Operation, ProviderID: attempt.ProviderID})
		return err == nil && obs.State == execution.TaskExited
	}) {
		t.Fatalf("the task did not exit: %+v %v", obs, err)
	}
	restart(t, a)
	awaitStep(t, a, step.ID, workflow.StatusPass)
	if runs := runsIn(t, ws); runs != 1 {
		t.Fatalf("the task ran %d times across the restart", runs)
	}
}

func TestIntegrationALostReplyAfterCreateIsAdopted(t *testing.T) {
	a, step, ws := taskRecoveryFixture(t, "exit 0")
	crashAt(t, a, step, "created")
	if attempt := onlyAttempt(t, a, step); attempt.ProviderID != "" {
		t.Fatalf("the provider identity was recorded before the crash: %+v", attempt)
	}
	restart(t, a)
	awaitStep(t, a, step.ID, workflow.StatusPass)
	if runs := runsIn(t, ws); runs != 1 {
		t.Fatalf("the task ran %d times after its lost reply", runs)
	}
}

func TestIntegrationAStopRecordedBeforeARestartIsCarriedOut(t *testing.T) {
	a, step, ws := taskRecoveryFixture(t, "sleep 300")
	crashAt(t, a, step, "started")
	attempt := onlyAttempt(t, a, step)
	if err := tasks.New(a.DB).RequestStop(context.Background(), attempt.Operation); err != nil {
		t.Fatal(err)
	}
	restart(t, a)
	awaitStep(t, a, step.ID, workflow.StatusError)
	// The step's outcome is recorded before its task is removed.
	var obs execution.TaskObservation
	var err error
	if !poll.Until(context.Background(), 30*time.Second, 50*time.Millisecond, func() bool {
		obs, err = execution.DockerTasks{}.Observe(context.Background(), execution.TaskRef{Key: attempt.Operation, ProviderID: attempt.ProviderID})
		return err == nil && obs.State == execution.TaskAbsent
	}) {
		t.Fatalf("the stopped task's container: %+v %v", obs, err)
	}
	if runs := runsIn(t, ws); runs != 1 {
		t.Fatalf("the task ran %d times", runs)
	}
}
