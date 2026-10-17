package workflowexec

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/diskspace"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/execution/executionfake"
	"flokkit.io/forge/internal/forge/tasks"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

// When Tasks refuses a task's outcome because its record has moved on, the
// record is the truth: nothing is collected, and the step becomes what the
// record says, never a success.

// countingResults passes every collection through, the first one corrupt
// when corruptFirst, and counts them.
func countingResults(collections *atomic.Int32, corruptFirst bool) func(int, http.ResponseWriter, *httptest.ResponseRecorder) {
	return func(n int, w http.ResponseWriter, reply *httptest.ResponseRecorder) {
		collections.Store(int32(n))
		if n == 1 && corruptFirst {
			corruptResult(nil)(n, w, reply)
			return
		}
		pass(w, reply)
	}
}

// An attempt whose record moved on while its task ran refuses the task's
// exit: the result is not collected, and the step becomes what the record
// says. Held: an error naming the held result, the workspace kept.
// Settled: nothing more is written. Refused: an error, the attempt settled
// with nothing held.
func TestAnExitTheRecordRefusesIsNeverCollected(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		decide func(*tasks.Records, string) error
		check  func(*testing.T, *taskForge, *executionfake.Tasks, *workflow.Step)
	}{
		"held": {func(r *tasks.Records, key string) error {
			return r.Hold(context.Background(), key, "held while it ran")
		},
			func(t *testing.T, a *taskForge, fake *executionfake.Tasks, step *workflow.Step) {
				got := awaitStep(t, a, step.ID, workflow.StatusError)
				if !strings.Contains(string(got.Evidence), "result not kept: held while it ran") {
					t.Fatalf("the step's evidence: %s", got.Evidence)
				}
				requireHeld(t, a, fake, taskKeyOf(step), "after the step")
			}},
		"settled": {func(r *tasks.Records, key string) error {
			return r.Settle(context.Background(), key, nil, "settled while it ran")
		},
			func(t *testing.T, a *taskForge, _ *executionfake.Tasks, step *workflow.Step) {
				if got, err := workflow.ReadStepIn(context.Background(), a.db, step.ID); err != nil || got.Status != workflow.StatusRunning {
					t.Fatalf("the step was written after its attempt settled: %v %v", got.Status, err)
				}
			}},
		"refused": {func(r *tasks.Records, key string) error {
			return r.Refuse(context.Background(), key, "refused while it ran")
		},
			func(t *testing.T, a *taskForge, _ *executionfake.Tasks, step *workflow.Step) {
				got := awaitStep(t, a, step.ID, workflow.StatusError)
				if !strings.Contains(string(got.Evidence), "refused while it ran") {
					t.Fatalf("the step's evidence: %s", got.Evidence)
				}
				if attempt, err := tasks.New(a.db).Get(context.Background(), taskKeyOf(step)); err != nil || attempt.Observed != tasks.Settled || !attempt.Released {
					t.Fatalf("the refused attempt: %+v %v", attempt, err)
				}
			}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, fake, step := fakeTaskFixture(t, true)
			var collections atomic.Int32
			state := resultWorker(t, a, fake, countingResults(&collections, false))
			executed := make(chan error, 1)
			go func() { executed <- a.work.ExecuteStep(a.ctx, step.ID) }()
			key := taskKeyOf(step)
			awaitStarted(t, fake, key, "the task never started on the worker")
			writeRemote(t, filepath.Join(state, "workspaces", key), map[string]string{"only-copy.txt": "keep this work\n"})
			if err := c.decide(tasks.New(a.db), key); err != nil {
				t.Fatal(err)
			}
			fake.Exit(key, 0)
			if err := <-executed; err != nil {
				t.Fatal(err)
			}
			if n := collections.Load(); n != 0 {
				t.Fatalf("the refused outcome was collected %d times", n)
			}
			c.check(t, a, fake, step)
		})
	}
}

