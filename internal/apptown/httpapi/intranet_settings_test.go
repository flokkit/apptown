package httpapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/ids"
)

// The intranet's release deployment is the one its settings name, and "lan"
// only when it is not wired up. Settings that cannot be read are an error
// for the tier map and the secrets page, never "lan" or a shorter list.
//
// Kills: a failed read of the intranet's settings taken as not wired.
func TestUnreadableIntranetSettingsAreNotTheDefault(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	dir := t.TempDir()
	integrations := integration.NewService(db, integration.Dependencies{Projects: project.New(db, nil), Secrets: secret.NewService(db, secret.Config{DataDir: dir})})
	if _, err := integrations.Bindings.Upsert(t.Context(), "portainer-deploy", integration.CapDeploy, "portainer", `{"urlFrom":"PORTAINER_URL","apiKeyFrom":"PORTAINER_API_KEY","appsDomainFrom":"APPTOWN_APPS_DOMAIN","releaseDeployment":"intranet-prd"}`, true); err != nil {
		t.Fatal(err)
	}
	tiers, secrets := Tiers{integrations: integrations}, Secrets{integrations: integrations}
	// Not wired up: the default, and no intranet deployment on the page.
	if got, err := tiers.releaseDeployment(t.Context()); err != nil || got != "lan" {
		t.Fatalf("not wired: %q %v", got, err)
	}
	if got, err := secrets.secretDeployments(t.Context(), ids.Nil); err != nil || len(got) != 2 {
		t.Fatalf("not wired: %v %v", got, err)
	}
	// Wired: the deployment its settings name.
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("PORTAINER_URL=https://portainer.example\nPORTAINER_API_KEY=k\nAPPTOWN_APPS_DOMAIN=apps.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := tiers.releaseDeployment(t.Context()); err != nil || got != "intranet-prd" {
		t.Fatalf("wired: %q %v", got, err)
	}
	if got, err := secrets.secretDeployments(t.Context(), ids.Nil); err != nil || len(got) != 3 || got[2] != "intranet-prd" {
		t.Fatalf("wired: %v %v", got, err)
	}
	// Unreadable: an error, never the default.
	if err := os.Remove(filepath.Join(dir, "secrets.env")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "secrets.env"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := tiers.releaseDeployment(t.Context()); err == nil {
		t.Fatalf("unreadable settings gave %q", got)
	}
	if got, err := secrets.secretDeployments(t.Context(), ids.Nil); err == nil {
		t.Fatalf("unreadable settings gave %v", got)
	}
}
