package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

// A step claimed just before its run is abandoned ends in error once its
// runner returns, whether the runner tried to record an outcome (refused by
// the attempt fence) or returned without one. It no longer shows "running"
// in an abandoned run. A runner of a run still running
// that returns without an outcome leaves its step to task recovery.
func TestAStepRunningWhenItsRunIsAbandonedEndsWhenItsRunnerReturns(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ abandoned, finishes bool }{{true, true}, {true, false}, {false, false}} {
		finishes := c.finishes
		q, run, _ := activityFixture(t)
		step := insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "dep-scan", Type: "scan", Status: StatusPending.String()})
		abandon := NewAbandonments(q.db, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{})
		var during StepState
		runner := func(e *Execution) {
			if _, err := e.Claim(); err != nil {
				t.Error(err)
				return
			}
			if c.abandoned {
				if _, err := abandon.Abandon(context.Background(), Abandonment{RunID: run.ID, Who: "operator", Reason: "stop"}); err != nil {
					t.Error(err)
					return
				}
			}
			current, _ := ReadStepIn(context.Background(), q.db, step)
			during = current.Status
			if finishes {
				_ = e.Finish(StepOutcome{Status: StatusPass, Evidence: json.RawMessage(`{}`)}, nil)
			}
		}
		s := newServiceWithAuthority(q.db, Dependencies{Automatic: []string{"scan"}, Runners: map[string]Runner{"scan": runner}}, allowProject(run.ProjectID))
		_ = s.ExecuteStep(context.Background(), step)
		got, err := ReadStepIn(context.Background(), q.db, step)
		if err != nil {
			t.Fatal(err)
		}
		var evidence struct{ Reason string }
		_ = json.Unmarshal(got.Evidence, &evidence)
		if !c.abandoned {
			if got.Status != StatusRunning {
				t.Fatalf("a live run's step left without an outcome was ended: %s", got.Status)
			}
			continue
		}
		if during != StatusRunning || got.Status != StatusError || evidence.Reason != AbandonedWhileRunning {
			t.Fatalf("finishes=%v: while the runner ran %s; after it returned %s %s", finishes, during, got.Status, got.Evidence)
		}
	}
}

// A call step has no runner to return: abandoning its run's tree ends it
// with its child.
func TestAbandoningARunEndsItsRunningCalls(t *testing.T) {
	t.Parallel()
	q, root, _ := activityFixture(t)
	child, call := activityChild(t, q.db, root, "checks")
	abandon := NewAbandonments(q.db, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{})
	result, err := abandon.Abandon(context.Background(), Abandonment{RunID: root.ID, Who: "operator", Reason: "stop"})
	if err != nil || len(result.RunIDs) != 2 || result.RunIDs[1] != child.ID {
		t.Fatalf("abandon: %+v %v", result, err)
	}
	got, err := ReadStepIn(context.Background(), q.db, call)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusError {
		t.Fatalf("the call step is %s after its tree was abandoned", got.Status)
	}
}

// A runner that waits on its context (a local agent, a wait for a slot) is
// told when its run is abandoned under it: Work cancels the execution's
// context with ErrStepAttemptLost, the runner returns, and the step ends.
// Current still answers "lost" through the cancelled context. A runner in a
// run still running, and one whose own step already finished before its run
// ended, keep their context.
func TestAnAbandonedRunCancelsItsWaitingRunner(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name            string
		abandon, finish bool
		wantCancelled   bool
	}{
		{name: "abandoned while waiting", abandon: true, wantCancelled: true},
		{name: "run still running"},
		{name: "finished, then its run ended", abandon: true, finish: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			q, run, _ := activityFixture(t)
			step := insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "agent", Type: "scan", Status: StatusPending.String()})
			abandon := NewAbandonments(q.db, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{})
			var cause, current, finished, inAttempt error
			wrote := false
			runner := func(e *Execution) {
				if _, err := e.Claim(); err != nil {
					t.Error(err)
					return
				}
				if c.finish {
					if err := e.Finish(StepOutcome{Status: StatusPass, Evidence: json.RawMessage(`{}`)}, nil); err != nil {
						t.Error(err)
						return
					}
				}
				if c.abandon {
					if _, err := abandon.Abandon(context.Background(), Abandonment{RunID: run.ID, Who: "operator", Reason: "stop"}); err != nil {
						t.Error(err)
						return
					}
				}
				// Long enough for many watches; a cancelled context ends it early.
				wait := time.NewTimer(500 * time.Millisecond)
				defer wait.Stop()
				if c.wantCancelled {
					wait.Reset(10 * time.Second)
				}
				select {
				case <-e.Context().Done():
					cause = context.Cause(e.Context())
					current = e.Current()
					// The fence still answers through the cancelled context:
					// a collector settles on "lost", not on a cancellation.
					finished = e.Finish(StepOutcome{Status: StatusPass, Evidence: json.RawMessage(`{}`)}, nil)
					inAttempt = e.InAttempt("scan", func(*database.Tx, int64) error { wrote = true; return nil })
				case <-wait.C:
				}
			}
			s := newServiceWithAuthority(q.db, Dependencies{Automatic: []string{"scan"}, Runners: map[string]Runner{"scan": runner}, AttemptWatch: 10 * time.Millisecond}, allowProject(run.ProjectID))
			_ = s.ExecuteStep(context.Background(), step)
			if cancelled := cause != nil; cancelled != c.wantCancelled {
				t.Fatalf("cancelled=%v (cause %v), want %v", cancelled, cause, c.wantCancelled)
			}
			if !c.wantCancelled {
				return
			}
			if !errors.Is(cause, ErrStepAttemptLost) || !errors.Is(current, ErrStepAttemptLost) {
				t.Fatalf("cause %v, Current after the cancel %v; both must be ErrStepAttemptLost", cause, current)
			}
			if !errors.Is(finished, ErrStepAttemptLost) || !errors.Is(inAttempt, ErrStepAttemptLost) || wrote {
				t.Fatalf("after the loss Finish %v, InAttempt %v (wrote %v); the fence must answer ErrStepAttemptLost", finished, inAttempt, wrote)
			}
			got, err := ReadStepIn(context.Background(), q.db, step)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != StatusError {
				t.Fatalf("the abandoned run's step is %s after its runner returned", got.Status)
			}
		})
	}
}

