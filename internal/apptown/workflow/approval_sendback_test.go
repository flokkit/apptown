package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
)

func approvalLandingSpec() schema.Spec {
	return schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "accept", Steps: []schema.Step{{Name: "approve-landing", Uses: "approval", With: map[string]any{"means": "Merge this work.", "approvers": []string{"owner"}}}}}}}
}

func TestApproverRejectionReturnsChatChangeWithItsFullNote(t *testing.T) {
	t.Parallel()
	a := stepAuthoritySpecFixture(t, approvalLandingSpec())
	// The command uses real eligibility and credential attribution, with no
	// maintainer grant or independent change-send-back authority.
	grants, err := a.Access.GrantsAt(t.Context(), access.ScopeProject, fmt.Sprint(a.ProjectID))
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range grants {
		if grant.Subject == "owner" && grant.RoleName == access.RoleProjectMaintainer {
			if _, err := a.Access.Revoke(t.Context(), grant.ID, "boss"); err != nil {
				t.Fatal(err)
			}
		}
	}
	a.Work.sendbacks.abandonments = NewAbandonments(a.DB, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{})
	ctx := stepVerifiedContext(t.Context(), access.Principal{Kind: "user", Name: "owner", Via: "token", Credential: "token:calendar-approval"})
	note := strings.Repeat("Keep the full requested correction. ", 30)
	rejected, err := a.Work.Approve(ctx, Approval{StepID: a.StepID, Decision: VoteReject, Comment: note})
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != StatusFail {
		t.Fatalf("rejection changed to %s", rejected.Status)
	}
	var evidence ApprovalEvidence
	if err := json.Unmarshal(rejected.Evidence, &evidence); err != nil {
		t.Fatal(err)
	}
	if len(evidence.Decisions) != 1 || evidence.Decisions[0].Approver != "owner" || evidence.Decisions[0].Via != "token-cli:calendar-approval" || evidence.Decisions[0].Decision != VoteReject || evidence.Decisions[0].Comment != note {
		t.Fatalf("vote lost: approver=%s via=%s decision=%s comment=%q", evidence.Decisions[0].Approver, evidence.Decisions[0].Via, evidence.Decisions[0].Decision, evidence.Decisions[0].Comment)
	}
	change, err := a.Work.Change(ctx, a.ChangeID)
	if err != nil || change.State != ChangeInProgress || change.ReturnedBy == nil || change.ReturnedBy.By != "owner" || change.ReturnedBy.Note != note || change.ReturnedBy.RejectedStep != a.StepID {
		t.Fatalf("returned change: %+v %v", change, err)
	}
	landing, err := readRun(ctx, a.DB, a.RunID)
	if err != nil || landing.Status != RunAbandoned {
		t.Fatalf("landing: %+v %v", landing, err)
	}
	receipts, err := audit.Recent(ctx, a.DB, audit.Filter{ProjectID: a.ProjectID, KindPrefix: "change.sendback"})
	if err != nil || len(receipts) != 1 || receipts[0].Actor != "owner" || receipts[0].ActorVia != "token-cli:calendar-approval" {
		t.Fatalf("receipts: %+v %v", receipts, err)
	}
	if _, err := a.Work.SendBack(ctx, SendBack{ChangeID: a.ChangeID, Note: note}); err == nil {
		t.Fatal("approver acquired independent send-back authority")
	}
	// A later landing hides the old note even if that landing subsequently stops.
	store := runStartStore{db: a.DB, stages: NewStages(nil, func(string) bool { return false })}
	later, err := store.create(ctx, StartRequest{ProjectID: a.ProjectID, ChangeID: a.ChangeID, WorkflowName: "land", InputsJSON: "{}"}, PreparedRun{Workflow: DefinitionRef{Name: "land", Version: 1}, Spec: approvalLandingSpec()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.sendbacks.abandonments.abandon(ctx, Abandonment{RunID: later.ID, Who: "owner", Reason: "stop"}, a.ProjectID); err != nil {
		t.Fatal(err)
	}
	change, err = a.Work.Change(ctx, a.ChangeID)
	if err != nil || change.ReturnedBy != nil {
		t.Fatalf("old note survived later landing: %+v %v", change, err)
	}
}

func TestLandingRejectionContinuesCarriedWorkAndRollsBackTogether(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			owner, work, _ := completedSendBackFixture(t)
			store := runStartStore{db: owner.db, stages: NewStages(nil, func(string) bool { return false })}
			landing, err := store.create(t.Context(), StartRequest{ProjectID: work.ProjectID, ChangeID: work.ChangeID, WorkflowName: "land", InputsJSON: "{}"}, PreparedRun{Workflow: DefinitionRef{Name: "land", Version: 1}, Spec: approvalLandingSpec()})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.prepared(t.Context(), landing.ID); err != nil {
				t.Fatal(err)
			}
			step := admittedStepID(t, queries{db: owner.db}, landing, "approve-landing")
			decisions := stepDecisionStore{owner.db, clocked{}}
			snapshot, err := decisions.prepare(t.Context(), step)
			if err != nil {
				t.Fatal(err)
			}
			if fail {
				dbtest.FailNextStatement(t, owner.db, "INSERT INTO audit_log")
			}
			result, err := decisions.approve(context.Background(), snapshot, "alice", Approval{Decision: VoteReject, Comment: "Keep the invitation."}, nil, "", nil, owner, "")
			if fail {
				if err == nil {
					t.Fatal("receipt failure accepted")
				}
				saved, err := readStep(t.Context(), owner.db, step)
				if err != nil || saved.Status != StatusPending {
					t.Fatalf("vote leaked: %+v %v", saved, err)
				}
				current, err := readRun(t.Context(), owner.db, landing.ID)
				if err != nil || current.Status != RunRunning {
					t.Fatalf("abandonment leaked: %+v %v", current, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.returned.RunID.IsZero() || result.returned.RunID == work.ID {
				t.Fatalf("no continuation: %+v", result)
			}
			agent, err := readStep(t.Context(), owner.db, result.returned.StepID)
			if err != nil || !strings.Contains(string(agent.Config), "Keep the invitation.") {
				t.Fatalf("feedback: %+v %v", agent, err)
			}
			change, err := ReadChangeIn(t.Context(), owner.db, work.ChangeID)
			if err != nil || change.ReturnedBy == nil {
				t.Fatalf("missing receipt reading: %+v %v", change, err)
			}
			// A new agent round ends the receipt's current-state lifetime.
			err = owner.db.InTx(t.Context(), func(tx *database.Tx) error {
				return recordStartReceiptIn(t.Context(), tx, result.returned.RunID, stepStartedReceipt{Step: agent.Name, Type: "agent/pi"}, time.Now().Add(time.Second))
			})
			if err != nil {
				t.Fatal(err)
			}
			change, err = ReadChangeIn(t.Context(), owner.db, work.ChangeID)
			if err != nil || change.ReturnedBy != nil {
				t.Fatalf("old note survived agent start: %+v %v", change, err)
			}
			rows, err := workflowsql.New(owner.db.SQL()).ChangeRuns(t.Context(), workflowsql.ChangeRunsParams{ProjectID: work.ProjectID, ChangeID: work.ChangeID})
			if err != nil || len(rows) != 3 {
				t.Fatalf("runs: %d %v", len(rows), err)
			}
		})
	}
}

func TestApprovalWithoutAnyAgentReturnsWorkToItsPerson(t *testing.T) {
	t.Parallel()
	a := stepAuthorityFixture(t, ApprovalConfig{Approvers: []string{"owner"}})
	a.Work.sendbacks.abandonments = NewAbandonments(a.DB, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{})
	ctx := stepVerifiedContext(t.Context(), access.Principal{Kind: "user", Name: "owner", Via: "session"})
	if _, err := a.Work.Approve(ctx, Approval{StepID: a.StepID, Decision: VoteReject, Comment: "Clarify the request."}); err != nil {
		t.Fatal(err)
	}
	change, err := a.Work.Change(ctx, a.ChangeID)
	if err != nil || change.State != ChangeInProgress || change.ReturnedBy == nil || change.ReturnedBy.Note != "Clarify the request." {
		t.Fatalf("returned work: %+v %v", change, err)
	}
	run, err := readRun(ctx, a.DB, a.RunID)
	if err != nil || run.Status != RunAbandoned {
		t.Fatalf("run: %+v %v", run, err)
	}
}
