package apptown

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/integration"
)

// An integration's listed health says whether AppTown checked it and
// when, by the server's clock; a kind with no check reads as not checked,
// never as working.
func TestIntegrationHealthSaysWhetherAndWhenItWasChecked(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	for _, b := range []struct{ name, capability, provider, config string }{
		{"store", "objectstore", "minio", `{}`},
		{"mirror", integration.CapRegistry, "npm", `{"format":"npm","url":"http://mirror.invalid"}`},
	} {
		if _, err := a.Integrations.Bindings.Save(context.Background(), b.name, b.capability, b.provider, b.config, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	before := time.Now().Add(-time.Second)
	w := authzCall(a.Handler(), "boss", "GET", "/api/v1/integrations", "")
	if w.Code != 200 {
		t.Fatalf("integrations: %d %s", w.Code, w.Body.String())
	}
	var list httpapi.IntegrationList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	got := map[string]integration.Health{}
	for _, e := range list.Integrations {
		got[e.Name] = e.Health
	}
	if h := got["store"]; h.Checked || h.OK || h.CheckedAt != "" {
		t.Fatalf("a kind with no check reads as checked: %+v", h)
	}
	h := got["mirror"]
	at, err := time.Parse(time.RFC3339, h.CheckedAt)
	if !h.Checked || !h.OK || err != nil || at.Before(before.Truncate(time.Second)) || at.After(time.Now().Add(time.Second)) {
		t.Fatalf("a checked kind has no server check time: %+v (%v)", h, err)
	}
	health := connectorHealthFor(a)
	if h := health.Test(t.Context(), "objectstore", "minio", map[string]any{}); h.Checked || h.OK {
		t.Fatalf("a test of a kind with no check passed: %+v", h)
	}
	if h := health.Health(t.Context(), integration.CapSecrets, "internal", nil); !h.Checked || h.CheckedAt == "" {
		t.Fatalf("a checked kind's reading is unstamped: %+v", h)
	}
}

// Testable is true exactly for the kinds whose Test connection asks the
// service with the configuration given: each answers a test of an empty
// configuration as checked, and the listing says so per entry,
// so the page never says such a kind cannot be checked.
func TestEveryKindSaysTrulyWhetherTestConnectionAsksIt(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	health := connectorHealthFor(a)
	for _, c := range integration.ConnectorCatalog() {
		if !health.Testable(c.Capability) {
			continue
		}
		if h, checked := health.test(t.Context(), c.Capability, c.Provider, map[string]any{}); !checked {
			t.Fatalf("%s says Test connection asks it, but an empty test is %+v (checked %v)", c.Capability, h, checked)
		}
	}
	if health.Testable("objectstore") || health.Testable(integration.CapSecrets) {
		t.Fatal("a kind tested only by its live check is not draft-tested")
	}
	if _, err := a.Integrations.Bindings.Save(context.Background(), "openrouter", integration.CapLLM, "openrouter", `{"provisioningKeyFrom":["OPENROUTER_PROVISIONING_KEY"],"maxLimit":5}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	w := authzCall(a.Handler(), "boss", "GET", "/api/v1/integrations", "")
	var list httpapi.IntegrationList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 {
		t.Fatalf("integrations: %d %v", w.Code, err)
	}
	for _, e := range list.Integrations {
		if e.Name == "openrouter" && (!e.Testable || e.Health.Checked) {
			t.Fatalf("AI models: testable %v, checked %v", e.Testable, e.Health.Checked)
		}
	}
}
