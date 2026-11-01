package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func TestApprovalBriefSnapshotAndCacheInvalidation(t *testing.T) {
	t.Parallel()
	q, run, approval := activityFixture(t)
	ctx := context.Background()
	worker := insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "build", Type: "build", Status: StatusPending.String(), ConfigJson: json.RawMessage(`{}`)})
	wq := workflowsql.New(q.db.SQL())
	if n, err := wq.StartAttempt(ctx, workflowsql.StartAttemptParams{At: time.Now(), ID: worker}); err != nil || n != 1 {
		t.Fatalf("start historical worker: %d %v", n, err)
	}
	if n, err := wq.FinishAttempt(ctx, workflowsql.FinishAttemptParams{At: time.Now(), Status: StatusPass.String(), EvidenceJson: json.RawMessage(`{}`), ID: worker, Attempt: 1}); err != nil || n != 1 {
		t.Fatalf("finish historical worker: %d %v", n, err)
	}
	// sql-guarantee: The cache fixture requires worker.finished_at before the fixed Jan 2 artifact; FinishAttempt and RecordStepEvaluation stamp database now(), with no historical clock parameter.
	completionExec(t, q.db, `UPDATE steps SET finished_at='2026-01-01T00:00:00Z' WHERE id=$1`, worker)
	created, err := time.Parse(time.RFC3339, "2026-01-02T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workflowsql.New(q.db.SQL()).InsertTextArtifact(ctx, workflowsql.InsertTextArtifactParams{ID: ids.New(created), RunID: run.ID, Kind: "approval-brief", Name: fmt.Sprintf("step-%d", approval), Body: "cached", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	s := newService(q.db, allowProject(run.ProjectID), Runtime{})
	first, err := s.ApprovalBrief(ctx, approval)
	if err != nil || first.Cached != "cached" {
		t.Fatalf("cache %+v: %v", first, err)
	}
	tx, err := q.db.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := readRun(ctx, tx, run.ID); err != nil {
		t.Fatal(err)
	} // establish the older snapshot
	// sql-guarantee: Advance only worker.finished_at past the Jan 2 cache after the read snapshot is established; generated finish mutations cannot supply this exact historical timestamp.
	completionExec(t, q.db, `UPDATE steps SET finished_at='2026-01-03T00:00:00Z' WHERE id=$1`, worker)
	old, err := readApprovalBriefIn(ctx, tx, RunRef{ID: run.ID, ProjectID: run.ProjectID}, approval)
	if err != nil || old.Cached != "cached" {
		t.Fatalf("mixed snapshot %+v: %v", old, err)
	}
	fresh, err := s.ApprovalBrief(ctx, approval)
	if err != nil || fresh.Cached != "" || fresh.Change == nil || fresh.Change.ID != run.ChangeID || len(fresh.Steps) != 2 {
		t.Fatalf("fresh %+v: %v", fresh, err)
	}
	if _, err := s.ApprovalBrief(ctx, worker); err == nil {
		t.Fatal("non-approval brief admitted")
	}
	dbtest.FailNextStatement(t, q.db, "SELECT a.id,a.run_id")
	if _, err := s.ApprovalBrief(ctx, approval); !errors.Is(err, dbtest.ErrInjectedFailure) {
		t.Fatalf("hidden cache failure: %v", err)
	}
}

// finishedStoredStep is a step of the run that ran and settled with status.
func finishedStoredStep(t *testing.T, db *database.DB, in workflowsql.InsertStageStepParams, status StepState) ids.ID {
	t.Helper()
	in.Status = StatusPending.String()
	id := insertStoredStep(t, db, in)
	q := workflowsql.New(db.SQL())
	if n, err := q.StartAttempt(context.Background(), workflowsql.StartAttemptParams{At: time.Now(), ID: id}); err != nil || n != 1 {
		t.Fatalf("start %s: %d %v", in.Name, n, err)
	}
	if n, err := q.FinishAttempt(context.Background(), workflowsql.FinishAttemptParams{At: time.Now(), Status: status.String(), EvidenceJson: json.RawMessage(`{}`), ID: id, Attempt: 1}); err != nil || n != 1 {
		t.Fatalf("finish %s: %d %v", in.Name, n, err)
	}
	return id
}

// An approval's brief stands on what its earlier stages passed, not only
// its own stage's steps, and shows nothing of a stage not reached. Its
// implementer's report is the one a review step of the change declares it
// audits, from any run of the change, never the newest artifact whose kind
// happens to end in "report" (a scanner's).
func TestTheApprovalBriefReadsEarlierStagesAndTheDeclaredReport(t *testing.T) {
	t.Parallel()
	q, landing, approval := activityFixture(t)
	ctx := context.Background()
	working := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: landing.ProjectID, ChangeID: landing.ChangeID, WorkflowName: "change-request", WorkflowVersion: 1, Kind: "pipeline", Status: RunDone.String(), CurrentStage: "work", DefinitionJson: json.RawMessage(`{"kind":"pipeline","role":"working","stages":[{"name":"work"}]}`)})
	finishedStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: working.ID, Stage: "work", Name: "review-change", Type: "agent/review", ConfigJson: json.RawMessage(`{"of":"change-report"}`)}, StatusPass)
	storedTextArtifact(t, q.db, working.ID, "change-report", "change-report", "", "the implementer's claim")
	finishedStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: landing.ID, Stage: "checks", Name: "guidance-conformance", Type: "guidance/conformance"}, StatusPass)
	insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: landing.ID, Stage: "after", Name: "smoke", Type: "tests", Status: StatusPending.String()})
	storedTextArtifact(t, q.db, landing.ID, "scan-report", "trivy", "", "CVE-2026-0001")

	s := newService(q.db, allowProject(landing.ProjectID), Runtime{})
	brief, err := s.ApprovalBrief(ctx, approval)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, step := range brief.Steps {
		names = append(names, step.Name)
	}
	if got := fmt.Sprint(names); got != "[accept guidance-conformance]" {
		t.Fatalf("the brief's steps: %s", got)
	}
	if brief.Report != "the implementer's claim" {
		t.Fatalf("the brief's implementer's report: %q", brief.Report)
	}
}
