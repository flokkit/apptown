package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

// Retry transactions admit their producer, review and human gate together.
func retryStageFixture(t *testing.T) (stepRetries, *Run, ids.ID, ids.ID, ids.ID) {
	t.Helper()
	spec := retryWorkflowSpec()
	spec.Stages[0].Steps = append(spec.Stages[0].Steps, schema.Step{Name: "accept", Uses: "approval", With: map[string]any{"quorum": 2}})
	owner, run, agent, review := retryFixtureWithSpec(t, spec)
	// sql-guarantee: Give every retry-stage row an old finished_at, including its pending human gate, to verify atomic invalidation clears the whole stage; generated finish writers cannot supply historical times.
	completionExec(t, owner.db, `UPDATE steps SET finished_at=now()-interval '1 minute' WHERE run_id=?`, run.ID)
	approval := admittedStepID(t, queries{db: owner.db}, run, "accept")
	return owner, run, agent, review, approval
}

func TestWorkflowRetryCompletedSendBack(t *testing.T) {
	t.Parallel()
	s, source, agent := completedSendBackFixture(t)
	// sql-guarantee: RC7 can retain completed history without start records; continuation must not invent readiness for that source.
	completionExec(t, s.db, `DELETE FROM run_starts WHERE run_id=?`, source.ID)
	review := admittedStepID(t, queries{db: s.db}, source, "review")
	retryScopeStep(t, s.db, review, json.RawMessage(`{}`))
	recordScopeStep(t, s.db, workflowsql.RecordApprovalParams{At: time.Now(), ID: review, Status: "pass"})
	if _, err := AddArtifactIn(context.Background(), s.db, source.ID, "input", "context", "", string([]byte{0, 255, 42}), time.Now()); err != nil {
		t.Fatal(err)
	}
	if n, err := workflowsql.New(s.db.SQL()).InsertRunEvent(context.Background(), workflowsql.InsertRunEventParams{ID: ids.New(time.Now().UTC()), RunID: source.ID, At: time.Now().UTC(), Kind: "guidance.pinned", PayloadJson: json.RawMessage(`{"hash":"frozen"}`)}); err != nil || n != 1 {
		t.Fatalf("store pinned guidance: %d %v", n, err)
	}
	result, err := s.sendBack(context.Background(), source.ChangeID, "Keep these exact words\nincluding this line", "alice", "", source.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if result.RunID == source.ID || result.StepID == agent {
		t.Fatal("completed history was reused")
	}
	sourceRun, continued := storedRun(t, s.db, source.ID), storedRun(t, s.db, result.RunID)
	expect(t, sourceRun.Status == RunDone, "source run: %s", sourceRun.Status)
	expect(t, stepCount(seenSteps(t, s.db, source.ID), func(s *Step) bool { return s.Status == StatusPass }) == 3, "source steps lost their verdicts")
	expect(t, continued.Status == RunRunning && continued.DefinitionJSON == sourceRun.DefinitionJSON, "continuation: %s %s", continued.Status, continued.DefinitionJSON)
	expect(t, stepCount(seenSteps(t, s.db, result.RunID), freshStep) == 3, "continuation steps are not fresh")
	cont := storedStep(t, s.db, result.StepID)
	sendback, _ := jsonText(t, cont.Config, "sendback")
	expect(t, jsonMatches(t, cont.Config, "retryMode=continue", "continueSession=original-session") && sendback == "Keep these exact words\nincluding this line", "continued agent config: %s", cont.Config)
	ch := seenChange(t, s.db, source.ChangeID)
	expect(t, ch.RunID == result.RunID && ch.State == "in_progress", "change: run %d state %s", ch.RunID, ch.State)
	start, ok := seenRunStart(t, s.db, result.RunID)
	expect(t, ok && !start.Ready, "continuation start: %v %+v", ok, start)
	expect(t, binaryArtifacts(t, s.db, result.RunID, []byte{0, 255, 42}) == 1, "binary artifact was not carried")
	expect(t, eventCount(t, s.db, result.RunID, "guidance.pinned", "hash=frozen") == 1, "pinned guidance was not carried")
	expect(t, auditCount(t, s.db, "change.sendback", nil) == 1, "send-back receipt")
	next, err := readRun(context.Background(), s.db, result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	nextSource, err := ReadSourceIn(context.Background(), s.db, RunRef{ID: next.ID, ProjectID: next.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	originalSource, err := ReadSourceIn(context.Background(), s.db, RunRef{ID: source.ID, ProjectID: source.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	if requireLocation(t)(nextSource.Workspace("test-data")) != requireLocation(t)(originalSource.Workspace("test-data")) {
		t.Fatal("continuation lost the source workspace")
	}
	step, err := readStep(context.Background(), s.db, result.StepID)
	if err != nil {
		t.Fatal(err)
	}
	attempts := newStepAttempts(s.db, func(kind string) bool { return kind == "agent/pi" || kind == "agent/review" })
	if _, err := attempts.Start(context.Background(), step, "agent/pi"); err == nil {
		t.Fatal("continuation executed before preparation")
	}
	prepared := 0
	preparer := &starter{records: runStartStore{db: s.db}, runtime: startRuntime{Prepare: func(context.Context, *Run) error { prepared++; return nil }}}
	if err := preparer.Prepare(context.Background(), next.ID); err != nil || prepared != 1 {
		t.Fatalf("continuation preparation: %d %v", prepared, err)
	}
	if _, exists := seenRunStart(t, s.db, source.ID); exists {
		t.Fatal("historical source gained readiness")
	}
	if _, err := attempts.Start(context.Background(), step, "agent/pi"); err != nil {
		t.Fatalf("the continued agent was not admitted: %v", err)
	}
	observed, err := AgentSessionIn(context.Background(), s.db, RunRef{ID: next.ID, ProjectID: next.ProjectID}, step.ID, RetryContinue)
	if err != nil {
		t.Fatal(err)
	}
	session := observed.ID
	if session != "original-session" {
		t.Fatalf("lost conversation: %q", session)
	}
}

func TestWorkflowRetrySendBackRollback(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"INSERT INTO workflow_continuations", "INSERT INTO run_starts", "INSERT INTO artifacts", "INSERT INTO steps", "UPDATE changes SET state=", "INSERT INTO audit_log"} {
		t.Run(failure, func(t *testing.T) {
			s, source, agent := completedSendBackFixture(t)
			landing := insertStoredRun(t, s.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: source.ProjectID, ChangeID: source.ChangeID, WorkflowName: "land", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(`{"role":"landing"}`)}).ID
			child := insertStoredRun(t, s.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: source.ProjectID, ChangeID: source.ChangeID, ParentRunID: landing, WorkflowName: "child", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(`{}`)}).ID
			transitionScopeChange(t, s.db, workflowsql.TransitionChangeParams{At: time.Now(), ID: source.ChangeID, State: "landing", RunID: landing})
			dbtest.FailNextStatement(t, s.db, failure)
			if _, err := s.sendBack(context.Background(), source.ChangeID, "fix it", "alice", "", source.ProjectID); !errors.Is(err, database.ErrInjectedFailure) {
				t.Fatalf("failure was hidden: %v", err)
			}
			// sql-guarantee: rollback must leave no run anywhere, including one filed under another change.
			completionCount(t, s.db, 3, `SELECT COUNT(*) FROM runs`)
			expect(t, storedRun(t, s.db, landing).Status == RunRunning && storedRun(t, s.db, child).Status == RunRunning, "landing tree moved")
			step := storedStep(t, s.db, agent)
			_, sent := jsonText(t, step.Config, "sendback")
			expect(t, step.Status == StatusPass && !sent, "agent: %s %s", step.Status, step.Config)
			ch := seenChange(t, s.db, source.ChangeID)
			expect(t, ch.State == "landing" && ch.RunID == landing, "change: %s %d", ch.State, ch.RunID)
			// sql-guarantee: no owner read lists continuation links; rollback must leave none.
			completionCount(t, s.db, 0, `SELECT COUNT(*) FROM workflow_continuations`)
			expect(t, auditCount(t, s.db, "", nil) == 0, "rolled-back send-back left a receipt")
			if _, err := s.sendBack(context.Background(), source.ChangeID, "fix it", "alice", "", source.ProjectID); err != nil {
				t.Fatal(err)
			}
			expect(t, storedRun(t, s.db, landing).Status == RunAbandoned && storedRun(t, s.db, child).Status == RunAbandoned, "landing tree was not abandoned")
		})
	}
}

func TestWorkflowRetryConcurrentAndInactive(t *testing.T) {
	t.Parallel()
	for _, completed := range []bool{false, true} {
		t.Run(fmt.Sprint(completed), func(t *testing.T) {
			s, source, _ := completedSendBackFixture(t)
			if !completed {
				transitionScopeRun(t, s.db, workflowsql.TransitionRunParams{At: time.Now(), ID: source.ID, NextStatus: "running"})
			}
			results := race(8, func(int) int {
				_, err := s.sendBack(context.Background(), source.ChangeID, "do this", "alice", "", source.ProjectID)
				if err == nil {
					return 1
				}
				var conflict *RetryError
				if !errors.As(err, &conflict) || conflict.Kind != RetryConflict {
					t.Errorf("unexpected error: %v", err)
				}
				return 0
			})
			if count(results, 1) != 1 {
				t.Fatalf("accepted requests: %v", results)
			}
			expect(t, auditCount(t, s.db, "change.sendback", nil) == 1, "send-back receipts")
		})
	}
	for _, state := range []string{"shipped", "abandoned", "inbox"} {
		t.Run(state, func(t *testing.T) {
			s, source, _ := completedSendBackFixture(t)
			transitionScopeChange(t, s.db, workflowsql.TransitionChangeParams{At: time.Now(), ID: source.ChangeID, State: state})
			if _, err := s.sendBack(context.Background(), source.ChangeID, "fix it", "alice", "", source.ProjectID); err == nil {
				t.Fatal("closed/unstarted change was sent back")
			}
			// sql-guarantee: a refused send-back creates no run anywhere.
			completionCount(t, s.db, 1, `SELECT COUNT(*) FROM runs`)
		})
	}
}

func TestWorkflowRetryManualAtomicAndFreshVerdicts(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"INSERT INTO run_events", "-- name: RetryStep"} {
		t.Run(failure, func(t *testing.T) {
			s, run, agent, review, approval := retryStageFixture(t)
			recordScopeStep(t, s.db, workflowsql.RecordApprovalParams{At: time.Now(), ID: approval, Status: "pass", EvidenceJson: json.RawMessage(`{"decisions":[{"approver":"alice","decision":"approve"}]}`)})
			overrideScopeStep(t, s.db, workflowsql.OverrideStepParams{At: time.Now(), ID: agent, OverrideBy: "alice", OverrideReason: "old exception"})
			req := retryIntent{RetryCommand: RetryCommand{StepID: agent, Mode: RetryContinue, Feedback: "human words"}, Actor: commandActor{Receipt: "alice"}}
			dbtest.FailNextStatement(t, s.db, failure)
			if _, err := s.retryExpected(context.Background(), req, nil); !errors.Is(err, database.ErrInjectedFailure) {
				t.Fatalf("retry committed past failure: %v", err)
			}
			kept := storedStep(t, s.db, agent)
			expect(t, kept.Status == StatusPass && kept.OverrideBy == "alice" && kept.FinishedAt.Valid, "agent: %+v", kept)
			expect(t, storedStep(t, s.db, review).Status == StatusFail, "review verdict moved")
			expect(t, storedStep(t, s.db, approval).Status == StatusPass, "approval verdict moved")
			wantEventCount(t, s.db, 7, anyEvent) // Admission and two completed attempts only.
			if _, err := s.retryExpected(context.Background(), req, nil); err != nil {
				t.Fatal(err)
			}
			expect(t, stepCount(seenSteps(t, s.db, run.ID), freshStep) == 3, "retried stage is not fresh")
			expect(t, jsonMatches(t, storedStep(t, s.db, agent).Config, "sendback=human words", "retryMode=continue"), "retry feedback was not queued")
			wantEventCount(t, s.db, 1, kindWith("step.invalidated", "previousEvidence.decisions.0.approver", "alice"))
			if _, err := s.retryExpected(context.Background(), req, nil); err == nil {
				t.Fatal("duplicate queued retry was accepted")
			}
		})
	}
}

func TestWorkflowRetryManualGuards(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"done", "abandoned", "later", "running", "pending"} {
		t.Run(change, func(t *testing.T) {
			w, run, agent, _, _ := retryStageFixture(t)
			switch change {
			case "done", "abandoned":
				transitionScopeRun(t, w.db, workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, NextStatus: change})
			case "later":
				transitionScopeRun(t, w.db, workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, NextStage: change})
			default:
				recordScopeRunSteps(t, w.db, run.ID, change)
			}
			if _, err := w.retryExpected(context.Background(), retryIntent{RetryCommand: RetryCommand{StepID: agent}, Actor: commandActor{Receipt: "alice"}}, nil); err == nil {
				t.Fatal("inactive or busy step was retried")
			}
			wantEventCount(t, w.db, 7, anyEvent) // Admission and two completed attempts only.
		})
	}
}

