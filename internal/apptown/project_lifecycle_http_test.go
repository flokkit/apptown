package apptown

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
)

// Project lifecycle over HTTP: slug law, name defaulting, conflict, patch,
// and the audit trail behind each act.
func TestProjectLifecycleHTTP(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{DockerClient: newFakeDocker()})
	testAdministrator(t, a, "bls")

	h := a.Handler()

	if w := postJSON(h, "/api/v1/projects", map[string]string{"slug": "Bad Slug"}); w.Code != 400 {
		t.Fatalf("bad slug accepted: %d", w.Code)
	}
	w := postJSON(h, "/api/v1/projects", map[string]string{"slug": "egg-catch"})
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"name":"egg-catch"`) {
		t.Fatalf("create (name defaults to slug): %d %s", w.Code, w.Body.String())
	}
	if w := postJSON(h, "/api/v1/projects", map[string]string{"slug": "egg-catch"}); w.Code != 409 {
		t.Fatalf("duplicate slug must 409: %d", w.Code)
	}
	req := httptest.NewRequest("GET", "/api/v1/projects/egg-catch", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("get: %d", rec.Code)
	}
	if audits := auditCount(t, a.DB, "project.created"); audits != 1 {
		t.Fatalf("project creation must be audited: %d", audits)
	}
	if st := deleteProjectAs(t, a, "bls", "egg-catch"); st.State != project.Deleted {
		t.Fatalf("delete: %+v", st)
	}
	if op := purgeProjectAs(t, a, "bls", "egg-catch"); op.State != operations.Succeeded {
		t.Fatalf("purge: %s %q", op.State, op.Detail)
	}
	if _, err := a.Projects.BySlug(context.Background(), "egg-catch"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted project is still found: %v", err)
	}
	deleted := 0
	for _, entry := range auditEntries(t, a.DB, "project.deleted") {
		if entry.Subject == "egg-catch" {
			deleted++
		}
	}
	if deleted != 1 {
		t.Fatalf("project deletion receipts: %d", deleted)
	}
}
