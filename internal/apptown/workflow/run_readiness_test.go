package workflow

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
)

func TestRunReadinessRefusesMissingAndUnpreparedExecution(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"missing", "unready"} {
		for _, boundary := range []string{"claim", "runner", "preparation", "dispatch"} {
			t.Run(state+"/"+boundary, func(t *testing.T) {
				q, run, id := admitSingleStep(t, schema.Step{Name: "work", Uses: "script"})
				// sql-guarantee: Supported historical records can lack their start; no owner operation deletes one or reverses readiness. These fixtures distinguish that absence from a committed start awaiting preparation.
				if state == "missing" {
					// sql-guarantee: Construct supported missing-start history; no runtime operation removes a start.
					completionExec(t, q.db, `DELETE FROM run_starts`)
				} else {
					// sql-guarantee: Isolate an unfinished preparation flag on admitted test records; no runtime operation reverses readiness.
					completionExec(t, q.db, `UPDATE run_starts SET ready=false`)
				}
				ctx := context.Background()
				step, err := readStep(ctx, q.db, id)
				if err != nil {
					t.Fatal(err)
				}
				switch boundary {
				case "claim":
					claim, err := newStepAttempts(q.db, func(string) bool { return true }).Start(ctx, step, "script")
					if err == nil || claim != nil {
						t.Fatalf("unprepared run received claim: %+v %v", claim, err)
					}
				case "runner":
					called := false
					s := &Service{db: q.db, attemptWatch: defaultAttemptWatch, runners: map[string]Runner{"script": func(*Execution) { called = true }}}
					err := s.runExecution(ctx, step, RunRef{})
					if err == nil || called {
						t.Fatalf("unprepared runner invoked: called=%v err=%v", called, err)
					}
				case "preparation":
					pending, err := (runStartStore{db: q.db}).pendingPreparation(ctx, run.ID)
					if state == "missing" {
						if err == nil || pending != nil {
							t.Fatalf("missing start treated as prepared: %+v %v", pending, err)
						}
					} else if err != nil || pending == nil {
						t.Fatalf("real preparation lost: %+v %v", pending, err)
					}
				case "dispatch":
					plan, err := newDispatchPlans(q.db, []string{"script"}).Plan(ctx, run.ID)
					if state == "missing" {
						if err == nil {
							t.Fatalf("missing start yielded dispatch: %+v", plan)
						}
					} else if err != nil || !plan.NeedsPreparation {
						t.Fatalf("real preparation lost: %+v %v", plan, err)
					}
				}
				current, err := readStep(ctx, q.db, id)
				if err != nil || current.Attempt != 0 || current.Status != StatusPending {
					t.Fatalf("refusal changed step: %+v %v", current, err)
				}
				wantEventCount(t, q.db, 0, ofKind("step.started"))
			})
		}
	}
}

