package deployment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// Each deployment's needs are started with, and its app given, a password
// of its own: not the catalogue's, not another deployment's, and the same
// at its next boot. A preview's is its own as well, the same each time it
// is prepared, so its identity does not move between preparations.
func TestEachDeploymentAndPreviewHasItsOwnNeedCredentials(t *testing.T) {
	t.Parallel()
	s, db, client, _, projectID := hostWorld(t, "")
	c, ws := llmContract(t, "app:\n  web:\n    image: reg.example/app:1\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres: {}\n")
	booted := func(dep *Deployment) string {
		t.Helper()
		res, err := bootDeployment(s, client, dep, c, ws)
		if err != nil {
			t.Fatal(err)
		}
		need := ""
		for _, svc := range res.Stack.Services {
			if svc.Name == "postgres" {
				need = svc.Env["POSTGRES_PASSWORD"]
			}
		}
		app := res.Stack.Plan.PublicPart().Env["PGPASSWORD"]
		if len(need) != 64 || app != need {
			t.Fatalf("%s: the need was started with %q and the app given %q", dep.Name, need, app)
		}
		return need
	}
	main, staging := llmDeployment(t, s, db, projectID, "main"), llmDeployment(t, s, db, projectID, "staging")
	first := booted(main)
	if again := booted(main); again != first {
		t.Fatal("the deployment's password changed at its next boot")
	}
	if other := booted(staging); other == first {
		t.Fatal("two deployments share a password")
	}
	prepared := func() docker.Prepared {
		t.Helper()
		p, err := s.env.PrepareBoot(t.Context(), projectID, "p", "preview", ws, c, s.env.NeedCredentials(PreviewNeedScope("apptown-preview-change-7")))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	a, b := prepared(), prepared()
	preview := a.NeedEnv["PGPASSWORD"]
	if len(preview) != 64 || preview == first || a.Public().Digest() != b.Public().Digest() {
		t.Fatalf("the preview's password %q (main's %q), its identity %s then %s", preview, first, a.Public().Digest(), b.Public().Digest())
	}
}

// A preview's identity follows the secret values it is given, not only
// their receipts: a built-in secret removed and set again comes back as
// version 1, so its receipt is the same while its value is not. Preparing
// the same values again gives the same identity, and the identity names no
// value.
func TestAPreviewsIdentityFollowsItsSecretValues(t *testing.T) {
	t.Parallel()
	s, _, _, dir, projectID := hostWorld(t, "")
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
	secrets, scope := s.env.secrets, secret.Scope("p", "preview", "")
	set := func(value string) {
		t.Helper()
		if err := secrets.Vault.Delete(t.Context(), scope, "WP_HOME"); err != nil {
			t.Fatal(err)
		}
		if _, err := secrets.Vault.Put(t.Context(), scope, "WP_HOME", value, "test"); err != nil {
			t.Fatal(err)
		}
		if err := secrets.Bindings.Put(t.Context(), projectID, secret.Binding{Deployment: "preview", Name: "WP_HOME", Store: secret.StoreInternal, Ref: scope, Version: "1", UpdatedBy: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	prepare := func() docker.PublicPrepared {
		t.Helper()
		p, err := s.env.PrepareBoot(t.Context(), projectID, "p", "preview", ws, c, s.env.NeedCredentials(PreviewNeedScope("apptown-preview-change-7")))
		if err != nil {
			t.Fatal(err)
		}
		return p.Public()
	}
	set("https://first.example")
	first := prepare()
	if again := prepare(); again.Digest() != first.Digest() {
		t.Fatal("the same values gave another identity")
	}
	set("https://second.example")
	second := prepare()
	if !reflect.DeepEqual(first.Receipts, second.Receipts) {
		t.Fatalf("fixture: the receipts differ (%+v, %+v), so they alone would tell the values apart", first.Receipts, second.Receipts)
	}
	if second.Digest() == first.Digest() {
		t.Fatal("a secret's value changed with the same receipt and the preview's identity did not")
	}
	public, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(public), "second.example") || len(second.SecretsFingerprint) != 64 {
		t.Fatalf("the public values: %s", public)
	}
}

// The fingerprint covers the integrations' environment, which may hold
// secrets and has no version: the same entries in any order give the same
// one, a changed value another, and it is keyed, so a reader cannot test
// guesses of a value against it.
func TestTheIntegrationEnvironmentIsIdentifiedWithoutItsValues(t *testing.T) {
	t.Parallel()
	s, _, _, _, _ := hostWorld(t, "")
	fingerprint := func(env ...string) string {
		t.Helper()
		f, err := s.env.secretsFingerprint(t.Context(), nil, env)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	first := fingerprint("ERRORS_DSN=https://key-one@errors/1", "FEEDBACK_APP_KEY=hunter2")
	if again := fingerprint("FEEDBACK_APP_KEY=hunter2", "ERRORS_DSN=https://key-one@errors/1"); again != first {
		t.Fatal("the same environment in another order gave another fingerprint")
	}
	if changed := fingerprint("ERRORS_DSN=https://key-one@errors/1", "FEEDBACK_APP_KEY=hunter3"); changed == first {
		t.Fatal("a changed integration value kept the fingerprint")
	}
	plain := sha256.Sum256([]byte(`{"secrets":null,"env":["ERRORS_DSN=https://key-one@errors/1","FEEDBACK_APP_KEY=hunter2"]}`))
	if first == hex.EncodeToString(plain[:]) {
		t.Fatal("the fingerprint is an unkeyed hash")
	}
	if none := fingerprint(); none != "" {
		t.Fatalf("nothing to identify has the fingerprint %q", none)
	}
}
