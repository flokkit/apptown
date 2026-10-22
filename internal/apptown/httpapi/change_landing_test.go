package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestLandChangeHTTPReturnsTheSelectedChangesLanding(t *testing.T) {
	t.Parallel()
	gate := []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}
	f := newWorkflowHTTPFixtureWithDefinitions(t, true, map[string]schema.Spec{
		"":     {Kind: "pipeline", Role: "working", Stages: gate},
		"land": {Kind: "pipeline", Role: "landing", Stages: gate},
	}, nil, nil)
	target, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "Land this change"})
	if err != nil {
		t.Fatal(err)
	}
	h := NewChangeAdmission(f.Projects, f.Access, f.Work, changeactions.New(f.Work, nil, f.DB), f.DB, "open")
	call := func(id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/", nil).WithContext(f.Context)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		h.LandChange(w, r)
		return w
	}
	for _, tc := range []struct {
		id   string
		code int
	}{{"bad", 400}, {"0000000f-423f-7000-8000-000000000000", 404}} {
		if w := call(tc.id); w.Code != tc.code {
			t.Fatalf("bad target: %d %s", w.Code, w.Body.String())
		}
	}
	w := call(fmt.Sprint(target.ID))
	var out struct {
		Change workflow.Change
		Run    workflow.Run
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 201 || out.Change.ID != target.ID || out.Run.ID.IsZero() || out.Run.ChangeID != target.ID || out.Run.ProjectID != f.ProjectID || out.Run.WorkflowName != "land" || out.Run.Status != workflow.RunRunning {
		t.Fatalf("landing response: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	runs, err := f.Work.ChangeRuns(f.Context, target.ID, workflow.ChangeRuns{})
	if err != nil || len(runs) != 1 || runs[0].ID != out.Run.ID {
		t.Fatalf("wrong landing admitted: %+v %v", runs, err)
	}
	other, err := f.Work.ChangeRuns(f.Context, f.ChangeID, workflow.ChangeRuns{})
	if err != nil || len(other) != 1 || other[0].ID != f.RunID {
		t.Fatalf("unselected change altered: %+v %v", other, err)
	}
	saved, err := f.Work.Change(f.Context, target.ID)
	if err != nil || saved.State != workflow.ChangeLanding {
		t.Fatalf("landing not underway: %+v %v", saved, err)
	}
	w = call(fmt.Sprint(target.ID))
	if w.Code != 409 {
		t.Fatalf("duplicate landing: %d %s", w.Code, w.Body.String())
	}
	// Another change of the project waits its turn: the refusal is a
	// conflict with a code the CLI waits on, not a sentence to match.
	waiting, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "Land after it"})
	if err != nil {
		t.Fatal(err)
	}
	if w := call(fmt.Sprint(waiting.ID)); w.Code != 409 || w.Header().Get("X-AppTown-Deny-Code") != "landing-occupied" {
		t.Fatalf("contended landing: %d %q %s", w.Code, w.Header().Get("X-AppTown-Deny-Code"), w.Body.String())
	}
	runs, err = f.Work.ChangeRuns(f.Context, target.ID, workflow.ChangeRuns{})
	if err != nil || len(runs) != 1 {
		t.Fatalf("duplicate admitted more work: %+v %v", runs, err)
	}
}
