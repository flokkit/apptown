package forge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/preview"

	"flokkit.io/forge/internal/forge/projectlifecycle"

	"flokkit.io/forge/internal/forge/workflow"
)

func TestConsolePreviewReportsBuildFailureAfterLongProgressLog(t *testing.T) {
	t.Parallel()
	const declaration = "build: ."
	f := newFakeDocker()
	a := testApp(t, Config{DockerClient: f})

	testAdministrator(t, a, "bls")
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "game", Name: "Game", Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	ctx := asPerson("bls")
	ch, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Title: "Shop"})
	if err != nil {
		t.Fatal(err)
	}
	ch, err = a.Work.Begin(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	project, change := p.ID, ch.ID
	ws := (workflow.Layout{DataDir: a.cfg.dataDir}).ChangeWorkspace(change)
	if err := os.MkdirAll(filepath.Join(ws, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		".apptown/app.yaml": "app:\n  game:\n    " + declaration + "\n    ports: [\"8080\"]\n    public: true\n",
		"Dockerfile":        "FROM example/godot:4.4.1\n",
	} {
		if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const failure = "ERROR: T uses the selected inventory item\nGAMEPLAY CHECKS: 1 failures"
	f.reply = func(args []string) (string, error, bool) {
		if len(args) > 0 && args[0] == "build" {
			return strings.Repeat("#1 importing game assets\n", 2000) + failure, errFakeExit, true
		}
		return "", nil, false
	}
	m := consoleMetaFor("00000000-00a5-7000-8000-000000000000", project, change)
	m.Actor, m.Token = "bls", "test-token"
	a.Console.Restore(m)
	r := httptest.NewRequest("POST", "/api/v1/console/00000000-00a5-7000-8000-000000000000/preview", strings.NewReader(`{}`))
	r.Header.Set("X-AppTown-Session-Token", "test-token")
	r.Header.Set("X-AppTown-Actor", "bls")
	w := httptest.NewRecorder()
	GETOnly(a.Handler()).ServeHTTP(w, r)
	var response struct{ Error string }
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Error, failure) {
		t.Fatalf("console lost the failing test: HTTP %d %s", w.Code, response.Error)
	}
	if len(response.Error) > 18*1024 || !strings.Contains(response.Error, "earlier build output omitted") {
		t.Fatalf("long build log was not bounded and identified: %d bytes", len(response.Error))
	}
	record := changePreviewViewsFor(a).State(ctx, ch)
	if record.State != preview.Broken || !strings.Contains(record.Reason, failure) {
		t.Fatalf("preview state lost the diagnostic: %+v", record)
	}
}
