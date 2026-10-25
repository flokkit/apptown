package deployment

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
)

func TestProviderIdentityRetirementPreservesReferencedConnection(t *testing.T) {
	for _, reference := range []string{"target", "repository"} {
		t.Run(reference, func(t *testing.T) {
			db, targets, target := targetRetirementFixture(t)
			bindings := integration.NewWithClock(db, nil)
			binding, err := bindings.ByName(t.Context(), target.Provider)
			if err != nil {
				t.Fatal(err)
			}
			if reference == "target" {
				// Reproduce migration171's local association of a preexisting target;
				// ordinary legacy target creation cannot create verified remote scope.
				// sql-guarantee: reproduce migration171 association, unavailable through legacy target creation.
				if _, err := db.SQL().ExecContext(t.Context(), "UPDATE targets SET connection_id=$1 WHERE id=$2", binding.ID, target.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				project := createTestProject(t, db, "retained-repo", "Retained repo")
				recorder := repositoryRecorder{db: db, project: project, connection: binding.ID, connector: binding.Name, now: time.Now}
				if err := recorder.Created(t.Context(), "registry", "application"); err != nil {
					t.Fatal(err)
				}
				owns, err := recorder.Owns(t.Context(), "registry", "application")
				if err != nil || !owns {
					t.Fatalf("saved ownership: %v %v", owns, err)
				}
				if _, err := bindings.Save(t.Context(), "different-connection", digitalocean.CapCloudAccount, "digitalocean", `{}`, "fixture"); err != nil {
					t.Fatal(err)
				}
				other, err := bindings.ByName(t.Context(), "different-connection")
				if err != nil {
					t.Fatal(err)
				}
				recorder.connection = other.ID // same display name must not confer ownership
				owns, err = recorder.Owns(t.Context(), "registry", "application")
				if err != nil || owns {
					t.Fatalf("ownership crossed connection identity: %v %v", owns, err)
				}
			}
			if err := targets.Retire(t.Context(), target.Name, "alice"); err != nil {
				t.Fatal(err)
			}
			saved, err := targets.ByID(t.Context(), target.ID)
			if err != nil || saved.Status != TargetRetired {
				t.Fatalf("retirement: %+v %v", saved, err)
			}
			var offers int
			if err := db.SQL().QueryRowContext(t.Context(), "SELECT count(*) FROM target_offers WHERE target_id=$1", target.ID).Scan(&offers); err != nil || offers != 0 {
				t.Fatalf("offers remain: %d %v", offers, err)
			}
			current, err := bindings.ByID(t.Context(), binding.ID)
			if err != nil || current.ID != binding.ID {
				t.Fatalf("retained identity disappeared: %+v %v", current, err)
			}
			if err := bindings.Delete(t.Context(), binding.Name, "alice"); !errors.Is(err, integration.ErrAttached) {
				t.Fatalf("referenced connection deletion: %v", err)
			}
		})
	}
}

func TestProviderCredentialCallbackRefusesReusedConnectionName(t *testing.T) {
	db := testDB(t)
	integrations := integration.NewService(db, integration.Dependencies{})
	bindings := integrations.Bindings
	if _, err := bindings.Save(t.Context(), "account", digitalocean.CapCloudAccount, "digitalocean", `{}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	original, err := bindings.ByName(t.Context(), "account")
	if err != nil {
		t.Fatal(err)
	}
	providers := &Providers{integrations: integrations}
	callback := providers.cloudClientForID(original.ID)
	if err := bindings.Delete(t.Context(), "account", "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := bindings.Save(t.Context(), "account", digitalocean.CapCloudAccount, "digitalocean", `{}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	// Refusal must come from the missing immutable binding, before credentials
	// are requested. No secret/provider adapter is installed in this fixture.
	_, _, err = callback(t.Context())
	if err == nil || !strings.Contains(err.Error(), integration.ErrNoBinding.Error()) {
		t.Fatalf("replacement binding reached credential resolution: %v", err)
	}
}
