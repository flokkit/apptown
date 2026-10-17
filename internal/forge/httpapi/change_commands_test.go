package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/workflow"
)

func TestStartChangeRunHTTPValidatesBeforeAdmission(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	grantWorkflowHTTPAuthority(t, f)
	change, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "Ready to run"})
	if err != nil {
		t.Fatal(err)
	}
	h := NewChanges(f.Work, f.Projects, f.Access, f.DB, nil)
	outsider := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "outsider", Via: "session"}, Verified: true})
	for _, tc := range []struct {
		name, id, body string
		ctx            context.Context
		status         int
	}{
		{"malformed JSON", fmt.Sprint(change.ID), `{"workflow":`, f.Context, 400},
		{"missing workflow", fmt.Sprint(change.ID), `{}`, f.Context, 400},
		{"wrong field type", fmt.Sprint(change.ID), `{"workflow":12}`, f.Context, 400},
		{"malformed ID", "oops", `{"workflow":"test"}`, f.Context, 400},
		{"missing change", "0000000f-423f-7000-8000-000000000000", `{"workflow":"test"}`, f.Context, 404},
		{"missing caller", fmt.Sprint(change.ID), `{"workflow":"test"}`, context.Background(), 401},
		{"other project member", fmt.Sprint(change.ID), `{"workflow":"test"}`, outsider, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", strings.NewReader(tc.body)).WithContext(tc.ctx)
			r.SetPathValue("id", tc.id)
			w := httptest.NewRecorder()
			h.StartChangeRun(w, r)
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != tc.status || body.Error == "" {
				t.Fatalf("HTTP %d: %s (%v)", w.Code, w.Body.String(), err)
			}
			runs, err := f.Work.ChangeRuns(f.Context, change.ID, workflow.ChangeRuns{})
			if err != nil || len(runs) != 0 {
				t.Fatalf("rejected request admitted a run: %+v %v", runs, err)
			}
		})
	}

	caller, _ := access.CallerFrom(f.Context)
	caller.Receipt = "bls"
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"workflow":"chosen-lane"}`)).WithContext(access.WithCaller(f.Context, caller))
	r.SetPathValue("id", fmt.Sprint(change.ID))
	r.Header.Set("X-Forge-Actor", "forged-attribution")
	w := httptest.NewRecorder()
	h.StartChangeRun(w, r)
	var out struct {
		Change workflow.Change `json:"change"`
		Run    workflow.Run    `json:"run"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 201 {
		t.Fatalf("start HTTP %d: %s (%v)", w.Code, w.Body.String(), err)
	}
	if out.Change.ID != change.ID || out.Run.ID.IsZero() || out.Run.ChangeID != change.ID || out.Run.ProjectID != f.ProjectID || out.Run.WorkflowName != "chosen-lane" {
		t.Fatalf("wrong admission response: %+v", out)
	}
	runs, err := f.Work.ChangeRuns(f.Context, change.ID, workflow.ChangeRuns{})
	if err != nil || len(runs) != 1 || runs[0].ID != out.Run.ID {
		t.Fatalf("response did not identify the admitted run: %+v %v", runs, err)
	}
	receipts, err := audit.Recent(f.Context, f.DB, audit.Filter{ProjectID: f.ProjectID, KindPrefix: "change.run-started"})
	if err != nil || len(receipts) != 1 || receipts[0].Actor != "bls" {
		t.Fatalf("transport trusted an attribution header: %+v %v", receipts, err)
	}
}

func TestStartChangeHTTPPreservesScopeAndReportsLifecycleConflict(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	grantWorkflowHTTPAuthority(t, f)
	change, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "Proposal", Propose: true})
	if err != nil {
		t.Fatal(err)
	}
	h := NewChanges(f.Work, f.Projects, f.Access, f.DB, nil)
	call := func(ctx context.Context) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
		r.SetPathValue("id", fmt.Sprint(change.ID))
		w := httptest.NewRecorder()
		h.StartChange(w, r)
		return w
	}
	outsider := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "outsider", Via: "session"}, Verified: true})
	if w := call(outsider); w.Code != 403 {
		t.Fatalf("foreign project member: %d %s", w.Code, w.Body.String())
	}
	stored, err := f.Work.Change(f.Context, change.ID)
	if err != nil || stored.State != workflow.ChangeProposed {
		t.Fatalf("denial promoted proposal: %+v %v", stored, err)
	}
	w := call(f.Context)
	var out struct {
		Change workflow.Change `json:"change"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.Change.ID != change.ID || out.Change.State != workflow.ChangeInProgress {
		t.Fatalf("promotion response: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	if _, err := f.Work.Abandon(f.Context, workflow.StopWork{ChangeID: change.ID, Reason: "withdrawn"}); err != nil {
		t.Fatal(err)
	}
	if w := call(f.Context); w.Code != 409 {
		t.Fatalf("terminal change restart: %d %s", w.Code, w.Body.String())
	}
	stored, err = f.Work.Change(f.Context, change.ID)
	if err != nil || stored.State != workflow.ChangeAbandoned {
		t.Fatalf("conflict changed terminal state: %+v %v", stored, err)
	}
}
