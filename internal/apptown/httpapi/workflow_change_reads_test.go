package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
)

func workflowChangeRead(t *testing.T, f workflowHTTPFixture, id string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/v1/changes/"+id, nil).WithContext(f.Context)
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	NewChangeDetail(f.Work, adrs.New(f.DB, access.Checked{Authority: identity.Scoped(f.Access, "adrs"), Projects: f.Projects}, nil, nil), deployment.New(r.Context(), f.DB, deployment.Dependencies{}), f.DB, usage.New(f.DB), t.TempDir()).GetChange(w, r)
	return w
}

func TestHandleGetChangeWithRunAnd404(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	w := workflowChangeRead(t, f, fmt.Sprint(f.ChangeID))
	if w.Code != 200 {
		t.Fatalf("get change: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Change struct {
			ID    ids.ID `json:"id"`
			Title string `json:"title"`
		} `json:"change"`
		ProjectSlug string `json:"projectSlug"`
		Detail      *struct {
			Run struct {
				ID     ids.ID `json:"id"`
				Number int64  `json:"number"`
			} `json:"run"`
		} `json:"detail"`
		// Each run is named by its number, the run detail's own.
		Runs []struct {
			ID     ids.ID `json:"id"`
			Number int64  `json:"number"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Change.ID != f.ChangeID || got.Change.Title != "original" || got.ProjectSlug != "shop" || got.Detail == nil || got.Detail.Run.ID != f.RunID || len(got.Runs) != 1 || got.Runs[0].ID != f.RunID || got.Runs[0].Number != got.Detail.Run.Number || got.Runs[0].Number <= 0 {
		t.Fatalf("change detail projection: %+v", got)
	}
	if nw := workflowChangeRead(t, f, "0000000f-423f-7000-8000-000000000000"); nw.Code != 404 {
		t.Fatalf("unknown change: %d", nw.Code)
	}
	if bw := workflowChangeRead(t, f, "not-a-number"); bw.Code != 400 {
		t.Fatalf("malformed id: %d", bw.Code)
	}
}

func TestHandleGetChangeNoRun(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	w := workflowChangeRead(t, f, fmt.Sprint(f.ChangeID))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"detail":null`) || !strings.Contains(w.Body.String(), `"runs":[]`) {
		t.Fatalf("no-run payload: %d %s", w.Code, w.Body.String())
	}
	// Work's change acts ride on the page: an idle change in progress lands.
	var view struct{ Next []workflow.NextAct }
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || len(view.Next) != 1 || view.Next[0].Act != workflow.NextLand {
		t.Fatalf("change acts: %+v %v", view.Next, err)
	}
}

func TestHandleListChanges(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	second, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "second"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(slug string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/projects/"+slug+"/changes", nil).WithContext(f.Context)
		r.SetPathValue("slug", slug)
		w := httptest.NewRecorder()
		NewChanges(f.Work, f.Projects, f.Access, f.DB, nil).ListChanges(w, r)
		return w
	}
	w := call("shop")
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var items []struct {
		ID           ids.ID `json:"id"`
		Title        string `json:"title"`
		WorkflowName string `json:"workflowName"`
		Stage        string `json:"stage"`
		RunStatus    string `json:"runStatus"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != second.ID || items[1].ID != f.ChangeID || items[1].WorkflowName != "test" || items[1].Stage != "review" || items[1].RunStatus != "running" || items[0].RunStatus != "" || items[0].WorkflowName != "" || items[0].Stage != "" {
		t.Fatalf("run and no-run ordering/projection: %+v", items)
	}
	if nw := call("ghost"); nw.Code != 404 {
		t.Fatalf("unknown project: %d", nw.Code)
	}
}

func TestHandlePatchChange(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	call := func(id, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PATCH", "/api/v1/changes/"+id, strings.NewReader(body)).WithContext(f.Context)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		NewChanges(f.Work, f.Projects, f.Access, f.DB, nil).PatchChange(w, r)
		return w
	}
	w := call(fmt.Sprint(f.ChangeID), `{"expectedEditRevision":0,"title":"  Human's Words  ","body":"why it matters","externalRef":"JIRA-7"}`)
	if w.Code != 200 {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	var out workflow.Change
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Title != "Human's Words" || out.Body != "why it matters" || out.ExternalRef != "JIRA-7" || out.EditRevision != 1 {
		t.Fatalf("patch projection: %+v", out)
	}
	// An edit names the reading it started from, and that reading must be current.
	if mw := call(fmt.Sprint(f.ChangeID), `{"body":"x"}`); mw.Code != 400 {
		t.Fatalf("edit without a revision: %d %s", mw.Code, mw.Body.String())
	}
	if mw := call(fmt.Sprint(f.ChangeID), `{"expectedEditRevision":0,"body":"x"}`); mw.Code != 409 || mw.Header().Get("X-AppTown-Deny-Code") != "change-edit-moved" {
		t.Fatalf("stale edit: %d %q %s", mw.Code, mw.Header().Get("X-AppTown-Deny-Code"), mw.Body.String())
	}
	w = call(fmt.Sprint(f.ChangeID), `{"expectedEditRevision":1,"body":""}`)
	if w.Code != 200 {
		t.Fatalf("clear body: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Body != "" || out.Title != "Human's Words" || out.ExternalRef != "JIRA-7" {
		t.Fatalf("omitted vs empty: %+v", out)
	}
	if bw := call(fmt.Sprint(f.ChangeID), "{not json"); bw.Code != 400 {
		t.Fatalf("bad json: %d", bw.Code)
	}
	if nw := call("0000000f-423f-7000-8000-000000000000", "{}"); nw.Code != 404 {
		t.Fatalf("unknown change: %d", nw.Code)
	}
}

func TestHandleChangeDiffErrorPaths(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	call := func(id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/changes/"+id+"/diff", nil).WithContext(f.Context)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		NewRepositoryViews(f.Projects, f.Access, f.DB, t.TempDir(), nil, nil).ChangeDiff(w, r)
		return w
	}
	if w := call(fmt.Sprint(f.ChangeID)); w.Code != 404 || !strings.Contains(w.Body.String(), "no branch") {
		t.Fatalf("branchless diff: %d %s", w.Code, w.Body.String())
	}
	if w := call("not-a-number"); w.Code != 400 {
		t.Fatalf("bad id: %d", w.Code)
	}
	if w := call("0000000f-423f-7000-8000-000000000000"); w.Code != 404 {
		t.Fatalf("unknown change: %d", w.Code)
	}
}
