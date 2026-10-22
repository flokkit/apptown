package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestStepClaimCannotBeRetargetedOrGrantedByRead(t *testing.T) {
	t.Parallel()
	q, _, id := admitSingleStep(t, schema.Step{Name: "accept", Uses: "script"})
	ctx := context.Background()
	snapshot, err := readStep(ctx, q.db, id)
	if err != nil {
		t.Fatal(err)
	}
	admissions := newStepAttempts(q.db, func(kind string) bool { return kind == "script" })
	claim, err := admissions.Start(ctx, snapshot, "script")
	if err != nil {
		t.Fatal(err)
	}
	if claim.OwnedAttempt() != 1 {
		t.Fatal("committed admission lost its claim")
	}
	reread, err := readStep(ctx, q.db, id)
	if err != nil || reread.OwnedAttempt() != 0 {
		t.Fatalf("read granted ownership: %+v %v", reread, err)
	}
	changed := *claim
	changed.ID = idstest.N(999999)
	if changed.OwnedAttempt() != 0 {
		t.Fatal("claim moved to another Step")
	}
	changed = *claim
	changed.RunID = idstest.N(999999)
	if changed.OwnedAttempt() != 0 {
		t.Fatal("claim moved to another Run")
	}
}

func TestStepAdmissionDoesNotIssueClaimWhenReceiptCannotCommit(t *testing.T) {
	t.Parallel()
	q, _, id := admitSingleStep(t, schema.Step{Name: "accept", Uses: "script"})
	ctx := context.Background()
	snapshot, err := readStep(ctx, q.db, id)
	if err != nil {
		t.Fatal(err)
	}
	admissions := newStepAttempts(q.db, func(kind string) bool { return kind == "script" })
	dbtest.FailNextStatement(t, q.db, "INSERT INTO run_events")
	if claim, err := admissions.Start(ctx, snapshot, "script"); claim != nil || err == nil {
		t.Fatalf("uncommitted claim: %+v %v", claim, err)
	}
	current, err := readStep(ctx, q.db, id)
	if err != nil || current.Attempt != 0 || current.Status != StatusPending {
		t.Fatalf("admission did not roll back: %+v %v", current, err)
	}
	// Admission wrote run.created, stage.entered and run.ready; rejection adds nothing.
	wantEventCount(t, q.db, 3, anyEvent)
	if _, err := admissions.Start(ctx, snapshot, "script"); err != nil {
		t.Fatal(err)
	}
	wantEventCount(t, q.db, 1, ofKind("step.started"))
}

func TestStepAttemptRejectsUnrunnableSnapshots(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"finished", "overridden", "changed configuration", "old attempt", "abandoned", "old stage"} {
		t.Run(name, func(t *testing.T) {
			q, run, id := admitSingleStep(t, schema.Step{Name: "accept", Uses: "scan"})
			ctx := context.Background()
			step, err := readStep(ctx, q.db, id)
			if err != nil {
				t.Fatal(err)
			}
			owner := newStepAttempts(q.db, func(kind string) bool { return kind == "scan" })
			queries := workflowsql.New(q.db.SQL())
			switch name {
			case "finished":
				if n, err := queries.RecordApproval(ctx, workflowsql.RecordApprovalParams{At: time.Now(), Status: StatusPass.String(), EvidenceJson: json.RawMessage(`{}`), ID: id, Attempt: 0, ExpectedStatus: StatusPending.String()}); err != nil || n != 1 {
					t.Fatalf("finish stored step: %d %v", n, err)
				}
			case "overridden":
				// An override on a pending Step is deliberately malformed and has no guarded mutation.
				// sql-guarantee: Retain status=pending and the observed attempt while setting only override_by to prove claim admission checks overrides; OverrideStep also sets status=pass, which would mask the override-specific guard.
				completionExec(t, q.db, `UPDATE steps SET override_by='alice'`)
			case "changed configuration":
				if n, err := queries.RetryStep(ctx, workflowsql.RetryStepParams{ConfigJson: json.RawMessage(`{"command":"new"}`), ID: id, Attempt: 0}); err != nil || n != 1 {
					t.Fatalf("replace stored configuration: %d %v", n, err)
				}
			case "old attempt":
				if n, err := queries.StartAttempt(ctx, workflowsql.StartAttemptParams{At: time.Now(), ID: id, Attempt: 0}); err != nil || n != 1 {
					t.Fatalf("advance stored attempt: %d %v", n, err)
				}
				if n, err := queries.RecheckStep(ctx, workflowsql.RecheckStepParams{ID: id, Attempt: 1}); err != nil || n != 1 {
					t.Fatalf("restore stored pending state: %d %v", n, err)
				}
			case "abandoned":
				if n, err := queries.TransitionRun(ctx, workflowsql.TransitionRunParams{At: time.Now(), NextStage: run.CurrentStage, NextStatus: RunAbandoned.String(), ID: run.ID, ExpectedStatus: run.Status.String(), CurrentStage: run.CurrentStage, RetryRevision: run.RetryRevision}); err != nil || n != 1 {
					t.Fatalf("abandon stored run: %d %v", n, err)
				}
			case "old stage":
				if n, err := queries.TransitionRun(ctx, workflowsql.TransitionRunParams{At: time.Now(), NextStage: "later", NextStatus: run.Status.String(), ID: run.ID, ExpectedStatus: run.Status.String(), CurrentStage: run.CurrentStage, RetryRevision: run.RetryRevision}); err != nil || n != 1 {
					t.Fatalf("advance stored stage: %d %v", n, err)
				}
			}
			if _, err := owner.Start(ctx, step, "scan"); !errors.Is(err, ErrStepAttemptLost) {
				t.Fatalf("admission: %v", err)
			}
			if step.OwnedAttempt() != 0 {
				t.Fatal("rejected admission owns an attempt")
			}
			// Admission wrote run.created, stage.entered and run.ready; rejection adds nothing.
			wantEventCount(t, q.db, 3, anyEvent)
		})
	}
}
