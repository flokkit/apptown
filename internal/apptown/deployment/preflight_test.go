package deployment

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// preflightApp has a worker part and a postgres need, whose DATABASE_URL
// boot injects.
const preflightApp = `app:
  web:
    image: reg.example/app:1
    ports: ["8080"]
    public: true
  worker:
    image: reg.example/worker:1
    command: ["node", "worker.js"]
init:
  - name: migrate
    image: reg.example/app:1
    command: ["./migrate.sh"]
needs:
  postgres: {}
`

// preflightSpec requires WP_HOME of the worker alone, and generates
// AUTH_KEY for everyone.
const preflightSpec = `[project]
name = "p"
revision = "1.0"

[profiles.default]
WP_HOME  = { description = "the site's address" }
AUTH_KEY = { description = "signs cookies", generate = true }

[scopes.worker]
secrets = ["WP_HOME", "AUTH_KEY"]

[scopes.web]
secrets = ["AUTH_KEY"]
`

// Preflight judges a release with delivery's rules and none of its
// effects: a main deployment whose WP_HOME only preview binds is refused
// as missing that setting, for the consumer it would reach, without its
// value; a generated secret is neither reported missing nor minted; the
// app's postgres need is neither provisioned nor its DATABASE_URL counted
// as a missing setting; once main binds WP_HOME it passes, still with no
// effect; and a store that cannot be read is unassessable, not missing.
func TestPreflightJudgesTheReleaseWithoutEffects(t *testing.T) {
	t.Parallel()
	s, _, client, dir, projectID := hostWorld(t, "")
	ws := filepath.Join(dir, "tree")
	if err := os.MkdirAll(filepath.Join(ws, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte(preflightApp), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, secret.SpecFile), []byte(preflightSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := docker.ReadSelectedContract(ws)
	if err != nil {
		t.Fatal(err)
	}
	local, err := s.Targets.ByName(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	main, err := s.Deployments.EnsureStanding(t.Context(), projectID, "main", local.ID, Attributes{Feed: "landing", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	secrets := s.env.secrets
	bind := func(deployment, store, value string) {
		t.Helper()
		scope := secret.Scope("p", deployment, "")
		if store == secret.StoreInternal {
			if _, err := secrets.Vault.Put(t.Context(), scope, "WP_HOME", value, "test"); err != nil {
				t.Fatal(err)
			}
		}
		if err := secrets.Bindings.Put(t.Context(), projectID, secret.Binding{Deployment: deployment, Name: "WP_HOME", Store: store, Ref: scope, Version: "1", UpdatedBy: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	noEffects := func(when string) {
		t.Helper()
		if cmds := client.Commands(); len(cmds) != 0 {
			t.Errorf("%s: preflight ran Docker: %v", when, cmds)
		}
		bound, err := secrets.Bindings.For(t.Context(), projectID, "main")
		if err != nil {
			t.Fatal(err)
		}
		if _, generated := bound["AUTH_KEY"]; generated {
			t.Errorf("%s: preflight bound the generated AUTH_KEY", when)
		}
		if _, _, err := secrets.Vault.Get(t.Context(), secret.Scope("p", "main", ""), "AUTH_KEY"); !errors.Is(err, secret.ErrNotStored) {
			t.Errorf("%s: preflight generated AUTH_KEY: %v", when, err)
		}
	}
	in := PreflightInput{ProjectID: projectID, DeploymentID: main.ID, Commit: "c0ffee", Workspace: ws, Contract: c}

	bind("preview", secret.StoreInternal, "https://preview.example")
	_, err = s.Preflight(t.Context(), in)
	var refused *PreflightError
	if !errors.As(err, &refused) || refused.Category != PreflightMissingSettings || strings.Join(refused.Missing, ",") != "WP_HOME" || strings.Join(refused.Consumers, ",") != "worker" {
		t.Fatalf("main without WP_HOME: %#v", err)
	}
	if refused.Check.Deployment != "main" || refused.Check.DeploymentID != main.ID || refused.Check.TargetID != local.ID || refused.Check.Commit != "c0ffee" || refused.Check.CheckedAt.IsZero() {
		t.Errorf("the refusal is not bound to what was checked: %+v", refused.Check)
	}
	if msg := err.Error(); !strings.Contains(msg, "main") || !strings.Contains(msg, "WP_HOME") || !strings.Contains(msg, "Settings → Secrets") || strings.Contains(msg, "preview.example") {
		t.Errorf("refusal words: %q", msg)
	}
	noEffects("refused")

	bind("main", secret.StoreInternal, "https://main.example")
	check, err := s.Preflight(t.Context(), in)
	if err != nil || check.Deployment != "main" {
		t.Fatalf("main with WP_HOME: %+v %v", check, err)
	}
	noEffects("passed")

	// This AppTown has no Infisical: a binding there cannot be read.
	bind("main", secret.StoreInfisical, "")
	_, err = s.Preflight(t.Context(), in)
	if !errors.As(err, &refused) || refused.Category != PreflightUnassessable || len(refused.Missing) != 0 || strings.Contains(err.Error(), "no value") {
		t.Fatalf("an unreadable store: %#v", err)
	}
	noEffects("unassessable")
}
