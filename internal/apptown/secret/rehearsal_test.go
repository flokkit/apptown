package secret

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids"
)

// A restore rehearsal is given exactly the bound secrets its rehearsal
// profile names, never by inheritance and never by the ordinary fallback
// to default, and every generated secret is made fresh for it.
//
// Kills: reading the rehearsal profile after inheritance (SMTP_PASSWORD
// would arrive); falling back to default with no rehearsal profile;
// using the source's generated value; refusing nothing on an inheriting
// profile; requiring a withheld secret; preferring the source's binding to
// the stand-in the profile declares, or taking a declared empty value for
// none (either way the bucket's write key would arrive).
func TestARehearsalGetsOnlyTheSecretsItsProfileNames(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "orders", Name: "Orders"})
	})
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(db, Config{DataDir: t.TempDir()})
	c := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web"}}}
	// The source deployment holds a production mail password, a read-only
	// lookup key, an uploads bucket's write key and a generated signing
	// key, all bound.
	for name, value := range map[string]string{"SMTP_PASSWORD": "prod-smtp", "LOOKUP_KEY": "prod-lookup", "AWS_SECRET_ACCESS_KEY": "prod-s3-write", "SECRET_KEY_BASE": "prod-signing"} {
		version, err := s.Vault.Put(t.Context(), "prod-scope", name, value, "test")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Bindings.Put(t.Context(), id, Binding{Deployment: "prod", Name: name, Store: StoreInternal, Ref: "prod-scope", Version: fmt.Sprint(version), UpdatedBy: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	const head = "[project]\nname = \"orders\"\nrevision = \"1.0\"\n[profiles.default]\nSMTP_PASSWORD = { description = \"production mail\" }\nLOOKUP_KEY = { description = \"lookups\" }\nAWS_SECRET_ACCESS_KEY = { description = \"uploads bucket\" }\nSECRET_KEY_BASE = { description = \"signing\", generate = true }\n"
	resolve := func(t *testing.T, spec string) (rehearsal, error) {
		t.Helper()
		ws := t.TempDir()
		if err := os.WriteFile(filepath.Join(ws, SpecFile), []byte(head+spec), 0o600); err != nil {
			t.Fatal(err)
		}
		return s.ResolveRehearsal(t.Context(), id, "prod", ws, c)
	}
	held := func(t *testing.T, r rehearsal, want map[string]string, withheld ...string) {
		t.Helper()
		env := r.Delivery.EnvFor("web")
		for name, value := range want {
			if env[name] != value {
				t.Errorf("%s: got %q, want %q", name, env[name], value)
			}
		}
		for name, value := range env {
			if _, ok := want[name]; !ok && name != "SECRET_KEY_BASE" {
				t.Errorf("%s was delivered (%q)", name, value)
			}
		}
		if key := env["SECRET_KEY_BASE"]; key == "" || key == "prod-signing" {
			t.Errorf("the generated key is %q: it must be made for the rehearsal", key)
		}
		if !slices.Equal(r.Withheld, withheld) {
			t.Errorf("withheld %v, want %v", r.Withheld, withheld)
		}
	}

	t.Run("no rehearsal profile delivers no bound secret", func(t *testing.T) {
		r, err := resolve(t, "")
		if err != nil {
			t.Fatal(err)
		}
		held(t, r, map[string]string{}, "AWS_SECRET_ACCESS_KEY", "LOOKUP_KEY", "SMTP_PASSWORD")
	})
	t.Run("an empty non-inheriting profile delivers no bound secret", func(t *testing.T) {
		r, err := resolve(t, "[profiles.rehearsal]\ndefaults = { inherit = false }\n")
		if err != nil {
			t.Fatal(err)
		}
		held(t, r, map[string]string{}, "AWS_SECRET_ACCESS_KEY", "LOOKUP_KEY", "SMTP_PASSWORD")
	})
	t.Run("a named secret comes from the source's binding", func(t *testing.T) {
		r, err := resolve(t, "[profiles.rehearsal]\ndefaults = { inherit = false }\nLOOKUP_KEY = { description = \"lookups\" }\n")
		if err != nil {
			t.Fatal(err)
		}
		held(t, r, map[string]string{"LOOKUP_KEY": "prod-lookup"}, "AWS_SECRET_ACCESS_KEY", "SMTP_PASSWORD")
	})
	t.Run("a declared stand-in is delivered, never the bound value", func(t *testing.T) {
		r, err := resolve(t, "[profiles.rehearsal]\ndefaults = { inherit = false }\nAWS_SECRET_ACCESS_KEY = { description = \"uploads bucket\", default = \"rehearsal-stand-in\" }\n")
		if err != nil {
			t.Fatal(err)
		}
		held(t, r, map[string]string{"AWS_SECRET_ACCESS_KEY": "rehearsal-stand-in"}, "LOOKUP_KEY", "SMTP_PASSWORD")
		for _, receipt := range r.Delivery.Receipts {
			if receipt.Name == "AWS_SECRET_ACCESS_KEY" && (receipt.Store != "default" || receipt.Version != "default") {
				t.Errorf("the stand-in's receipt says %s at %s, want the profile's default", receipt.Store, receipt.Version)
			}
		}
	})
	t.Run("a declared empty value is delivered empty, never the bound value", func(t *testing.T) {
		r, err := resolve(t, "[profiles.rehearsal]\ndefaults = { inherit = false }\nAWS_SECRET_ACCESS_KEY = { description = \"uploads bucket\", default = \"\" }\n")
		if err != nil {
			t.Fatal(err)
		}
		held(t, r, map[string]string{"AWS_SECRET_ACCESS_KEY": ""}, "LOOKUP_KEY", "SMTP_PASSWORD")
		if value, given := r.Delivery.EnvFor("web")["AWS_SECRET_ACCESS_KEY"]; !given || value != "" {
			t.Errorf("the declared empty value was not delivered: %q given=%v", value, given)
		}
	})
	t.Run("an inheriting profile is refused", func(t *testing.T) {
		_, err := resolve(t, "[profiles.rehearsal]\nLOOKUP_KEY = { description = \"lookups\" }\n")
		if !errors.Is(err, ErrRehearsalInherits) {
			t.Fatalf("got %v, want the inheritance refusal", err)
		}
	})
}