func TestWorkflowRetryFencesStaleAdvancement(t *testing.T) {
	t.Parallel()
	for _, automatic := range []bool{false, true} {
		t.Run(fmt.Sprint(automatic), func(t *testing.T) {
			w, run, agent, review, _ := retryStageFixture(t)
			// The engine read passes, then somebody requested changed work
			// before it could commit its advancement decision.
			run, err := readRun(context.Background(), w.db, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if automatic {
				recordScopeStep(t, w.db, workflowsql.RecordApprovalParams{At: time.Now(), ID: review, Status: "pending"})
				current, readErr := readStep(context.Background(), w.db, review)
				if readErr != nil {
					t.Fatal(readErr)
				}
				claim, claimErr := newStepAttempts(w.db, func(kind string) bool { return kind == "agent/pi" || kind == "agent/review" }).Start(context.Background(), current, "agent/review")
				if claimErr != nil {
					t.Fatal(claimErr)
				}
				_, err = (reworkStore{db: w.db}).fail(context.Background(), claim, StepOutcome{Status: StatusFail, Evidence: json.RawMessage(`{"reason":"fix the bug"}`)})
			} else {
				_, err = w.retryExpected(context.Background(), retryIntent{RetryCommand: RetryCommand{StepID: agent, Mode: RetryContinue}, Actor: commandActor{Receipt: "alice"}}, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			store := advanceStore{db: w.db, completion: completionRecorderFunc(func(context.Context, *database.Tx, CompletedRun) (ReleaseReceipt, error) {
				t.Error("stale completion reached its external effects")
				return ReleaseReceipt{}, nil
			})}
			if err := store.complete(context.Background(), run); !errors.Is(err, ErrRunMoved) {
				t.Fatalf("stale completion: %v", err)
			}
			if _, err := store.transition(context.Background(), run, "later", "alice"); !errors.Is(err, ErrRunMoved) {
				t.Fatalf("stale transition: %v", err)
			}
			got := storedRun(t, w.db, run.ID)
			expect(t, got.Status == RunRunning && got.CurrentStage == "finish" && got.RetryRevision == 1, "stale advancement moved the run: %+v", got)
		})
	}
}

func TestWorkflowRetryEarlierProducerAndBusyLanding(t *testing.T) {
	t.Parallel()
	s, run, _ := completedSendBackFixture(t)
	transitionScopeRun(t, s.db, workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, NextStatus: "running", NextStage: "later"})
	result, err := s.sendBack(context.Background(), run.ChangeID, "earlier work needs a change", "alice", "", run.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, storedRun(t, s.db, run.ID).Status == RunAbandoned, "earlier run was not abandoned")
	next := storedRun(t, s.db, result.RunID)
	expect(t, next.Status == RunRunning && next.CurrentStage == run.CurrentStage, "continuation: %+v", next)
	transitionScopeRun(t, s.db, workflowsql.TransitionRunParams{At: time.Now(), ID: result.RunID, NextStatus: "done"})
	recordScopeRunSteps(t, s.db, result.RunID, "pass")
	landing := insertStoredRun(t, s.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, ChangeID: run.ChangeID, WorkflowName: "land", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(`{"role":"landing"}`)}).ID
	insertStoredStep(t, s.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: landing, Stage: "finish", Name: "deploy", Type: "deploy", Status: StatusRunning.String(), ConfigJson: json.RawMessage(`{}`)})
	if _, err := s.sendBack(context.Background(), run.ChangeID, "interrupt live deployment", "alice", "", run.ProjectID); err == nil {
		t.Fatal("send-back overlapped a live landing runner")
	}
	expect(t, storedRun(t, s.db, landing).Status == RunRunning, "live landing was interrupted")
}

