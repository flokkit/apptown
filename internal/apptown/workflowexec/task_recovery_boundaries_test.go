package workflowexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/poll"
)

// The provider accepted Start, but its response was lost. This is different
// from AbandonAt("started"), which runs after every call succeeded.
type lostStartReply struct{ execution.TaskDriver }

func (d lostStartReply) Start(ctx context.Context, ref execution.TaskRef) error {
	if err := d.TaskDriver.Start(ctx, ref); err != nil {
		return err
	}
	return errors.New("reply lost after provider accepted Start")
}

func TestALostStartReplyIsAdopted(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	a.tasks.Driver = lostStartReply{fake}
	// A lost Start reply is settled by observing the task: it runs, so this
	// attempt adopts it and waits for it, in the same process.
	done := make(chan error, 1)
	go func() { done <- a.work.ExecuteStep(a.ctx, step.ID) }()
	key := taskKeyOf(step)
	awaitStarted(t, fake, key, "the task never started")
	// The adoption is a recorded decision: having observed the task running,
	// the attempt records it started, then waits for it. Witness that record
	// (or the step settling, which would be the wrong decision).
	if !poll.Until(context.Background(), 30*time.Second, 10*time.Millisecond, func() bool {
		attempt, err := tasks.New(a.db).Get(context.Background(), key)
		got, stepErr := workflow.ReadStepIn(context.Background(), a.db, step.ID)
		return err == nil && attempt.Observed != tasks.Reserved || stepErr != nil || got.Status != workflow.StatusRunning
	}) {
		t.Fatal("the attempt never recorded a decision after a lost Start reply")
	}
	if attempt, err := tasks.New(a.db).Get(context.Background(), key); err != nil || attempt.Observed != tasks.Started {
		t.Fatalf("after a lost Start reply the attempt is %+v (%v), not adopted as started", attempt, err)
	}
	got, err := workflow.ReadStepIn(context.Background(), a.db, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := fake.Observe(context.Background(), execution.TaskRef{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	if obs.State != execution.TaskRunning || got.Status != workflow.StatusRunning {
		t.Fatalf("lost Start reply discarded task instead of adopting it: provider=%s step=%s evidence=%s", obs.State, got.Status, got.Evidence)
	}
	fake.Exit(key, 0)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	awaitStep(t, a, step.ID, workflow.StatusPass)
	if starts := fake.Starts(key); starts != 1 {
		t.Fatalf("the task started %d times", starts)
	}
}

// Do not discard the provider result if the step outcome transaction fails.
func TestAFailedSettlementKeepsTheTaskCollectible(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, false)
	dbtest.FailNextStatement(t, a.db, "-- name: FinishAttempt")
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	got, err := workflow.ReadStepIn(context.Background(), a.db, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := tasks.New(a.db).Get(context.Background(), taskKeyOf(step))
	if err != nil {
		t.Fatal(err)
	}
	obs, err := fake.Observe(context.Background(), execution.TaskRef{Key: taskKeyOf(step)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == workflow.StatusRunning && (attempt.Observed == tasks.Settled || obs.State == execution.TaskAbsent) {
		t.Fatalf("failed step write lost its result: step=%s task=%s provider=%s", got.Status, attempt.Observed, obs.State)
	}
}

// When the step's outcome cannot be written at all, the task and its record
// stay as they are, and a restarted AppTown collects it and settles the step.
func TestAStepWhoseOutcomeCouldNotBeWrittenIsSettledAfterARestart(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, false)
	a.tasks.SettleTries = 1
	dbtest.FailNextStatement(t, a.db, "-- name: FinishAttempt")
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	key := taskKeyOf(step)
	got, err := workflow.ReadStepIn(context.Background(), a.db, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := tasks.New(a.db).Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workflow.StatusRunning || attempt.Observed != tasks.Collecting || !fake.Present(key) {
		t.Fatalf("after the failed write: step=%s task=%s present=%v", got.Status, attempt.Observed, fake.Present(key))
	}
	b := restarted(t, a)
	awaitStep(t, b, step.ID, workflow.StatusPass)
	// The task is removed once its step's outcome is written: wait for it.
	poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, func() bool { return !fake.Present(key) })
	if fake.Starts(key) != 1 || fake.Present(key) {
		t.Fatalf("started %d times; still present: %v", fake.Starts(key), fake.Present(key))
	}
}

// Reconstruct the exact durable boundary after Git commit and before Publish.
func TestRecoveryPublishesWorkCommittedBeforeACrash(t *testing.T) {
	t.Parallel()
	a, _, step := fakeTaskFixture(t, false)
	a.tasks.AbandonAt = func(stage string) bool { return stage == "started" }
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	a.tasks.AbandonAt = nil
	run, err := workflow.ReadRunIn(context.Background(), a.db, step.RunID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := workflow.ReadSourceIn(context.Background(), a.db, workflow.RunRef{ID: step.RunID, ProjectID: run.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	ws := requireLocation(t)(source.Workspace(a.dir))
	recovered, err := a.work.RecoverExecution(a.ctx, step.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.branches.Publish(context.Background(), scm.Publication{ProjectID: run.ProjectID, Repository: requireLocation(t)(source.Repository(a.dir)), Branch: source.Branch, From: ws, Grant: scm.Grant{Generation: recovered.Step().SourceGeneration()}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "result.txt"), []byte("task result"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, committed, err := scm.CommitWorkspace(recovered.Context(), ws, "task commit before crash"); err != nil || !committed {
		t.Fatalf("commit: %t %v", committed, err)
	}
	before, err := gitexec.Run(recovered.Context(), requireLocation(t)(source.Repository(a.dir)), "rev-parse", source.Branch)
	if err != nil {
		t.Fatal(err)
	}
	head, err := gitexec.Run(recovered.Context(), ws, "rev-parse", "HEAD")
	if err != nil || head == before {
		t.Fatalf("fixture must have unpublished commit: %s %s %v", head, before, err)
	}
	restarted(t, a)
	awaitStep(t, a, step.ID, workflow.StatusPass)
	after, err := gitexec.Run(recovered.Context(), requireLocation(t)(source.Repository(a.dir)), "rev-parse", source.Branch)
	if err != nil {
		t.Fatalf("step passed, but its canonical branch does not exist: %v", err)
	}
	if after != head {
		t.Fatalf("step passed without publishing its committed result: canonical=%s workspace=%s", after, head)
	}
}

// A recovered collector must arrange workflow continuation after settlement.
func TestARecoveredTaskContinuesItsWorkflow(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	a.tasks.AbandonAt = func(stage string) bool { return stage == "started" }
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	b := startTaskAppTown(t, a.db, a.dir, a.image, a.definitions)
	b.tasks.Driver = fake
	b.recover(t)
	// The recovered task is the dispatcher's work: Wait returns once it
	// settles, so the task ends first.
	fake.Exit(taskKeyOf(step), 0)
	b.work.Wait()
	awaitStep(t, b, step.ID, workflow.StatusPass)
	var snapshot *workflow.RunSnapshot
	var err error
	if !poll.Until(context.Background(), 2*time.Second, 10*time.Millisecond, func() bool {
		snapshot, err = b.work.Snapshot(b.ctx, step.RunID, workflow.DetailView)
		return err != nil || snapshot.Run.Status != workflow.RunRunning
	}) {
		b.work.ResumePending()
		b.work.Wait()
		after, err := b.work.Snapshot(b.ctx, step.RunID, workflow.DetailView)
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("recovered task passed, but workflow stayed %s in stage %s; an extra manual redispatch moved it to %s", snapshot.Run.Status, snapshot.Run.CurrentStage, after.Run.Status)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// A live durable stop must settle the step too, or startup treats it as an
// orphan with no unsettled task and admits a fresh execution.
func TestARecordedStopSettlesTheStepBeforeTheTask(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	done := make(chan error, 1)
	go func() { done <- a.work.ExecuteStep(a.ctx, step.ID) }()
	awaitStarted(t, fake, taskKeyOf(step), "task did not start")
	if err := tasks.New(a.db).RequestStop(context.Background(), taskKeyOf(step)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not complete")
	}
	got, err := workflow.ReadStepIn(context.Background(), a.db, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := tasks.New(a.db).Get(context.Background(), taskKeyOf(step))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == workflow.StatusRunning && attempt.Observed == tasks.Settled {
		t.Fatalf("stopped task settled but its step is running; startup will orphan and replay it")
	}
}
