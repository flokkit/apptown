package workflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

func snapshotProjectionFixture(t *testing.T) (*Service, ids.ID) {
	t.Helper()
	db := testDB(t)
	project := workflowTestProject(t, db, "snapshot-projection", "Snapshot projection", "")
	spec := schema.Spec{Kind: "lifecycle", Stages: []schema.Stage{
		{Name: "work", Steps: []schema.Step{{Name: "do", Uses: "agent/pi"}, {Name: "ok", Uses: "approval"}}},
		{Name: "after", Steps: []schema.Step{{Name: "done", Uses: "approval"}}},
	}}
	runID := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: project, WorkflowName: "build", WorkflowVersion: 1, Kind: "lifecycle", CurrentStage: "work", DefinitionJson: []byte(mustJSON(spec)), Status: RunRunning.String()}).ID
	return newService(db, allowProject(project), Runtime{}), runID
}

func TestWorkflowSnapshotAssemblesDetailProjection(t *testing.T) {
	t.Parallel()
	work, runID := snapshotProjectionFixture(t)
	insertStoredStep(t, work.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: runID, Stage: "work", Name: "do", Type: "agent/pi", Status: StatusPass.String(), ConfigJson: []byte(`{"m":1}`)})
	insertStoredStep(t, work.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: runID, Stage: "work", Name: "ok", Type: "approval", Status: StatusPending.String()})
	insertRunEventForTest(t, work.db, runID, "stage.entered", nil)
	insertTextArtifactForTest(t, work.db, runID, "scan", "report", "", strings.Repeat("x", 20000), time.Time{})

	detail, err := work.Snapshot(context.Background(), runID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Stages) != 2 || detail.Stages[0] != "work" || detail.Stages[1] != "after" {
		t.Fatalf("stages from definition: %v", detail.Stages)
	}
	if len(detail.Steps) != 2 || detail.Steps[0].Name != "do" || detail.Steps[1].Name != "ok" {
		t.Fatalf("steps retain creation order: %+v", detail.Steps)
	}
	if detail.State != "waiting on you" {
		t.Fatalf("pending approval state: %q", detail.State)
	}
	if len(detail.Events) != 1 || detail.Events[0].Kind != "stage.entered" {
		t.Fatalf("events: %+v", detail.Events)
	}
	if len(detail.Artifacts) != 1 {
		t.Fatalf("artifacts: %+v", detail.Artifacts)
	}
	artifact := detail.Artifacts[0]
	if len(artifact.Body) != 16384 || artifact.BodyBytes != 20000 {
		t.Fatalf("artifact preview length=%d stored bytes=%d", len(artifact.Body), artifact.BodyBytes)
	}
}

func TestWorkflowSnapshotOmitsDerivedStateForDoneRun(t *testing.T) {
	t.Parallel()
	work, runID := snapshotProjectionFixture(t)
	transitionStoredRunForTest(t, work.db, runID, "", RunDone)
	detail, err := work.Snapshot(context.Background(), runID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != "" || detail.StateDetail != "" {
		t.Fatalf("finished run derived state=%q detail=%q", detail.State, detail.StateDetail)
	}
}
