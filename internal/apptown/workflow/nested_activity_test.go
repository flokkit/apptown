package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A run waiting on a called workflow shows the child's own gate verdict, the
// same in the list and the detail view.
func TestANestedRunReadsTheSameInListAndDetail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	spec := schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "outer", Steps: []schema.Step{{Name: "call", Uses: "workflow/call"}}}}}
	db, parent := settlementFixture(t, spec)
	call := insertStoredCall(t, db, parent.ID, "outer", "call", 0, nil)
	childSpec := schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "inner", Steps: []schema.Step{{Name: "scan", Uses: "scan", With: map[string]any{"on": "source"}}}}}}
	child := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: parent.ProjectID, ChangeID: parent.ChangeID, ParentRunID: parent.ID, ParentStepID: call, WorkflowName: "child", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "inner", DefinitionJson: json.RawMessage(mustJSON(childSpec))})
	recordStoredCallEvidence(t, db, call, 0, json.RawMessage(fmt.Sprintf(`{"childRunId":%q,"workflow":"child"}`, child.ID)))
	insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: child.ID, Stage: "inner", Name: "scan:osv", Type: "scan", Status: StatusPass.String()})
	graph, err := readWorkflowActivity(ctx, db, []ids.ID{parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	activity, err := graph.classify(parent.ID, nil, map[ids.ID]bool{})
	if err != nil {
		t.Fatal(err)
	}
	listed := graph.judgedByGate(parent.ID, activity)
	detail, err := readWorkflowSnapshot(ctx, db, parent.ID, DetailView, nil, parent.ProjectID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if listed.State != detail.State || listed.Detail != detail.StateDetail {
		t.Fatalf("same nested run: list=%+v, detail=%s/%s", listed, detail.State, detail.StateDetail)
	}
}

// The parent's activity is the child's, judged by the child's own gate.
func TestANestedRunShowsItsChildsGate(t *testing.T) {
	t.Parallel()
	g := workflowActivityGraph{
		idstest.N(1): {id: idstest.N(1), projectID: idstest.N(1), state: RunRunning, stage: &schema.Stage{Name: "outer", Steps: []schema.Step{{Name: "call", Uses: "workflow/call"}}}, steps: []workflowActivityStep{{id: idstest.N(3), attempt: 1, name: "call", kind: "workflow/call", state: StatusRunning, evidence: json.RawMessage(`{"childRunId":"00000000-0002-7000-8000-000000000000","workflow":"child"}`)}}},
		idstest.N(2): {id: idstest.N(2), projectID: idstest.N(1), parentID: idstest.N(1), parentStepID: idstest.N(3), parentAttempt: 1, state: RunRunning, stage: &schema.Stage{Name: "inner", Steps: []schema.Step{{Name: "scan", Uses: "scan", With: map[string]any{"on": "source"}}}}, steps: []workflowActivityStep{{id: idstest.N(4), name: "scan:osv", kind: "scan", state: StatusPass}}},
	}
	a, err := g.classify(idstest.N(1), nil, map[ids.ID]bool{})
	if err != nil {
		t.Fatal(err)
	}
	got := g.judgedByGate(idstest.N(1), a)
	child, err := g.classify(idstest.N(2), nil, map[ids.ID]bool{})
	if err != nil {
		t.Fatal(err)
	}
	want := g.judgedByGate(idstest.N(2), child)
	if got.State != want.State || got.Detail != want.Detail+" (in child)" {
		t.Fatalf("parent hides child's actual gate refusal: parent=%+v child=%+v", got, want)
	}
}
