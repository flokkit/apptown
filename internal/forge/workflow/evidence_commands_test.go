package workflow

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

func TestAttachArtifactCommitsReceiptBeforeRefreshing(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"none", "receipt", "commit", "refresh"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			q, run, id := admitSingleStep(t, schema.Step{Name: "accept", Uses: "artifact/require", With: map[string]any{"kind": "report"}})
			reported := 0
			s := newServiceWithAuthority(q.db, Dependencies{Report: func(error) { reported++ }}, allowProject(run.ProjectID))
			switch fault {
			case "receipt":
				dbtest.FailNextStatement(t, q.db, "INSERT INTO run_events")
			case "commit":
				completionExec(t, q.db, `CREATE FUNCTION veto_artifact_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'artifact commit rejected'; END $$`)
				completionExec(t, q.db, `CREATE CONSTRAINT TRIGGER veto_artifact_commit AFTER INSERT ON artifacts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION veto_artifact_commit()`)
			case "refresh":
				changed, err := workflowsql.New(q.db.SQL()).RetryStep(context.Background(), workflowsql.RetryStepParams{ConfigJson: []byte(`{"kind":[]}`), ID: id, Attempt: 0})
				if err != nil || changed != 1 {
					t.Fatalf("store invalid assertion declaration: changed=%d %v", changed, err)
				}
			}
			artifact, err := s.AttachArtifact(context.Background(), run.ID, ArtifactDraft{Kind: "report", Name: "report", Body: "\x00binary\xff"})
			if fault == "receipt" || fault == "commit" {
				if err == nil || !artifact.IsZero() {
					t.Fatalf("failed attachment: id=%d err=%v", artifact, err)
				}
				// sql-guarantee: rollback proves no artifact row was written; the artifact reads are scoped to a run and cannot prove a store-wide absence.
				completionCount(t, q.db, 0, `SELECT count(*) FROM artifacts`)
				wantEventCount(t, q.db, 3, anyEvent) // Admission receipts only.
				if fault == "commit" {
					completionExec(t, q.db, `DROP TRIGGER veto_artifact_commit ON artifacts`)
				}
				artifact, err = s.AttachArtifact(context.Background(), run.ID, ArtifactDraft{Kind: "report", Name: "report", Body: "\x00binary\xff"})
			}
			if err != nil || artifact.IsZero() {
				t.Fatalf("committed attachment: id=%d err=%v", artifact, err)
			}
			wantEventCount(t, q.db, 1, kindWith("artifact.added", "id", fmt.Sprint(artifact)))
			var body []byte
			// sql-guarantee: the binary body column's exact bytes are the storage guarantee; the artifact read decodes them.
			if err := q.db.QueryRow(`SELECT body_bytes FROM artifacts WHERE id=?`, artifact).Scan(&body); err != nil || string(body) != "\x00binary\xff" {
				t.Fatalf("body=%q err=%v", body, err)
			}
			if fault == "refresh" {
				if reported != 1 {
					t.Fatalf("lost post-commit refresh failure: %d", reported)
				}
				changed, retryErr := workflowsql.New(q.db.SQL()).RetryStep(context.Background(), workflowsql.RetryStepParams{ConfigJson: []byte(`{"kind":"report"}`), ID: id, Attempt: 0})
				if retryErr != nil || changed != 1 {
					t.Fatalf("restore assertion declaration: changed=%d %v", changed, retryErr)
				}
				if err := s.RefreshAssertions(context.Background(), run.ID); err != nil {
					t.Fatal(err)
				}
			}
			wantStepState(t, q.db, id, StatusPass)
		})
	}
}

func TestEvidenceAndExecutionCommandsRequireScope(t *testing.T) {
	t.Parallel()
	q, run, id := activityFixture(t)
	var calls int
	denied := newServiceWithAuthority(q.db, Dependencies{Runners: map[string]Runner{"approval": func(*Execution) { calls++ }}}, authorityFunc(func(context.Context, operationPermission) (string, error) { return "", ErrCallerRequired }))
	if _, err := denied.AttachArtifact(context.Background(), run.ID, ArtifactDraft{Kind: "report"}); !errors.Is(err, ErrCallerRequired) {
		t.Fatal(err)
	}
	if err := denied.ExecuteStep(context.Background(), id); !errors.Is(err, ErrCallerRequired) {
		t.Fatal(err)
	}
	if err := denied.RefreshChangeEvidence(context.Background(), run.ProjectID, run.ChangeID); !errors.Is(err, ErrCallerRequired) {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("unadmitted runner invoked")
	}
	// sql-guarantee: rollback proves no artifact row was written; the artifact reads are scoped to a run and cannot prove a store-wide absence.
	completionCount(t, q.db, 0, `SELECT count(*) FROM artifacts`)
	other := workflowTestProject(t, q.db, "other", "Other", "")
	moved := newServiceWithAuthority(q.db, Dependencies{}, authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
		if p.Action == actionAttachEvidence {
			// sql-guarantee: authority admits this run before only its project identity moves; no Workflow mutation exposes project reassignment.
			completionExec(t, q.db, `UPDATE runs SET project_id=?,number=number+100000 WHERE id=?`, other, run.ID)
		}
		return "caller", nil
	}))
	if _, err := moved.AttachArtifact(context.Background(), run.ID, ArtifactDraft{Kind: "report"}); !errors.Is(err, ErrRunMoved) {
		t.Fatalf("moved scope: %v", err)
	}
	// sql-guarantee: rollback proves no artifact row was written; the artifact reads are scoped to a run and cannot prove a store-wide absence.
	completionCount(t, q.db, 0, `SELECT count(*) FROM artifacts`)
}
