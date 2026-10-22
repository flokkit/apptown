package apptown

import (
	"context"
	"strings"
	"testing"
)

// The integration seeds are the fifth stock kind, synced under the same
// contract as the ways of working (internal/apptown/wow's Sync): idempotent
// on re-sync, and a binding authored here under a seed's name is kept.
func TestSeedIntegrationsSyncIsIdempotentAndKeepsYours(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	if _, err := a.Integrations.Bindings.Save(context.Background(), "gitlab", "scm", "gitlab", `{"urlFrom":"MY_GITLAB"}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	first := a.SyncSeedIntegrations(t.Context())
	if first.Errors != 0 || first.Added == 0 || first.Kept != 1 {
		t.Fatalf("first sync seeds cleanly and keeps yours once: %+v", first)
	}
	again := a.SyncSeedIntegrations(t.Context())
	if again.Added != 0 || again.Updated != 0 || again.Errors != 0 || again.Kept != 1 {
		t.Fatalf("re-sync of unchanged seeds changes nothing: %+v", again)
	}
	binding, err := a.Integrations.Bindings.ByName(t.Context(), "gitlab")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(binding.ConfigJSON, "MY_GITLAB") || binding.Managed {
		t.Fatalf("the custom binding must be untouched: %s managed=%v", binding.ConfigJSON, binding.Managed)
	}
}

// The Portainer environment the intranet releases to is a setting
// (apptownd -portainer-environment), default local, recorded in the managed
// portainer-deploy binding at every start. A start with the name already
// recorded changes nothing (an upgrade keeps the operator's name and its
// version); a start with another name re-applies it.
//
// Kills: the seed's environment a literal (the operator's own name shipped
// as everyone's default); the setting not carried into the seed.
func TestThePortainerEnvironmentIsTheConfiguredSetting(t *testing.T) {
	t.Parallel()
	recorded := func(a *App) (string, int) {
		t.Helper()
		b, err := a.Integrations.Bindings.ByName(t.Context(), "portainer-deploy")
		if err != nil {
			t.Fatal(err)
		}
		env, _ := a.Integrations.ConnectorConfig(t.Context(), "portainer-deploy", "deploy")["environment"].(string)
		return env, b.Version
	}

	a := testApp(t)
	a.SyncSeedIntegrations(t.Context())
	if env, _ := recorded(a); env != "local" {
		t.Fatalf("with no setting the environment is %q, want local", env)
	}

	a = testApp(t, Config{PortainerEnvironment: "office-portainer"})
	a.SyncSeedIntegrations(t.Context())
	env, version := recorded(a)
	if env != "office-portainer" {
		t.Fatalf("the configured environment is recorded as %q", env)
	}
	if again := a.SyncSeedIntegrations(t.Context()); again.Updated != 0 {
		t.Fatalf("a start with the recorded name changes a seed: %+v", again)
	}
	if _, v := recorded(a); v != version {
		t.Fatalf("a start with the recorded name moved the binding from v%d to v%d", version, v)
	}

	a.cfg.portainerEnvironment = "second-portainer" // the next start's setting
	if c := a.SyncSeedIntegrations(t.Context()); c.Updated != 1 {
		t.Fatalf("a changed setting is re-applied once: %+v", c)
	}
	if env, v := recorded(a); env != "second-portainer" || v == version {
		t.Fatalf("after the change the binding says %q at v%d (was v%d)", env, v, version)
	}
}
