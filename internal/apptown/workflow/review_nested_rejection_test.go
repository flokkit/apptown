package workflow

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
)

// Rejection must stop waiting calls atomically, while real work still refuses it.
func TestReviewRejectionOfCalledApprovalKeepsVote(t *testing.T) {
	t.Parallel()
	for _, busy := range []bool{false, true} {
		t.Run(map[bool]string{false: "waiting call", true: "running work"}[busy], func(t *testing.T) {
			a := stepAuthoritySpecFixture(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "outer", Steps: []schema.Step{{Name: "call", Uses: "workflow/call", With: map[string]any{"workflow": "review"}}}}}})
			a.Work.sendbacks.abandonments = NewAbandonments(a.DB, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{})
			store := runStartStore{db: a.DB, stages: NewStages(nil, func(string) bool { return false })}
			spec := schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"approvers": []string{"owner"}}}}}}}
			if busy {
				spec.Stages[0].Steps = append(spec.Stages[0].Steps, schema.Step{Name: "check", Uses: "script", With: map[string]any{"run": "true"}})
			}
			child, err := store.create(t.Context(), StartRequest{ProjectID: a.ProjectID, ChangeID: a.ChangeID, WorkflowName: "review", InputsJSON: "{}", parentRunID: a.RunID, parentStepID: a.StepID}, PreparedRun{Workflow: DefinitionRef{Name: "review", Version: 1}, Spec: spec})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.prepared(t.Context(), child.ID); err != nil {
				t.Fatal(err)
			}
			if busy {
				// Admit and claim a real task through the owner's store; the fixture has no dispatcher.
				task := admittedStepID(t, queries{db: a.DB}, child, "check")
				if n, err := workflowsql.New(a.DB.SQL()).StartAttempt(t.Context(), workflowsql.StartAttemptParams{ID: task, At: time.Now()}); err != nil || n != 1 {
					t.Fatalf("start task: %d %v", n, err)
				}
			}
			step := admittedStepID(t, queries{db: a.DB}, child, "accept")
			ctx := stepVerifiedContext(t.Context(), access.Principal{Kind: "user", Name: "owner", Via: "session"})
			_, voteErr := a.Work.Approve(ctx, Approval{StepID: step, Decision: VoteReject, Comment: "Revise the request."})
			if busy {
				if voteErr == nil || !strings.Contains(voteErr.Error(), "wait for the running steps") {
					t.Fatal("running work accepted send-back")
				}
			} else if voteErr != nil {
				t.Fatalf("rejection was refused: %v", voteErr)
			}
			saved, err := readStep(t.Context(), a.DB, step)
			if busy {
				if err != nil || saved.Status != StatusPending {
					t.Fatalf("refused vote leaked: %+v %v", saved, err)
				}
			} else if err != nil || saved.Status != StatusFail {
				t.Fatalf("rejection not retained: %+v %v", saved, err)
			}

			var evidence ApprovalEvidence
			if err := json.Unmarshal(saved.Evidence, &evidence); err != nil {
				t.Fatal(err)
			}
			if !busy && (len(evidence.Decisions) != 1 || evidence.Decisions[0].Decision != VoteReject || evidence.Decisions[0].Comment != "Revise the request." || evidence.Decisions[0].Approver != "owner") {
				t.Fatalf("lost rejection: %+v", evidence)
			}
			for _, id := range []ids.ID{a.RunID, child.ID} {
				run, err := readRun(ctx, a.DB, id)
				want := RunAbandoned
				if busy {
					want = RunRunning
				}
				if err != nil || run.Status != want {
					t.Fatalf("run: %+v %v", run, err)
				}
			}
			receipts, err := audit.Recent(ctx, a.DB, audit.Filter{ProjectID: a.ProjectID, KindPrefix: "change.sendback"})
			count := 1
			if busy {
				count = 0
			}
			if err != nil || len(receipts) != count {
				t.Fatalf("receipts: %+v %v", receipts, err)
			}
			if !busy {
				change, err := a.Work.Change(ctx, a.ChangeID)
				if err != nil || change.State != ChangeInProgress || change.ReturnedBy == nil || change.ReturnedBy.RejectedStep != step || change.ReturnedBy.Note != "Revise the request." {
					t.Fatalf("returned change: %+v %v", change, err)
				}
				var receipt sendBackReceipt
				if err := json.Unmarshal(receipts[0].Payload, &receipt); err != nil {
					t.Fatal(err)
				}
				if receipt.Note != "Revise the request." || receipts[0].Actor != "owner" || receipt.RejectedStep != step || len(receipt.StoppedLandings) != 1 || receipt.StoppedLandings[0] != a.RunID {
					t.Fatalf("receipt: %+v", receipt)
				}
			}
		})
	}
}
