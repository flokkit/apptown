package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestExecutionKeepsClaimPrivateAndRefreshesAfterRunnerReturns(t *testing.T) {
	t.Parallel()
	q, run := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{
		{Name: "accept", Uses: "script", With: map[string]any{"n": json.Number("9007199254740993")}},
		{Name: "artifact", Uses: "artifact/require", With: map[string]any{"kind": "report"}},
	}}}})
	id, check := admittedStepID(t, q, run, "accept"), admittedStepID(t, q, run, "artifact")
	s := newServiceWithAuthority(q.db, Dependencies{Automatic: []string{"script"}, Report: func(err error) { t.Error(err) }, Runners: map[string]Runner{"script": func(e *Execution) {
		if _, _, _, err := e.ClaimedStep("script"); !errors.Is(err, ErrStepAttemptLost) {
			t.Fatal("participant admitted before claim", err)
		}
		input := e.Step()
		input.ID = idstest.N(999999)
		input.Config[0] = '!'
		if _, err := e.Claim(); err != nil {
			t.Fatal(err)
		}
		projectID, runID, stepID, err := e.ClaimedStep("script")
		if err != nil || projectID != run.ProjectID || runID != run.ID || stepID != id {
			t.Fatalf("participant scope: %d %d %d %v", projectID, runID, stepID, err)
		}
		if _, _, _, err := e.ClaimedStep("deploy"); !errors.Is(err, ErrStepAttemptLost) {
			t.Fatal("participant admitted wrong kind", err)
		}
		exposed := e.Step()
		exposed.ID = idstest.N(999999)
		if err := e.Finish(StepOutcome{Status: StatusPass, Evidence: json.RawMessage(`{"n":9007199254740993}`)}, nil); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := e.ClaimedStep("script"); !errors.Is(err, ErrStepAttemptLost) {
			t.Fatal("participant admitted after completion", err)
		}
		wantEventCount(t, q.db, 0, ofKind("check.evaluated"))
		if _, err := AddArtifactIn(context.Background(), q.db, run.ID, "report", "report", "", "body", time.Now()); err != nil {
			t.Fatal(err)
		}
	}}}, allowProject(run.ProjectID))
	if err := s.ExecuteStep(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	step := wantStepState(t, q.db, id, StatusPass)
	wantJSONField(t, "evidence", step.Evidence, "n", "9007199254740993")
	wantJSONField(t, "config", step.Config, "n", "9007199254740993")
	wantStepState(t, q.db, check, StatusPass)
	wantEventCount(t, q.db, 0, ofKind("advance.ok"))
}

func TestExecutionClaimsOnceAfterWaitingAndRejectsMovedScope(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"competing claim", "abandoned", "project moved", "attempt moved"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			q, run, id := admitSingleStep(t, schema.Step{Name: "accept", Uses: "script"})
			entered, release := make(chan struct{}, 2), make(chan struct{})
			var winners atomic.Int32
			s := newServiceWithAuthority(q.db, Dependencies{Automatic: []string{"script"}, Runners: map[string]Runner{"script": func(e *Execution) {
				entered <- struct{}{}
				<-release
				if _, err := e.Claim(); err == nil {
					winners.Add(1)
					if err := e.Finish(StepOutcome{Status: StatusPass}, nil); err != nil {
						t.Error(err)
					}
				} else if !errors.Is(err, ErrStepAttemptLost) {
					t.Error(err)
				}
			}}}, allowProject(run.ProjectID))
			var wg sync.WaitGroup
			count := 1
			if change == "competing claim" {
				count = 2
			}
			for range count {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := s.ExecuteStep(context.Background(), id); err != nil {
						t.Error(err)
					}
				}()
			}
			for range count {
				<-entered
			}
			switch change {
			case "abandoned":
				transitionStoredRunForTest(t, q.db, run.ID, "", RunAbandoned)
			case "project moved":
				other := workflowTestProject(t, q.db, "other", "Other", "")
				// sql-guarantee: the claim fence changes only the admitted run project while the runner is blocked; no Workflow mutation exposes project reassignment.
				completionExec(t, q.db, `UPDATE runs SET project_id=?,number=number+100000 WHERE id=?`, other, run.ID)
			case "attempt moved":
				step, err := readStep(context.Background(), q.db, id)
				if err != nil {
					t.Fatal(err)
				}
				if err := requireAttemptWrite(workflowsql.New(q.db.SQL()).StartParentCall(context.Background(), workflowsql.StartParentCallParams{ID: id, Attempt: step.Attempt, OldStatus: step.Status.String(), Status: step.Status.String(), EvidenceJson: step.Evidence})); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			wg.Wait()
			want := int32(0)
			if change == "competing claim" {
				want = 1
			}
			if winners.Load() != want {
				t.Fatalf("claimed %d, want %d", winners.Load(), want)
			}
			wantEventCount(t, q.db, int(want), ofKind("step.started"))
		})
	}
}

