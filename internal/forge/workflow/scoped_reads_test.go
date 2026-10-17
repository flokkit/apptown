package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

func storedTextArtifact(t *testing.T, db *database.DB, runID ids.ID, kind, name, ref, body string) ids.ID {
	t.Helper()
	id, err := AddArtifactIn(context.Background(), db, runID, kind, name, ref, body, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestScopedEvidenceAndHistoryKeepProjectAndBody(t *testing.T) {
	t.Parallel()
	q, run, step := activityFixture(t)
	ctx := context.Background()
	other := workflowTestProject(t, q.db, "private", "Private", "")
	foreign := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: other, ChangeID: run.ChangeID, WorkflowName: "foreign", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(`{}`)}).ID
	body := "exact\n{\"n\":9007199254740993}\ntrail  "
	first := storedTextArtifact(t, q.db, run.ID, "scan-report", "scanner", "", body)
	storedTextArtifact(t, q.db, foreign, "scan-report", "scanner", "", "secret")
	s := newService(q.db, allowProject(run.ProjectID), Runtime{})
	ref := RunRef{ID: run.ID, ProjectID: run.ProjectID}
	counted := &countedQueries{Handle: q.db.SQL()}
	reports, err := ReadRunArtifactsIn(ctx, counted, ref, ArtifactSelection{Kinds: []string{"scan-report"}, Latest: true})
	if err != nil || len(reports) != 1 || reports[0].ID != first || reports[0].Body != body || counted.reads != 1 {
		t.Fatalf("reports %+v reads %d: %v", reports, counted.reads, err)
	}
	changeReports, err := s.ChangeArtifacts(ctx, run.ChangeID, ArtifactSelection{})
	if err != nil || len(changeReports) != 1 || changeReports[0].Body != body {
		t.Fatalf("Change reports %+v: %v", changeReports, err)
	}
	history, err := s.ChangeRuns(ctx, run.ChangeID, ChangeRuns{RootsOnly: true})
	if err != nil || len(history) != 1 || *history[0] != *run {
		t.Fatalf("history %+v: %v", history, err)
	}
	call := insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "child", Type: "workflow/call", Status: "running"})
	child := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, ChangeID: run.ChangeID, ParentRunID: run.ID, ParentStepID: call, WorkflowName: "child", WorkflowVersion: 1, Kind: "pipeline", Status: "running", CurrentStage: "finish", DefinitionJson: json.RawMessage(`{}`)})
	recordStoredCallEvidence(t, q.db, call, 0, json.RawMessage(fmt.Sprintf(`{"childRunId":%q,"workflow":"child"}`, child.ID)))
	history, err = s.ChangeRuns(ctx, run.ChangeID, ChangeRuns{RootsOnly: true, Limit: 1})
	if err != nil || len(history) != 1 || history[0].ID == child.ID {
		t.Fatalf("root history %+v: %v", history, err)
	}
	observed, err := s.Step(ctx, step)
	if err != nil || observed.Run.ID != run.ID || observed.Step.ID != step || observed.Step.OwnedAttempt() != 0 {
		t.Fatalf("Step %+v: %v", observed, err)
	}
	steps, err := s.RunSteps(ctx, run.ID)
	if err != nil || len(steps) != 2 {
		t.Fatalf("Steps %+v: %v", steps, err)
	}
	if _, err := s.RunArtifacts(ctx, foreign, ArtifactSelection{}); err == nil {
		t.Fatal("foreign Run admitted")
	}
	for _, ref := range []RunRef{{ID: run.ID}, {ProjectID: run.ProjectID}} {
		if _, err := ReadRunArtifactsIn(ctx, q.db, ref, ArtifactSelection{}); err == nil {
			t.Fatal("unscoped artifact read")
		}
	}
	dbtest.FailNextStatement(t, q.db, "SELECT a.id,a.run_id")
	if _, err := s.RunArtifacts(ctx, run.ID, ArtifactSelection{}); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("hidden evidence failure: %v", err)
	}
}

func TestAcceptedScanReportsExcludeInFlightAndForeignRuns(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	ctx := context.Background()
	if n, err := workflowsql.New(q.db.SQL()).TransitionRun(ctx, workflowsql.TransitionRunParams{At: time.Now(), NextStage: run.CurrentStage, NextStatus: RunDone.String(), ID: run.ID, ExpectedStatus: run.Status.String(), CurrentStage: run.CurrentStage, RetryRevision: run.RetryRevision}); err != nil || n != 1 {
		t.Fatalf("finish accepted report run: %d %v", n, err)
	}
	storedTextArtifact(t, q.db, run.ID, "scan-report", "a", "", "old")
	latest := storedTextArtifact(t, q.db, run.ID, "scan-report", "a", "", "accepted")
	active := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, WorkflowName: "active", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "work", DefinitionJson: json.RawMessage(`{}`)}).ID
	storedTextArtifact(t, q.db, active, "scan-report", "a", "", "in-flight")
	s := newService(q.db, allowProject(run.ProjectID), Runtime{})
	reports, err := s.AcceptedScanReports(ctx, run.ProjectID)
	if err != nil || len(reports) != 1 || reports[0].ID != latest {
		t.Fatalf("accepted %+v: %v", reports, err)
	}
	if _, err := s.AcceptedScanReports(ctx, ids.Nil); err == nil {
		t.Fatal("unscoped posture")
	}
	if n, err := workflowsql.New(q.db.SQL()).TransitionRun(ctx, workflowsql.TransitionRunParams{At: time.Now(), NextStage: run.CurrentStage, NextStatus: RunAbandoned.String(), ID: run.ID, ExpectedStatus: RunDone.String(), CurrentStage: run.CurrentStage, RetryRevision: run.RetryRevision}); err != nil || n != 1 {
		t.Fatalf("abandon accepted report run: %d %v", n, err)
	}
	reports, err = s.AcceptedScanReports(ctx, run.ProjectID)
	if err != nil || reports == nil || len(reports) != 0 {
		t.Fatalf("abandoned %+v: %v", reports, err)
	}
}
