package apptown

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestSecretHTTPSeparatesBindingFromRevealAndKeepsDeploymentScope(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	path := "/api/v1/projects/game/secrets/main/API_TOKEN"
	for _, tc := range []struct {
		who, body string
		code      int
	}{{"outsider", `{"value":"forbidden"}`, 403}, {"owner", `{"value":`, 400}, {"owner", `{"store":"unknown","value":"forbidden"}`, 400}} {
		w := authzCall(h, tc.who, "PUT", path, tc.body)
		if w.Code != tc.code {
			t.Fatalf("bind rejection: %d %s", w.Code, w.Body.String())
		}
		if bindings := boundSecrets(t, a, idstest.N(1), "main"); len(bindings) != 0 {
			t.Fatalf("rejected bind persisted: %+v", bindings)
		}
	}
	const value = "private-test-secret-keep-out-of-projections"
	w := authzCall(h, "owner", "PUT", path, `{"value":"`+value+`"}`)
	var bound struct {
		Name, Deployment, Version string
		Bound                     bool
	}
	if err := json.Unmarshal(w.Body.Bytes(), &bound); err != nil || w.Code != 200 || !bound.Bound || bound.Name != "API_TOKEN" || bound.Deployment != "main" || bound.Version != "1" || strings.Contains(w.Body.String(), value) {
		t.Fatalf("bind: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	for _, who := range []string{"owner", "outsider"} {
		w := authzCall(h, who, "GET", path+"/reveal", "")
		if w.Code != 403 || strings.Contains(w.Body.String(), value) {
			t.Fatalf("non-root reveal: %d %s", w.Code, w.Body.String())
		}
	}
	if w := authzCall(h, "boss", "GET", "/api/v1/projects/game/secrets/preview/API_TOKEN/reveal", ""); w.Code != 404 {
		t.Fatalf("cross-deployment reveal: %d %s", w.Code, w.Body.String())
	}
	w = authzCall(h, "boss", "GET", path+"/reveal", "")
	var revealed struct {
		Name, Deployment, Value string
		Version                 int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &revealed); err != nil || w.Code != 200 || revealed.Value != value || revealed.Name != "API_TOKEN" || revealed.Deployment != "main" {
		t.Fatalf("reveal: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	// Deleting a binding in one deployment must not remove another's value.
	if _, err := a.Secrets.Vault.Put(t.Context(), secret.Scope("game", "preview", ""), "API_TOKEN", "preview-only", "boss"); err != nil {
		t.Fatal(err)
	}
	if err := a.Secrets.Bindings.Put(t.Context(), idstest.N(1), secret.Binding{Deployment: "preview", Name: "API_TOKEN", Store: secret.StoreInternal, Ref: secret.Scope("game", "preview", ""), Version: "1", UpdatedBy: "boss"}); err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "outsider", "DELETE", path, ""); w.Code != 403 {
		t.Fatalf("foreign unbind: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "owner", "DELETE", path, ""); w.Code != 200 {
		t.Fatalf("unbind: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", "GET", path+"/reveal", ""); w.Code != 404 {
		t.Fatalf("unbound reveal: %d %s", w.Code, w.Body.String())
	}
	w = authzCall(h, "boss", "GET", "/api/v1/projects/game/secrets/preview/API_TOKEN/reveal", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "preview-only") {
		t.Fatalf("neighbor lost: %d %s", w.Code, w.Body.String())
	}
}

func TestPreviewAgentVisibilityHTTPEnforcesAuthorityAndStorageScope(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	const base = "/api/v1/projects/game/secrets/"
	for _, who := range []string{"owner", "outsider"} {
		response := authzCall(h, who, "GET", "/api/v1/authz/me?project=game", "")
		var holdings struct {
			Project map[string]bool `json:"project"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &holdings); err != nil || response.Code != 200 || holdings.Project["secret:bind"] != (who == "owner") {
			t.Fatalf("secret binding control authority: %d %s", response.Code, response.Body.String())
		}
	}
	for _, dep := range []string{"main", "preview"} {
		w := authzCall(h, "owner", "PUT", base+dep+"/NEXTUP_PASSWORD", `{"value":"typed-preview-password"}`)
		if w.Code != 200 {
			t.Fatalf("bind: %d %s", w.Code, w.Body.String())
		}
	}
	path := base + "preview/NEXTUP_PASSWORD/agent-visibility"
	for _, tc := range []struct {
		who, path string
		code      int
	}{{"outsider", path, 403}, {"owner", base + "main/NEXTUP_PASSWORD/agent-visibility", 400}, {"owner", base + "preview/MISSING/agent-visibility", 404}, {"owner", path, 200}} {
		w := authzCall(h, tc.who, "PUT", tc.path, `{"agentVisible":true}`)
		if w.Code != tc.code {
			t.Fatalf("toggle: %d %s, want %d", w.Code, w.Body.String(), tc.code)
		}
	}
	b := boundSecrets(t, a, idstest.N(1), "preview")["NEXTUP_PASSWORD"]
	if !b.AgentVisible {
		t.Fatal("toggle not persisted")
	}
	// Changing a typed value preserves the preview binding's explicit choice.
	if w := authzCall(h, "owner", "PUT", base+"preview/NEXTUP_PASSWORD", `{"value":"replacement-preview"}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if !boundSecrets(t, a, idstest.N(1), "preview")["NEXTUP_PASSWORD"].AgentVisible {
		t.Fatal("value change cleared choice")
	}
	if _, err := a.Secrets.Vault.Put(t.Context(), "project:game", "KEY", "shared-project-key", "boss"); err != nil {
		t.Fatal(err)
	}
	for _, dep := range []string{"main", "preview"} {
		if err := a.Secrets.Bindings.Put(t.Context(), idstest.N(1), secret.Binding{Deployment: dep, Name: "KEY", Store: secret.StoreInternal, Ref: "project:game", Version: "1", Generated: true, UpdatedBy: "boss"}); err != nil {
			t.Fatal(err)
		}
	}
	w := authzCall(h, "owner", "PUT", base+"preview/KEY/agent-visibility", `{"agentVisible":true}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "This value is used by a deployment. Give previews their own value to let agents see it") {
		t.Fatalf("shared toggle: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "outsider", "POST", base+"preview/KEY/separate-preview", `{}`); w.Code != 403 {
		t.Fatalf("separation authority: %d", w.Code)
	}
	if w := authzCall(h, "owner", "POST", base+"main/KEY/separate-preview", `{}`); w.Code != 422 {
		t.Fatalf("main separation: %d", w.Code)
	}
	if w := authzCall(h, "owner", "POST", base+"preview/KEY/separate-preview", `{}`); w.Code != 200 {
		t.Fatalf("separate: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "owner", "PUT", base+"preview/KEY/agent-visibility", `{"agentVisible":true}`); w.Code != 200 {
		t.Fatalf("independent toggle: %d %s", w.Code, w.Body.String())
	}
	if b := boundSecrets(t, a, idstest.N(1), "main")["KEY"]; b.Ref != "project:game" || b.Version != "1" || b.AgentVisible {
		t.Fatal("main binding changed")
	}
}
