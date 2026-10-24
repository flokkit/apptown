package verification

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/stock"
)

// A new WordPress project is makeable from the published stock content, while
// its initial administrator credential never reaches the long-lived server.
func TestWordPressStarterScopesItsInitialAdministratorCredential(t *testing.T) {
	packs, err := wow.ReadStockPacks(stock.FS)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, pack := range packs {
		if pack.Name == "wordpress-bedrock" {
			found = true
			var proposed []string
			for _, svc := range pack.Services {
				proposed = append(proposed, svc.Name)
			}
			if !slices.Equal(proposed, []string{"mariadb", "s3"}) {
				t.Fatalf("WordPress must propose its deployment-owned database and object storage, proposes %v", proposed)
			}
		}
	}
	if !found {
		t.Fatal("WordPress pack is absent from the stock library")
	}
	raw, err := fs.ReadFile(stock.FS, "seeds/wordpress-bedrock/.apptown/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	contract, err := appcontract.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = fs.ReadFile(stock.FS, "seeds/wordpress-bedrock/secretspec.toml")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := secret.ParseSpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.CheckScopesAgainst(contract); err != nil {
		t.Fatal(err)
	}
	consumers, all := spec.ConsumersOf("WP_ADMIN_PASSWORD")
	if all || len(consumers) != 1 || consumers[0] != "init-install" {
		t.Fatalf("initial admin password escapes init: consumers=%v all=%v", consumers, all)
	}
	for _, name := range []string{"WP_HOME", "AUTH_KEY", "SECURE_AUTH_KEY", "LOGGED_IN_KEY", "NONCE_KEY", "AUTH_SALT", "SECURE_AUTH_SALT", "LOGGED_IN_SALT", "NONCE_SALT"} {
		consumers, all := spec.ConsumersOf(name)
		if all || !slices.Contains(consumers, "web") || !slices.Contains(consumers, "init-install") {
			t.Fatalf("%s must reach web and init explicitly, got %v all=%v", name, consumers, all)
		}
	}
	// The site's address is the deployment's own, so no person has to supply
	// it before a preview or main deployment can boot.
	for _, profile := range spec.ProfileNames() {
		for _, s := range spec.SecretsIn(profile) {
			if s.Name == "WP_HOME" && (s.Required || s.Generate) {
				t.Fatalf("profile %s makes WP_HOME a value someone must supply (required=%v generate=%v)", profile, s.Required, s.Generate)
			}
		}
	}
	if len(spec.Ignored) != 0 {
		t.Fatalf("starter promises unsupported secret features: %v", spec.Ignored)
	}
}

// Uploads live in the deployment's own s3 need, so nobody types an S3
// setting: the starter declares the need, and its secret declaration
// declares none of what the need gives the app (read from the catalogue's
// contract) nor a public media address, which the need does not have.
func TestWordPressStarterTakesItsUploadsStoreFromTheS3Need(t *testing.T) {
	raw, err := fs.ReadFile(stock.FS, "seeds/wordpress-bedrock/.apptown/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	contract, err := appcontract.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(contract.Needs, "s3") {
		t.Fatalf("the starter does not declare the s3 need: %v", contract.Needs)
	}
	raw, err = fs.ReadFile(stock.FS, "seeds/wordpress-bedrock/secretspec.toml")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := secret.ParseSpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range s3Settings(t) {
		for _, profile := range spec.ProfileNames() {
			for _, s := range spec.SecretsIn(profile) {
				if s.Name == name {
					t.Errorf("profile %s declares %s, which a person would have to supply", profile, name)
				}
			}
		}
	}
}

// An existing project adopts a new pack version through a Change whose agent
// is told an empty change is right when every check already holds. So the
// pack's check must fail on the starter before the s3 need (rc.5: MariaDB
// alone, six S3 settings in secretspec.toml), or that project keeps asking
// for them. It names every variable the need provides.
func TestWordPressUploadsCheckFailsOnTheHandEnteredS3Starter(t *testing.T) {
	items, _ := wow.ReadStockGuidance(stock.FS, nil)
	var check *conformance.CheckDeclaration
	for _, it := range items {
		if it.Name != "lang/selection/wordpress-bedrock" {
			continue
		}
		var cs []conformance.CheckDeclaration
		if err := json.Unmarshal([]byte(it.ChecksJSON), &cs); err != nil {
			t.Fatal(err)
		}
		for i := range cs {
			if cs[i].Name == "uploads-through-s3-need" {
				check = &cs[i]
			}
		}
	}
	if check == nil {
		t.Fatal("the WordPress guidance has no uploads-through-s3-need check")
	}
	for _, name := range s3Settings(t) {
		if !strings.Contains(check.Sh, name) {
			t.Errorf("the check does not look for %s", name)
		}
	}
	var spec strings.Builder
	spec.WriteString("[project]\nname = \"wordpress\"\n\n[profiles.default]\n")
	for _, name := range s3Settings(t) {
		fmt.Fprintf(&spec, "%s = {description = \"%s\", required = true}\n", name, name)
	}
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"composer.json":     `{"require": {"humanmade/s3-uploads": "3.0.13"}}`,
		".apptown/app.yaml": "app:\n  web:\n    build: .\n    public: true\nneeds:\n  mariadb: {version: \"11.4.13\"}\n",
		"secretspec.toml":   spec.String(),
	})
	results, _, errs := NewEvaluator(localChecks{}, nil, stock.Versions()).runShChecks(context.Background(), stock.Images().Sh, []conformance.DeclaredCheck{{Block: "lang/selection/wordpress-bedrock", Version: 1, Check: *check}}, dir, "t-wordpress-rc5")
	if errs != 0 || len(results) != 1 {
		t.Fatalf("the check did not run on the rc.5 starter: %d errors, %v", errs, results)
	}
	if r := results[0]; r.Verdict != workflow.VerdictFail {
		t.Fatalf("the rc.5 starter passes %s: %s %s", r.Check, r.Verdict, r.Output)
	} else {
		for _, want := range append([]string{"declares no s3 need"}, s3Settings(t)...) {
			if !strings.Contains(r.Output, want) {
				t.Errorf("the finding does not name %q: %s", want, r.Output)
			}
		}
	}
}

// s3Settings are the names a WordPress starter must not ask a person for:
// every variable the s3 need provides, and the public media address the
// need does not give.
func s3Settings(t *testing.T) []string {
	t.Helper()
	raw, err := fs.ReadFile(stock.FS, "dependencies/s3.yaml")
	if err != nil {
		t.Fatal(err)
	}
	kind, err := dependency.Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(kind.Provides) == 0 {
		t.Fatal("the s3 kind provides nothing")
	}
	return append(slices.Sorted(maps.Keys(kind.Provides)), "S3_PUBLIC_URL")
}
