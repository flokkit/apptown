package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/ids"
)

func TestReworkCommandsDispatchOnlyCommittedWork(t *testing.T) {
	t.Parallel()
	for _, human := range []bool{false, true} {
		for _, failure := range []string{"none", "receipt", "commit"} {
			t.Run(fmt.Sprintf("human=%v/%s", human, failure), func(t *testing.T) {
				owner, run, agent, review, approval := retryStageFixture(t)
				ctx := context.Background()
				var calls atomic.Int32
				service := newServiceWithAuthority(owner.db, Dependencies{Automatic: []string{"agent/pi", "agent/review"}, Runners: map[string]Runner{"agent/pi": func(task *Execution) {
					id := task.Step().ID
					calls.Add(1)
					if id != agent {
						t.Errorf("wrong rework agent: %d", id)
						return
					}
					// Independent reads must see the committed reset and receipt before the
					// external runner is invoked. No transaction handle crosses this boundary.
					step, err := readStep(ctx, owner.db, id)
					if err != nil {
						t.Error(err)
						return
					}
					stored, err := readRun(ctx, owner.db, run.ID)
					var events []Event
					if err == nil {
						events, err = readWorkflowEvents(ctx, owner.db, run.ID, TraceView)
					}
					committed := err == nil && stored.RetryRevision == 1 && countEvents(events, ofKind("step.rework")) > 0
					if !committed || step.Status != StatusPending {
						t.Errorf("runner preceded commit: step=%+v committed=%v err=%v", step, committed, err)
						return
					}
					_, err = task.Claim()
					if err != nil {
						t.Error(err)
						return
					}
					if err := task.Finish(StepOutcome{Status: StatusPass}, nil); err != nil {
						t.Error(err)
					}
				}, "agent/review": func(task *Execution) {
					if _, err := task.Claim(); err != nil {
						t.Error(err)
						return
					}
					if err := task.Finish(StepOutcome{Status: StatusPass}, nil); err != nil {
						t.Error(err)
					}
				}}, Report: func(err error) { t.Error(err) }}, allowProject(run.ProjectID))
				// This package tests command-to-dispatch ordering; Git preparation and
				// advancement composition have their own integration tests.
				service.advancer = newAdvancer(owner.db, advanceRuntime{Stages: service.stages, Assertions: service.assertions, Checks: advancementScopeChecks{refresh: func(context.Context, *Run) error { return nil }}, Completion: completionRecorderFunc(noCompletionEffects)})
				service.dispatcher.prepare = nil
				service.dispatcher.advance = func(ids.ID) {}
				t.Cleanup(service.Stop)
				var claim *Step
				if !human {
					claim = reworkReviewClaim(t, owner.db, review)
				}
				command := func() error {
					if !human {
						return service.finishAttempt(ctx, claim, StepOutcome{Status: StatusFail, Rework: true, Evidence: json.RawMessage(`{"reason":"revise"}`)})
					}
					_, err := service.Approve(ctx, Approval{StepID: approval, Decision: VoteReject, Comment: "revise"})
					return err
				}
				before := completionSnapshot(t, owner.db)
				switch failure {
				case "receipt":
					dbtest.FailNextStatement(t, owner.db, "INSERT INTO run_events")
				case "commit":
					completionExec(t, owner.db, `CREATE FUNCTION reject_rework_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'rework commit refused'; END $$`)
					completionExec(t, owner.db, `CREATE CONSTRAINT TRIGGER reject_rework_commit AFTER INSERT ON run_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_rework_commit()`)
				}
				err := command()
				service.Wait()
				if failure != "none" {
					if err == nil || calls.Load() != 0 || completionSnapshot(t, owner.db) != before {
						t.Fatalf("uncommitted command: calls=%d err=%v", calls.Load(), err)
					}
					completionExec(t, owner.db, `DROP FUNCTION IF EXISTS reject_rework_commit() CASCADE`)
					err = command()
					service.Wait()
				}
				if err != nil || calls.Load() != 1 {
					t.Fatalf("committed command: calls=%d err=%v", calls.Load(), err)
				}
			})
		}
	}
}

