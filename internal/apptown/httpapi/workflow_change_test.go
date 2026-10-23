package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
)

func TestChangeCreationRecordsExplicitChatNamingIntent(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	h := NewChangeAdmission(f.Projects, f.Access, f.Work, nil, f.DB, "open")
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/projects/shop/changes", strings.NewReader(body)).WithContext(f.Context)
		r.SetPathValue("slug", "shop")
		w := httptest.NewRecorder()
		h.CreateChange(w, r)
		return w
	}
	for _, tc := range []struct {
		body, title  string
		nameFromChat bool
	}{
		{`{"title":"A placeholder","titleFromChat":true}`, "A placeholder", true},
		{`{"title":"Chat change (human words)"}`, "Chat change (human words)", false},
	} {
		response := call(tc.body)
		if response.Code != 201 {
			t.Fatalf("creation HTTP %d: %s", response.Code, response.Body.String())
		}
		var out struct {
			Change workflow.Change `json:"change"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Change.ProjectID != f.ProjectID || out.Change.Title != tc.title {
			t.Fatalf("created change: %+v", out.Change)
		}
		applied, err := f.Work.NameFromChat(f.Context, f.ProjectID, out.Change.ID, "Build a game", "bls")
		if err != nil || applied != tc.nameFromChat {
			t.Fatalf("chat naming intent: %v %v", applied, err)
		}
	}
	for _, body := range []string{`{"title":"invalid","titleFromChat":true,"workflow":"test"}`} {
		response := call(body)
		if response.Code != 400 {
			t.Fatalf("invalid naming HTTP %d: %s", response.Code, response.Body.String())
		}
	}
	changes, err := f.Work.Changes(f.Context, f.ProjectID, workflow.ChangeList{})
	if err != nil || len(changes) != 3 { // fixture's original Change plus two valid requests
		t.Fatalf("invalid request created a change: %+v %v", changes, err)
	}
}

func TestChangeListReadFailureAtHTTP(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	t.Cleanup(f.DB.FailNextStatement("FROM changes wi LEFT JOIN runs r"))
	r := httptest.NewRequest("GET", "/api/v1/projects/shop/changes", nil).WithContext(f.Context)
	r.SetPathValue("slug", "shop")
	w := httptest.NewRecorder()
	NewChanges(f.Work, f.Projects, f.Access, f.DB, nil).ListChanges(w, r)
	if w.Code != 500 || strings.Contains(w.Body.String(), "original") {
		t.Fatalf("HTTP read failure: %d %s", w.Code, w.Body.String())
	}
}

func TestHandlePatchChangeFailsWithoutPartialWrite(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	t.Cleanup(f.DB.FailNextStatement("INSERT INTO audit_log"))
	r := httptest.NewRequest("PATCH", fmt.Sprintf("/api/v1/changes/%d", f.ChangeID), strings.NewReader(`{"expectedEditRevision":0,"title":"human","body":"new brief"}`)).WithContext(f.Context)
	r.SetPathValue("id", fmt.Sprint(f.ChangeID))
	w := httptest.NewRecorder()
	NewChanges(f.Work, f.Projects, f.Access, f.DB, nil).PatchChange(w, r)
	if w.Code != 500 {
		t.Fatalf("failed receipt HTTP %d: %s", w.Code, w.Body.String())
	}
	ch, err := f.Work.Change(f.Context, f.ChangeID)
	if err != nil || ch.Title != "original" || ch.Body != "" {
		t.Fatalf("failed edit changed visible record: %+v %v", ch, err)
	}
}

func TestChangeCreationHTTPRequiresInitiatorReceipt(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	h := NewChangeAdmission(f.Projects, f.Access, f.Work, nil, f.DB, "open")
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/projects/shop/changes", strings.NewReader(`{"title":"Must be attributed"}`)).WithContext(f.Context)
		r.SetPathValue("slug", "shop")
		w := httptest.NewRecorder()
		h.CreateChange(w, r)
		return w
	}
	t.Cleanup(f.DB.FailNextStatement("INSERT INTO audit_log"))
	if response := call(); response.Code != 422 {
		t.Fatalf("unrecorded creation HTTP %d: %s", response.Code, response.Body.String())
	}
	changes, err := f.Work.Changes(f.Context, f.ProjectID, workflow.ChangeList{})
	if err != nil || len(changes) != 1 {
		t.Fatalf("failed creation became visible: %+v %v", changes, err)
	}
	if response := call(); response.Code != 201 {
		t.Fatalf("explicit retry HTTP %d: %s", response.Code, response.Body.String())
	}
	changes, err = f.Work.Changes(f.Context, f.ProjectID, workflow.ChangeList{})
	if err != nil || len(changes) != 2 {
		t.Fatalf("retry: %+v %v", changes, err)
	}
	receipts, err := audit.Recent(f.Context, f.DB, audit.Filter{ProjectID: f.ProjectID, KindPrefix: "change.created"})
	if err != nil || len(receipts) != 2 || receipts[1].Actor != "bls" {
		t.Fatalf("creation attribution: %+v %v", receipts, err)
	}
	// The change read says who opened it: the actor its creation receipt names.
	for _, receipt := range receipts {
		var created struct {
			ChangeID ids.ID `json:"changeId"`
		}
		if err := json.Unmarshal(receipt.Payload, &created); err != nil {
			t.Fatal(err)
		}
		ch, err := f.Work.Change(f.Context, created.ChangeID)
		if err != nil || ch.OpenedBy == "" || ch.OpenedBy != receipt.Actor {
			t.Fatalf("change %d opened by %q, receipt names %q: %v", created.ChangeID, ch.OpenedBy, receipt.Actor, err)
		}
	}
}
