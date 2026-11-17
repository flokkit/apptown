package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
)

func TestAdvanceKeepsFailedGatingCheckFailed(t *testing.T) {
	q, parent, _ := activityFixture(t)
	run := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: parent.ProjectID, WorkflowName: "review", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "review", DefinitionJson: []byte(`{"kind":"pipeline","stages":[{"name":"review","steps":[{"name":"check","uses":"script"}]}]}`)})
	storedReadyStart(t, q.db, run.ID)
	// sql-guarantee: seed a settled executor outcome without constructing an execution runtime; the guarantee is advancement's serialized progress.
	finishedStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "review", Name: "check", Type: "script"}, StatusFail)
	checks := advancementScopeChecks{refresh: func(context.Context, *Run) error { return nil }}
	owner := newAdvancer(q.db, advanceRuntime{checks: checks})
	service := newService(q.db, allowProject(run.ProjectID), Runtime{Advancement: owner, Starter: newStarter(q.db, stages{}, nil, startRuntime{}, clocked{})})
	snapshot, err := service.Snapshot(t.Context(), run.ID, TraceView)
	if err != nil || len(snapshot.Steps) != 1 || snapshot.Steps[0].Observation {
		t.Fatalf("fixture: %+v %v", snapshot, err)
	}
	out, err := service.Advance(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Blocking []struct {
			Observation    bool
			Progress       string
			CheckAttention bool
		}
	}
	if err := json.Unmarshal(raw, &wire); err != nil || len(wire.Blocking) != 1 {
		t.Fatalf("advance: %s %v", raw, err)
	}
	if s := wire.Blocking[0]; s.Observation || s.Progress != "failed" || !s.CheckAttention {
		t.Fatalf("failed gate serialized as %+v; body: %s", s, raw)
	}
}
