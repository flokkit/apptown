package apptown

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/ids"
)

// The console plane's third sanctioned mutation: preview. Token-gated,
// change-scoped, and reachable through the GET-only listener.
func TestConsolePreviewGate(t *testing.T) {
	t.Parallel()
	a := testApp(t)

	testAdministrator(t, a, "bls")
	pid := createReadTestProject(t, a, "p")
	changeID := createReadTestChange(t, a, pid, "t")
	pm := consoleMetaFor("00000000-00c6-7000-8000-000000000000", pid, ids.Nil)
	pm.Actor, pm.Token = "bls", "ptok"
	a.Console.Restore(pm)
	cm := consoleMetaFor("00000000-00c5-7000-8000-000000000000", pid, changeID)
	cm.Actor, cm.Token = "bls", "ctok"
	a.Console.Restore(cm)
	h := GETOnly(a.Handler())

	do := func(sid, tok string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/console/"+sid+"/preview", strings.NewReader(`{}`))
		req.Header.Set("X-AppTown-Actor", "bls")
		if tok != "" {
			req.Header.Set("X-AppTown-Session-Token", tok)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	if w := do("00000000-00c5-7000-8000-000000000000", ""); w.Code != 403 {
		t.Fatalf("no token must 403, got %d", w.Code)
	}
	if w := do("00000000-00c5-7000-8000-000000000000", "wrong"); w.Code != 403 {
		t.Fatalf("wrong token must 403, got %d", w.Code)
	}
	if w := do("00000000-00c6-7000-8000-000000000000", "ptok"); w.Code != 422 || !strings.Contains(w.Body.String(), "change chat") {
		t.Fatalf("project chat must be refused in plain words: %d %s", w.Code, w.Body.String())
	}
	// A change chat with no worktree and no landing fails at the boot step
	// (not the gate): proof the request made it through GETOnly and the
	// token check. (A LANDED change would boot its as-landed snapshot.)
	if w := do("00000000-00c5-7000-8000-000000000000", "ctok"); w.Code != 422 || !strings.Contains(w.Body.String(), "no selected historical source") {
		t.Fatalf("gate passed but boot should refuse (no worktree): %d %s", w.Code, w.Body.String())
	}
	// Other POSTs on the plane stay dead.
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GETOnly must still block ordinary POSTs: %d", w.Code)
	}
}
