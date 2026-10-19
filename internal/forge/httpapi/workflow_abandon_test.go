package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

func TestAbandonmentHTTPReportsFailure(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/runs/%d/abandon", f.RunID), strings.NewReader(`{"reason":"stop"}`)).WithContext(f.Context)
		r.SetPathValue("id", fmt.Sprint(f.RunID))
		w := httptest.NewRecorder()
		NewRuns(f.Work, nil).AbandonRun(w, r)
		return w
	}
	t.Cleanup(f.DB.FailNextStatement("INSERT INTO audit_log"))
	if w := call(); w.Code != 500 {
		t.Fatalf("failed receipt: %d %s", w.Code, w.Body.String())
	}
	if run, err := f.Work.Run(f.Context, f.RunID); err != nil || run.Status != workflow.RunRunning {
		t.Fatalf("failed abandonment changed run: %+v %v", run, err)
	}
	if w := call(); w.Code != 200 {
		t.Fatalf("explicit retry: %d %s", w.Code, w.Body.String())
	}
	if w := call(); w.Code != 409 {
		t.Fatalf("terminal conflict: %d %s", w.Code, w.Body.String())
	}
}

func TestAbandonRunCascades(t *testing.T) {
	t.Parallel()
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "child", Uses: "workflow/call", With: map[string]any{"workflow": "child"}}}}}}
	f := newWorkflowHTTPFixtureWithSpec(t, true, spec, nil, nil)
	// No automatic runners are registered: admit the child explicitly while
	// the service remains live, rather than relying on a refused dispatch.
	snapshot, err := f.Work.Snapshot(f.Context, f.RunID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1 {
		t.Fatalf("parent call step: %+v %v", snapshot, err)
	}
	child, err := f.Work.Call(f.Context, workflow.WorkflowCall{ParentStepID: snapshot.Steps[0].ID, Workflow: "child"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(reason string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/runs/%d/abandon", f.RunID), strings.NewReader(fmt.Sprintf(`{"reason":%q}`, reason))).WithContext(f.Context)
		r.SetPathValue("id", fmt.Sprint(f.RunID))
		w := httptest.NewRecorder()
		NewRuns(f.Work, nil).AbandonRun(w, r)
		return w
	}
	if w := call("testing teardown"); w.Code != 200 {
		t.Fatalf("abandon: %d %s", w.Code, w.Body.String())
	}
	for _, id := range []ids.ID{f.RunID, child.ID} {
		run, err := workflow.ReadRunIn(f.Context, f.DB, id)
		if err != nil || run.Status != workflow.RunAbandoned {
			t.Fatalf("run %d should be abandoned: %+v %v", id, run, err)
		}
	}
	if w := call("again"); w.Code != 409 {
		t.Fatalf("re-abandon: %d %s", w.Code, w.Body.String())
	}
}

func TestAbandonRunHTTPRejectsMalformedInputBeforeStoppingWork(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body)).WithContext(f.Context)
		r.SetPathValue("id", fmt.Sprint(f.RunID))
		w := httptest.NewRecorder()
		NewRuns(f.Work, nil).AbandonRun(w, r)
		return w
	}
	for _, body := range []string{`{"reason":`, `{"reason":42}`} {
		if w := call(body); w.Code != 400 {
			t.Fatalf("malformed abandon: %d %s", w.Code, w.Body.String())
		}
		run, err := f.Work.Run(f.Context, f.RunID)
		if err != nil || run.Status != workflow.RunRunning {
			t.Fatalf("bad request stopped work: %+v %v", run, err)
		}
	}
	// An omitted body still means an intentional stop without an explanation.
	w := call("")
	var out struct {
		OK        bool
		Abandoned []ids.ID
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || !out.OK || len(out.Abandoned) != 1 || out.Abandoned[0] != f.RunID {
		t.Fatalf("empty-body stop: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	run, err := f.Work.Run(f.Context, f.RunID)
	if err != nil || run.Status != workflow.RunAbandoned {
		t.Fatalf("accepted stop did not settle: %+v %v", run, err)
	}
}
