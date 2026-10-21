package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

func TestApprovalMalformedConfigurationOrEvidenceCannotRecordAVote(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ column, value string }{
		{"config_json", `{"quorum":"2"}`},
		{"evidence_json", `{"decisions":[{"approver":"other","decision":"unknown"}]}`},
		{"evidence_json", `{"decisions":[{"approver":"other","decision":"reject"}]}`},
	} {
		t.Run(tc.column+tc.value, func(t *testing.T) {
			s, run, _, _, approval := retryStageFixture(t)
			current, err := workflowsql.New(s.db.SQL()).StepByID(context.Background(), approval)
			if err != nil {
				t.Fatal(err)
			}
			if tc.column == "config_json" {
				// RetryStep accepts opaque configuration; the malformed quorum must be
				// refused by approval even when the row has a clean pending timestamp.
				if err := requireAttemptWrite(workflowsql.New(s.db.SQL()).RetryStep(context.Background(), workflowsql.RetryStepParams{ID: approval, Attempt: current.Attempt, ConfigJson: []byte(tc.value)})); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := requireAttemptWrite(workflowsql.New(s.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: approval, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: current.Status, EvidenceJson: []byte(tc.value)})); err != nil {
					t.Fatal(err)
				}
			}
			before := completionSnapshot(t, s.db)
			if _, err := newService(s.db, allowProject(run.ProjectID), Runtime{}).Approve(context.Background(), Approval{StepID: approval, Decision: VoteApprove, Comment: "reviewed"}); err == nil {
				t.Fatal("malformed approval admitted a vote")
			}
			if after := completionSnapshot(t, s.db); after != before {
				t.Fatal("malformed data changed votes, steps or receipts")
			}
		})
	}
}