func TestRunReadinessProtectsAssertionObservationAndRestart(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"missing", "unready"} {
		t.Run(state, func(t *testing.T) {
			owner, run, id := assertionOwnerFixture(t, "scm/merged", `{}`)
			// sql-guarantee: Only historical storage fixtures can remove or reverse a start; no runtime operation permits either transition.
			if state == "missing" {
				// sql-guarantee: Construct supported missing-start history; no runtime operation removes a start.
				completionExec(t, owner.db, `DELETE FROM run_starts`)
			} else {
				// sql-guarantee: Isolate an unfinished preparation flag on admitted test records; no runtime operation reverses readiness.
				completionExec(t, owner.db, `UPDATE run_starts SET ready=false`)
			}
			called := false
			owner.merge = mergeObserverFunc(func(context.Context, MergeAssertion) (MergeObservation, error) {
				called = true
				return MergeObservation{Merged: true}, nil
			})
			if _, err := owner.Evaluate(context.Background(), Assertion{StepID: id}); err == nil || called {
				t.Fatalf("unready merge observation: called=%v err=%v", called, err)
			}
			wantUntouchedStep(t, owner.db, id, StatusPending)
			candidates, err := newDispatchPlans(owner.db, nil).RestartCandidates(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, candidate := range candidates {
				found = found || candidate == run.ID
			}
			if !found {
				t.Fatal("restart omitted the run with no automatic step")
			}
			err = owner.db.InTx(context.Background(), func(tx *database.Tx) error {
				observed, err := ObserveExecutionIn(context.Background(), tx, ExecutionSelection{Change: ChangeRef{ID: run.ChangeID, ProjectID: run.ProjectID}, RunID: run.ID})
				if state == "missing" {
					if err == nil {
						t.Fatalf("missing readiness observed as normal: %+v", observed)
					}
				} else if err != nil || observed == nil || !observed.Unready {
					t.Fatalf("real preparation observation: %+v %v", observed, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunReadinessGuardsChildRetryAndAdvancement(t *testing.T) {
	t.Parallel()
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			owner, run, agent, _, _ := retryStageFixture(t)
			// sql-guarantee: Construct missing historical evidence or unfinished preparation without inventing a runtime transition that resets readiness.
			if missing {
				// sql-guarantee: Construct supported missing-start history; no runtime operation removes a start.
				completionExec(t, owner.db, `DELETE FROM run_starts`)
			} else {
				// sql-guarantee: Isolate an unfinished preparation flag on admitted test records; no runtime operation reverses readiness.
				completionExec(t, owner.db, `UPDATE run_starts SET ready=false`)
			}
			ctx := context.Background()
			_, err := owner.retryExpected(ctx, retryIntent{RetryCommand: RetryCommand{StepID: agent}, Actor: commandActor{Receipt: "alice"}}, nil)
			var refused runReadinessError
			if !errors.As(err, &refused) || refused.missing != missing {
				t.Fatalf("retry readiness: %v", err)
			}
			wantEventCount(t, owner.db, 0, ofKind("step.retried"))
			_, err = newAdvancer(owner.db, advanceRuntime{}).Advance(ctx, run.ID, "alice")
			if !errors.As(err, &refused) || refused.missing != missing {
				t.Fatalf("advancement reached effects: %v", err)
			}
			admissions := newRunAdmissions(owner.db, NewStages(nil, func(string) bool { return false }), nil)
			_, err = admissions.Create(ctx, StartRequest{ProjectID: run.ProjectID, ChangeID: run.ChangeID, ParentRunID: run.ID, WorkflowName: "child", InputsJSON: "{}", RequestedBy: "alice"}, PreparedRun{Workflow: DefinitionRef{Name: "child", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "child", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}})
			if !errors.As(err, &refused) || refused.missing != missing {
				t.Fatalf("child readiness: %v", err)
			}
			wantRunCount(t, owner.db, 1, func(*Run) bool { return true })
		})
	}
}

func TestHistoricalRunReadinessDoesNotBecomePreparation(t *testing.T) {
	t.Parallel()
	owner, run, _ := completedSendBackFixture(t)
	// sql-guarantee: Model supported terminal history predating run starts.
	// sql-guarantee: Construct supported missing-start history; no runtime operation removes a start.
	completionExec(t, owner.db, `DELETE FROM run_starts WHERE run_id=$1`, run.ID)
	ctx := context.Background()
	if pending, err := (runStartStore{db: owner.db}).pendingPreparation(ctx, run.ID); err != nil || pending != nil {
		t.Fatalf("terminal preparation: %+v %v", pending, err)
	}
	if plan, err := newDispatchPlans(owner.db, nil).Plan(ctx, run.ID); err != nil || plan.NeedsPreparation || len(plan.Steps) > 0 {
		t.Fatalf("terminal dispatch: %+v %v", plan, err)
	}
	if err := owner.db.InTx(ctx, func(tx *database.Tx) error {
		observed, err := ObserveExecutionIn(ctx, tx, ExecutionSelection{Change: ChangeRef{ID: run.ChangeID, ProjectID: run.ProjectID}, RunID: run.ID})
		if err != nil || observed == nil || observed.Unready || observed.Run.Status != RunDone {
			t.Fatalf("terminal observation: %+v %v", observed, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, exists := seenRunStart(t, owner.db, run.ID); exists {
		t.Fatal("history gained a start")
	}
}

func TestRunReadinessRefusesStaleCheckReset(t *testing.T) {
	t.Parallel()
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			owner, run, _, review := retryFixture(t)
			// sql-guarantee: Stale checks need historical finish times, and absent or reversed start evidence is deliberately outside runtime transitions.
			completionExec(t, owner.db, `UPDATE steps SET finished_at=now()-interval '1 minute' WHERE id=$1`, review)
			if missing {
				// sql-guarantee: Construct supported missing-start history; no runtime operation removes a start.
				completionExec(t, owner.db, `DELETE FROM run_starts`)
			} else {
				// sql-guarantee: Isolate an unfinished preparation flag on admitted test records; no runtime operation reverses readiness.
				completionExec(t, owner.db, `UPDATE run_starts SET ready=false`)
			}
			changed, err := (reworkStore{db: owner.db}).refresh(context.Background(), run.ID)
			var refused runReadinessError
			if changed || !errors.As(err, &refused) || refused.missing != missing {
				t.Fatalf("stale checks reset without readiness: changed=%v err=%v", changed, err)
			}
			wantStepState(t, owner.db, review, StatusFail)
			wantRunEventCount(t, owner.db, run.ID, 0, ofKind("step.reverified"))
			wantRetryRevision(t, owner.db, run.ID, 0)
		})
	}
}

func TestRunReadinessPreservesAdmittedOutcomeWithoutRework(t *testing.T) {
	t.Parallel()
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			owner, run, agent, review := retryFixture(t)
			ctx := context.Background()
			retryScopeStep(t, owner.db, review, []byte(`{}`))
			step, err := readStep(ctx, owner.db, review)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := newStepAttempts(owner.db, func(string) bool { return true }).Start(ctx, step, "agent/review")
			if err != nil {
				t.Fatal(err)
			}
			// sql-guarantee: An already-admitted task may be collected from historical storage lacking readiness; no production operation removes or reverses this evidence.
			if missing {
				// sql-guarantee: Construct supported missing-start history; no runtime operation removes a start.
				completionExec(t, owner.db, `DELETE FROM run_starts`)
			} else {
				// sql-guarantee: Isolate an unfinished preparation flag on admitted test records; no runtime operation reverses readiness.
				completionExec(t, owner.db, `UPDATE run_starts SET ready=false`)
			}
			result, err := (reworkStore{db: owner.db}).fail(ctx, claim, StepOutcome{Status: StatusFail, Evidence: []byte(`{"reason":"needs changes"}`), Rework: true})
			var refused runReadinessError
			if err != nil || !result.AgentID.IsZero() || !errors.As(result.refusal, &refused) || refused.missing != missing {
				t.Fatalf("admitted outcome/rework: %+v %v", result, err)
			}
			wantStepState(t, owner.db, review, StatusFail)
			wantStepState(t, owner.db, agent, StatusPass)
			wantRunEventCount(t, owner.db, run.ID, 0, ofKind("step.rework"))
			wantRunEventCount(t, owner.db, run.ID, 3, ofKind("step.finished"))
		})
	}
}

func TestHistoricalAcceptedWorkStartsFreshLandingPreparation(t *testing.T) {
	t.Parallel()
	admissions, request, definition := runAdmissionFixture(t)
	ctx := context.Background()
	source, err := admissions.Create(ctx, request, definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := admissions.Prepared(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	transitionStoredRunForTest(t, admissions.records.db, source.ID, "", RunDone)
	// sql-guarantee: Completed accepted work can predate start records; the new landing must own readiness without backfilling its source.
	// sql-guarantee: Construct supported missing-start history; no runtime operation removes a start.
	completionExec(t, admissions.records.db, `DELETE FROM run_starts WHERE run_id=$1`, source.ID)
	request.AcceptedRunID = source.ID
	request.WorkflowName = "land"
	definition.Workflow.Name = "land"
	definition.Spec.Role = "landing"
	landing, err := admissions.Create(ctx, request, definition)
	if err != nil {
		t.Fatal(err)
	}
	if start, ok := seenRunStart(t, admissions.records.db, landing.ID); !ok || start.Ready {
		t.Fatalf("new landing start: %+v %v", start, ok)
	}
	prepared := 0
	preparer := &starter{records: admissions.records, runtime: startRuntime{Prepare: func(_ context.Context, actual *Run) error {
		if actual.ID != landing.ID {
			t.Fatal("prepared historical source")
		}
		prepared++
		return nil
	}}}
	if err := preparer.Prepare(ctx, landing.ID); err != nil || prepared != 1 {
		t.Fatalf("landing preparation: %d %v", prepared, err)
	}
	if _, ok := seenRunStart(t, admissions.records.db, source.ID); ok {
		t.Fatal("accepted source gained readiness")
	}
	wantRunEventCount(t, admissions.records.db, landing.ID, 1, ofKind("run.ready"))
}

func TestReadinessReadFailureCannotAdmitExecution(t *testing.T) {
	t.Parallel()
	q, run, id := admitSingleStep(t, schema.Step{Name: "work", Uses: "script"})
	ctx := context.Background()
	step, err := readStep(ctx, q.db, id)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, q.db, "-- name: RunStartByID")
	claim, err := newStepAttempts(q.db, func(string) bool { return true }).Start(ctx, step, "script")
	// QueryRow faults are surfaced by Scan as PostgreSQL's missing-function
	// error; the Exec sentinel is not the contract of this injection path.
	var injected *pgconn.PgError
	if claim != nil || !errors.As(err, &injected) || injected.Code != "42883" || !strings.Contains(injected.Message, "forge_injected_failure") {
		t.Fatalf("read failure admitted execution or lost its cause: %+v %v", claim, err)
	}
	wantRunEventCount(t, q.db, run.ID, 0, ofKind("step.started"))
	if claim, err := newStepAttempts(q.db, func(string) bool { return true }).Start(ctx, step, "script"); err != nil || claim == nil {
		t.Fatalf("ready control: %+v %v", claim, err)
	}
}

type readinessLandingWork func(context.Context, *Change) error

func (f readinessLandingWork) PublishForLanding(ctx context.Context, change *Change) error {
	return f(ctx, change)
}

func TestRunReadinessRefusesLandingPublicationAndReset(t *testing.T) {
	t.Parallel()
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			a := stepAuthorityRunnerFixture(t, schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "gates", Steps: []schema.Step{{Name: "check", Uses: "scan"}}}}}, map[string]Runner{"scan": func(*Execution) { t.Error("refused landing dispatched") }})
			a.Work.Stop()
			ctx := stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "token"})
			calls := 0
			a.Work.landingWork = readinessLandingWork(func(context.Context, *Change) error { calls++; return nil })
			// sql-guarantee: Model historical absence or incomplete preparation on an otherwise admitted landing; runtime APIs cannot remove readiness.
			if missing {
				// sql-guarantee: Construct supported missing-start history; no runtime operation removes a start.
				completionExec(t, a.Work.db, `DELETE FROM run_starts`)
			} else {
				// sql-guarantee: Isolate an unfinished preparation flag on admitted test records; no runtime operation reverses readiness.
				completionExec(t, a.Work.db, `UPDATE run_starts SET ready=false`)
			}
			_, err := a.Work.RecheckLanding(ctx, LandingRecheck{ChangeID: a.ChangeID})
			var refused runReadinessError
			if calls != 0 || !errors.As(err, &refused) || refused.missing != missing {
				t.Fatalf("landing preflight: calls=%d err=%v", calls, err)
			}
			_, err = a.Work.landingRechecks.Recheck(ctx, LandingRecheck{ChangeID: a.ChangeID})
			if !errors.As(err, &refused) || refused.missing != missing {
				t.Fatalf("landing reset owner: %v", err)
			}
			wantRunEventCount(t, a.Work.db, a.RunID, 0, ofKind("step.recheck"))
			wantRetryRevision(t, a.Work.db, a.RunID, 0)
		})
	}
}
