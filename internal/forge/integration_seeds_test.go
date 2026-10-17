package forge

import (
	"context"
	"strings"
	"testing"
)

// The integration seeds are the fifth stock kind, synced under the same
// contract as the ways of working (internal/forge/wow's Sync): idempotent
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
