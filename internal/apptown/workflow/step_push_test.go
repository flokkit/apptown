package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A step attempt may push only its own scratch ref, for the change its run
// works on, building on that change's branch and recorded as its run. A run
// that works on no change has no scratch ref, so its steps push nothing.
// The Git transport's refusals are proved in the root (scratch_push_test.go).
func TestAStepMayPushOnlyItsOwnScratchRefOfItsRunsChange(t *testing.T) {
	t.Parallel()
	q, run, step := activityFixture(t)
	work := New(context.Background(), q.db, Dependencies{})
	ctx := context.Background()
	ch, err := ReadChangeIn(ctx, q.db, run.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := work.StepPush(ctx, identity.Workload{ProjectID: run.ProjectID, RunID: run.ID, StepID: step, Attempt: 2})
	want := StepPush{Ref: scm.ScratchRef(run.ChangeID, step, 2), Branch: ch.Branch, Actor: fmt.Sprintf("run/%d", run.ID)}
	if err != nil || got != want {
		t.Fatalf("the step's push: %+v %v, want %+v", got, err, want)
	}

	alone := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, WorkflowName: "test", WorkflowVersion: 1, Kind: "pipeline",
		Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(`{"kind":"pipeline","stages":[{"name":"finish"}]}`)})
	if got, err := work.StepPush(ctx, identity.Workload{ProjectID: run.ProjectID, RunID: alone.ID, StepID: step, Attempt: 1}); err == nil || !strings.Contains(err.Error(), "works on no change") {
		t.Fatalf("a step of a run with no change: %+v %v", got, err)
	}
	// A run named in another project is not read as this one.
	if got, err := work.StepPush(ctx, identity.Workload{ProjectID: idstest.N(999999), RunID: run.ID, StepID: step, Attempt: 1}); err == nil {
		t.Fatalf("a step whose run is in another project: %+v", got)
	}
}
