package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
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
	if id := a.addGrant("user", "reader", access.RoleProjectViewer, "", access.ScopeProject, idstest.N(1).String(), "boss", ""); id.IsZero() {
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

func TestBuildRejectsDeploymentOptionsBeforeExternalWork(t *testing.T) {
	t.Parallel()
	a, f := bootWorld(t)
	f.reset()
	_, _, err := a.Ways.PublishWorkflow(t.Context(), `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: invalid-build}
spec:
  kind: pipeline
  stages:
    - name: build
      steps: [{name: build, uses: build, with: {deployment: other}}]
`, "fixture")
	if err == nil || !strings.Contains(err.Error(), "build takes no options") {
		t.Fatalf("build options admitted: %v", err)
	}
	if calls := f.joined(); len(calls) != 0 {
		t.Fatalf("invalid options performed work: %v", calls)
	}
}

func TestEnvironmentLabelsAndDefaultHTTPUseCurrentAuthorityAndStableIdentity(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	target, err := a.Deployment.Targets.Put(t.Context(), &deployment.Target{Name: "customer-host", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), "production", target.ID, deployment.Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	targetPath := "/api/v1/targets/" + target.ID.String() + "/label"
	depPath := "/api/v1/projects/game/deployments/" + dep.ID.String() + "/label"
	defaultPath := "/api/v1/projects/game/release/default"
	_, err = a.Integrations.Bindings.Save(t.Context(), "label-connection", "build", "portainer", `{"urlFrom":"URL","apiKeyFrom":"KEY"}`, "boss")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := a.Integrations.Bindings.ByName(t.Context(), "label-connection")
	if err != nil {
		t.Fatal(err)
	}
	connectionPath := "/api/v1/integrations/" + connection.ID.String() + "/label"
	if response := authzCall(h, "outsider", http.MethodGet, defaultPath, ""); response.Code != http.StatusOK {
		t.Fatalf("authorized preference read: %d %s", response.Code, response.Body)
	}

	for _, c := range []struct{ path, body string }{
		{connectionPath, `{"expectedLabel":"label-connection","label":"Customer connection"}`},
		{targetPath, `{"expectedLabel":"customer-host","label":"Customer host"}`},
		{depPath, `{"expectedLabel":"production","label":"Customer production"}`},
		{defaultPath, fmt.Sprintf(`{"expectedRevision":0,"deploymentId":%q}`, dep.ID.String())},
	} {
		if w := authzCall(h, "outsider", http.MethodPut, c.path, c.body); w.Code != http.StatusForbidden {
			t.Fatalf("unauthorized %s: %d %s", c.path, w.Code, w.Body)
		}
		if w := authzCall(h, "boss", http.MethodPut, c.path, c.body); w.Code != http.StatusOK {
			t.Fatalf("authorized %s: %d %s", c.path, w.Code, w.Body)
		}
		if w := authzCall(h, "boss", http.MethodPut, c.path, c.body); w.Code != http.StatusConflict {
			t.Fatalf("stale %s: %d %s", c.path, w.Code, w.Body)
		}
	}
	current, err := a.Deployment.Deployments.ByID(t.Context(), dep.ID)
	if err != nil || current.Name != dep.Name || current.TargetID != target.ID || current.DisplayLabel != "Customer production" {
		t.Fatalf("identity changed: %+v %v", current, err)
	}
	var choice deployment.ReleaseDefault
	w := authzCall(h, "boss", http.MethodGet, defaultPath, "")
	if err := json.Unmarshal(w.Body.Bytes(), &choice); err != nil || w.Code != http.StatusOK || choice.DeploymentID != dep.ID || choice.Revision != 1 {
		t.Fatalf("default read: %d %s %v", w.Code, w.Body, err)
	}
	if w := authzCall(h, "boss", http.MethodPut, defaultPath, `{"expectedRevision":1,"deploymentId":null}`); w.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", w.Code, w.Body)
	}
	w = authzCall(h, "boss", http.MethodGet, defaultPath, "")
	choice = deployment.ReleaseDefault{}
	if err := json.Unmarshal(w.Body.Bytes(), &choice); err != nil || w.Code != http.StatusOK || !choice.DeploymentID.IsZero() || choice.Revision != 2 {
		t.Fatalf("cleared choice: %d %s %v", w.Code, w.Body, err)
	}
}

func TestDeploymentAddressHTTPChecksCurrentAuthorityAndRevision(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	target, err := a.Deployment.Targets.Put(t.Context(), &deployment.Target{Name: "address-http", Kind: deployment.KindPortainer, Origin: deployment.OriginAttached, Status: deployment.TargetReady, Address: map[string]any{"tier": "lan"}, Ingress: map[string]any{"domain": "apps.example"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Deployment.Targets.Offer(t.Context(), target, "*", deployment.OfferCaps{}, "boss"); err != nil {
		t.Fatal(err)
	}
	dep, err := a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), "lan", target.ID, deployment.Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/projects/game/deployments/" + dep.ID.String() + "/address"
	h := a.Handler()
	if w := authzCall(h, "outsider", http.MethodPut, path, `{"expectedRevision":0,"customHostname":"customer.example"}`); w.Code != http.StatusForbidden {
		t.Fatalf("unauthorized edit: %d %s", w.Code, w.Body)
	}
	if w := authzCall(h, "boss", http.MethodPut, path, `{"expectedRevision":0,"customHostname":"customer.example"}`); w.Code != http.StatusOK {
		t.Fatalf("authorized edit: %d %s", w.Code, w.Body)
	}
	if w := authzCall(h, "boss", http.MethodPut, path, `{"expectedRevision":0,"customHostname":"stale.example"}`); w.Code != http.StatusConflict {
		t.Fatalf("stale edit: %d %s", w.Code, w.Body)
	}
	w := authzCall(h, "boss", http.MethodGet, path, "")
	var reading deployment.AddressReading
	if err := json.Unmarshal(w.Body.Bytes(), &reading); err != nil || w.Code != http.StatusOK || reading.CustomHostname != "customer.example" || reading.Revision != 1 {
		t.Fatalf("address read: %d %s %v", w.Code, w.Body, err)
	}
}
