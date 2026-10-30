package workflow

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func TestWorkflowStepReadSurfacesShareSnapshot(t *testing.T) {
	t.Parallel()
	db, run, _ := completionFixture(t, "working")
	runID := run.ID
	if n, err := workflowsql.New(db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), NextStage: run.CurrentStage, NextStatus: RunDone.String(), ID: run.ID, ExpectedStatus: run.Status.String(), CurrentStage: run.CurrentStage, RetryRevision: run.RetryRevision}); err != nil || n != 1 {
		t.Fatalf("finish projected run: %d %v", n, err)
	}
	work := newService(db, allowProject(run.ProjectID), Runtime{})
	empty, err := work.RunSteps(context.Background(), runID)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty read must be an array: %v %v", empty, err)
	}
	for _, stage := range []string{"after", "work"} {
		id := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: runID, Stage: stage, Name: stage, Type: "agent/pi", ConfigJson: json.RawMessage(`{"value":9007199254740993}`), Status: "pending"})
		for range 5 {
			incrementScopeAttempt(t, db, id)
		}
		overrideScopeStep(t, db, workflowsql.OverrideStepParams{At: time.Now(), ID: id, OverrideReason: "reviewed"})
		if n, err := workflowsql.New(db.SQL()).ReopenApproval(context.Background(), workflowsql.ReopenApprovalParams{ID: id, Attempt: 5, EvidenceJson: json.RawMessage(`{"number":9007199254740993}`)}); err != nil || n != 1 {
			t.Fatalf("store pending projection: %d %v", n, err)
		}
	}
	steps, err := work.RunSteps(context.Background(), runID)
	if err != nil || len(steps) != 2 || steps[0].Stage != "after" || steps[0].ID.Compare(steps[1].ID) >= 0 {
		t.Fatalf("read must retain creation order across stages: %+v %v", steps, err)
	}
	detail, err := work.Snapshot(context.Background(), runID, DetailView)
	// Snapshot display knows that the run ended; record readers retain the same stored facts.
	if err == nil {
		for _, step := range detail.Steps {
			if !step.runEnded {
				t.Fatal("ended run lost display context")
			}
			step.runEnded = false
		}
	}
	if err != nil || !reflect.DeepEqual(steps, detail.Steps) {
		t.Fatalf("detail has a different snapshot: %+v %v", detail, err)
	}
	for _, step := range steps {
		point, err := ReadStepIn(context.Background(), db, step.ID)
		if err != nil || !reflect.DeepEqual(point, step) || step.OwnedAttempt() != 0 {
			t.Fatalf("point/read-model snapshot or ownership differs: %+v %v", point, err)
		}
	}
	body, err := json.Marshal(steps)
	if err != nil || !strings.Contains(string(body), `"config":{"value":9007199254740993}`) || !strings.Contains(string(body), `"evidence":{"number":9007199254740993}`) || strings.Contains(strings.ToLower(string(body)), "attempt") {
		t.Fatalf("wire JSON lost precision or exposed ownership: %s %v", body, err)
	}
	dbtest.FailNextStatement(t, db, "RunSteps")
	if d, err := work.Snapshot(context.Background(), runID, DetailView); d != nil || err == nil {
		t.Fatalf("detail concealed its step read failure: %+v %v", d, err)
	}
}
