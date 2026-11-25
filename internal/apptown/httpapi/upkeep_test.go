package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
)

func TestChangeListRetainsAutomatedOriginAfterPersonTouchesIt(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	system := access.WithCaller(f.Context, access.Caller{Verified: true, Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule", ProjectID: f.ProjectID}})
	ch, err := f.Work.Create(system, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "Dependency updates"})
	if err != nil {
		t.Fatal(err)
	}
	title := ch.Title
	if _, err := f.Work.Edit(f.Context, workflow.ChangeEdit{ID: ch.ID, Title: &title, ExpectedRevision: new(int64)}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/projects/shop/changes", nil).WithContext(f.Context)
	r.SetPathValue("slug", "shop")
	w := httptest.NewRecorder()
	NewChanges(f.Work, f.Projects, f.Access, f.DB, nil).ListChanges(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var rows []struct {
		ID              ids.ID `json:"id"`
		AutomatedOrigin bool   `json:"automatedOrigin"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == ch.ID {
			if !row.AutomatedOrigin {
				t.Fatal("change list lost automated origin")
			}
			return
		}
	}
	t.Fatal("touched AppTown change missing")
}
