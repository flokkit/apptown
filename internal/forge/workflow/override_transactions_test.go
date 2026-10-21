package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

func overrideFixture(t *testing.T) (*Service, *Run, ids.ID) {
	t.Helper()
	db, run, _ := completionFixture(t, "working")
	id := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "checks", Type: "script", Status: StatusRunning.String()})
	for attempt := int64(0); attempt < 3; attempt++ {
		next := StatusRunning
		if attempt == 2 {
			next = StatusFail
		}
		if err := requireAttemptWrite(workflowsql.New(db.SQL()).StartParentCall(context.Background(), workflowsql.StartParentCallParams{ID: id, Attempt: attempt, OldStatus: StatusRunning.String(), Status: next.String(), EvidenceJson: []byte(`{"reason":"failed check"}`)})); err != nil {
			t.Fatal(err)
		}
	}
	s := newService(db, authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
		if p.Action != actionIdentify && (p.Action != actionOverrideStep || p.StepID != id || p.RunID != run.ID || p.ProjectID != run.ProjectID || p.ChangeID != run.ChangeID) {
			t.Errorf("wrong authority scope: %+v", p)
		}
		return "verified-alice", nil
	}), Runtime{})
	return s, run, id
}

func TestStepOverrideCommitsItsReceiptWithDecision(t *testing.T) {
	t.Parallel()
	s, run, id := overrideFixture(t)
	step, err := s.Override(context.Background(), Override{StepID: id, Reason: "reviewed the evidence"})
	if err != nil {
		t.Fatal(err)
	}
	if step.Status != StatusPass || step.OverrideBy != "verified-alice" || step.Attempt != 3 || step.OwnedAttempt() != 0 || string(step.Evidence) != `{"reason":"failed check"}` || !step.FinishedAt.Valid {
		t.Fatalf("override snapshot: %+v", step)
	}
	var receipts []overrideReceipt
	for _, e := range seenEvents(t, s.db, run.ID) {
		if e.Kind == "override" {
			var receipt overrideReceipt
			if err := json.Unmarshal(e.PayloadJson, &receipt); err != nil {
				t.Fatal(err)
			}
			receipts = append(receipts, receipt)
		}
	}
	if len(receipts) != 1 {
		t.Fatalf("override receipts: %+v", receipts)
	}
	receipt := receipts[0]
	if receipt != (overrideReceipt{"checks", "verified-alice", "reviewed the evidence", 3}) {
		t.Fatalf("receipt %+v", receipt)
	}
}

func TestStepOverrideRollsBackWriteReceiptAndCommitFailure(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"write error", "missing write", "receipt error", "missing receipt", "commit error", "read error", "authority error"} {
		t.Run(failure, func(t *testing.T) {
			s, _, id := overrideFixture(t)
			original := s.authority
			switch failure {
			case "write error":
				dbtest.FailNextStatement(t, s.db, "UPDATE steps SET status='pass'")
			case "receipt error":
				dbtest.FailNextStatement(t, s.db, "INSERT INTO run_events")
			case "read error":
				dbtest.FailNextStatement(t, s.db, "-- name: StepByID :one")
			case "authority error":
				s.authority = authorityFunc(func(context.Context, operationPermission) (string, error) {
					return "", errors.New("authority unavailable")
				})
			default:
				table, event := "steps", "UPDATE"
				if failure != "missing write" {
					table, event = "run_events", "INSERT"
				}
				body := "RETURN NULL;"
				trigger := "CREATE TRIGGER reject_override BEFORE " + event + " ON " + table
				if failure == "commit error" {
					body = "RAISE EXCEPTION 'receipt rejected at commit';"
					trigger = "CREATE CONSTRAINT TRIGGER reject_override AFTER " + event + " ON " + table + " DEFERRABLE INITIALLY DEFERRED"
				}
				completionExec(t, s.db, `CREATE FUNCTION reject_override() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`)
				completionExec(t, s.db, trigger+" FOR EACH ROW EXECUTE FUNCTION reject_override()")
			}
			step, err := s.Override(context.Background(), Override{StepID: id, Reason: "reviewed"})
			if step != nil || err == nil {
				t.Fatalf("%s survived: %+v, %v", failure, step, err)
			}
			step = storedStep(t, s.db, id)
			expect(t, step.Status == StatusFail && step.OverrideBy == "" && step.OverrideReason == "" && !step.FinishedAt.Valid && step.Attempt == 3, "failed override changed the step: %+v", step)
			wantEventCount(t, s.db, 0, anyEvent)
			completionExec(t, s.db, `DROP FUNCTION IF EXISTS reject_override() CASCADE`)
			s.authority = original
			if _, err := s.Override(context.Background(), Override{StepID: id, Reason: "reviewed"}); err != nil {
				t.Fatalf("explicit rerun: %v", err)
			}
		})
	}
}

