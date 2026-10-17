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

func TestHandleApprovalBriefCaches(t *testing.T) {
	t.Parallel()
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"means": "you are accepting the visual design"}}}}}}
	f := newWorkflowHTTPFixtureWithSpec(t, true, spec, nil, nil)
	snapshot, err := f.Work.Snapshot(f.Context, f.RunID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1 {
		t.Fatalf("admitted approval: %+v %v", snapshot, err)
	}
	stepID := snapshot.Steps[0].ID
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/steps/%d/brief", stepID), nil).WithContext(f.Context)
		r.SetPathValue("id", fmt.Sprint(stepID))
		w := httptest.NewRecorder()
		NewApprovalBriefs(f.Work, noBriefModel{}, nil, f.DB, t.TempDir()).ApprovalBrief(w, r)
		return w
	}
	first := call()
	if first.Code != 200 {
		t.Fatalf("brief: %d %s", first.Code, first.Body.String())
	}
	var body1 struct {
		Brief string `json:"brief"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &body1); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body1.Brief, "WHAT YOUR OK MEANS HERE") {
		t.Fatalf("brief body missing the means line:\n%s", body1.Brief)
	}
	cached, err := f.Work.RunArtifacts(f.Context, f.RunID, workflow.ArtifactSelection{Kinds: []string{"approval-brief"}})
	if err != nil || len(cached) != 1 || cached[0].Body != body1.Brief {
		t.Fatalf("saved brief: %+v %v", cached, err)
	}
	second := call()
	if second.Code != 200 {
		t.Fatalf("cached brief: %d %s", second.Code, second.Body.String())
	}
	var body2 struct {
		Brief string `json:"brief"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &body2); err != nil {
		t.Fatal(err)
	}
	if body2.Brief != body1.Brief {
		t.Fatalf("cached brief differs:\n%q\nvs\n%q", body2.Brief, body1.Brief)
	}
	again, err := f.Work.RunArtifacts(f.Context, f.RunID, workflow.ArtifactSelection{Kinds: []string{"approval-brief"}})
	if err != nil || len(again) != 1 || again[0].ID != cached[0].ID || again[0].Body != body1.Brief {
		t.Fatalf("cache hit changed the saved artifact: %+v %v", again, err)
	}
}

func TestHandleApprovalBriefRejectsNonApproval(t *testing.T) {
	t.Parallel()
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "proof", Uses: "artifact/require", With: map[string]any{"kind": "report"}}, {Name: "accept", Uses: "approval"}}}}}
	f := newWorkflowHTTPFixtureWithSpec(t, true, spec, nil, nil)
	snapshot, err := f.Work.Snapshot(f.Context, f.RunID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 2 {
		t.Fatalf("fixture steps: %+v %v", snapshot, err)
	}
	var proofID ids.ID
	for _, step := range snapshot.Steps {
		if step.Type != "approval" {
			proofID = step.ID
		}
	}
	if proofID.IsZero() {
		t.Fatal("fixture has no non-approval step")
	}
	r := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/steps/%d/brief", proofID), nil).WithContext(f.Context)
	r.SetPathValue("id", fmt.Sprint(proofID))
	w := httptest.NewRecorder()
	NewApprovalBriefs(f.Work, noBriefModel{}, nil, f.DB, t.TempDir()).ApprovalBrief(w, r)
	if w.Code != 404 {
		t.Fatalf("a non-approval step must 404: %d %s", w.Code, w.Body.String())
	}
}
