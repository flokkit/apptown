package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
)

// Persisted failed verdicts are local to Work tests; a real claim still owns
// the next machine outcome. Root composition tests create attempts via commands.
func reworkReviewClaim(t *testing.T, db *database.DB, id ids.ID) *Step {
	t.Helper()
	{
		current, err := workflowsql.New(db.SQL()).StepByID(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if rows, err := workflowsql.New(db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: id, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: "pending", EvidenceJson: current.EvidenceJson}); err != nil || rows != 1 {
			t.Fatalf("stored step status: rows=%d err=%v", rows, err)
		}
	}
	step, err := readStep(context.Background(), db, id)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := newStepAttempts(db, func(kind string) bool { return kind == "agent/review" }).Start(context.Background(), step, "agent/review")
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func TestReworkRequiresEveryWriteAndReceipt(t *testing.T) {
	t.Parallel()
	for _, human := range []bool{false, true} {
		for _, failure := range []string{"step error", "first receipt error", "last receipt error", "step suppressed", "revision suppressed", "receipt suppressed"} {
			t.Run(fmt.Sprintf("human=%v/%s", human, failure), func(t *testing.T) {
				t.Parallel()
				spec := retryWorkflowSpec()
				spec.Stages[0].OnEnter[0].With["model"] = "chosen"
				spec.Stages[0].Steps = append(spec.Stages[0].Steps, schema.Step{Name: "accept", Uses: "approval", With: map[string]any{"quorum": 2}})
				s, run, agent, review := retryFixtureWithSpec(t, spec)
				approval := admittedStepID(t, queries{db: s.db}, run, "accept")
				var claim *Step
				if !human {
					claim = reworkReviewClaim(t, s.db, review)
				}
				reset := func() error {
					if !human {
						_, err := (reworkStore{s.db, clocked{}}).fail(context.Background(), claim, StepOutcome{Status: StatusFail, Evidence: json.RawMessage(`{"reason":"new failure"}`)})
						return err
					}
					_, err := newService(s.db, allowProject(run.ProjectID), Runtime{}).Approve(context.Background(), Approval{StepID: approval, Decision: VoteReject, Comment: "keep these words"})
					return err
				}
				before := completionSnapshot(t, s.db)
				switch failure {
				case "step error":
					dbtest.FailNextStatement(t, s.db, "UPDATE steps SET status='pending'")
				case "first receipt error":
					dbtest.FailNextStatement(t, s.db, "INSERT INTO run_events")
				case "last receipt error":
					completionExec(t, s.db, `ALTER TABLE run_events ADD CONSTRAINT reject_rework CHECK(kind!='step.rework')`)
				default:
					table, write, condition := "steps", "UPDATE", "NEW.status='pending'"
					if failure == "revision suppressed" {
						table, condition = "runs", "NEW.retry_revision != OLD.retry_revision"
					}
					if failure == "receipt suppressed" {
						table, write, condition = "run_events", "INSERT", "NEW.kind='step.rework'"
					}
					completionExec(t, s.db, `CREATE FUNCTION veto_rework() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN RETURN NULL; END IF; RETURN NEW; END $$`)
					completionExec(t, s.db, `CREATE TRIGGER veto_rework BEFORE `+write+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION veto_rework()`)
				}
				if err := reset(); err == nil {
					t.Fatal("required failure was hidden")
				}
				if completionSnapshot(t, s.db) != before {
					t.Fatal("partial verdict, reset, revision or receipt committed")
				}
				completionExec(t, s.db, `ALTER TABLE run_events DROP CONSTRAINT IF EXISTS reject_rework`)
				completionExec(t, s.db, `DROP FUNCTION IF EXISTS veto_rework() CASCADE`)
				if err := reset(); err != nil {
					t.Fatalf("explicit rerun: %v", err)
				}
				queued := storedStep(t, s.db, agent)
				expect(t, queued.Status == StatusPending && !queued.StartedAt.Valid && !queued.FinishedAt.Valid && jsonMatches(t, queued.Config, "retryMode=continue", "model=chosen"), "rerun agent: %+v", queued)
				expect(t, storedRun(t, s.db, run.ID).RetryRevision == 1, "rerun did not advance the revision once")
			})
		}
	}
}

func TestWorkflowReworkRefusesIneligibleProducer(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"late verdict", "missing producer", "already pending"} {
		t.Run(state, func(t *testing.T) {
			s, run, agent, review, _ := retryStageFixture(t)
			claim := reworkReviewClaim(t, s.db, review)
			switch state {
			case "late verdict":
				if rows, err := workflowsql.New(s.db.SQL()).InsertRunEvent(context.Background(), workflowsql.InsertRunEventParams{ID: ids.New(time.Now()), RunID: run.ID, At: time.Now(), Kind: "step.started", PayloadJson: []byte(`{"step":"build"}`)}); err != nil || rows != 1 {
					t.Fatalf("stored historical receipt: rows=%d err=%v", rows, err)
				}
			case "missing producer":
				// sql-guarantee: Remove only the producer after claiming its reviewer; project-wide deletion would also remove the reviewer whose late verdict is tested.
				completionExec(t, s.db, `DELETE FROM steps WHERE id=$1`, agent)
			case "already pending":
				{
					current, err := workflowsql.New(s.db.SQL()).StepByID(context.Background(), agent)
					if err != nil {
						t.Fatal(err)
					}
					if rows, err := workflowsql.New(s.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: agent, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: "pending", EvidenceJson: current.EvidenceJson}); err != nil || rows != 1 {
						t.Fatalf("stored step status: rows=%d err=%v", rows, err)
					}
				}
			}
			result, err := (reworkStore{s.db, clocked{}}).fail(context.Background(), claim, StepOutcome{Status: StatusFail, Evidence: json.RawMessage(`{"reason":"judged the previous draft"}`)})
			if err != nil || !result.AgentID.IsZero() {
				t.Fatalf("ineligible producer reopened: %+v %v", result, err)
			}
			wantEventCount(t, s.db, 0, ofKind("step.rework"))
			if state == "late verdict" {
				expect(t, storedStep(t, s.db, agent).Status == StatusPass, "late verdict reopened the producer")
			}
			verdict := storedStep(t, s.db, review)
			expect(t, verdict.Status == StatusFail && jsonMatches(t, verdict.Evidence, "reason=judged the previous draft"), "review verdict: %s %s", verdict.Status, verdict.Evidence)
		})
	}
}

func TestWorkflowReworkRoundLimit(t *testing.T) {
	t.Parallel()
	s, run, agent, review, _ := retryStageFixture(t)
	for i := 0; i < 2; i++ {
		if rows, err := workflowsql.New(s.db.SQL()).InsertRunEvent(context.Background(), workflowsql.InsertRunEventParams{ID: ids.New(time.Now()), RunID: run.ID, At: time.Now(), Kind: "step.rework", PayloadJson: []byte(`{}`)}); err != nil || rows != 1 {
			t.Fatalf("stored historical receipt: rows=%d err=%v", rows, err)
		}
	}
	if result, err := (reworkStore{s.db, clocked{}}).fail(context.Background(), reworkReviewClaim(t, s.db, review), StepOutcome{Status: StatusFail, Evidence: json.RawMessage(`{"reason":"third round"}`)}); err != nil || result.AgentID != agent {
		t.Fatalf("third round refused: %+v %v", result, err)
	}
	wantEventCount(t, s.db, 3, ofKind("step.rework"))
	{
		current, err := workflowsql.New(s.db.SQL()).StepByID(context.Background(), agent)
		if err != nil {
			t.Fatal(err)
		}
		if rows, err := workflowsql.New(s.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: agent, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: "pass", EvidenceJson: current.EvidenceJson}); err != nil || rows != 1 {
			t.Fatalf("stored step status: rows=%d err=%v", rows, err)
		}
	}
	if result, err := (reworkStore{s.db, clocked{}}).fail(context.Background(), reworkReviewClaim(t, s.db, review), StepOutcome{Status: StatusFail, Evidence: json.RawMessage(`{"reason":"still fails"}`)}); err != nil || !result.AgentID.IsZero() {
		t.Fatalf("limit exceeded: %+v %v", result, err)
	}
	wantEventCount(t, s.db, 3, ofKind("step.rework"))

	// A person's retry starts a fresh allowance of automatic rounds: the
	// cap counts reworks since they last acted, not the whole run.
	if _, err := s.retryExpected(context.Background(), retryIntent{RetryCommand: RetryCommand{StepID: agent, Mode: RetryContinue}, Actor: commandActor{Receipt: "alice"}}, nil); err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 4; round++ {
		current, err := workflowsql.New(s.db.SQL()).StepByID(context.Background(), agent)
		if err != nil {
			t.Fatal(err)
		}
		if rows, err := workflowsql.New(s.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: agent, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: "pass", EvidenceJson: current.EvidenceJson}); err != nil || rows != 1 {
			t.Fatalf("stored step status: rows=%d err=%v", rows, err)
		}
		want := agent
		if round == 4 {
			want = ids.Nil
		}
		if result, err := (reworkStore{s.db, clocked{}}).fail(context.Background(), reworkReviewClaim(t, s.db, review), StepOutcome{Status: StatusFail, Evidence: json.RawMessage(fmt.Sprintf(`{"reason":"round %d after the retry"}`, round))}); err != nil || result.AgentID != want {
			t.Fatalf("round %d after a person's retry: %+v %v", round, result, err)
		}
	}
}

func TestWorkflowReworkRejectionHistoryAndConcurrentVotes(t *testing.T) {
	t.Parallel()
	s, _, agent, _, approval := retryStageFixture(t)
	people(t, s.db, "alice", "bob", "carol")
	cast := func(who string, decision ApprovalDecision, comment string) error {
		service := newService(s.db, authorityFunc(func(context.Context, operationPermission) (string, error) { return who, nil }), Runtime{})
		_, err := service.Approve(context.Background(), Approval{StepID: approval, Decision: decision, Comment: comment})
		return err
	}
	if err := cast("bob", VoteApprove, "first round OK"); err != nil {
		t.Fatal(err)
	}
	result := race(8, func(i int) int {
		err := cast(fmt.Sprintf("reviewer%d", i), VoteReject, "first round fix")
		if err == nil {
			return 1
		}
		if !errors.Is(err, errStepDecisionConflict) {
			t.Error(err)
		}
		return 0
	})
	if count(result, 1) != 1 {
		t.Fatalf("concurrent rejection winners: %v", result)
	}
	{
		current, err := workflowsql.New(s.db.SQL()).StepByID(context.Background(), agent)
		if err != nil {
			t.Fatal(err)
		}
		if rows, err := workflowsql.New(s.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: agent, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: "pass", EvidenceJson: current.EvidenceJson}); err != nil || rows != 1 {
			t.Fatalf("stored step status: rows=%d err=%v", rows, err)
		}
	}
	if err := cast("alice", VoteReject, "second round fix"); err != nil {
		t.Fatal(err)
	}
	{
		current, err := workflowsql.New(s.db.SQL()).StepByID(context.Background(), agent)
		if err != nil {
			t.Fatal(err)
		}
		if rows, err := workflowsql.New(s.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: agent, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: "pass", EvidenceJson: current.EvidenceJson}); err != nil || rows != 1 {
			t.Fatalf("stored step status: rows=%d err=%v", rows, err)
		}
	}
	if err := cast("alice", VoteApprove, "now OK"); err != nil {
		t.Fatal(err)
	}
	if err := cast("bob", VoteApprove, "agreed"); err != nil {
		t.Fatal(err)
	}
	gate := storedStep(t, s.db, approval)
	raw := string(gate.Evidence)
	var evidence ApprovalEvidence
	if err := json.Unmarshal(gate.Evidence, &evidence); err != nil {
		t.Fatal(err)
	}
	if len(evidence.PreviousDecisions) != 3 || len(evidence.Decisions) != 2 || evidence.LastRejection.Comment != "second round fix" {
		t.Fatalf("lost history: %s", raw)
	}
	expect(t, gate.Status == StatusPass, "approval: %s", gate.Status)
}

func TestWorkflowReworkRacesAbandonment(t *testing.T) {
	t.Parallel()
	for _, human := range []bool{false, true} {
		t.Run(fmt.Sprint(human), func(t *testing.T) {
			s, run, agent, review, approval := retryStageFixture(t)
			var claim *Step
			if !human {
				claim = reworkReviewClaim(t, s.db, review)
			}
			race(2, func(i int) int {
				if i == 0 {
					if _, err := NewAbandonments(s.db, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{}).Abandon(context.Background(), Abandonment{RunID: run.ID}); err != nil {
						t.Error(err)
					}
				} else {
					if human {
						_, err := newService(s.db, allowProject(run.ProjectID), Runtime{}).Approve(context.Background(), Approval{StepID: approval, Decision: VoteReject, Comment: "fix"})
						if err != nil && !errors.Is(err, errStepDecisionConflict) {
							t.Error(err)
						}
					} else {
						// A verdict whose attempt was lost to abandonment is refused, not hidden.
						if _, err := (reworkStore{s.db, clocked{}}).fail(context.Background(), claim, StepOutcome{Status: StatusFail, Evidence: json.RawMessage(`{"reason":"fix"}`)}); err != nil && !errors.Is(err, ErrStepAttemptLost) {
							t.Error(err)
						}
					}
				}
				return 0
			})
			var calls atomic.Int32
			dispatcher := newDispatcher(s.db, dispatcherConfig{Life: context.Background(), Runners: map[string]func(ids.ID){"agent/pi": func(ids.ID) { calls.Add(1) }}, Report: func(err error) { t.Error(err) }})
			// Even an already-created dispatch plan must recheck abandonment.
			dispatcher.Dispatch(DispatchPlan{RunID: run.ID, Steps: []ReadyStep{{ID: agent, Type: "agent/pi"}}}, false)
			dispatcher.Wait()
			if calls.Load() != 0 {
				t.Fatal("abandoned work dispatched")
			}
			current, err := readStep(context.Background(), s.db, agent)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := newStepAttempts(s.db, func(kind string) bool { return kind == "agent/pi" }).Start(context.Background(), current, "agent/pi"); !errors.Is(err, ErrStepAttemptLost) {
				t.Fatalf("abandoned work claim: %v", err)
			}
			// Original attempt only; abandoned retry never claims.
			wantEventCount(t, s.db, 1, kindWith("step.started", "step", "build"))
		})
	}
}

func TestWorkflowReworkAgentClaimHasOneWinner(t *testing.T) {
	t.Parallel()
	s, _, agent, review, _ := retryStageFixture(t)
	if result, err := (reworkStore{s.db, clocked{}}).fail(context.Background(), reworkReviewClaim(t, s.db, review), StepOutcome{Status: StatusFail, Evidence: json.RawMessage(`{"reason":"fix the bug"}`)}); err != nil || result.AgentID != agent {
		t.Fatalf("rework not queued: %+v %v", result, err)
	}
	step, err := readStep(context.Background(), s.db, agent)
	if err != nil {
		t.Fatal(err)
	}
	result := race(8, func(int) int {
		claim := *step
		if _, err := newStepAttempts(s.db, func(kind string) bool { return kind == "agent/pi" }).Start(context.Background(), &claim, "agent/pi"); err == nil {
			return 1
		}
		return 0
	})
	if count(result, 1) != 1 {
		t.Fatalf("agent claims: %v", result)
	}
	// Original attempt plus exactly one retry.
	wantEventCount(t, s.db, 2, kindWith("step.started", "step", "build"))
}

func TestWorkflowReworkClearsPreviousAttemptOverride(t *testing.T) {
	t.Parallel()
	store, run, agent, _, approval := retryStageFixture(t)
	{
		current, err := workflowsql.New(store.db.SQL()).StepByID(context.Background(), agent)
		if err != nil {
			t.Fatal(err)
		}
		if err := requireAttemptWrite(workflowsql.New(store.db.SQL()).OverrideStep(context.Background(), workflowsql.OverrideStepParams{At: time.Now(), ID: agent, Attempt: current.Attempt, ExpectedStatus: current.Status, OverrideBy: "alice", OverrideReason: "previous attempt accepted"})); err != nil {
			t.Fatal(err)
		}
	}
	_, err := newService(store.db, allowProject(run.ProjectID), Runtime{}).Approve(context.Background(), Approval{StepID: approval, Decision: VoteReject, Comment: "new work is needed"})
	if err != nil {
		t.Fatalf("rework not queued: %v", err)
	}
	snapshot, err := readStep(context.Background(), store.db, agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newStepAttempts(store.db, func(kind string) bool { return kind == "agent/pi" }).Start(context.Background(), snapshot, "agent/pi"); err != nil {
		t.Fatalf("queued rework cannot be admitted: %v", err)
	}
	expect(t, eventCount(t, store.db, run.ID, "step.rework", "previousAgentOverrideBy=alice", "previousAgentOverrideReason=previous attempt accepted") == 1, "rework receipt lost the previous override")
}
