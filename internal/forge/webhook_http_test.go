package forge

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/ids"
)

// A webhook creates a change only with the right token, on a workflow that
// declares on: webhook, on a project that selects it.
func TestWebhookDoor(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")

	if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "secrets.env"), []byte("APPTOWN_WEBHOOK_TOKEN=s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plainTestProject(t, a.DB, "hooky", "H")
	p, _ := a.Projects.BySlug(context.Background(), "hooky")
	wf := `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: on-push, category: change}
spec:
  kind: lifecycle
  on: { webhook: {} }
  stages:
    - name: apply
      steps:
        - {name: ok, uses: approval, with: {means: "I looked"}}
`
	seedLibraryWorkflow(t, a, p.ID, "on-push", wf)
	h := a.Handler()
	var latest *httptest.ResponseRecorder
	post := func(path, token, body string) int {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("X-AppTown-Token", token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		latest = w
		return w.Code
	}
	if c := post("/api/v1/hooks/hooky/on-push", "", `{}`); c != 403 {
		t.Errorf("no token: got %d, want 403", c)
	}
	if c := post("/api/v1/hooks/hooky/on-push", "wrong", `{}`); c != 403 {
		t.Errorf("bad token: got %d, want 403", c)
	}
	if c := post("/api/v1/hooks/hooky/on-push", "s3cret", `{"title":"a push"}`); c != 201 {
		t.Errorf("good token: got %d, want 201", c)
	}
	var admitted struct{ ChangeID, RunID ids.ID }
	if err := json.Unmarshal(latest.Body.Bytes(), &admitted); err != nil {
		t.Fatal(err)
	}
	change, err := a.Work.Change(asPerson("bls"), admitted.ChangeID)
	if err != nil || change.ProjectID != p.ID || change.Title != "a push" || change.Type != "webhook" {
		t.Fatalf("webhook change: %+v %v", change, err)
	}
	run, err := a.Work.Run(asPerson("bls"), admitted.RunID)
	if err != nil || run.ChangeID != change.ID || run.WorkflowName != "on-push" {
		t.Fatalf("webhook run: %+v %v", run, err)
	}
	made := 0
	for _, c := range projectChanges(t, a, p.ID) {
		if c.Type == "webhook" {
			made++
		}
	}
	if made != 1 {
		t.Fatalf("the webhook must create exactly one change, got %d", made)
	}
	// A workflow the project has not selected is a 404, not a change.
	if c := post("/api/v1/hooks/hooky/nope", "s3cret", `{}`); c != 404 {
		t.Errorf("unknown workflow: got %d, want 404", c)
	}
	// The token never travels in the URL, where proxies log it: a query
	// token is refused, even a right one, and says to use the header.
	if c := post("/api/v1/hooks/hooky/on-push?token=s3cret", "", `{}`); c != 403 || !strings.Contains(latest.Body.String(), "X-AppTown-Token header") {
		t.Fatalf("query token: %d %s", c, latest.Body.String())
	}
	// Webhook bodies are intentionally opaque: malformed JSON is a brief,
	// not a request-decoding error, and input is capped at 32 KiB.
	raw := strings.Repeat("x", 33*1024)
	if c := post("/api/v1/hooks/hooky/on-push", "s3cret", raw); c != 201 {
		t.Fatalf("raw body: %d %s", c, latest.Body.String())
	}
	if err := json.Unmarshal(latest.Body.Bytes(), &admitted); err != nil {
		t.Fatal(err)
	}
	change, err = a.Work.Change(asPerson("bls"), admitted.ChangeID)
	if err != nil || len(change.Body) != 32*1024 || change.Body != raw[:32*1024] {
		t.Fatalf("bounded raw brief: %v", err)
	}
	if c := post("/api/v1/hooks/missing/on-push", "s3cret", `{}`); c != 404 {
		t.Fatalf("missing project: %d", c)
	}

}

// A token still named as before the AppTown rename opens nothing, and the
// refusal names the rename, so the operator knows what to change.
func TestWebhookRefusesTheOldTokenNameByName(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "secrets.env"), []byte("FORGE_WEBHOOK_TOKEN=s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plainTestProject(t, a.DB, "hooky", "H")
	req := httptest.NewRequest("POST", "/api/v1/hooks/hooky/on-push", strings.NewReader(`{}`))
	req.Header.Set("X-AppTown-Token", "s3cret")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "rename it to APPTOWN_WEBHOOK_TOKEN") {
		t.Fatalf("old token name: %d %s", w.Code, w.Body.String())
	}
}
