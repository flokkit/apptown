package httpapi

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestHandleWorkspaceGuards(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	call := func(id ids.ID) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/runs/%d/workspace/", id), nil).WithContext(f.Context)
		r.SetPathValue("id", fmt.Sprint(id))
		w := httptest.NewRecorder()
		NewWorkspaces(f.DB, t.TempDir()).Workspace(w, r)
		return w
	}
	if w := call(f.RunID); w.Code != 404 || !strings.Contains(w.Body.String(), "no workspace") {
		t.Fatalf("missing workspace: %d %s", w.Code, w.Body.String())
	}
	if w := call(idstest.N(424242)); w.Code != 404 {
		t.Fatalf("unknown run: %d", w.Code)
	}
}
