package workflow

import (
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
)

func TestLandingStatusMatchesListAndSnapshot(t *testing.T) {
	db, run, _ := completionFixture(t, "landing")
	insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "build", Type: "build", Status: "running"})
	svc := newService(db, allowProject(run.ProjectID), Runtime{})
	t.Cleanup(svc.Stop)
	detail, err := svc.Snapshot(t.Context(), run.ID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	if detail.WaitingOn.Says != "going live" {
		t.Fatalf("snapshot: %+v", detail.WaitingOn)
	}
	rows, err := svc.Runs(t.Context(), RunQuery{ProjectID: run.ProjectID, State: RunRunning, RootsOnly: true})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows: %+v, %v", rows, err)
	}
	if rows[0].WaitingOn.Says != detail.WaitingOn.Says {
		t.Fatalf("list says %q, change page says %q", rows[0].WaitingOn.Says, detail.WaitingOn.Says)
	}
}
