package workflowexec

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
)

func TestATaskRefusedForItsKeyIsNeverTouched(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, false)
	ctx := context.Background()
	key := taskKeyOf(step)
	foreign, err := fake.Ensure(ctx, execution.TaskSpec{Key: key, Image: "foreign", Argv: []string{"sh", "-c", "true"}, Env: []string{"FAKE_BLOCK=1"}, Admission: execution.Admission{Profile: "foreign", Network: execution.NetworkNone}})
	if err != nil {
		t.Fatal(err)
	}
	if err := fake.Start(ctx, execution.TaskRef{Key: key, ProviderID: foreign.ID}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fake.Release(ctx, execution.TaskRef{Key: key}) })
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	got, err := workflow.ReadStepIn(ctx, a.db, step.ID)
	if err != nil || got.Status != workflow.StatusError {
		t.Fatalf("expected refused admission: %+v %v", got, err)
	}
	b := restarted(t, a)
	b.work.Wait()
	obs, err := fake.Observe(ctx, execution.TaskRef{Key: key, ProviderID: foreign.ID})
	if err != nil || obs.State != execution.TaskRunning {
		t.Fatalf("restart acted on a task whose request was refused: %+v %v", obs, err)
	}
}

type slowWait struct{ execution.TaskDriver }

func (d slowWait) Wait(ctx context.Context, ref execution.TaskRef) (int, error) {
	// A backend round trip takes time, even though Observe already said exited.
	select {
	case <-ctx.Done():
		return -1, ctx.Err()
	case <-time.After(50 * time.Millisecond):
	}
	return d.TaskDriver.Wait(ctx, ref)
}

func TestATaskThatExitedInTimeKeepsItsResultAfterAnOutage(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, false)
	a.tasks.AbandonAt = func(s string) bool { return s == "started" }
	ctx := context.Background()
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	key := taskKeyOf(step)
	obs, err := fake.Observe(ctx, execution.TaskRef{Key: key})
	if err != nil || obs.State != execution.TaskExited {
		t.Fatalf("must finish before simulated outage: %+v %v", obs, err)
	}
	// sql-guarantee: represent the clock passing during downtime, after exit.
	if _, err := a.db.SQL().ExecContext(context.Background(), `UPDATE task_attempts SET deadline=now()-interval '1 second' WHERE operation=$1`, key); err != nil {
		t.Fatal(err)
	}
	a.tasks.Driver = slowWait{fake}
	b := restarted(t, a)
	b.work.Wait()
	got, err := workflow.ReadStepIn(ctx, a.db, step.ID)
	if err != nil || got.Status != workflow.StatusPass {
		t.Fatalf("finished task was reclassified after downtime: %+v %v", got, err)
	}
}

func TestNothingStartsAfterTheRecordedDeadline(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, false)
	a.tasks.AbandonAt = func(s string) bool { return s == "created" }
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	key := taskKeyOf(step)
	// The execution budget ended, but the five-minute grace for observing and stopping has not: grace must not authorize fresh execution.
	// sql-guarantee: time cannot pass in a test; the deadline is moved instead of waiting for it.
	if _, err := a.db.SQL().ExecContext(context.Background(), `UPDATE task_attempts SET deadline=now()-interval '1 second' WHERE operation=$1`, key); err != nil {
		t.Fatal(err)
	}
	b := restarted(t, a)
	b.work.Wait()
	if n := fake.Starts(key); n != 0 {
		t.Fatalf("task started %d times after its execution deadline", n)
	}
}

func TestAChangedWorkerEndpointDoesNotMoveAnAdmittedTask(t *testing.T) {
	t.Parallel()
	a, first, step := fakeTaskFixture(t, false)
	a.onWorker(t, first)
	a.tasks.AbandonAt = func(s string) bool { return s == "created" }
	ctx := context.Background()
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	key := taskKeyOf(step)
	attempt, err := tasks.New(a.db).Get(ctx, key)
	if err != nil || attempt.ProviderID != "" || !first.Present(key) {
		t.Fatalf("create-before-record boundary: %+v %v", attempt, err)
	}
	b := startTaskAppTown(t, a.db, a.dir, a.image, a.definitions)
	second := &executionfake.Tasks{}
	b.onWorker(t, second) // a different authenticated worker endpoint, same profile name
	b.tasks.Poll = a.tasks.Poll
	if b.tasks.Backend == a.tasks.Backend {
		t.Fatal("the two worker endpoints share an identity")
	}
	b.recover(t)
	b.work.Wait()
	if n := second.Starts(key); n != 0 {
		t.Fatalf("task admitted to the first worker ran %d times on the replacement endpoint", n)
	}
}

func TestARecordedStopStopsTheTaskEvenWhenTheStepCannotBeRecorded(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	a.tasks.SettleTries = 1
	ctx := context.Background()
	key := taskKeyOf(step)
	t.Cleanup(func() { _ = fake.Release(ctx, execution.TaskRef{Key: key}) })
	done := make(chan error, 1)
	go func() { done <- a.work.ExecuteStep(a.ctx, step.ID) }()
	awaitStarted(t, fake, key, "task did not start")
	dbtest.FailNextStatement(t, a.db, "-- name: FinishAttempt")
	if err := tasks.New(a.db).RequestStop(ctx, key); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not return")
	}
	obs, err := fake.Observe(ctx, execution.TaskRef{Key: key})
	if err != nil || obs.State == execution.TaskRunning {
		t.Fatalf("durable stop lost its collector without stopping execution: %+v %v", obs, err)
	}
}

type stalledObserve struct {
	execution.TaskDriver
	release <-chan struct{}
}

func (d stalledObserve) Observe(ctx context.Context, ref execution.TaskRef) (execution.TaskObservation, error) {
	select {
	case <-ctx.Done():
		return execution.TaskObservation{}, execution.ErrTaskUnobservable
	case <-d.release:
		return d.TaskDriver.Observe(ctx, ref)
	}
}

func TestAnUnansweredBackendCannotHoldRecoveryPastTheDeadline(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	a.tasks.AbandonAt = func(s string) bool { return s == "started" }
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: simulate an outage extending past the recorded deadline.
	if _, err := a.db.SQL().ExecContext(context.Background(), `UPDATE task_attempts SET deadline=now()-interval '1 second' WHERE operation=$1`, taskKeyOf(step)); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	a.tasks.Driver = stalledObserve{TaskDriver: fake, release: release}
	b := restarted(t, a)
	t.Cleanup(func() { close(release); b.work.Wait() })
	done := make(chan struct{})
	go func() { b.work.Wait(); close(done) }()
	select {
	case <-done:
	// Without a bounded context the collector waits for ever; the window
	// allows for a busy machine running tests in parallel.
	case <-time.After(5 * time.Second):
		t.Fatal("an unanswered Observe still blocks the collector after its deadline; the driver honors cancellation but received no bounded context")
	}
}
