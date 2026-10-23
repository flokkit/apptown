package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
)

func TestApprovalInboxUsesCanonicalRecordsAndVisibility(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	project := workflowTestProject(t, db, "completion", "Completion", "")
	other := workflowTestProject(t, db, "private", "Private", "")
	foreign := createScopeChange(t, db, ChangeDraft{ProjectID: other, Type: "feature", Title: "secret"})
	// The read fixture starts with a cross-project change reference so the inbox
	// must omit its title even though it can read the owning run and approval.
	run := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: project, ChangeID: foreign, WorkflowName: "test", WorkflowVersion: 1, Kind: "pipeline", Status: "running", CurrentStage: "finish", DefinitionJson: json.RawMessage(`{"kind":"pipeline","role":"working","stages":[{"name":"finish"}]}`)})
	step := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "accept", Type: "approval", Status: "pending"})
	q := queries{db: db}
	ctx := context.Background()
	wq := workflowsql.New(q.db.SQL())
	// Two retried attempts: a nonzero attempt, and a final config that
	// differs from the earlier one.
	for attempt := int64(0); attempt < 2; attempt++ {
		if n, err := wq.StartAttempt(ctx, workflowsql.StartAttemptParams{At: time.Now(), ID: step, Attempt: attempt}); err != nil || n != 1 {
			t.Fatalf("start inbox attempt %d: %d %v", attempt+1, n, err)
		}
		if n, err := wq.FinishAttempt(ctx, workflowsql.FinishAttemptParams{At: time.Now(), Status: StatusFail.String(), EvidenceJson: json.RawMessage(`{}`), ID: step, Attempt: attempt + 1}); err != nil || n != 1 {
			t.Fatalf("finish inbox attempt %d: %d %v", attempt+1, n, err)
		}
		config := json.RawMessage(`{}`)
		if attempt == 1 {
			config = json.RawMessage(`{"means":"inspect the result"}`)
		}
		if n, err := wq.RetryStep(ctx, workflowsql.RetryStepParams{ConfigJson: config, ID: step, Attempt: attempt + 1}); err != nil || n != 1 {
			t.Fatalf("retry inbox attempt %d: %d %v", attempt+1, n, err)
		}
	}
	insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "later", Name: "future", Type: "approval", Status: StatusNeedsHuman.String(), ConfigJson: json.RawMessage(`{}`)})
	s := newService(q.db, allowProject(run.ProjectID), Runtime{})
	inbox, err := s.ApprovalInbox(ctx, ids.Nil)
	if err != nil || len(inbox) != 1 {
		t.Fatalf("inbox %+v: %v", inbox, err)
	}
	v := inbox[0]
	if v.Run.ID != run.ID || v.Step.ID != step || v.Step.Attempt != 2 || v.Step.OwnedAttempt() != 0 || v.ChangeTitle != "" || v.ProjectSlug != "completion" {
		t.Fatalf("observation %+v", v)
	}
	config, err := ReadApprovalConfig(v.Step.Config)
	if err != nil || config.Means != "inspect the result" {
		t.Fatalf("config %+v: %v", config, err)
	}
	if _, err := s.ApprovalInbox(ctx, other); err == nil {
		t.Fatal("foreign inbox admitted")
	}
	recordScopeStep(t, q.db, workflowsql.RecordApprovalParams{At: time.Now(), ID: step, Status: "pass"})
	inbox, err = s.ApprovalInbox(ctx, ids.Nil)
	if err != nil || inbox == nil || len(inbox) != 0 {
		t.Fatalf("settled inbox %+v: %v", inbox, err)
	}
	recordScopeStep(t, q.db, workflowsql.RecordApprovalParams{At: time.Now(), ID: step, Status: "pending"})
	s = newService(q.db, projectAuthority{project: run.ProjectID, authorityFunc: authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
		if p.Action == actionReadEvidence && p.ProjectID == other {
			return "", &access.Denied{Reason: "private"}
		}
		if p.Action == actionReadEvidence {
			// sql-guarantee: Move run.project_id during evidence authorization to test inbox scope revalidation; admission fixes project ownership and no generated mutation reparents an existing run.
			completionExec(t, q.db, `UPDATE runs SET project_id=?,number=number+100000 WHERE id=?`, other, run.ID)
		}
		return "reader", nil
	})}, Runtime{})
	inbox, err = s.ApprovalInbox(ctx, ids.Nil)
	if err != nil || len(inbox) != 0 {
		t.Fatalf("moved scope leaked %+v: %v", inbox, err)
	}
}

func TestProjectOpenCountsUseTheChangeLifecycle(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	s := newService(q.db, allowProject(run.ProjectID), Runtime{})
	for _, state := range []ChangeState{ChangeInProgress, ChangeLanding, ChangeShipped, ChangeAbandoned} {
		transitionScopeChange(t, q.db, workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, State: state.String()})
		got, err := s.ProjectActivity(context.Background(), run.ProjectID)
		want := 1
		if state.Closed() {
			want = 0
		}
		if err != nil || got[run.ProjectID].OpenChanges != want {
			t.Fatalf("%s %+v: %v", state, got, err)
		}
	}
}