// Work stopping is not proof an attempt was lost: a step whose context ends
// with Work's lifetime keeps that cancellation in its fenced transactions,
// so a runner cut off at shutdown leaves its step for startup repair rather
// than reading it as abandoned.
func TestShutdownIsNotReadAsALostAttempt(t *testing.T) {
	t.Parallel()
	q, run, id := admitSingleStep(t, schema.Step{Name: "accept", Uses: "script"})
	life, end := context.WithCancel(context.Background())
	defer end()
	var finished error
	s := New(life, q.db, Dependencies{Automatic: []string{"script"}, Runners: map[string]Runner{"script": func(e *Execution) {
		if _, err := e.Claim(); err != nil {
			t.Error(err)
			return
		}
		end()
		finished = e.Finish(StepOutcome{Status: StatusPass, Evidence: json.RawMessage(`{}`)}, nil)
	}}})
	s.authority = allowProject(run.ProjectID)
	s.dispatcher.runners["script"](id)
	if finished == nil || errors.Is(finished, ErrStepAttemptLost) || !errors.Is(finished, context.Canceled) {
		t.Fatalf("Finish after Work stopped: %v; want the cancellation, not a lost attempt", finished)
	}
}

// Work can confirm an attempt's loss while one of its fenced transactions is
// already under way (a task exits as its run is abandoned): the context the
// transaction chose is then cancelled under it. The fence is asked again,
// detached, and answers ErrStepAttemptLost; no outcome or InAttempt write
// lands. A collector then settles and releases what it holds rather than
// reading a cancellation it treats as a failed settlement.
func TestALossDuringAFencedTransactionIsAnsweredByTheFence(t *testing.T) {
	t.Parallel()
	for _, op := range []string{"finish", "in-attempt"} {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			q, run, _ := activityFixture(t)
			step := insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "task", Type: "scan", Status: StatusPending.String()})
			abandon := NewAbandonments(q.db, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{})
			var answer error
			wrote := false
			runner := func(e *Execution) {
				if _, err := e.Claim(); err != nil {
					t.Error(err)
					return
				}
				// After the transaction chose its context: abandon the run and
				// let Work's watch cancel the step before the fence is read.
				e.service.beforeFence = func(e *Execution) {
					e.service.beforeFence = nil
					if _, err := abandon.Abandon(context.Background(), Abandonment{RunID: run.ID, Who: "operator", Reason: "stop"}); err != nil {
						t.Error(err)
					}
					select {
					case <-e.Context().Done():
					case <-time.After(10 * time.Second):
						t.Error("Work never cancelled the abandoned run's step")
					}
				}
				if op == "finish" {
					answer = e.Finish(StepOutcome{Status: StatusPass, Evidence: json.RawMessage(`{}`)}, nil)
				} else {
					answer = e.InAttempt("scan", func(*database.Tx, int64) error { wrote = true; return nil })
				}
			}
			s := newServiceWithAuthority(q.db, Dependencies{Automatic: []string{"scan"}, Runners: map[string]Runner{"scan": runner}, AttemptWatch: 10 * time.Millisecond}, allowProject(run.ProjectID))
			_ = s.ExecuteStep(context.Background(), step)
			if !errors.Is(answer, ErrStepAttemptLost) || wrote {
				t.Fatalf("%s answered %v (wrote %v); the fence must answer ErrStepAttemptLost", op, answer, wrote)
			}
			got, err := ReadStepIn(context.Background(), q.db, step)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status == StatusPass {
				t.Fatalf("an outcome landed for the abandoned run's step: %s", got.Evidence)
			}
		})
	}
}

// A claimed execution kept past its runner's return still records its
// outcome: Work releases the context on return (the watch's resources), but
// whoever kept the execution reaches the fence, as before the release. Work
// that waits on the context stops.
func TestAnExecutionKeptPastItsRunnerStillRecordsItsOutcome(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	step := insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "agent", Type: "scan", Status: StatusPending.String()})
	var kept *Execution
	runner := func(e *Execution) {
		if _, err := e.Claim(); err != nil {
			t.Error(err)
			return
		}
		kept = e
	}
	s := newServiceWithAuthority(q.db, Dependencies{Automatic: []string{"scan"}, Runners: map[string]Runner{"scan": runner}}, allowProject(run.ProjectID))
	if err := s.ExecuteStep(context.Background(), step); err != nil || kept == nil {
		t.Fatalf("execute: %v, claimed %v", err, kept != nil)
	}
	if kept.Context().Err() == nil {
		t.Fatal("a returned runner's context was not released")
	}
	if err := kept.Finish(StepOutcome{Status: StatusPass, Evidence: json.RawMessage(`{}`)}, nil); err != nil {
		t.Fatalf("finish after the runner returned: %v", err)
	}
	got, err := ReadStepIn(context.Background(), q.db, step)
	if err != nil || got.Status != StatusPass {
		t.Fatalf("step after a kept execution finished: %+v %v", got, err)
	}
}
