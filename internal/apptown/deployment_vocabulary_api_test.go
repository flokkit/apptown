package apptown

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestDeploymentVocabularyRoutesKeepAuthorizationAndRetireOldPaths(t *testing.T) {
	t.Parallel()
	a := testApp(t)

	pid := plainTestProject(t, a.DB, "game", "Game")
	if pid != idstest.N(1) {
		t.Fatalf("first project must retain id 1 for vocabulary fixture, got %d", pid)
	}
	testAdministrator(t, a, "boss")
	if err := a.EnsureAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	if id := a.addGrant("user", "reader", access.RoleProjectViewer, "", access.ScopeProject, idstest.N(1).String(), "fixture", "boss", ""); id.IsZero() {
		t.Fatal("reader grant was not created")
	}
	h := a.Handler()
	call := func(who, method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req.Header.Set("X-AppTown-Actor", who)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	if w := call("reader", "GET", "/api/v1/projects/game/deployments"); w.Code != 200 {
		t.Fatalf("read deployment: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/v1/projects/game/deployments", "/api/v1/projects/game/deployments/main/deploy", "/api/v1/projects/game/deployments/main/backups"} {
		if w := call("reader", "POST", path); w.Code != 403 {
			t.Fatalf("reader mutated %s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/api/v1/projects/game/environments", "/api/v1/projects/game/environments/main/deploy", "/api/v1/projects/game/environments/main/backups"} {
		for _, method := range []string{"GET", "POST"} {
			if w := call("boss", method, path); w.Code != 404 {
				t.Fatalf("retired route %s %s: %d", method, path, w.Code)
			}
		}
	}
}

func TestDeployRejectsObsoleteSelectorBeforeExternalWork(t *testing.T) {
	t.Parallel()
	a, f := bootWorld(t)
	_, step := bootStep(t, a, "landing", schema.Step{Name: "deploy", Uses: "deploy", With: map[string]any{"environment": "other"}})
	f.reset()
	a.Work.ExecuteStep(a.asEngine(context.Background()), step)
	result, err := workflow.ReadStepIn(context.Background(), a.DB, step)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != workflow.StatusError || !strings.Contains(string(result.Evidence), "unknown field") {
		t.Fatalf("obsolete selector silently defaulted: %s %s", result.Status, result.Evidence)
	}
	if calls := f.joined(); len(calls) != 0 {
		t.Fatalf("invalid selector performed external work: %v", calls)
	}
	// sql-guarantee: no attempt may be written anywhere; the owner lists only open attempts, and a refused selector must leave not even a finished one
	completionCount(t, a.DB, 0, `SELECT count(*) FROM deployment_attempts`)
}
