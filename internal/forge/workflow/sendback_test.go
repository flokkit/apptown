package workflow

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/access"
)

func TestSendBackRequiresAuthorityAndRechecksScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if _, err := newService(nil, nil, Runtime{}).SendBack(ctx, SendBack{ChangeID: idstest.N(1), Note: "again"}); !errors.Is(err, ErrCallerRequired) {
		t.Fatalf("missing caller: %v", err)
	}
	for _, move := range []bool{false, true} {
		t.Run(fmt.Sprint(move), func(t *testing.T) {
			t.Parallel()
			owner, run, _, _ := retryFixture(t)
			other := workflowTestProject(t, owner.db, "other", "Other", "")
			svc := newService(owner.db, authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
				if p.Action == actionSendBackChange {
					if p.ProjectID != run.ProjectID || p.ChangeID != run.ChangeID {
						t.Fatalf("wrong scope %+v", p)
					}
					if !move {
						return "", &access.Denied{Reason: "no permission"}
					}
					// sql-guarantee: No owner mutation rehomes a Change; move the authorized identity to another project to isolate send-back's scope fence.
					completionExec(t, owner.db, `UPDATE changes SET project_id=?,number=number+100000 WHERE id=?`, other, run.ChangeID)
				}
				return "verified", nil
			}), Runtime{Automatic: owner.automatic})
			if _, err := svc.SendBack(ctx, SendBack{ChangeID: run.ChangeID, Note: "again"}); err == nil {
				t.Fatal("unauthorized or moved work admitted")
			}
			// sql-guarantee: no workflow read enumerates continuations; the total proves the refused command wrote none.
			completionCount(t, owner.db, 0, `SELECT COUNT(*) FROM workflow_continuations`)
			wantEventCount(t, owner.db, 0, ofKind("step.retried"))
		})
	}
}

func TestContinuationRequiresInheritedRowsAndOwner(t *testing.T) {
	t.Parallel()
	for _, table := range []string{"run_inference_owners", "run_starts", "workflow_continuations", "artifacts", "steps", "run_events"} {
		t.Run(table, func(t *testing.T) {
			t.Parallel()
			owner, run, _, _ := retryFixture(t)
			{
				current, err := workflowsql.New(owner.db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: run.ID})
				if err != nil {
					t.Fatal(err)
				}
				if rows, err := workflowsql.New(owner.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, ExpectedStatus: current.Status, CurrentStage: current.CurrentStage, RetryRevision: current.RetryRevision, NextStage: current.CurrentStage, NextStatus: "done"}); err != nil || rows != 1 {
					t.Fatalf("stored run transition: rows=%d err=%v", rows, err)
				}
			}
			{
				rows, err := workflowsql.New(owner.db.SQL()).RunSteps(context.Background(), workflowsql.RunStepsParams{RunID: run.ID})
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) == 0 {
					t.Fatal("missing stage checks")
				}
				for _, step := range rows {
					if err := requireAttemptWrite(workflowsql.New(owner.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: step.ID, Attempt: step.Attempt, ExpectedStatus: step.Status, Status: StatusPass.String(), EvidenceJson: step.EvidenceJson})); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Real admission already recorded the inference owner to inherit.
			if _, err := AddArtifactIn(context.Background(), owner.db, run.ID, "input", "binary", "", string([]byte{0, 255, 42}), time.Now()); err != nil {
				t.Fatal(err)
			}
			if rows, err := workflowsql.New(owner.db.SQL()).InsertRunEvent(context.Background(), workflowsql.InsertRunEventParams{ID: ids.New(time.Now()), RunID: run.ID, At: time.Now(), Kind: "guidance.pinned", PayloadJson: []byte(`{"n":9007199254740993}`)}); err != nil || rows != 1 {
				t.Fatalf("stored historical receipt: rows=%d err=%v", rows, err)
			}
			condition := "true"
			if table == "run_events" {
				condition = "NEW.kind='guidance.pinned'"
			}
			completionExec(t, owner.db, `CREATE FUNCTION veto_continuation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN RETURN NULL; END IF; RETURN NEW; END $$`)
			completionExec(t, owner.db, `CREATE TRIGGER veto_continuation BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION veto_continuation()`)
			svc := newService(owner.db, allowProject(run.ProjectID), Runtime{Automatic: owner.automatic})
			if _, err := svc.SendBack(context.Background(), SendBack{ChangeID: run.ChangeID, Note: "again"}); err == nil {
				t.Fatal("suppressed required record admitted")
			}
			wantRunCount(t, owner.db, 1, nil)
			// sql-guarantee: no workflow read enumerates continuations; the total proves the refused command wrote none.
			completionCount(t, owner.db, 0, `SELECT COUNT(*) FROM workflow_continuations`)
			wantReceiptCount(t, owner.db, 0, receiptKind("change.sendback"))
		})
	}
}

// A change sent back with a note continues in a new run, and that run's
// latest person feedback is the note and who sent it: the reviewer of the
// continued work reads it as part of the request. A run nobody sent back
// has none.
func TestASentBackRunsPersonFeedbackIsTheNote(t *testing.T) {
	t.Parallel()
	s, source, _ := completedSendBackFixture(t)
	ctx := context.Background()
	if got, err := LatestPersonFeedbackIn(ctx, s.db, RunRef{ID: source.ID, ProjectID: source.ProjectID}); err != nil || got != nil {
		t.Fatalf("a run nobody sent back: %+v %v", got, err)
	}
	result, err := s.sendBack(ctx, source.ChangeID, "also upgrade the logging library", "alice", "", source.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LatestPersonFeedbackIn(ctx, s.db, RunRef{ID: result.RunID, ProjectID: source.ProjectID})
	if err != nil || got == nil || got.Note != "also upgrade the logging library" || got.By != "alice" {
		t.Fatalf("the continued run's person feedback: %+v %v", got, err)
	}
	if got, err := LatestPersonFeedbackIn(ctx, s.db, RunRef{ID: result.RunID, ProjectID: idstest.N(999999)}); err != nil || got != nil {
		t.Fatalf("another project's view of the run: %+v %v", got, err)
	}
	// The continued run's approval brief carries the note as part of the
	// ask, so an approver is not told the requested work is a deviation.
	ref := RunRef{ID: result.RunID, ProjectID: source.ProjectID}
	steps, err := ReadScopedStepsIn(ctx, s.db, ref)
	if err != nil {
		t.Fatal(err)
	}
	var accept ids.ID
	for _, step := range steps {
		if step.Type == "approval" {
			accept = step.ID
		}
	}
	tx, err := s.db.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	brief, err := readApprovalBriefIn(ctx, tx, ref, accept)
	if err != nil || brief.Asked == nil || brief.Asked.Note != "also upgrade the logging library" || brief.Asked.By != "alice" {
		t.Fatalf("the continued run's approval brief: %+v %v", brief.Asked, err)
	}
}
