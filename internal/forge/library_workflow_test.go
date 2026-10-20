package forge

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCustomLibraryWorkflow(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")

	a.Ways.SyncStockWorkflows(t.Context())
	h := a.Handler()

	// Stock workflows synced from the binary are marked origin='stock'.
	workflows, err := a.Ways.Workflows(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var stockName string
	for _, entry := range workflows {
		if entry.Origin == "stock" {
			stockName = entry.Name
			break
		}
	}
	if stockName == "" {
		t.Fatal("expected stock library workflows after sync")
	}
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/workflows/library", strings.NewReader(body))
		req.Header.Set("X-AppTown-Actor", "bls")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Author a CUSTOM instance-level workflow (a real stock body, renamed so it
	// parses but doesn't collide).
	_, yaml, err := a.Ways.WorkflowVersion(t.Context(), stockName, 0)
	if err != nil {
		t.Fatal(err)
	}
	custom := strings.Replace(yaml, "name: "+stockName, "name: team/my-flow", 1)
	if custom == yaml {
		// name may be quoted or on a mapping line; fall back to a broad replace.
		custom = strings.Replace(yaml, stockName, "team/my-flow", 1)
	}
	if rec := post(jsonBody(custom)); rec.Code != 200 {
		t.Fatalf("create custom lib workflow: %d %s", rec.Code, rec.Body.String())
	}
	origin, err := a.Ways.WorkflowOrigin(t.Context(), "team/my-flow")
	if err != nil {
		t.Fatalf("custom workflow not stored: %v", err)
	}
	latest, _, err := a.Ways.WorkflowVersion(t.Context(), "team/my-flow", 0)
	if err != nil {
		t.Fatalf("custom workflow version: %v", err)
	}
	if origin != "" || latest != 1 {
		t.Fatalf("custom workflow must be origin='' v1, got origin=%q v%d", origin, latest)
	}

	// Editing it bumps a version.
	if rec := post(jsonBody(custom + "\n")); rec.Code != 200 {
		t.Fatalf("edit custom: %d %s", rec.Code, rec.Body.String())
	}
	latest, _, err = a.Ways.WorkflowVersion(t.Context(), "team/my-flow", 0)
	if err != nil {
		t.Fatal(err)
	}
	if latest != 2 {
		t.Fatalf("edit should bump to v2, got v%d", latest)
	}

	// A stock name is read-only — re-authoring it is refused.
	if rec := post(jsonBody(yaml)); rec.Code != 409 {
		t.Fatalf("re-authoring a stock workflow must 409, got %d %s", rec.Code, rec.Body.String())
	}

	// Delete: custom ok, stock refused.
	del := func(name string) int {
		req := httptest.NewRequest("DELETE", "/api/v1/workflows/library/"+url.PathEscape(name), nil)
		req.Header.Set("X-AppTown-Actor", "bls")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := del("team/my-flow"); code != 200 {
		t.Fatalf("delete custom should be 200, got %d", code)
	}
	if _, err := a.Ways.WorkflowOrigin(t.Context(), "team/my-flow"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted custom workflow remains in the library: %v", err)
	}
	if code := del(stockName); code != 409 {
		t.Fatalf("delete stock should be 409, got %d", code)
	}
}

func jsonBody(yaml string) string {
	b, _ := json.Marshal(map[string]string{"yaml": yaml})
	return string(b)
}
