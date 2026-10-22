package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func TestHandleTraceNotFoundAndBadID(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	call := func(id string) int {
		r := httptest.NewRequest("GET", "/api/v1/runs/"+id+"/trace", nil).WithContext(f.Context)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		NewTraces(f.Work, nil, f.DB).Trace(w, r)
		return w.Code
	}
	if got := call("0000000f-423f-7000-8000-000000000000"); got != 404 {
		t.Fatalf("unknown run: %d", got)
	}
	if got := call("not-a-number"); got != 400 {
		t.Fatalf("bad id: %d", got)
	}
}

func TestHandleListRunsState(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	second, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "finished work"})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := f.Work.StartRun(f.Context, workflow.StartChangeRun{ChangeID: second.ID, Workflow: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.DB.InTx(context.Background(), func(tx *database.Tx) error {
		return workflow.TransitionRunIn(f.Context, tx, finished, workflow.RunDone, finished.CurrentStage, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	call := func(query string) []map[string]any {
		r := httptest.NewRequest("GET", "/api/v1/runs?project=shop"+query, nil).WithContext(f.Context)
		w := httptest.NewRecorder()
		NewRuns(f.Work, nil).ListRuns(w, r)
		if w.Code != 200 {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		var rows []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	rows := call("")
	if len(rows) != 1 || rows[0]["state"] != "waiting on you" || rows[0]["id"] != f.RunID.String() {
		t.Fatalf("default running and honest state: %v", rows)
	}
	rows = call("&status=all")
	if len(rows) != 2 {
		t.Fatalf("all runs: %v", rows)
	}
	seen := map[ids.ID]bool{}
	for _, row := range rows {
		id, _ := ids.Parse(row["id"].(string))
		seen[id] = true
		if id == finished.ID && row["status"] != "done" {
			t.Fatalf("finished status: %v", row)
		}
	}
	if !seen[f.RunID] || !seen[finished.ID] {
		t.Fatalf("run identities: %v", seen)
	}
}
