package workflowexec

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/poll"
)

func TestAScriptTaskCarriesItsProfilesAdmission(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, false)
	a.tasks.Profile.Limits = execution.Limits{CPUs: 2, PIDs: 256}
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	awaitStep(t, a, step.ID, workflow.StatusPass)
	if obs, err := fake.Observe(context.Background(), execution.TaskRef{Key: taskKeyOf(step)}); err != nil || obs.State != execution.TaskAbsent {
		t.Fatalf("a collected task was not released: %+v %v", obs, err)
	}
	attempt, err := tasks.New(a.db).Get(context.Background(), taskKeyOf(step))
	if err != nil || attempt.Observed != tasks.Settled || !strings.HasPrefix(attempt.Backend, "local-docker ") {
		t.Fatalf("the settled attempt: %+v %v", attempt, err)
	}
	got := fake.AdmissionOf(taskKeyOf(step))
	if got.Profile != "local-docker" || got.Network != execution.NetworkDefault || got.Limits.CPUs != 2 || got.Limits.PIDs != 256 || got.Limits.WallClock != 10*time.Minute || got.Expires.IsZero() {
		t.Fatalf("the task's admission: %+v", got)
	}
}

func TestAStopRecordedBeforeARestartIsCarriedOutAfterIt(t *testing.T) {
	t.Parallel()
	for _, backend := range taskBackends {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			stopRecordedBeforeARestart(t, backend)
		})
	}
}

func stopRecordedBeforeARestart(t *testing.T, backend string) {
	a, fake, step := fakeTaskFixture(t, true)
	if backend == "worker" {
		a.onWorker(t, fake)
	}
	a.tasks.AbandonAt = func(s string) bool { return s == "started" }
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	a.tasks.AbandonAt = nil
	if err := tasks.New(a.db).RequestStop(context.Background(), taskKeyOf(step)); err != nil {
		t.Fatal(err)
	}
	restarted(t, a)
	awaitStep(t, a, step.ID, workflow.StatusError)
	// The step's outcome is recorded before its task is removed.
	if !poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, func() bool { return !fake.Present(taskKeyOf(step)) }) {
		t.Fatal("the stopped task was not removed")
	}
}

// A restart does not renew a task's budget: a recovered task whose deadline
// passed while AppTown was down is stopped and its step times out.
func TestARecoveredTaskKeepsItsDeadline(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	a.tasks.AbandonAt = func(s string) bool { return s == "started" }
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	a.tasks.AbandonAt = nil
	// sql-guarantee: time cannot pass in a test; the deadline is moved instead of waiting ten minutes for it.
	if _, err := a.db.Exec(`UPDATE task_attempts SET deadline=now()-interval '1 second' WHERE operation=?`, taskKeyOf(step)); err != nil {
		t.Fatal(err)
	}
	b := restarted(t, a)
	settled := awaitStep(t, b, step.ID, workflow.StatusError)
	if !strings.Contains(string(settled.Evidence), "timed out") {
		t.Fatalf("the step's evidence: %s", settled.Evidence)
	}
	// The step's outcome is recorded before its task is removed.
	if !poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, func() bool { return !fake.Present(taskKeyOf(step)) }) {
		t.Fatal("the task was left on its backend past its deadline")
	}
}

// A task created before a long outage is not started after its admission
// expired.
func TestAnExpiredTaskIsNotStartedAfterARestart(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, false)
	a.tasks.AbandonAt = func(s string) bool { return s == "created" }
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	a.tasks.AbandonAt = nil
	attempt, err := tasks.New(a.db).Get(context.Background(), taskKeyOf(step))
	if err != nil {
		t.Fatal(err)
	}
	expired := attempt.Admitted
	expired.Expires = time.Now().Add(-time.Minute)
	body, _ := json.Marshal(expired)
	// sql-guarantee: time cannot pass in a test; the recorded expiry is moved instead of waiting past it.
	if _, err := a.db.Exec(`UPDATE task_attempts SET admission=? WHERE operation=?`, string(body), taskKeyOf(step)); err != nil {
		t.Fatal(err)
	}
	b := restarted(t, a)
	settled := awaitStep(t, b, step.ID, workflow.StatusError)
	if !strings.Contains(string(settled.Evidence), "expired") || fake.Starts(taskKeyOf(step)) != 0 {
		t.Fatalf("an expired task: started %d times; evidence %s", fake.Starts(taskKeyOf(step)), settled.Evidence)
	}
	// The step's outcome is recorded before its task is removed.
	if !poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, func() bool { return !fake.Present(taskKeyOf(step)) }) {
		t.Fatal("the expired task was left on its backend")
	}
}