func TestWorkflowRetryEntryPrerequisitesAndSkippedDependents(t *testing.T) {
	t.Parallel()
	spec := retryWorkflowSpec()
	spec.Stages[0].OnEnter = append(spec.Stages[0].OnEnter, schema.Step{Name: "after", Uses: "script"})
	spec.Stages[0].Steps = append(spec.Stages[0].Steps, schema.Step{Name: "accept", Uses: "approval"})
	w, run, agent, _ := retryFixtureWithSpec(t, spec)
	after := admittedStepID(t, queries{db: w.db}, run, "after")
	if err := newDispatchPlans(w.db, []string{"script"}).Skip(context.Background(), run.ID, after, "build"); err != nil {
		t.Fatal(err)
	}
	recordScopeStep(t, w.db, workflowsql.RecordApprovalParams{At: time.Now(), ID: agent, Status: "fail"})
	if _, err := w.retryExpected(context.Background(), retryIntent{RetryCommand: RetryCommand{StepID: after}, Actor: commandActor{Receipt: "alice"}}, nil); err == nil {
		t.Fatal("retry bypassed failed prerequisite")
	}
	if _, err := w.retryExpected(context.Background(), retryIntent{RetryCommand: RetryCommand{StepID: agent}, Actor: commandActor{Receipt: "alice"}}, nil); err != nil {
		t.Fatal(err)
	}
	expect(t, storedStep(t, w.db, after).Status == StatusPending, "skipped dependent was not reopened")
}

