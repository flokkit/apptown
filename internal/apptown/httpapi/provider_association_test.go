package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
)

func TestProviderAssociationHTTPChecksCurrentAuthority(t *testing.T) {
	w := newTargetHTTPWorld(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" {
			t.Errorf("association wrote provider: %s", r.Method)
			out.WriteHeader(500)
			return
		}
		switch r.URL.Path {
		case "/api/endpoints":
			fmt.Fprint(out, `[{"Id":3,"Name":"docker","Type":1}]`)
		case "/api/endpoints/3":
			fmt.Fprint(out, `{"Id":3,"Name":"docker","Type":1}`)
		case "/api/endpoints/3/docker/info":
			fmt.Fprint(out, `{"ID":"engine-a"}`)
		default:
			t.Errorf("unexpected provider observation %s", r.URL)
			out.WriteHeader(404)
		}
	}))
	defer server.Close()
	writeTargetSecret(t, w, "PT_URL="+server.URL+"\nPT_KEY=scope-secret\n")
	target, err := w.deployment.Targets.Put(t.Context(), &deployment.Target{Name: "legacy", Kind: deployment.KindPortainer, Status: deployment.TargetReady, Address: map[string]any{"urlFrom": "PT_URL", "apiKeyFrom": "PT_KEY", "environment": "docker"}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/targets/legacy/provider-association"
	values := map[string]string{"name": "legacy"}
	for _, tc := range []struct {
		method  string
		body    string
		handler http.HandlerFunc
	}{{"GET", "", w.targets.ProviderAssociation}, {"POST", "{}", w.targets.AssociateProvider}} {
		response := deploymentBodyRequest(tc.handler, "outsider", tc.method, path, tc.body, values)
		if response.Code != http.StatusForbidden {
			t.Fatalf("unauthorized %s: %d %s", tc.method, response.Code, response.Body)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("provider observed before authority")
	}
	code, preview := targetCall(t, w.targets.ProviderAssociation, "GET", path, "", values)
	if code != http.StatusOK {
		t.Fatalf("authorized observation: %d %+v", code, preview)
	}
	current, err := w.deployment.Targets.ByID(t.Context(), target.ID)
	if err != nil || len(current.ProviderScope) != 0 {
		t.Fatalf("read persisted scope: %+v %v", current, err)
	}
	body, err := json.Marshal(map[string]any{"expected": preview["scope"], "expectedRevision": preview["revision"], "resources": preview["resources"]})
	if err != nil {
		t.Fatal(err)
	}
	code, result := targetCall(t, w.targets.AssociateProvider, "POST", path, string(body), values)
	if code != http.StatusOK || result["associated"] != true {
		t.Fatalf("authorized association: %d %+v", code, result)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "scope-secret") {
		t.Fatal("association exposed credential")
	}
}
