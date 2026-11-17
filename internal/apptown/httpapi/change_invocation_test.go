package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestCreateChangeHTTPFailedAdmissionLeavesNoChange(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	h := NewChangeAdmission(f.Projects, f.Access, f.Work, changeactions.New(f.Work, f.DB), f.DB, "open")
	clear := dbtest.NextStatementFailure(f.DB, "INSERT INTO steps")
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"title":"do not orphan","workflow":"test"}`)).WithContext(f.Context)
	r.SetPathValue("slug", "shop")
	w := httptest.NewRecorder()
	h.CreateChange(w, r)
	clear()
	var response map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 422 || response["error"] == nil || response["change"] != nil {
		t.Fatalf("failed invoke response: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	changes, err := f.Work.Changes(f.Context, f.ProjectID, workflow.ChangeList{})
	if err != nil || len(changes) != 1 || changes[0].ID != f.ChangeID {
		t.Fatalf("failed invoke left an orphan: %+v %v", changes, err)
	}
}

func TestCreateChangeHTTPPostCommitFailureNamesTheAdmittedWork(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"preparation", "dispatch"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			f := newWorkflowHTTPFixture(t, true)
			h := NewChangeAdmission(f.Projects, f.Access, f.Work, changeactions.New(f.Work, f.DB), f.DB, "open")
			if failure == "preparation" {
				t.Cleanup(dbtest.NextStatementFailure(f.DB, "UPDATE run_starts SET ready"))
			} else {
				f.Work.Stop()
			}
			r := httptest.NewRequest("POST", "/", strings.NewReader(`{"title":"admitted work","workflow":"test"}`)).WithContext(f.Context)
			r.SetPathValue("slug", "shop")
			w := httptest.NewRecorder()
			h.CreateChange(w, r)
			if w.Code != 422 {
				t.Fatalf("post-commit response: %d %s", w.Code, w.Body.String())
			}
			changes, err := f.Work.Changes(f.Context, f.ProjectID, workflow.ChangeList{})
			if err != nil || len(changes) != 2 {
				t.Fatalf("committed work: %+v %v", changes, err)
			}
			created := changes[0]
			if !strings.Contains(w.Body.String(), fmt.Sprintf("change %d run %d", created.ID, created.RunID)) {
				t.Fatalf("missing committed identities: %s", w.Body.String())
			}
			want := workflow.ChangeAbandoned
			if failure == "dispatch" {
				want = workflow.ChangeInProgress
			}
			if created.State != want {
				t.Fatalf("settled state: %s, want %s", created.State, want)
			}
		})
	}
}

func TestCallerTitleHTTPRefusesWith400(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	grantWorkflowHTTPAuthority(t, f)
	h := NewChangeAdmission(f.Projects, f.Access, f.Work, changeactions.New(f.Work, f.DB), f.DB, "open")
	title := strings.Repeat("界", 101)
	check := func(w *httptest.ResponseRecorder) {
		t.Helper()
		var out struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 400 || out.Error != "Title is the goal in a few words; put the instructions in the body." {
			t.Fatalf("HTTP %d: %s (%v)", w.Code, w.Body.String(), err)
		}
	}
	for _, lane := range []string{"", "test"} {
		body, _ := json.Marshal(map[string]any{"title": title, "workflow": lane})
		r := httptest.NewRequest("POST", "/", strings.NewReader(string(body))).WithContext(f.Context)
		r.SetPathValue("slug", "shop")
		w := httptest.NewRecorder()
		h.CreateChange(w, r)
		check(w)
	}
	ch, err := f.Work.Change(f.Context, f.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"title": title, "expectedEditRevision": ch.EditRevision})
	r := httptest.NewRequest("PATCH", "/", strings.NewReader(string(body))).WithContext(f.Context)
	r.SetPathValue("id", ch.ID.String())
	w := httptest.NewRecorder()
	NewChanges(f.Work, f.Projects, f.Access, f.DB, nil).PatchChange(w, r)
	check(w)
}