func TestWorkflowRetryPreservesLegacySessionAndUnconsumedFeedback(t *testing.T) {
	t.Parallel()
	w, run, agent, _, _ := retryStageFixture(t)
	retryScopeStep(t, w.db, agent, json.RawMessage(`{"sendback":"the original human message"}`))
	recordScopeStep(t, w.db, workflowsql.RecordApprovalParams{At: time.Now(), ID: agent, Status: "error"})
	for range 2 {
		if n, err := workflowsql.New(w.db.SQL()).InsertRunEvent(context.Background(), workflowsql.InsertRunEventParams{ID: ids.New(time.Now().UTC()), RunID: run.ID, At: time.Now().UTC(), Kind: "step.started", PayloadJson: json.RawMessage(`{"step":"build"}`)}); err != nil || n != 1 {
			t.Fatalf("store historical start: %d %v", n, err)
		}
	}
	if _, err := w.retryExpected(context.Background(), retryIntent{RetryCommand: RetryCommand{StepID: agent, Mode: RetryContinue}, Actor: commandActor{Receipt: "alice"}}, nil); err != nil {
		t.Fatal(err)
	}
	step, err := readStep(context.Background(), w.db, agent)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := newStepAttempts(w.db, func(kind string) bool { return kind == "agent/pi" || kind == "agent/review" }).Start(context.Background(), step, "agent/pi")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Sendback string `json:"sendback"`
	}
	if err := json.Unmarshal(claim.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	observed, err := AgentSessionIn(context.Background(), w.db, RunRef{ID: run.ID, ProjectID: run.ProjectID}, step.ID, RetryContinue)
	if err != nil {
		t.Fatal(err)
	}
	session := observed.ID
	// One admitted agent start plus two historical starts without session IDs.
	if session != fmt.Sprintf("step-%d-a3", agent) || cfg.Sendback != "the original human message" {
		t.Fatalf("legacy continuation: %q %+v", session, cfg)
	}
}

