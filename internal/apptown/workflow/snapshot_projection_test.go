package workflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
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

// Completed expanded checks and dynamic checks read the same gate membership
// as the stage engine; each run reads its own definition.
func TestSnapshotGateMembershipUsesRecordedDeclarations(t *testing.T) {
	t.Parallel()
	for _, explicit := range []bool{true, false} {
		t.Run(map[bool]string{true: "explicit", false: "default all"}[explicit], func(t *testing.T) {
			t.Parallel()
			definition := `{"kind":"pipeline","stages":[{"name":"checks","steps":[{"name":"a","uses":"scan","with":{"on":"source"}},{"name":"b","uses":"script"}]` + map[bool]string{true: `,"gate":{"require":["a"]}`, false: ``}[explicit] + `},{"name":"ship"}]}`
			q, run, _ := activityFixture(t)
			child := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, ChangeID: run.ChangeID, ParentRunID: run.ID, WorkflowName: "child", WorkflowVersion: 1, Kind: "pipeline", Status: RunDone.String(), CurrentStage: "ship", DefinitionJson: []byte(definition)})
			insertRunEventForTest(t, q.db, child.ID, "stage.entered", []byte(`{"stage":"checks","checks":[{"declaration":"a","name":"concrete-scanner","type":"scan"}]}`))
			for _, name := range []string{"concrete-scanner", "b", "dynamic"} {
				kind := "script"
				if name == "concrete-scanner" {
					kind = "scan"
				}
				finishedStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: child.ID, Stage: "checks", Name: name, Type: kind}, StatusPass)
			}
			svc := newService(q.db, allowProject(run.ProjectID), Runtime{})
			snap, err := svc.Snapshot(t.Context(), child.ID, TraceView)
			if err != nil {
				t.Fatal(err)
			}
			for _, step := range snap.Steps {
				if want := explicit && step.Name != "concrete-scanner"; step.Observation != want {
					t.Fatalf("%s observation=%v want %v", step.Name, step.Observation, want)
				}
			}
		})
	}
}