func TestWorkflowPartialApprovalRequiresVoteWrite(t *testing.T) {
	t.Parallel()
	store, run, _, _, approval := retryStageFixture(t)
	completionExec(t, store.db, `CREATE FUNCTION veto_vote() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	completionExec(t, store.db, `CREATE TRIGGER veto_vote BEFORE UPDATE ON steps FOR EACH ROW EXECUTE FUNCTION veto_vote()`)
	before := completionSnapshot(t, store.db)
	if _, err := newService(store.db, allowProject(run.ProjectID), Runtime{}).Approve(context.Background(), Approval{StepID: approval, Decision: VoteApprove, Comment: "looks good"}); err == nil {
		t.Fatal("approval succeeded without its vote")
	}
	if completionSnapshot(t, store.db) != before {
		t.Fatal("missing vote left an approval receipt")
	}
	completionExec(t, store.db, `DROP TRIGGER veto_vote ON steps`)
	if _, err := newService(store.db, allowProject(run.ProjectID), Runtime{}).Approve(context.Background(), Approval{StepID: approval, Decision: VoteApprove, Comment: "looks good"}); err != nil {
		t.Fatal(err)
	}
	gate := storedStep(t, store.db, approval)
	expect(t, gate.Status == StatusPending && jsonMatches(t, gate.Evidence, "approvals=1"), "approval after rerun: %s %s", gate.Status, gate.Evidence)
}

func TestStepApprovalFencesAuthoritySnapshot(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"config", "type", "scope", "attempt", "revision", "stage"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			store, run, _, _, id := retryStageFixture(t)
			service := newService(store.db, authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
				if p.Action == actionIdentify {
					return "alice", nil
				}
				if p.Action != actionApproveStep || p.StepID != id || p.RunID != run.ID || p.ProjectID != run.ProjectID || p.ChangeID != run.ChangeID {
					t.Fatalf("authority: %+v", p)
				}
				switch change {
				case "config":
					{
						current, err := workflowsql.New(store.db.SQL()).StepByID(context.Background(), id)
						if err != nil {
							t.Fatal(err)
						}
						if err := requireAttemptWrite(workflowsql.New(store.db.SQL()).RetryStep(context.Background(), workflowsql.RetryStepParams{ID: id, Attempt: current.Attempt, ConfigJson: []byte(`{"approvers":["other"],"quorum":2}`)})); err != nil {
							t.Fatal(err)
						}
						// Restore the current verdict/evidence; only configuration changes among
						// the authority snapshot's fields. Its fence does not include timestamps.
						if err := requireAttemptWrite(workflowsql.New(store.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: id, Attempt: current.Attempt, ExpectedStatus: StatusPending.String(), Status: current.Status, EvidenceJson: current.EvidenceJson})); err != nil {
							t.Fatal(err)
						}
					}
				case "type":
					// sql-guarantee: Change the approval's immutable runner kind after authorization to test the saved decision fence.
					completionExec(t, store.db, `UPDATE steps SET type='script' WHERE id=?`, id)
				case "scope":
					// sql-guarantee: No owner mutation detaches an existing run's Change; erase that binding after authorization to isolate its scope fence.
					completionExec(t, store.db, `UPDATE runs SET change_id=NULL WHERE id=?`, run.ID)
				case "attempt":
					{
						current, err := workflowsql.New(store.db.SQL()).StepByID(context.Background(), id)
						if err != nil {
							t.Fatal(err)
						}
						if err := requireAttemptWrite(workflowsql.New(store.db.SQL()).StartParentCall(context.Background(), workflowsql.StartParentCallParams{ID: id, Attempt: current.Attempt, OldStatus: current.Status, Status: current.Status, StartedAt: current.StartedAt, EvidenceJson: current.EvidenceJson})); err != nil {
							t.Fatal(err)
						}
					}
				case "revision":
					{
						current, err := workflowsql.New(store.db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: run.ID})
						if err != nil {
							t.Fatal(err)
						}
						if err := requireRunWrite(workflowsql.New(store.db.SQL()).InvalidateRunDecision(context.Background(), workflowsql.InvalidateRunDecisionParams{At: time.Now(), ID: run.ID, CurrentStage: current.CurrentStage, RetryRevision: current.RetryRevision})); err != nil {
							t.Fatal(err)
						}
					}
				case "stage":
					{
						current, err := workflowsql.New(store.db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: run.ID})
						if err != nil {
							t.Fatal(err)
						}
						if err := requireRunWrite(workflowsql.New(store.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, ExpectedStatus: current.Status, CurrentStage: current.CurrentStage, RetryRevision: current.RetryRevision, NextStage: "later", NextStatus: current.Status})); err != nil {
							t.Fatal(err)
						}
					}
				}
				return "alice", nil
			}), Runtime{})
			result, err := service.Approve(context.Background(), Approval{StepID: id, Decision: VoteApprove, Comment: ""})
			if result != nil || !errors.Is(err, errStepDecisionConflict) {
				t.Fatalf("changed %s: %+v %v", change, result, err)
			}
			wantEventCount(t, store.db, 0, ofKind("approval"))
		})
	}
}

// An approval judges its stage's work, so it is refused while that work is
// still to run and given once it has finished; the refusal records nothing.
// In the gap between one step finishing and the next starting, apptown quick
// -y once approved before boot and review had run (live smoke, run 507).
func TestAnApprovalWaitsForItsStagesWork(t *testing.T) {
	t.Parallel()
	s, run, _, review, approval := retryStageFixture(t)
	// sql-guarantee: the review as the dispatcher leaves it before starting it; no owner operation stops between a finished step and the next one's start.
	retryScopeStep(t, s.db, review, json.RawMessage(`{}`))
	service := newService(s.db, allowProject(run.ProjectID), Runtime{})
	before := completionSnapshot(t, s.db)
	_, err := service.Approve(context.Background(), Approval{StepID: approval, Decision: VoteApprove, Comment: "early"})
	var early *approvalTooEarly
	if !errors.As(err, &early) || len(early.Unfinished) != 1 || early.Unfinished[0] != "review" {
		t.Fatalf("approval before the review ran: %v", err)
	}
	if completionSnapshot(t, s.db) != before {
		t.Fatal("a refused early approval changed votes, steps or receipts")
	}
	// sql-guarantee: the review's pass as its runner records it; running a reviewer needs the agent runtime.
	recordScopeStep(t, s.db, workflowsql.RecordApprovalParams{At: time.Now(), ID: review, Status: "pass"})
	if _, err := service.Approve(context.Background(), Approval{StepID: approval, Decision: VoteApprove, Comment: "after"}); err != nil {
		t.Fatalf("approval after the review passed: %v", err)
	}
}
