package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/ids"
)

func TestHandleAuditLogFilters(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	for _, row := range []struct {
		projectID     ids.ID
		kind, subject string
	}{
		{f.ProjectID, "project.created", "shop"},
		{f.ProjectID, "workflow.saved", "build"},
		{ids.Nil, "instance.setting", "models"},
	} {
		audit.At(time.Now()).BestEffort(context.Background(), f.DB, "bls", row.projectID, row.kind, row.subject, map[string]any{"x": 1})
	}
	call := func(query string) (int, []map[string]any) {
		r := httptest.NewRequest("GET", "/api/v1/audit"+query, nil).WithContext(f.Context)
		w := httptest.NewRecorder()
		NewAuditLog(f.DB, f.Projects).AuditLog(w, r)
		var rows []map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &rows)
		return w.Code, rows
	}
	if code, rows := call(""); code != 200 || len(rows) < 3 {
		t.Fatalf("all rows: %d %v", code, rows)
	}
	if code, rows := call("?project=shop"); code != 200 || len(rows) < 3 {
		t.Fatalf("project and instance rows: %d %v", code, rows)
	}
	if code, rows := call("?kind=project"); code != 200 || len(rows) != 1 || rows[0]["kind"] != "project.created" {
		t.Fatalf("kind: %d %v", code, rows)
	}
	if code, _ := call("?project=ghost"); code != 404 {
		t.Fatalf("unknown project: %d", code)
	}
}
