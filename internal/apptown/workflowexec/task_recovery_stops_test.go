package workflowexec

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/poll"
)

type unansweredStop struct{ execution.TaskDriver }

func (d unansweredStop) Stop(context.Context, execution.TaskRef) error {
	return execution.ErrTaskUnobservable
}

// A failed Stop can leave the process running. Even if it subsequently exits
// zero, a timeout already observed and recorded must never turn into a pass.
func TestARecordedTimeoutSurvivesAFailedSettlement(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	a.tasks.AbandonAt = func(s string) bool { return s == "started" }
	ctx := context.Background()
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	key := taskKeyOf(step)
	// sql-guarantee: represent downtime beyond the original execution budget.
	if _, err := a.db.Exec(`UPDATE task_attempts SET deadline=now()-interval '1 second' WHERE operation=?`, key); err != nil {
		t.Fatal(err)
	}
	a.tasks.Driver = unansweredStop{fake}
	b := startTaskAppTown(t, a.db, a.dir, a.image, a.definitions)
	b.tasks.Driver, b.tasks.Poll, b.tasks.SettleTries = a.tasks.Driver, a.tasks.Poll, 1
	dbtest.FailNextStatement(t, a.db, "-- name: FinishAttempt")
	b.recover(t)
	b.work.Wait()
	prior, err := tasks.New(a.db).Get(ctx, key)
	if err != nil || prior.Observed != tasks.Collecting || prior.ExitCode == nil || *prior.ExitCode != -1 {
		t.Fatalf("timeout boundary: %+v %v", prior, err)
	}
	fake.Exit(key, 0) // the un-stopped command finishes after its budget
	b.tasks.Driver = fake
	c := restarted(t, b)
	c.work.Wait()
	got, err := workflow.ReadStepIn(ctx, a.db, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workflow.StatusError || !strings.Contains(string(got.Evidence), "timed out") {
		t.Fatalf("recorded timeout changed after recovery: status=%s evidence=%s", got.Status, got.Evidence)
	}
}

// The stop is already durable. Failure to redundantly record it must not
// prevent the provider effect, leaving a live task with a terminal step.
func TestARecordedStopIsCarriedOutWithoutWritingItAgain(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	a.tasks.AbandonAt = func(s string) bool { return s == "started" }
	ctx := context.Background()
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	key := taskKeyOf(step)
	t.Cleanup(func() {
		_ = fake.Stop(ctx, execution.TaskRef{Key: key})
		_ = fake.Release(ctx, execution.TaskRef{Key: key})
	})
	if err := tasks.New(a.db).RequestStop(ctx, key); err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, a.db, "-- name: RequestStop")
	b := restarted(t, a)
	b.work.Wait()
	obs, err := fake.Observe(ctx, execution.TaskRef{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	got, err := workflow.ReadStepIn(ctx, a.db, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.State == execution.TaskRunning {
		t.Fatalf("durable stop abandoned: provider=%s step=%s", obs.State, got.Status)
	}
}

type unansweredCleanup struct{ execution.TaskDriver }

func (d unansweredCleanup) Stop(context.Context, execution.TaskRef) error {
	return execution.ErrTaskUnobservable
}

func (d unansweredCleanup) Release(context.Context, execution.TaskRef) error {
	return execution.ErrTaskUnobservable
}

// A durable timeout is not proof that its process stopped. If neither kill
// nor remove was answered, startup must still carry out the recorded stop.
func TestATimeoutKeepsItsUnfinishedStopRecoverable(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	a.tasks.AbandonAt = func(s string) bool { return s == "started" }
	ctx := context.Background()
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	key := taskKeyOf(step)
	t.Cleanup(func() { _ = fake.Release(context.Background(), execution.TaskRef{Key: key}) })
	// sql-guarantee: model downtime beyond the admitted ten-minute budget.
	if _, err := a.db.Exec(`UPDATE task_attempts SET deadline=now()-interval '1 second' WHERE operation=?`, key); err != nil {
		t.Fatal(err)
	}
	a.tasks.Driver = unansweredCleanup{fake}
	b := restarted(t, a)
	b.work.Wait()
	got, err := workflow.ReadStepIn(ctx, a.db, step.ID)
	if err != nil || got.Status != workflow.StatusError || !strings.Contains(string(got.Evidence), "timed out") {
		t.Fatalf("timeout was not settled: %+v %v", got, err)
	}
	prior, err := tasks.New(a.db).Get(ctx, key)
	if err != nil || !prior.StopWanted {
		t.Fatalf("stop was not recorded: %+v %v", prior, err)
	}
	// Connectivity recovers before the next AppTown restart.
	b.tasks.Driver = fake
	c := restarted(t, b)
	c.work.Wait()
	obs, err := fake.Observe(ctx, execution.TaskRef{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	if obs.State == execution.TaskRunning {
		t.Fatalf("timeout forgot its unfinished stop across restart: recorded=%s desiredStop=%v provider=%s step=%s", prior.Observed, prior.StopWanted, obs.State, got.Status)
	}
	// Once its stop is confirmed, the attempt is settled and released, a
	// timeout still, with its code.
	if _, err := c.tasks.ReleaseSettled(ctx); err != nil {
		t.Fatal(err)
	}
	settled, err := tasks.New(a.db).Get(ctx, key)
	if err != nil || settled.Observed != tasks.Settled || !settled.Released || !settled.TimedOut || settled.ExitCode == nil || *settled.ExitCode != -1 {
		t.Fatalf("the recovered timeout's receipt: %+v %v", settled, err)
	}
}

// An exit waiting for its step settlement remains an unsettled attempt.
// A stop recorded at that boundary must not disappear behind collection.
func TestCollectingAnExitDoesNotDiscardARecordedStop(t *testing.T) {
	t.Parallel()
	a, _, step := fakeTaskFixture(t, false)
	a.tasks.SettleTries = 1
	ctx := context.Background()
	dbtest.FailNextStatement(t, a.db, "-- name: FinishAttempt")
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	key := taskKeyOf(step)
	prior, err := tasks.New(a.db).Get(ctx, key)
	if err != nil || prior.Observed != tasks.Collecting || prior.ExitCode == nil || *prior.ExitCode != 0 {
		t.Fatalf("collection boundary: %+v %v", prior, err)
	}
	if err := tasks.New(a.db).RequestStop(ctx, key); err != nil {
		t.Fatal(err)
	}
	b := restarted(t, a)
	b.work.Wait()
	got, err := workflow.ReadStepIn(ctx, a.db, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workflow.StatusError {
		t.Fatalf("collecting ignored the durable stop: status=%s evidence=%s", got.Status, got.Evidence)
	}
}

// A task waiting for its exit is stopped when its run is abandoned: the
// abandonment leaves the step running until its runner returns, and the
// runner's watch asks Work whether the attempt still owns its step, not only
// whether the step row is running. Without that the task ran to its deadline
// in an abandoned run.
func TestATaskIsStoppedWhenItsRunIsAbandoned(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	a.tasks.Poll = 10 * time.Millisecond
	key := taskKeyOf(step)
	executed := make(chan error, 1)
	go func() { executed <- a.work.ExecuteStep(a.ctx, step.ID) }()
	if !poll.Until(context.Background(), 10*time.Second, 5*time.Millisecond, func() bool { return fake.Starts(key) > 0 }) {
		t.Fatal("the task never started")
	}
	if _, err := a.work.Abandon(a.ctx, workflow.StopWork{RunID: step.RunID, Reason: "stop"}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-executed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the task of an abandoned run was not stopped: its runner waits for its deadline")
	}
	if fake.Present(key) {
		t.Fatal("the abandoned run's task is still there")
	}
	got, err := workflow.ReadStepIn(context.Background(), a.db, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == workflow.StatusRunning {
		t.Fatalf("the abandoned run's step still shows running: %s", got.Evidence)
	}
}

// A task that exits after Work has cancelled its abandoned step is still
// collected, settled and released: its outcome is refused by the attempt
// fence (ErrStepAttemptLost), not by the cancellation, so the collector
// settles what it holds. The task runner's own ownership tick is kept out of
// the way, so the exit is what the runner sees.
func TestATaskThatExitsAfterItsRunWasAbandonedIsSettledAndReleased(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	a.tasks.Poll = time.Hour
	key := taskKeyOf(step)
	executed := make(chan error, 1)
	go func() { executed <- a.work.ExecuteStep(a.ctx, step.ID) }()
	if !poll.Until(context.Background(), 10*time.Second, 5*time.Millisecond, func() bool { return fake.Starts(key) > 0 }) {
		t.Fatal("the task never started")
	}
	if _, err := a.work.Abandon(a.ctx, workflow.StopWork{RunID: step.RunID, Reason: "stop"}); err != nil {
		t.Fatal(err)
	}
	// Work's watch (200 ms in the fixture) cancels the step's context well
	// inside this.
	<-time.After(2 * time.Second)
	fake.Exit(key, 0)
	select {
	case err := <-executed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the runner did not return after its task exited")
	}
	attempt, err := tasks.New(a.db).Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Observed != tasks.Settled || fake.Present(key) {
		t.Fatalf("the exited task of an abandoned run is %s, present=%v; want settled and released", attempt.Observed, fake.Present(key))
	}
	got, err := workflow.ReadStepIn(context.Background(), a.db, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == workflow.StatusRunning || got.Status == workflow.StatusPass {
		t.Fatalf("the abandoned run's step is %s: %s", got.Status, got.Evidence)
	}
}
