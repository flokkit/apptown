package httpapi

import (
	"strings"
	"testing"
)

// A Fly.io target is attached by naming the AppTown secret that holds an
// org token and a region. The org is the token's own answer; a dry run asks
// Fly and records nothing; attaching records the connector and the target
// together; an unavailable Machines region, a token that reaches no org
// and a token for apps alone are refused before anything is recorded; and
// retiring the target removes its connector.
//
// Kills: an attach that guesses the org, records on a dry run, accepts a
// deploy token, or leaves the connector behind its retired target.
func TestAttachFlyTarget(t *testing.T) {
	t.Parallel()
	w, api := targetFlyWorld(t)
	call := func(body string) (int, map[string]any) {
		return targetCall(t, w.targets.AttachTarget, "POST", "/api/v1/targets", body, nil)
	}
	code, out := call(`{"kind":"fly-machines","name":"fly-syd","tokenFrom":"FLY_API_TOKEN","region":"syd","dryRun":true}`)
	if detail, _ := out["detail"].(string); code != 200 || !strings.Contains(detail, "org acme, region syd") || !strings.Contains(detail, "every app in the org acme") {
		t.Fatalf("dry run: %d %v", code, out)
	}
	if _, err := w.deployment.Targets.ByName(t.Context(), "fly-syd"); err == nil {
		t.Fatal("a dry run recorded a target")
	}
	if cfg, err := w.integrations.ConnectorConfig(t.Context(), "fly-syd", "cloud-account"); err != nil || cfg != nil {
		t.Fatalf("a dry run recorded a connector: %v %v", cfg, err)
	}
	for body, want := range map[string]string{
		`{"kind":"fly-machines","name":"fly-x","tokenFrom":"NO_SUCH_TOKEN","region":"syd"}`:                   "does not resolve",
		`{"kind":"fly-machines","name":"fly-x","tokenFrom":"FLY_API_TOKEN","region":"missing"}`:               "no region",
		`{"kind":"fly-machines","name":"fly-x","tokenFrom":"FLY_API_TOKEN","region":"syd","org":"elsewhere"}`: "does not reach the org elsewhere",
	} {
		if code, out := call(body); code != 422 || !strings.Contains(out["error"].(string), want) {
			t.Fatalf("%s: %d %v, want %q", body, code, out, want)
		}
	}
	if code, out := call(`{"kind":"fly-machines","name":"fly-jnb","tokenFrom":"FLY_API_TOKEN","region":"jnb","dryRun":true}`); code != 200 {
		t.Fatalf("a Machines region must not require Managed Postgres: %d %v", code, out)
	}
	code, out = call(`{"kind":"fly-machines","name":"fly-syd","tokenFrom":"FLY_API_TOKEN","region":"syd"}`)
	if code != 200 {
		t.Fatalf("attach: %d %v", code, out)
	}
	target, err := w.deployment.Targets.ByName(t.Context(), "fly-syd")
	if err != nil || target.Kind != "fly-machines" || target.Address["org"] != "acme" || target.Address["region"] != "syd" || target.NeedsMode != "emulators" || !target.Capabilities.PersistentVolumes || target.Ingress["domain"] != "fly.dev" {
		t.Fatalf("target: %+v %v", target, err)
	}
	cfg, err := w.integrations.ConnectorConfig(t.Context(), "fly-syd", "cloud-account")
	if err != nil || cfg["tokenFrom"] != "FLY_API_TOKEN" || cfg["org"] != "acme" || cfg["region"] != "syd" {
		t.Fatalf("connector: %+v %v", cfg, err)
	}
	// A token for named apps alone cannot create one.
	api.Orgs, api.AppsOnly = nil, []string{"web"}
	if code, out := call(`{"kind":"fly-machines","name":"fly-y","tokenFrom":"FLY_API_TOKEN","region":"syd"}`); code != 422 || !strings.Contains(out["error"].(string), "org token") {
		t.Fatalf("a deploy token: %d %v", code, out)
	}
	api.Orgs, api.AppsOnly = []string{"acme"}, nil
	if code, out := targetCall(t, w.targets.RetireTarget, "POST", "/api/v1/targets/fly-syd/retire", `{"confirm":"fly-syd"}`, map[string]string{"name": "fly-syd"}); code != 200 {
		t.Fatalf("retire: %d %v", code, out)
	}
	if cfg, err := w.integrations.ConnectorConfig(t.Context(), "fly-syd", "cloud-account"); err != nil || cfg != nil {
		t.Fatalf("the retired target kept its connector: %v %v", cfg, err)
	}
}
