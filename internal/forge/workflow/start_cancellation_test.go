package workflow

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

// Cancel at the exact boundary, after the real readiness transaction commits.
// Everything downstream, including Service's Dispatch binding, remains real.
type cancelAfterReadiness struct {
	runStartRecords
	cancel context.CancelFunc
}

func (r cancelAfterReadiness) prepared(ctx context.Context, id ids.ID) error {
	if err := r.runStartRecords.prepared(ctx, id); err != nil {
		return err
	}
	r.cancel()
	return nil
}

func TestStartRunDispatchesAfterCallerCancellationFollowingReadiness(t *testing.T) {
	testDispatchAfterReadinessCancellation(t, false)
}

func TestCreateAndRunDispatchesAfterCallerCancellationFollowingReadiness(t *testing.T) {
	testDispatchAfterReadinessCancellation(t, true)
}

func testDispatchAfterReadinessCancellation(t *testing.T, invoke bool) {
	t.Helper()
	t.Parallel()
	var calls atomic.Int32
	s, in := invocationRunnerFixture(t, func(task *Execution) {
		calls.Add(1)
		if _, err := task.Claim(); err != nil {
			t.Error(err)
			return
		}
		if err := task.Finish(StepOutcome{Status: StatusPass}, nil); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.starter.records = cancelAfterReadiness{s.starter.records, cancel}
	var ch *Change
	var run *Run
	var err error
	if invoke {
		ch, run, err = s.CreateAndRun(ctx, in)
	} else {
		ch, err = s.Create(ctx, in.Change)
		if err != nil {
			t.Fatal(err)
		}
		run, err = s.StartRun(ctx, StartChangeRun{ChangeID: ch.ID, Workflow: in.Workflow})
	}
	if err != nil || ch == nil || run == nil || ctx.Err() != context.Canceled {
		t.Fatalf("ready admission: change=%+v run=%+v canceled=%v error=%v", ch, run, ctx.Err(), err)
	}
	s.Wait()
	if calls.Load() != 1 {
		t.Fatalf("runner calls=%d; ready work was not handed off exactly once", calls.Load())
	}
	ready, err := workflowsql.New(s.db.SQL()).RunStartByID(t.Context(), run.ID)
	if err != nil || !ready.Ready {
		t.Fatalf("readiness: %+v %v", ready, err)
	}
	stored, err := s.Change(t.Context(), ch.ID)
	if err != nil || stored.State != ChangeInProgress || stored.RunID != run.ID {
		t.Fatalf("caller cancellation settled ready work: %+v %v", stored, err)
	}
}

func TestCancellationDuringPreparationKeepsEachAdmissionContract(t *testing.T) {
	t.Parallel()
	for _, invoke := range []bool{false, true} {
		name := "existing start retains unready run"
		if invoke {
			name = "new invoke abandons unready work"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, in := invocationRunnerFixture(t, func(*Execution) { t.Error("unready work dispatched") })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			s.starter.runtime.Prepare = func(context.Context, *Run) error { cancel(); return ctx.Err() }
			var ch *Change
			var run *Run
			var err error
			if invoke {
				ch, run, err = s.CreateAndRun(ctx, in)
			} else {
				ch, err = s.Create(ctx, in.Change)
				if err != nil {
					t.Fatal(err)
				}
				run, err = s.StartRun(ctx, StartChangeRun{ChangeID: ch.ID, Workflow: in.Workflow})
			}
			if !errors.Is(err, context.Canceled) || ch == nil || run == nil {
				t.Fatalf("cancelled preparation: %+v %+v %v", ch, run, err)
			}
			s.Wait()
			ready, err := workflowsql.New(s.db.SQL()).RunStartByID(t.Context(), run.ID)
			if err != nil || ready.Ready {
				t.Fatalf("cancelled preparation published readiness: %+v %v", ready, err)
			}
			stored, err := s.Change(t.Context(), ch.ID)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := s.Snapshot(t.Context(), run.ID, DetailView)
			if err != nil {
				t.Fatal(err)
			}
			wantChange, wantRun := ChangeInProgress, RunRunning
			if invoke {
				wantChange, wantRun = ChangeAbandoned, RunAbandoned
			}
			if stored.State != wantChange || snapshot.Run.Status != wantRun {
				t.Fatalf("cancellation changed its admission contract: %s/%s, want %s/%s", stored.State, snapshot.Run.Status, wantChange, wantRun)
			}
		})
	}
}

func TestCreateAndRunCancelledBeforeAdmissionCreatesNothing(t *testing.T) {
	t.Parallel()
	s, in := invocationFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ch, run, err := s.CreateAndRun(ctx, in)
	if !errors.Is(err, context.Canceled) || ch != nil || run != nil {
		t.Fatalf("cancelled admission: %+v %+v %v", ch, run, err)
	}
	assertNoInvocation(t, s)
}