func TestExecutionOutcomeFailureDoesNotNotifyAndCanBeRetried(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"encoding", "receipt", "commit"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			q, run, id := admitSingleStep(t, schema.Step{Name: "accept", Uses: "script"})
			var notifications int
			s := newServiceWithAuthority(q.db, Dependencies{Automatic: []string{"script"}, Report: func(err error) {
				var verdict *StepError
				if errors.As(err, &verdict) {
					notifications++
				}
			}, Runners: map[string]Runner{"script": func(e *Execution) {
				if _, err := e.Claim(); err != nil {
					t.Fatal(err)
				}
				var encodingError error
				switch fault {
				case "encoding":
					encodingError = errors.New("invalid external evidence")
				case "receipt":
					dbtest.FailNextStatement(t, q.db, "INSERT INTO run_events")
				case "commit":
					completionExec(t, q.db, `CREATE FUNCTION veto_finish_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'finish commit rejected'; END $$`)
					completionExec(t, q.db, `CREATE CONSTRAINT TRIGGER veto_finish_commit AFTER UPDATE ON steps DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION veto_finish_commit()`)
				}
				outcome := StepOutcome{Status: StatusError, Evidence: json.RawMessage(`{"reason":"tool broke"}`)}
				if err := e.Finish(outcome, encodingError); err == nil {
					t.Fatal("fault did not reject outcome")
				}
				if notifications != 0 {
					t.Fatal("uncommitted error notified")
				}
				wantStepState(t, q.db, id, StatusRunning)
				if fault == "commit" {
					completionExec(t, q.db, `DROP TRIGGER veto_finish_commit ON steps`)
				}
				if err := e.Finish(outcome, nil); err != nil {
					t.Fatal(err)
				}
			}}}, allowProject(run.ProjectID))
			if err := s.ExecuteStep(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if notifications != 1 {
				t.Fatalf("notifications=%d", notifications)
			}
			wantEventCount(t, q.db, 1, ofKind("step.finished"))
		})
	}
}

func TestExecutionCannotClaimANewerRetry(t *testing.T) {
	t.Parallel()
	q, run, id := admitSingleStep(t, schema.Step{Name: "accept", Uses: "script"})
	calls := 0
	s := newServiceWithAuthority(q.db, Dependencies{Automatic: []string{"script"}, Runners: map[string]Runner{"script": func(e *Execution) {
		calls++
		if _, err := e.Claim(); err != nil {
			t.Fatal(err)
		}
		if err := e.Finish(StepOutcome{Status: StatusPass}, nil); err != nil {
			t.Fatal(err)
		}
		if calls == 1 {
			// Persist the next retry's pending state while the old runner still exists.
			transitionStoredStepForTest(t, q.db, id, StatusPending)
			if _, err := e.Claim(); !errors.Is(err, ErrStepAttemptLost) {
				t.Fatalf("old execution claimed retry: %v", err)
			}
		}
	}}}, allowProject(run.ProjectID))
	for range 2 {
		if err := s.ExecuteStep(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	wantEqual(t, "attempt", wantStepState(t, q.db, id, StatusPass).Attempt, 2)
}

type refusingEvalGate struct{ why string }

func (g refusingEvalGate) RefusalIn(context.Context, database.Queryer, ids.ID) (string, error) {
	return g.why, nil
}

// A step of an eval's copy starts only while its eval may proceed: a gate
// that says no stops the step, waiting on a person, with why, and the
// runner does nothing.
func TestAStepIsClaimedOnlyWhileItsEvalMayProceed(t *testing.T) {
	t.Parallel()
	for _, why := range []string{"", "eval 3's budget of $5.00 is spent"} {
		q, run := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "work", Uses: "script"}}}}})
		id := admittedStepID(t, q, run, "work")
		evalCopyForTest(t, q.db, run.ProjectID, idstest.N(3))
		ran := false
		s := newServiceWithAuthority(q.db, Dependencies{EvalGate: refusingEvalGate{why}, Automatic: []string{"script"}, Report: func(error) {}, Runners: map[string]Runner{"script": func(e *Execution) {
			if _, err := e.Claim(); err != nil {
				return
			}
			ran = true
			_ = e.Finish(StepOutcome{Status: StatusPass, Evidence: json.RawMessage(`{}`)}, nil)
		}}}, allowProject(run.ProjectID))
		_ = s.ExecuteStep(context.Background(), id)
		if why == "" {
			if !ran {
				t.Fatal("an admitted step did not run")
			}
			continue
		}
		step := wantStepState(t, q.db, id, StatusNeedsHuman)
		if ran {
			t.Fatal("a refused step ran")
		}
		wantJSONField(t, "evidence", step.Evidence, "reason", why)
	}
}