func TestWorkflowRetryCompletedSendBackKeepsInferenceOwner(t *testing.T) {
	t.Parallel()
	s, source, _ := completedSendBackBy(t, "known-owner")
	result, err := s.sendBack(context.Background(), source.ChangeID, "continue the same work", "someone-else", "", source.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, storedInferenceOwner(t, s.db, result.RunID) == "known-owner", "continuation changed the inference owner")

	unattributed, older, _ := completedSendBackFixture(t)
	// sql-guarantee: Model a legacy completed run with no inference-owner row so continuation must use run/<id>; admission always inserts ownership and no generated mutation deletes that historical attribution row.
	completionExec(t, unattributed.db, `DELETE FROM run_inference_owners WHERE run_id=?`, older.ID)
	again, err := unattributed.sendBack(context.Background(), older.ChangeID, "continue unattributed history", "someone-else", "", older.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, storedInferenceOwner(t, unattributed.db, again.RunID) == fmt.Sprintf("run/%d", older.ID), "unattributed continuation owner")
}

func TestWorkflowContinuationRetainsCheckExpectations(t *testing.T) {
	t.Parallel()
	store, source, _ := completedSendBackFixture(t)
	// These are the checks of the already-completed source stage. A new explicit
	// continuation resets outcomes, while retaining its declared check identities.
	steps, err := readStageSteps(context.Background(), store.db, source.ID, source.CurrentStage)
	if err != nil {
		t.Fatal(err)
	}
	var checks []Check
	for _, s := range steps {
		checks = append(checks, Check{Declaration: s.Name, Name: s.Name, Type: s.Type})
	}
	if n, err := workflowsql.New(store.db.SQL()).InsertRunEvent(context.Background(), workflowsql.InsertRunEventParams{ID: ids.New(time.Now().UTC()), RunID: source.ID, At: time.Now().UTC(), Kind: "stage.entered", PayloadJson: json.RawMessage(mustJSON(map[string]any{"stage": source.CurrentStage, "checks": checks}))}); err != nil || n != 1 {
		t.Fatalf("store source-stage expectations: %d %v", n, err)
	}
	result, err := store.sendBack(context.Background(), source.ChangeID, "More work please", "alice", "", source.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readStageChecks(context.Background(), store.db, result.RunID, source.CurrentStage)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, checks) {
		t.Fatalf("continuation lost expectations: %+v", got)
	}
	expect(t, stepCount(seenSteps(t, store.db, result.RunID), func(s *Step) bool { return s.Status == StatusPending }) == len(steps), "continuation stage is not pending")
}