func TestReworkCommandRetainsRequestFromItsRunningStage(t *testing.T) {
	t.Parallel()
	owner, run, agent, review, _ := retryStageFixture(t)
	transitionStoredStepForTest(t, owner.db, agent, StatusPending)
	transitionStoredStepForTest(t, owner.db, review, StatusPending)
	var calls, reviews atomic.Int32
	service := newServiceWithAuthority(owner.db, Dependencies{Automatic: []string{"agent/pi", "agent/review"}, Runners: map[string]Runner{
		"agent/pi": func(task *Execution) {
			_, err := task.Claim()
			if err != nil {
				t.Error(err)
				return
			}
			calls.Add(1)
			if err := task.Finish(StepOutcome{Status: StatusPass}, nil); err != nil {
				t.Error(err)
			}
		},
		"agent/review": func(task *Execution) {
			_, err := task.Claim()
			if err != nil {
				t.Error(err)
				return
			}
			outcome := StepOutcome{Status: StatusPass}
			if reviews.Add(1) == 1 {
				outcome = StepOutcome{Status: StatusFail, Rework: true, Evidence: json.RawMessage(`{"reason":"fix it"}`)}
			}
			if err := task.Finish(outcome, nil); err != nil {
				t.Error(err)
			}
		},
	}, Report: func(err error) { t.Error(err) }}, allowProject(run.ProjectID))
	service.dispatcher.prepare = nil
	service.dispatcher.advance = func(ids.ID) {}
	t.Cleanup(service.Stop)
	service.Dispatch(DispatchPlan{RunID: run.ID, Steps: []ReadyStep{{ID: agent, Type: "agent/pi", OnEnter: true}, {ID: review, Type: "agent/review"}}})
	service.Wait()
	if calls.Load() != 2 || reviews.Load() != 2 {
		t.Fatalf("initial work plus committed rework: agents=%d reviews=%d", calls.Load(), reviews.Load())
	}
	wantEventCount(t, owner.db, 1, ofKind("step.rework"))
}

func TestWorkflowReworkRecoversCommittedIntent(t *testing.T) {
	t.Parallel()
	for _, human := range []bool{false, true} {
		for _, claimed := range []bool{false, true} {
			t.Run(fmt.Sprintf("human=%v/claimed=%v", human, claimed), func(t *testing.T) {
				owner, run, agentID, reviewID, approval := retryStageFixture(t)
				ctx := context.Background()
				service := newServiceWithAuthority(owner.db, Dependencies{Automatic: []string{"agent/pi", "agent/review"}}, allowProject(run.ProjectID))
				service.Stop()
				review := reworkReviewClaim(t, owner.db, reviewID)
				var agent *Step
				if human {
					if err := service.finishAttempt(ctx, review, StepOutcome{Status: StatusFail}); err != nil {
						t.Fatal(err)
					}
					if _, err := service.Approve(ctx, Approval{StepID: approval, Decision: VoteReject, Comment: "revise"}); err != nil {
						t.Fatal(err)
					}
				} else if err := service.finishAttempt(ctx, review, StepOutcome{Status: StatusFail, Rework: true, Evidence: json.RawMessage(`{"reason":"revise"}`)}); err != nil {
					t.Fatal(err)
				}
				if claimed {
					current, err := readStep(ctx, owner.db, agentID)
					if err != nil {
						t.Fatal(err)
					}
					agent, err = service.stepAttempts.Start(ctx, current, current.Type)
					if err != nil {
						t.Fatal(err)
					}
				}
				// Lose the process before dispatch or after claiming the rework.
				if _, err := database.Transact(ctx, owner.db, func(tx *database.Tx) (InterruptedSteps, error) {
					return InterruptStepsIn(ctx, tx, nil, nil, time.Now())
				}); err != nil {
					t.Fatal(err)
				}
				var calls atomic.Int32
				deps := Dependencies{Automatic: []string{"agent/pi", "agent/review"}, Runners: map[string]Runner{}, Report: func(err error) { t.Error(err) }}
				for _, kind := range deps.Automatic {
					deps.Runners[kind] = func(task *Execution) {
						current := task.Step()
						if current.ID == agentID {
							var config struct {
								RetryMode string `json:"retryMode"`
							}
							if err := json.Unmarshal(current.Config, &config); err != nil {
								t.Error(err)
								return
							}
							if current.Status != StatusPending || config.RetryMode != "continue" {
								t.Errorf("bad resume: %+v", current)
								return
							}
							calls.Add(1)
						}
						claim, err := task.Claim()
						if err != nil {
							t.Error(err)
							return
						}
						if claimed && current.ID == agentID {
							if claim.Attempt != agent.Attempt+1 {
								t.Errorf("restart attempt: %d -> %d", agent.Attempt, claim.Attempt)
							}
							for _, outcome := range []StepOutcome{{Status: StatusPass}, {Status: StatusFail, Rework: true, Evidence: json.RawMessage(`{"reason":"stale"}`)}} {
								if err := service.finishAttempt(ctx, agent, outcome); !errors.Is(err, ErrStepAttemptLost) {
									t.Errorf("stale finish: %v", err)
								}
							}
						}
						if err := task.Finish(StepOutcome{Status: StatusPass}, nil); err != nil {
							t.Error(err)
						}
					}
				}
				restarted := newServiceWithAuthority(owner.db, deps, allowProject(run.ProjectID))
				restarted.dispatcher.prepare = nil
				restarted.dispatcher.advance = func(ids.ID) {}
				t.Cleanup(restarted.Stop)
				for range 2 {
					restarted.ResumePending()
					restarted.Wait()
				}
				if calls.Load() != 1 {
					t.Fatalf("agent calls: %d", calls.Load())
				}
				wantRunEventCount(t, owner.db, run.ID, 1, ofKind("step.rework"))
				resumed := 0
				if claimed {
					resumed = 1
				}
				wantRunEventCount(t, owner.db, run.ID, resumed, kindWith("step.resumed", "step", fmt.Sprint(agentID)))
			})
		}
	}
}
