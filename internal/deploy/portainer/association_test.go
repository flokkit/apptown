package portainer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
	"gopkg.in/yaml.v3"
)

func TestMarkedComposePreservesApplicationAndBindsDeployment(t *testing.T) {
	id := idstest.N(10)
	marked, err := MarkCompose("services:\n  app:\n    image: registry.example/app@sha256:123\n    environment:\n      MESSAGE: 'keep this exactly'\nvolumes:\n  data: {}\n", id)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal([]byte(marked), &document); err != nil {
		t.Fatal(err)
	}
	if document["x-apptown-deployment"] != id.String() {
		t.Fatal("deployment marker missing")
	}
	app := document["services"].(map[string]any)["app"].(map[string]any)
	if app["image"] != "registry.example/app@sha256:123" || app["environment"].(map[string]any)["MESSAGE"] != "keep this exactly" {
		t.Fatal("marking compose changed application inputs")
	}
	if _, ok := document["volumes"].(map[string]any)["data"]; !ok {
		t.Fatal("marking compose lost data volume")
	}
	if _, err := MarkCompose(marked, id); err == nil {
		t.Fatal("overwriting a supplied marker must refuse")
	}
}

func TestStackAssociationDoesNotInferOwnershipFromCurrentNumericID(t *testing.T) {
	id := idstest.N(10)
	content := "services: {}"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/stacks/4/file" {
			t.Errorf("unexpected resource %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"StackFileContent": content})
	}))
	defer server.Close()
	scope := Scope{Version: 1, APIBase: server.URL, EndpointID: 3, DaemonID: "daemon"}
	cfg := Target{PortainerURL: server.URL, Scope: &scope}
	stack := Stack{ID: 4, Name: "recorded-stack", EndpointID: 3}
	if err := VerifyStackAssociation(context.Background(), cfg, stack, id, nil); err == nil {
		t.Fatal("a current numeric ID without association was trusted")
	}
	legacy := &StackAssociation{DeploymentID: id, Scope: scope, StackID: stack.ID, StackName: stack.Name}
	if err := VerifyStackAssociation(context.Background(), cfg, stack, id, legacy); err != nil {
		t.Fatal(err)
	}
	wrong := *legacy
	wrong.Scope.DaemonID = "another-daemon"
	if err := VerifyStackAssociation(context.Background(), cfg, stack, id, &wrong); err == nil {
		t.Fatal("legacy association from another scope accepted")
	}
	content = "x-apptown-deployment: " + idstest.N(11).String() + "\nservices: {}"
	if err := VerifyStackAssociation(context.Background(), cfg, stack, id, legacy); err == nil {
		t.Fatal("conflicting marker was overridden by legacy association")
	}
	content = strings.ReplaceAll(content, idstest.N(11).String(), id.String())
	if err := VerifyStackAssociation(context.Background(), cfg, stack, id, nil); err != nil {
		t.Fatal(err)
	}
	before := calls
	stack.EndpointID = 8
	if err := VerifyStackAssociation(context.Background(), cfg, stack, id, legacy); err == nil {
		t.Fatal("another endpoint accepted")
	}
	if calls != before {
		t.Fatal("wrong endpoint made a resource request")
	}
}

func TestUnreadableStackProofRefusesLegacyFallback(t *testing.T) {
	id := idstest.N(10)
	for _, response := range []string{`{}`, `null`, `{"StackFileContent":""}`, `{"StackFileContent":"null"}`, `{"StackFileContent":"{}"}`, `{"StackFileContent":"services: null"}`, `{"StackFileContent":"x-apptown-deployment: null\nservices: {}"}`} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(response))
			}))
			defer server.Close()
			scope := Scope{Version: 1, APIBase: server.URL, EndpointID: 3, DaemonID: "daemon"}
			cfg := Target{PortainerURL: server.URL, Scope: &scope}
			stack := Stack{ID: 4, Name: "recorded-stack", EndpointID: 3}
			legacy := &StackAssociation{DeploymentID: id, Scope: scope, StackID: stack.ID, StackName: stack.Name}
			if err := VerifyStackAssociation(context.Background(), cfg, stack, id, legacy); err == nil {
				t.Fatal("unreadable or malformed current proof authorized legacy mutation")
			}
		})
	}
}
