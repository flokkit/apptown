package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/briefs"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
)

func TestHandleApprovalBriefWithoutModelIsEmptyAndNotCached(t *testing.T) {
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
		NewApprovalBriefs(f.Work, briefs.New(f.Work, noBriefModel{}, nil, nil, nil)).ApprovalBrief(w, r)
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
	if body1.Brief != "" {
		t.Fatalf("expected no brief:\n%s", body1.Brief)
	}
	cached, err := f.Work.RunArtifacts(f.Context, f.RunID, workflow.ArtifactSelection{Kinds: []string{"approval-brief"}})
	if err != nil || len(cached) != 0 {
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
	if err != nil || len(again) != 0 {
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
	NewApprovalBriefs(f.Work, briefs.New(f.Work, noBriefModel{}, nil, nil, nil)).ApprovalBrief(w, r)
	if w.Code != 404 {
		t.Fatalf("a non-approval step must 404: %d %s", w.Code, w.Body.String())
	}
}

type noBriefModel struct{ err error }

func (m noBriefModel) Resolve(context.Context, ids.ID, string) (string, string, string, error) {
	return "", "", "", m.err
}

// The HTTP response keeps absent advice explicit and persists successful prose
// under the held attempt's key through the workflow owner.
func TestHeldCheckAdviceResponseAndArtifact(t *testing.T) {
	t.Parallel()
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "proof", Uses: "artifact/require", With: map[string]any{"kind": "report"}}, {Name: "accept", Uses: "approval"}}}}}
	f := newWorkflowHTTPFixtureWithSpec(t, true, spec, nil, nil)
	if err := f.Work.RefreshAssertions(f.Context, f.RunID); err != nil {
		t.Fatal(err)
	}
	d, err := f.Work.Snapshot(f.Context, f.RunID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	if d.WaitingOn.Kind != "check" {
		t.Fatalf("not held: %+v", d.WaitingOn)
	}
	call := func(service *briefs.Service) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/steps/"+d.WaitingOn.Step.String()+"/advice", nil).WithContext(f.Context)
		r.SetPathValue("id", d.WaitingOn.Step.String())
		w := httptest.NewRecorder()
		NewApprovalBriefs(f.Work, service).FailureAdvice(w, r)
		return w
	}
	absent := call(briefs.New(f.Work, noBriefModel{}, nil, nil, nil))
	if absent.Code != 200 || strings.TrimSpace(absent.Body.String()) != `{"text":"","recommend":""}` {
		t.Fatalf("absent: %d %s", absent.Code, absent.Body.String())
	}
	c := &adviceCompleter{}
	service := briefs.New(f.Work, adviceModel{}, c, nil, nil)
	for range 2 {
		w := call(service)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"recommend":"retry"`) {
			t.Fatalf("advice: %d %s", w.Code, w.Body.String())
		}
	}
	artifacts, err := f.Work.RunArtifacts(f.Context, f.RunID, workflow.ArtifactSelection{Kinds: []string{"failure-advice"}})
	if err != nil || len(artifacts) != 1 || artifacts[0].Name != fmt.Sprintf("step-%s-attempt-%d", d.Steps[0].ID, d.Steps[0].Attempt) || c.calls != 1 {
		t.Fatalf("artifacts %+v calls %d: %v", artifacts, c.calls, err)
	}
}

type adviceModel struct{}

func (adviceModel) Resolve(_ context.Context, _ ids.ID, slot string) (string, string, string, error) {
	if slot != "main-strong" {
		return "", "", "", fmt.Errorf("unexpected slot %s", slot)
	}
	return "strong", slot, "", nil
}

type adviceCompleter struct{ calls int }

func (c *adviceCompleter) Ask(_ context.Context, call modelcall.Call) (string, *modelcall.Usage, error) {
	c.calls++
	return "retry\nThe required report is missing. Check again after it is attached.", nil, nil
}
