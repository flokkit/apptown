package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
)

func TestWorkflowRetryHTTPRejectsInvalidJSONAndReportsPending(t *testing.T) {
	t.Parallel()
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "review", Uses: "agent/review"}}}}}
	executed := make(chan *workflow.Step, 4)
	runner := func(task *workflow.Execution) {
		step, err := task.Claim()
		if err != nil {
			t.Error(err)
			return
		}
		executed <- step
		if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusFail}, nil); err != nil {
			t.Error(err)
		}
	}
	f := newWorkflowHTTPFixtureWithSpec(t, true, spec, []string{"agent/review"}, map[string]workflow.Runner{"agent/review": runner})
	f.Work.Wait()
	snapshot, err := f.Work.Snapshot(f.Context, f.RunID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1 || snapshot.Steps[0].Status != workflow.StatusFail {
		t.Fatalf("failed review fixture: %+v %v", snapshot, err)
	}
	stepID := snapshot.Steps[0].ID
	select {
	case <-executed:
	default:
		t.Fatal("fixture review never executed")
	}
	for _, body := range []string{`{"mode":`, `{"mode":"unknown"}`, `{"feedback":42}`, `{"feedback":"Feedback belongs on the implementing agent."}`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body)).WithContext(f.Context)
		r.SetPathValue("id", fmt.Sprint(stepID))
		resp := httptest.NewRecorder()
		NewRuns(f.Work).StepRun(resp, r)
		if resp.Code != 400 {
			t.Fatalf("invalid retry: %d %s", resp.Code, resp.Body.String())
		}
	}
	f.Work.Wait()
	select {
	case step := <-executed:
		t.Fatalf("invalid retry executed: %+v", step)
	default:
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`)).WithContext(f.Context)
	r.SetPathValue("id", fmt.Sprint(stepID))
	resp := httptest.NewRecorder()
	NewRuns(f.Work).StepRun(resp, r)
	if resp.Code != 202 || !strings.Contains(resp.Body.String(), `"pending"`) {
		t.Fatalf("accepted response: %d %s", resp.Code, resp.Body.String())
	}
	f.Work.Wait()
	select {
	case step := <-executed:
		if step.ID != stepID || step.RunID != f.RunID {
			t.Fatalf("retry targeted wrong work: %+v", step)
		}
	default:
		t.Fatal("accepted retry never reached its runner")
	}
}

func TestStepRunHTTPReturnsEvaluatedAssertionAndRefusesApproval(t *testing.T) {
	t.Parallel()
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{
		{Name: "proof", Uses: "artifact/require", With: map[string]any{"kind": "report"}},
		{Name: "accept", Uses: "approval"},
	}}}}
	f := newWorkflowHTTPFixtureWithSpec(t, true, spec, nil, nil)
	if _, err := workflow.AddArtifactIn(f.Context, f.DB, f.RunID, "report", "report", "", "{}", time.Now()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.Work.Snapshot(f.Context, f.RunID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 2 {
		t.Fatalf("fixture steps: %+v %v", snapshot, err)
	}
	for _, step := range snapshot.Steps {
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`)).WithContext(f.Context)
		r.SetPathValue("id", fmt.Sprint(step.ID))
		out := httptest.NewRecorder()
		NewRuns(f.Work).StepRun(out, r)
		if step.Type == "approval" {
			if out.Code != 400 {
				t.Fatalf("approval execution: %d %s", out.Code, out.Body.String())
			}
			continue
		}
		var updated workflow.Step
		if err := json.Unmarshal(out.Body.Bytes(), &updated); err != nil || out.Code != 200 || updated.ID != step.ID || updated.Status != workflow.StatusPass {
			t.Fatalf("evaluated assertion: %d %s %v", out.Code, out.Body.String(), err)
		}
	}
	f.Work.Wait()
}
