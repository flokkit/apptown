package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/remediation"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// An empty remediation list serialises as [], never null — AppTown's
// own first browser error was the page reading the length of null.
func TestRemediationListIsNeverNull(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	work := workflow.New(context.Background(), db, workflow.Dependencies{})
	t.Cleanup(work.Stop)
	h := NewRemediations(remediation.New(db, remediation.Dependencies{Work: work}))
	req := httptest.NewRequest("GET", "/api/v1/conformance/remediations", nil)
	w := httptest.NewRecorder()
	h.ListRemediations(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"remediations":[]`) {
		t.Fatalf("want an empty list, got %d %s", w.Code, w.Body.String())
	}
}