// A hold the record refuses (the attempt was held meanwhile, for another
// reason) is not tried again as a failed write: the step becomes what the
// record says.
func TestAHoldTheRecordRefusesEndsTheStepFromTheRecord(t *testing.T) {
	t.Parallel()
	var db *database.DB
	a, _, step, _ := runOnResultWorkerWith(t, map[string]string{"only-copy.txt": "keep this work\n"},
		corruptResult(func() {
			var running ids.ID
			// sql-guarantee: the fixture's one step, read from inside the worker's reply; the test holds no handle on it yet.
			if err := db.QueryRow(`SELECT id FROM steps WHERE status='running'`).Scan(&running); err != nil {
				t.Error(err)
			}
			if err := tasks.New(db).Hold(context.Background(), "step-"+running.String()+"-attempt-1", "held meanwhile"); err != nil {
				t.Error(err)
			}
		}), func(a *taskForge) { db = a.db })
	got, err := workflow.ReadStepIn(context.Background(), a.db, step.ID)
	if err != nil || got.Status != workflow.StatusError || !strings.Contains(string(got.Evidence), "result not kept: held meanwhile") {
		t.Fatalf("the step: %v %s %v", got.Status, got.Evidence, err)
	}
}

// holdingAtEnsure holds the attempt in its record, then refuses its request
// before creating anything.
type holdingAtEnsure struct {
	*executionfake.Tasks
	records *tasks.Records
}

func (d holdingAtEnsure) Ensure(ctx context.Context, spec execution.TaskSpec) (execution.TaskObservation, error) {
	if err := d.records.Hold(ctx, spec.Key, "held before it was created"); err != nil {
		return execution.TaskObservation{}, err
	}
	return execution.TaskObservation{}, execution.ErrUnsupported
}

// A refusal the record does not accept (the attempt is held) is not tried
// again as a failed write: the step becomes what the record says.
func TestARefusalTheRecordRefusesEndsTheStepFromTheRecord(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	a.tasks.Driver = holdingAtEnsure{fake, tasks.New(a.db)}
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	got, err := workflow.ReadStepIn(context.Background(), a.db, step.ID)
	if err != nil || got.Status != workflow.StatusError || !strings.Contains(string(got.Evidence), "result not kept: held before it was created") {
		t.Fatalf("the step: %v %s %v", got.Status, got.Evidence, err)
	}
}

// refusingForDisk refuses every request, before creating anything, for
// disk space, as a worker or local Docker short of space does.
type refusingForDisk struct{ *executionfake.Tasks }

func (refusingForDisk) Ensure(context.Context, execution.TaskSpec) (execution.TaskObservation, error) {
	return execution.TaskObservation{}, fmt.Errorf("%w: the worker's state folder (/state) has 0.5 GiB free of 100.0 GiB; new work needs at least 5.0 GiB free", diskspace.ErrRefused)
}

// A task refused for disk space made nothing: its step ends with the
// refusal's words, and its attempt is settled and released rather than
// left unknown and observed again.
//
// Kills: diskspace.ErrRefused dropped from the clean-refusal case (the
// attempt is failed as an unknown effect instead).
func TestATaskRefusedForDiskSpaceEndsItsStepWithNothingLeft(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	a.tasks.Driver = refusingForDisk{fake}
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	got, err := workflow.ReadStepIn(context.Background(), a.db, step.ID)
	if err != nil || got.Status == workflow.StatusPass || !strings.Contains(string(got.Evidence), "0.5 GiB free of 100.0 GiB") {
		t.Fatalf("the step: %v %s %v", got.Status, got.Evidence, err)
	}
	if attempt, err := tasks.New(a.db).Get(context.Background(), taskKeyOf(step)); err != nil || attempt.Observed != tasks.Settled || !attempt.Released {
		t.Fatalf("the refused attempt: %+v %v", attempt, err)
	}
}

