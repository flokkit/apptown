package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestCreateChangeHTTPFailedAdmissionLeavesNoChange(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	h := NewChangeAdmission(f.Projects, f.Access, f.Work, changeactions.New(f.Work, nil, f.DB), f.DB, "open")
	clear := f.DB.FailNextStatement("INSERT INTO steps")
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
			h := NewChangeAdmission(f.Projects, f.Access, f.Work, changeactions.New(f.Work, nil, f.DB), f.DB, "open")
			if failure == "preparation" {
				t.Cleanup(f.DB.FailNextStatement("UPDATE run_starts SET ready"))
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