func TestStepOverrideRequiresAnUndecidedCurrentStage(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"running", "pass", "overridden", "later stage", "done run", "abandoned run", "invalid state"} {
		t.Run(state, func(t *testing.T) {
			s, run, id := overrideFixture(t)
			switch state {
			case "done run", "abandoned run":
				status := RunDone
				if state == "abandoned run" {
					status = RunAbandoned
				}
				transitionStoredRunForTest(t, s.db, run.ID, "", status)
			case "later stage":
				transitionStoredRunForTest(t, s.db, run.ID, "later", RunRunning)
			case "overridden":
				// sql-guarantee: the conflict matrix requires fail status with override_by already set but no override receipt; OverrideStep would also pass and finish the step.
				completionExec(t, s.db, `UPDATE steps SET override_by='bob' WHERE id=?`, id)
			case "invalid state":
				step, err := readStep(context.Background(), s.db, id)
				if err != nil {
					t.Fatal(err)
				}
				changed, err := workflowsql.New(s.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), Status: state, EvidenceJson: step.Evidence, ID: id, Attempt: step.Attempt, ExpectedStatus: step.Status.String()})
				if err != nil || changed != 1 {
					t.Fatalf("store invalid state: changed=%d %v", changed, err)
				}
			default:
				next := map[string]StepState{"running": StatusRunning, "pass": StatusPass}[state]
				transitionStoredStepForTest(t, s.db, id, next)
			}
			step, err := s.Override(context.Background(), Override{StepID: id, Reason: "reviewed"})
			want := errStepDecisionConflict
			if state == "invalid state" {
				want = errStepState
			}
			if step != nil || !errors.Is(err, want) {
				t.Fatalf("%s admitted: %+v, %v", state, step, err)
			}
			wantEventCount(t, s.db, 0, anyEvent)
		})
	}
}

func TestStepOverrideHasOneConcurrentWinner(t *testing.T) {
	t.Parallel()
	s, _, id := overrideFixture(t)
	outcomes := race(8, func(int) int {
		_, err := s.Override(context.Background(), Override{StepID: id, Reason: "reviewed"})
		if err == nil {
			return 1
		}
		if !errors.Is(err, errStepDecisionConflict) {
			t.Error(err)
		}
		return 0
	})
	if count(outcomes, 1) != 1 {
		t.Fatalf("winners %v", outcomes)
	}
	wantEventCount(t, s.db, 1, anyEvent)
}

func TestStepOverrideCancellationReleasesAdmission(t *testing.T) {
	t.Parallel()
	s, run, id := overrideFixture(t)
	gate, err := s.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	holdProjectLock(t, gate, run.ProjectID)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.Override(ctx, Override{StepID: id, Reason: "reviewed"})
		done <- err
	}()
	t.Cleanup(func() { cancel(); _ = gate.Rollback() })
	dbtest.WaitForLock(t, s.db, "-- name: ProjectForUpdate")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("override did not release cancelled admission")
	}
	expectOverrideRefused(t, s, id)
}

// expectOverrideRefused: the step keeps its failure and no run has a receipt.
func expectOverrideRefused(t *testing.T, s *Service, id ids.ID) {
	t.Helper()
	step := storedStep(t, s.db, id)
	expect(t, step.Status == StatusFail && step.OverrideBy == "", "refused override changed the step: %+v", step)
	wantEventCount(t, s.db, 0, anyEvent)
}

func TestStepOverrideFencesChangesAfterAdmissionRead(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"scope", "attempt", "revision", "stage"} {
		t.Run(change, func(t *testing.T) {
			s, run, id := overrideFixture(t)
			// The policy admits the caller, then the world moves before the decision writes.
			s.authority = authorityFunc(func(ctx context.Context, p operationPermission) (string, error) {
				if p.Action == actionIdentify {
					return "verified-alice", nil
				}
				who, err := "verified-alice", error(nil)
				switch change {
				case "scope":
					// sql-guarantee: authority admits this exact run before only change_id is cleared; no Workflow mutation exposes relationship reassignment.
					completionExec(t, s.db, `UPDATE runs SET change_id=NULL WHERE id=?`, run.ID)
				case "attempt":
					step, err := readStep(ctx, s.db, id)
					if err != nil {
						t.Fatal(err)
					}
					if err := requireAttemptWrite(workflowsql.New(s.db.SQL()).StartParentCall(ctx, workflowsql.StartParentCallParams{ID: id, Attempt: step.Attempt, OldStatus: step.Status.String(), Status: step.Status.String(), EvidenceJson: step.Evidence})); err != nil {
						t.Fatal(err)
					}
				case "revision":
					invalidateStoredRunForTest(t, s.db, run.ID)
				case "stage":
					transitionStoredRunForTest(t, s.db, run.ID, "later", RunRunning)
				}
				return who, err
			})
			step, err := s.Override(context.Background(), Override{StepID: id, Reason: "reviewed"})
			if step != nil || !errors.Is(err, errStepDecisionConflict) {
				t.Fatalf("changed %s accepted: %+v, %v", change, step, err)
			}
			expectOverrideRefused(t, s, id)
		})
	}
}
