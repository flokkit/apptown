package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/complaint"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestComplainLinkedToRun(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	box := complaint.NewBox(f.DB, nil, nil)
	req := httptest.NewRequest("POST", "/api/v1/complaints", strings.NewReader(`{"text":"the gate was unclear","runId":"`+f.RunID.String()+`","step":"accept"}`))
	req = req.WithContext(f.Context)
	w := httptest.NewRecorder()
	NewComplaintIntake(box, nil, f.DB).Complain(w, req)
	if w.Code != 201 {
		t.Fatalf("file linked: %d %s", w.Code, w.Body.String())
	}
	complaints, err := box.All(req.Context(), 10)
	if err != nil || len(complaints) != 1 {
		t.Fatal(err)
	}
	got := complaints[0]
	if got.Source != "run" || got.Workflow != "test" || got.Project != "shop" || got.ChangeID != f.ChangeID || got.RunID != f.RunID {
		t.Fatalf("run linkage wrong: %+v", got)
	}
}

// A run files complaints in the project it works in, which its credential
// names: a runId of another project's run is refused, and with no runId the
// complaint is still the run's project's. What it is about is the filer's
// to say, among three values.
func TestARunsComplaintLandsInItsOwnProject(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	box := complaint.NewBox(f.DB, nil, nil)
	file := func(principalProject ids.ID, body string) *httptest.ResponseRecorder {
		ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "instrument", Name: "run/1", Instrument: "run", Via: "run", ProjectID: principalProject}, Verified: true})
		w := httptest.NewRecorder()
		NewComplaintIntake(box, nil, f.DB).Complain(w, httptest.NewRequest("POST", "/api/v1/complaints", strings.NewReader(body)).WithContext(ctx))
		return w
	}
	runID := f.RunID.String()
	if w := file(idstest.N(999999), `{"text":"elsewhere","runId":"`+runID+`"}`); w.Code != 403 {
		t.Fatalf("a run naming another project's run: %d %s", w.Code, w.Body.String())
	}
	if w := file(f.ProjectID, `{"text":"wrong about","about":"the model"}`); w.Code != 422 {
		t.Fatalf("an about outside the three: %d %s", w.Code, w.Body.String())
	}
	if w := file(f.ProjectID, `{"text":"the pack asks for tables","about":"way-of-working"}`); w.Code != 201 {
		t.Fatalf("filed: %d %s", w.Code, w.Body.String())
	}
	all, err := box.All(context.Background(), 10)
	if err != nil || len(all) != 1 || all[0].Project != "shop" || all[0].Source != "run" || all[0].About != complaint.AboutWayOfWorking {
		t.Fatalf("filed in the run's own project: %+v %v", all, err)
	}
}
