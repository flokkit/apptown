package forge

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/complaint"
	"flokkit.io/forge/internal/forge/workflow"
)

// itoa is a local shorthand for building the id path values and JSON bodies.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// Archive and delete: answering archives, archiving alone archives, the
// sweep archives only what is older than the window, delete removes the
// text and leaves a ledger line.
func TestComplaintsArchiveAndDelete(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "boss")

	h := a.Handler()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-AppTown-Actor", "boss")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	old := "2026-01-01T00:00:00Z"
	oldOne := ids.New(time.Now())
	// sql-guarantee: Archive-by-age needs an old complaint before the HTTP request; File always uses the current clock and the timestamp-capable generated insert is private to Complaint.
	if _, err := a.DB.Exec(`INSERT INTO complaints (id,number,at,text) VALUES (?,1000,?,'old one')`, oldOne, old); err != nil {
		t.Fatal(err)
	}
	filed := map[string]ids.ID{}
	for _, text := range []string{"new one", "to answer", "to delete"} {
		id, err := a.Complaints.File(t.Context(), complaint.Entry{Text: text})
		if err != nil {
			t.Fatal(err)
		}
		filed[text] = id
	}
	if w := call("POST", "/api/v1/complaints/archive-older", `{"hours":24}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"archived":1`) {
		t.Fatalf("archive-older: %d %s", w.Code, w.Body.String())
	}
	if w := call("POST", "/api/v1/complaints/"+filed["to answer"].String()+"/answer", `{"answer":"fixed"}`); w.Code != 200 {
		t.Fatalf("answer: %d %s", w.Code, w.Body.String())
	}
	if w := call("POST", "/api/v1/complaints/"+filed["new one"].String()+"/archive", `{}`); w.Code != 200 {
		t.Fatalf("archive: %d %s", w.Code, w.Body.String())
	}
	if w := call("POST", "/api/v1/complaints/"+filed["new one"].String()+"/archive", `{}`); w.Code != 404 {
		t.Errorf("archiving twice should say there was nothing to do: %d", w.Code)
	}
	if w := call("DELETE", "/api/v1/complaints/"+filed["to delete"].String(), ``); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	w := call("GET", "/api/v1/complaints", ``)
	var out struct {
		Complaints []struct {
			ID         ids.ID `json:"id"`
			ArchivedAt string `json:"archivedAt"`
			State      string `json:"state"`
		} `json:"complaints"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Complaints) != 3 {
		t.Fatalf("expected 3 complaints after delete, got %d", len(out.Complaints))
	}
	want := map[ids.ID]string{oldOne: "archived", filed["new one"]: "archived", filed["to answer"]: "answered"}
	for _, c := range out.Complaints {
		if c.State != want[c.ID] {
			t.Errorf("complaint %d: state %q, want %q (sweep archives, archive archives, answer stays answered)", c.ID, c.State, want[c.ID])
		}
	}
	// Answered → Archived is a person's act; New → Answered → Archived.
	if w := call("POST", "/api/v1/complaints/"+filed["to answer"].String()+"/archive", `{}`); w.Code != 200 {
		t.Fatalf("archive an answered one: %d", w.Code)
	}
	// Ninety days on, everything older is gone, whatever its state.
	a.sweepOldComplaints(context.Background())
	all, err := a.Complaints.All(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if left := len(all); left != 2 {
		t.Errorf("the ninety-day sweep should have removed the January one only: %d left", left)
	}
	if n := auditsWhere(t, a.DB, "complaint.deleted", func(e audit.Entry) bool { return e.Subject == filed["to delete"].String() }); n != 1 {
		t.Error("delete should leave a ledger line")
	}
}

// Complaints are inert and reachable without identity (internal mode): the
// intake works from a run context, resolves the change, and changes nothing.
func TestComplaintIntakeIsInertAndOpen(t *testing.T) {
	t.Parallel()
	a, h := internalApp(t)
	runID := admittedOpenRun(t, a, "p")

	w := postJSON(h, "/api/v1/complaints", map[string]any{
		"text": "the dep scan wedged", "why": "no visibility", "wish": "a wait verb", "runId": runID})
	if w.Code != 201 && w.Code != 200 {
		t.Fatalf("complaint intake must be open: %d %s", w.Code, w.Body.String())
	}
	complaints, err := a.Complaints.All(context.Background(), 10)
	if err != nil || len(complaints) != 1 {
		t.Fatal(err)
	}
	if complaints[0].Source != "run" || complaints[0].Project != "p" {
		t.Fatalf("complaint must resolve its run context: %+v", complaints[0])
	}
	// Inert: the run is untouched.
	run, _ := workflow.ReadRunIn(context.Background(), a.DB, runID)
	if run.Status != workflow.RunRunning {
		t.Fatalf("filing a complaint must trigger nothing: run is %q", run.Status)
	}
}

// A complaint is named by its number within AppTown as well as its id:
// answering "complaint 1" answers the first filed; a number none has is
// not found in those words.
func TestAComplaintIsAnsweredByItsNumber(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "boss")
	id, err := a.Complaints.File(t.Context(), complaint.Entry{Text: "first"})
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	if w := authzCall(h, "boss", "POST", "/api/v1/complaints/1/answer", `{"answer":"fixed"}`); w.Code != 200 {
		t.Fatalf("answer 1: %d %s", w.Code, w.Body.String())
	}
	all, err := a.Complaints.All(t.Context(), 10)
	if err != nil || len(all) != 1 || all[0].ID != id || all[0].Number != 1 || all[0].Answer != "fixed" {
		t.Fatalf("answered: %+v %v", all, err)
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/complaints/9/answer", `{"answer":"x"}`); w.Code != 404 || !strings.Contains(w.Body.String(), "no complaint 9") {
		t.Fatalf("answer 9: %d %s", w.Code, w.Body.String())
	}
}
