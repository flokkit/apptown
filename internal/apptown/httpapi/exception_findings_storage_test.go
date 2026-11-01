package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
)

func TestExceptionReadFailuresReachEveryFindingsView(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	service := conformance.New(f.DB, confirmationHTTPAuthority{}, confirmationHTTPSource{}, nil)
	for _, view := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"list", NewConformance(service, f.Projects, nil).ListDispositions},
		{"project", NewFindings(f.Projects, f.Work, service, f.DB, t.TempDir()).ProjectFindings},
		{"run", NewFindings(f.Projects, f.Work, service, f.DB, t.TempDir()).RunFindings},
		{"export", NewConformance(service, f.Projects, nil).OpenVEX},
	} {
		t.Run(view.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil).WithContext(f.Context)
			req.SetPathValue("slug", "shop")
			req.SetPathValue("id", f.RunID.String())
			rec := httptest.NewRecorder()
			t.Cleanup(dbtest.NextStatementFailure(f.DB, "FROM dispositions"))
			view.handler(rec, req)
			if rec.Code != 500 {
				t.Fatalf("storage failure became %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// The project posture shows every accepted scan report: one whose scanner
// is no longer configured, or that cannot be read in its scanner's format,
// is listed as unread, never dropped or read as no findings (which would
// read as clean), and each report names the change its run worked on, not
// its run id in the change's place. The run's own findings view lists
// exactly its unreadable reports, with why.
func TestProjectFindingsListUnreadableReportsAndTheirChange(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	for _, name := range []string{"gitleaks", "semgrep"} {
		if _, err := integration.NewWithClock(f.DB, nil).Save(context.Background(), name, "scanner", "tool", `{"format":"sarif","kind":"secret","image":"scanner:1","command":"scan"}`, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	// A run whose id differs from its change's: a change without a run
	// comes between.
	if _, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "unscanned"}); err != nil {
		t.Fatal(err)
	}
	change, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "scanned"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.Work.StartRun(f.Context, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if run.ID == run.ChangeID {
		t.Fatalf("fixture: run %d works on change %d; the ids must differ", run.ID, run.ChangeID)
	}
	for name, body := range map[string]string{"gitleaks": `{"version":"2.1.0","runs":[]}`, "retired-scanner": `[]`, "semgrep": `[]`} {
		if _, err := f.Work.AttachArtifact(f.Context, run.ID, workflow.ArtifactDraft{Kind: "scan-report", Name: name, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Work.AttachArtifact(f.Context, run.ID, workflow.ArtifactDraft{Kind: "conformance-report", Name: "review", Body: `not json`}); err != nil {
		t.Fatal(err)
	}
	findings := NewFindings(f.Projects, f.Work, conformance.New(f.DB, confirmationHTTPAuthority{}, confirmationHTTPSource{}, nil), f.DB, t.TempDir())
	get := func(handler http.HandlerFunc, out any) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/", nil).WithContext(f.Context)
		req.SetPathValue("slug", "shop")
		req.SetPathValue("id", run.ID.String())
		rec := httptest.NewRecorder()
		handler(rec, req)
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil || rec.Code != 200 {
			t.Fatalf("findings: %d %s %v", rec.Code, rec.Body.String(), err)
		}
		return rec.Body.String()
	}
	var runView struct {
		Unread []struct{ Name, Kind, Reason string }
	}
	body := get(findings.RunFindings, &runView)
	unread := map[string]string{}
	for _, u := range runView.Unread {
		unread[u.Name] = u.Reason
	}
	if len(unread) != 3 || !strings.Contains(unread["retired-scanner"], "retired-scanner") || !strings.Contains(unread["review"], "conformance report") || !strings.Contains(unread["semgrep"], "could not be read as sarif") {
		t.Fatalf("a run's unreadable reports must be listed with their reasons, and only those: %s", body)
	}
	// sql-guarantee: the fixture's run has a pending approval; no operation completes it.
	if _, err := f.DB.SQL().ExecContext(context.Background(), `UPDATE runs SET status='done' WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	var posture struct {
		Sources []struct {
			Binding    string
			RunID      ids.ID
			ChangeID   ids.ID
			Unreadable string
		}
	}
	body = get(findings.ProjectFindings, &posture)
	by := map[string]int{}
	for i, s := range posture.Sources {
		by[s.Binding] = i
		if s.RunID != run.ID || s.ChangeID != run.ChangeID {
			t.Fatalf("source %s names run %d, change %d; want run %d of change %d", s.Binding, s.RunID, s.ChangeID, run.ID, run.ChangeID)
		}
	}
	retired, ok := by["retired-scanner"]
	if !ok || !strings.Contains(posture.Sources[retired].Unreadable, "retired-scanner") {
		t.Fatalf("a report whose scanner is gone must be listed as unread: %s", body)
	}
	if broken, ok := by["semgrep"]; !ok || !strings.Contains(posture.Sources[broken].Unreadable, "could not be read as sarif") {
		t.Fatalf("a report its scanner's format cannot read must be listed as unread, not as no findings: %s", body)
	}
	if known, ok := by["gitleaks"]; !ok || posture.Sources[known].Unreadable != "" {
		t.Fatalf("a configured scanner's report reads: %s", body)
	}
}