// A held attempt whose step a restart adopts (an earlier invocation of the
// attempt is unsettled, and the held step's error cannot be recorded) is
// not collected again, though collecting would now succeed: it stays held
// and its step is never settled as a success.
func TestARecoveredHeldAttemptIsNeverCollectedAgain(t *testing.T) {
	t.Parallel()
	var collections atomic.Int32
	a, fake, step, _ := runOnResultWorkerWith(t, map[string]string{"only-copy.txt": "keep this work\n"}, countingResults(&collections, true),
		func(a *taskForge) { a.tasks.AbandonAt = func(s string) bool { return s == "held" } })
	a.tasks.AbandonAt = nil
	key := taskKeyOf(step)
	requireHeld(t, a, fake, key, "before the restart")
	ctx := context.Background()
	records := tasks.New(a.db)
	held, err := records.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	earlier := held.Admission
	earlier.Operation, earlier.Invocation, earlier.Container = key+"-earlier", "earlier", key+"-earlier"
	if _, err := records.Reserve(ctx, earlier); err != nil {
		t.Fatal(err)
	}
	if err := records.Start(ctx, earlier.Operation); err != nil {
		t.Fatal(err)
	}
	if err := records.Exited(ctx, earlier.Operation, 0); err != nil {
		t.Fatal(err)
	}
	restore := a.db.FailStatements("-- name: FinishAttempt", 1000)
	b := restarted(t, a)
	drained(t, b)
	restore()
	if n := collections.Load(); n != 1 {
		t.Fatalf("the held result was collected %d times", n)
	}
	if got, err := workflow.ReadStepIn(ctx, a.db, step.ID); err != nil || got.Status == workflow.StatusPass {
		t.Fatalf("the held step: %v %v", got.Status, err)
	}
	requireHeld(t, b, fake, key, "after recovery")
}

// decidingAtStart records decide in the attempt's record as its task
// starts.
type decidingAtStart struct {
	*executionfake.Tasks
	decide func(ctx context.Context, key string) error
}

func (d decidingAtStart) Start(ctx context.Context, ref execution.TaskRef) error {
	if err := d.decide(ctx, ref.Key); err != nil {
		return err
	}
	return d.Tasks.Start(ctx, ref)
}

// A start the record refuses ends the step from the record at once, the
// task's exit not waited for: held, an error naming the held result; in a
// state a start does not follow (an exit recorded), an error with its
// outcome unknown, never a success.
func TestAStartTheRecordRefusesIsNotWaitedFor(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		decide  func(r *tasks.Records) func(context.Context, string) error
		reason  string
		becomes tasks.Observed
	}{
		"held": {func(r *tasks.Records) func(context.Context, string) error {
			return func(ctx context.Context, key string) error { return r.Hold(ctx, key, "held as it started") }
		}, "result not kept: held as it started", tasks.Held},
		"not accepted": {func(r *tasks.Records) func(context.Context, string) error {
			return func(ctx context.Context, key string) error { return r.Exited(ctx, key, 0) }
		}, "its outcome is unknown", tasks.Unknown},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, fake, step := fakeTaskFixture(t, true)
			a.tasks.Driver = decidingAtStart{fake, c.decide(tasks.New(a.db))}
			key := taskKeyOf(step)
			t.Cleanup(func() { fake.Exit(key, 0) })
			executed := make(chan error, 1)
			go func() { executed <- a.work.ExecuteStep(a.ctx, step.ID) }()
			select {
			case err := <-executed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the step waited for a task whose start its record refused")
			}
			got := awaitStep(t, a, step.ID, workflow.StatusError)
			if !strings.Contains(string(got.Evidence), c.reason) {
				t.Fatalf("the step's evidence: %s", got.Evidence)
			}
			if attempt, err := tasks.New(a.db).Get(context.Background(), key); err != nil || attempt.Observed != c.becomes {
				t.Fatalf("the attempt: %+v %v", attempt, err)
			}
		})
	}
}
