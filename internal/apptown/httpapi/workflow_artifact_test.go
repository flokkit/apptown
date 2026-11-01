package httpapi

import (
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
)

func TestWorkflowArtifactHTTPMissingAndUnavailable(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	h := NewRuns(f.Work, nil)
	r := httptest.NewRequest("GET", "/api/v1/artifacts/00000001-869f-7000-8000-000000000000", nil).WithContext(f.Context)
	r.SetPathValue("id", "00000001-869f-7000-8000-000000000000")
	out := httptest.NewRecorder()
	h.GetArtifact(out, r)
	if out.Code != 404 {
		t.Fatalf("missing artifact: %d", out.Code)
	}
	t.Cleanup(dbtest.NextStatementFailure(f.DB, "SELECT a.run_id,r.project_id FROM artifacts"))
	out = httptest.NewRecorder()
	h.GetArtifact(out, r)
	if out.Code != 500 {
		t.Fatalf("unavailable artifact: %d %s", out.Code, out.Body.String())
	}
}
