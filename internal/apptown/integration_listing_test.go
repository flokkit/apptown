package apptown

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// The integrations list says what names each integration: the projects that
// attach it and the standing deployments on targets it reaches. A row the
// engine keeps for itself is not an attachment.
func TestIntegrationListSaysWhatUsesEachIntegration(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	for _, b := range []struct{ name, capability, provider string }{{"store", "objectstore", "minio"}, {"lan", "deploy", "portainer"}} {
		if _, err := a.Integrations.Bindings.Save(context.Background(), b.name, b.capability, b.provider, `{}`, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Integrations.Attachments.Attach(t.Context(), idstest.N(1), "objectstore", "store", `{}`, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Integrations.Attachments.Remember(t.Context(), idstest.N(2), "objectstore", "store", `{}`); err != nil {
		t.Fatal(err)
	}
	target, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: "lan-host", Kind: deployment.KindDockerHost, Provider: "lan", Origin: deployment.OriginAttached, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), "main", target.ID, (deployment.Attributes{}).WithDefaults()); err != nil {
		t.Fatal(err)
	}
	// A preview comes and goes with its change; it is not a standing use.
	if err := a.Deployment.Deployments.RecordPreview(context.Background(), idstest.N(1), idstest.N(7), target.ID, "preview-7", 0, ""); err != nil {
		t.Fatal(err)
	}
	w := authzCall(a.Handler(), "boss", "GET", "/api/v1/integrations", "")
	if w.Code != 200 {
		t.Fatalf("integrations: %d %s", w.Code, w.Body.String())
	}
	var list httpapi.IntegrationList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	got := map[string]httpapi.IntegrationUse{}
	for _, e := range list.Integrations {
		got[e.Name] = e.UsedBy
	}
	want := map[string]httpapi.IntegrationUse{
		"store": {Projects: []integration.AttachedProject{{Slug: "game", Name: "G", Capability: "objectstore"}}, Deployments: []deployment.ProviderDeployment{}},
		"lan":   {Projects: []integration.AttachedProject{}, Deployments: []deployment.ProviderDeployment{{Project: "game", Name: "main", Target: "lan-host"}}},
	}
	for name, use := range want {
		if !reflect.DeepEqual(got[name], use) {
			t.Errorf("%s used by %+v, want %+v", name, got[name], use)
		}
	}
}

// The list never measures a connection, even before its first observation.
// Scheduled collection and Check now both use the saved binding check.
func TestPackageRegistryIsCheckedByTheJobAndNeverByThePage(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusOK) }))
	t.Cleanup(server.Close)
	b, err := a.Integrations.Bindings.Save(t.Context(), "packages", integration.CapRegistry, "registry", mustJSON(map[string]any{"format": "npm", "url": server.URL}), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	_ = b
	h := a.Handler()
	read := func() {
		t.Helper()
		w := authzCall(h, "boss", http.MethodGet, "/api/v1/integrations", "")
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	read()
	if calls.Load() != 0 {
		t.Fatal("page checked the registry before the job")
	}
	binding, err := a.Integrations.Bindings.ByName(t.Context(), "packages")
	if err != nil {
		t.Fatal(err)
	}
	kind := apptownJobKinds(a)[integration.HealthObservationJob]
	if _, err := kind.Run(t.Context(), binding.HealthObservationKey(), nil); err != nil {
		t.Fatal(err)
	}
	result, err := a.Integrations.RecordedHealth(t.Context(), binding.Name)
	if err != nil || result.StateLabel != integration.HealthPassed || calls.Load() != 1 {
		t.Fatalf("job result: %+v %v calls=%d", result, err, calls.Load())
	}
	read()
	if calls.Load() != 1 {
		t.Fatal("page renewed the reading")
	}
	if _, err := a.Integrations.TestSaved(t.Context(), binding.Name, connectorHealthFor(a)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("Check now did not run the same single probe: %d", calls.Load())
	}
	read()
	if calls.Load() != 2 {
		t.Fatal("page checked after Check now")
	}
}
