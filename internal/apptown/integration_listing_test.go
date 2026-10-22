package apptown

import (
	"context"
	"encoding/json"
	"reflect"
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
	if err := a.Integrations.Attachments.Attach(t.Context(), idstest.N(1), "objectstore", "store", `{}`, integration.Entered, "fixture", nil); err != nil {
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