// A task recorded for another backend than the restarted AppTown's is not
// acted on through the wrong driver: it stays recorded and running.
func TestARecordedTaskForAnotherBackendIsLeftAlone(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	a.tasks.AbandonAt = func(s string) bool { return s == "started" }
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	b := startTaskAppTown(t, a.db, a.dir, a.image, a.definitions)
	b.tasks.Driver, b.tasks.Poll, b.tasks.Profile.Name, b.tasks.Backend = fake, a.tasks.Poll, "worker", "worker at https://elsewhere"
	b.recover(t)
	// Whatever recovery dispatched has finished, so a takeover, stop or
	// settlement of the other backend's task would have happened by now.
	drained(t, b)
	got, err := workflow.ReadStepIn(context.Background(), a.db, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := tasks.New(a.db).Get(context.Background(), taskKeyOf(step))
	if err != nil {
		t.Fatal(err)
	}
	obs, err := fake.Observe(context.Background(), execution.TaskRef{Key: taskKeyOf(step)})
	if err != nil || obs.State != execution.TaskRunning || got.Status != workflow.StatusRunning || attempt.Observed == tasks.Settled || fake.Starts(taskKeyOf(step)) != 1 {
		t.Fatalf("step %s, attempt %s, task %+v %v, starts %d", got.Status, attempt.Observed, obs, err, fake.Starts(taskKeyOf(step)))
	}
	fake.Exit(taskKeyOf(step), 0)
}

// A task's deadline is written and judged by one clock, the task runner's:
// with that clock an hour ahead of the wall clock, or twelve minutes behind
// it, a script task given ten minutes runs and passes, and its deadline is
// ten minutes from the runner's now. (The backend judges the admission's
// expiry, five minutes past the deadline, by its own clock, so the clock
// behind is kept within that.)
//
// Kills: the deadline written from the wall clock (an hour ahead it is
// already past) or judged by it (twelve minutes behind it is already past).
func TestATasksDeadlineIsTheTaskRunnersClock(t *testing.T) {
	t.Parallel()
	for _, skew := range []time.Duration{time.Hour, -12 * time.Minute} {
		t.Run(skew.String(), func(t *testing.T) {
			t.Parallel()
			a, _, step := fakeTaskFixture(t, false)
			a.tasks.Now = func() time.Time { return time.Now().Add(skew) }
			if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
				t.Fatal(err)
			}
			awaitStep(t, a, step.ID, workflow.StatusPass)
			attempt, err := tasks.New(a.db).Get(context.Background(), taskKeyOf(step))
			if err != nil {
				t.Fatal(err)
			}
			if left := attempt.Deadline.Sub(time.Now().Add(skew)); left <= 0 || left > 10*time.Minute {
				t.Fatalf("the deadline is %s from the runner's now, want within ten minutes", left)
			}
		})
	}
}

// deadlineDriver is a task driver that records how long each Start and
// Observe was given by its context: a real backend's client stops waiting
// then. (It goes on regardless, so a wrong wait fails the test, not hangs it.)
type deadlineDriver struct {
	execution.TaskDriver
	mu    sync.Mutex
	given map[string][]time.Duration
}

func (d *deadlineDriver) record(ctx context.Context, call string) {
	if deadline, ok := ctx.Deadline(); ok {
		d.mu.Lock()
		d.given[call] = append(d.given[call], time.Until(deadline))
		d.mu.Unlock()
	}
}

func (d *deadlineDriver) Start(ctx context.Context, ref execution.TaskRef) error {
	d.record(ctx, "start")
	return d.TaskDriver.Start(ctx, ref)
}

func (d *deadlineDriver) Observe(ctx context.Context, ref execution.TaskRef) (execution.TaskObservation, error) {
	d.record(ctx, "observe")
	return d.TaskDriver.Observe(ctx, ref)
}

// A task's deadline is a time on the runner's clock; the backend is given
// the real time left until it. With the runner's clock an hour ahead of the
// real one, or twelve minutes behind, the start and each observation are
// given at most the task's ten minutes, and never a context already ended.
//
// Kills: a context given the deadline itself (an hour ahead, the backend
// would be waited on for an hour and ten minutes; twelve minutes behind, its
// start is refused as already past).
func TestATasksWaitsAreTheTimeLeftOnTheRunnersClock(t *testing.T) {
	t.Parallel()
	for _, skew := range []time.Duration{time.Hour, -12 * time.Minute} {
		t.Run(skew.String(), func(t *testing.T) {
			t.Parallel()
			a, _, step := fakeTaskFixture(t, false)
			a.tasks.Now = func() time.Time { return time.Now().Add(skew) }
			driver := &deadlineDriver{TaskDriver: a.tasks.Driver, given: map[string][]time.Duration{}}
			a.tasks.Driver = driver
			if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
				t.Fatal(err)
			}
			awaitStep(t, a, step.ID, workflow.StatusPass)
			driver.mu.Lock()
			defer driver.mu.Unlock()
			if len(driver.given["start"]) == 0 || len(driver.given["observe"]) == 0 {
				t.Fatalf("calls with a deadline: %v", driver.given)
			}
			for call, given := range driver.given {
				for _, left := range given {
					if left <= 0 || left > 10*time.Minute {
						t.Fatalf("%s was given %s, want within the task's ten minutes", call, left)
					}
				}
			}
		})
	}
}
