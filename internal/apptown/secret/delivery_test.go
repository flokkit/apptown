package secret

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids"
)

func TestDeliveryOwnsProfilesScopesGeneratedValuesAndReceipts(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "demo", Name: "Demo"})
	})
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, SpecFile), []byte(testSpec), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewService(db, Config{DataDir: t.TempDir()})
	c := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web"}, {Name: "worker"}}, Init: []appcontract.InitContainer{{Name: "migrate"}}}
	// Missing required values report names, never fabricated values.
	_, err = s.Resolve(t.Context(), id, "demo", "main", ws, c)
	var missing *MissingError
	if !errors.As(err, &missing) || strings.Join(missing.Names, ",") != "MIGRATION_DB_ROLE,STRIPE_API_KEY" {
		t.Fatalf("missing: %v", err)
	}
	for name, value := range map[string]string{"STRIPE_API_KEY": "private-stripe", "MIGRATION_DB_ROLE": "private-role"} {
		version, err := s.Vault.Put(t.Context(), "test", name, value, "test")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Bindings.Put(t.Context(), id, Binding{Deployment: "main", Name: name, Store: StoreInternal, Ref: "test", Version: fmt.Sprint(version), UpdatedBy: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.Resolve(t.Context(), id, "demo", "main", ws, c)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewService(db, Config{DataDir: t.TempDir()}).Resolve(t.Context(), id, "demo", "main", ws, c)
	if err != nil {
		t.Fatal(err)
	}
	key := first.EnvFor("web")["SECRET_KEY_BASE"]
	if key == "" || second.EnvFor("web")["SECRET_KEY_BASE"] != key || first.EnvFor("worker")["SECRET_KEY_BASE"] != key {
		t.Fatal("generated value changed")
	}
	if first.EnvFor("worker")["STRIPE_API_KEY"] != "" || first.EnvFor("web")["MIGRATION_DB_ROLE"] != "" || first.EnvFor("init-migrate")["MIGRATION_DB_ROLE"] != "private-role" {
		t.Fatal("consumer isolation changed")
	}
	raw, _ := json.Marshal(first.Receipts)
	if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), key) {
		t.Fatal("receipt contains a resolved value")
	}
	if first.Profile != "default" || len(first.Receipts) != 3 {
		t.Fatalf("receipt metadata: %+v", first.Receipts)
	}
	// Preview can omit Stripe, while the migration value remains required.
	if err := s.Bindings.Put(t.Context(), id, Binding{Deployment: "preview", Name: "MIGRATION_DB_ROLE", Store: StoreInternal, Ref: "test", Version: "1", UpdatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	preview, err := s.Resolve(t.Context(), id, "demo", "preview", ws, c)
	if err != nil || preview.Profile != "preview" || preview.EnvFor("web")["STRIPE_API_KEY"] != "" {
		t.Fatalf("preview: %v", err)
	}
	if preview.EnvFor("web")["SECRET_KEY_BASE"] == key {
		t.Fatal("environment domains shared a generated value")
	}
	wantCount(t, "generated receipts", len(ledger(t, db, "secret.generated")), 2)
}

// Check is Resolve's judgement without its effects: the same missing
// names, with the consumers they would reach; a generated secret not yet
// bound counts as provided and is neither minted nor bound; a binding to
// a store that cannot be read is unavailable (unknown), never missing.
func TestCheckJudgesAsResolveDoesWithoutEffects(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "demo", Name: "Demo"})
	})
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, SpecFile), []byte(testSpec), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewService(db, Config{DataDir: t.TempDir()})
	c := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web"}, {Name: "worker"}}, Init: []appcontract.InitContainer{{Name: "migrate"}}}
	noneGenerated := func(when string) {
		t.Helper()
		bound, err := s.Bindings.For(t.Context(), id, "main")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := bound["SECRET_KEY_BASE"]; ok {
			t.Errorf("%s: Check bound the generated secret", when)
		}
		if _, _, err := s.Vault.Get(t.Context(), Scope("demo", "main", ""), "SECRET_KEY_BASE"); !errors.Is(err, ErrNotStored) {
			t.Errorf("%s: Check minted the generated secret: %v", when, err)
		}
		wantCount(t, when+": generated receipts", len(ledger(t, db, "secret.generated")), 0)
	}
	err = s.Check(t.Context(), id, "demo", "main", ws, c)
	var missing *MissingError
	if !errors.As(err, &missing) || strings.Join(missing.Names, ",") != "MIGRATION_DB_ROLE,STRIPE_API_KEY" || strings.Join(missing.Consumers, ",") != "init-migrate,web" || missing.Deployment != "main" {
		t.Fatalf("missing: %#v", err)
	}
	noneGenerated("missing")
	for name, value := range map[string]string{"STRIPE_API_KEY": "private-stripe", "MIGRATION_DB_ROLE": "private-role"} {
		if _, err := s.Vault.Put(t.Context(), "test", name, value, "test"); err != nil {
			t.Fatal(err)
		}
		if err := s.Bindings.Put(t.Context(), id, Binding{Deployment: "main", Name: name, Store: StoreInternal, Ref: "test", Version: "1", UpdatedBy: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Check(t.Context(), id, "demo", "main", ws, c); err != nil {
		t.Fatalf("all provided: %v", err)
	}
	noneGenerated("provided")
	// This service has no Infisical configured: a binding there is unknown.
	if err := s.Bindings.Put(t.Context(), id, Binding{Deployment: "main", Name: "STRIPE_API_KEY", Store: StoreInfisical, Ref: "STRIPE", Version: "infisical", UpdatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	err = s.Check(t.Context(), id, "demo", "main", ws, c)
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) || errors.As(err, &missing) || unavailable.Name != "STRIPE_API_KEY" || unavailable.Store != StoreInfisical {
		t.Fatalf("unreadable store: %#v", err)
	}
	if _, err := s.Resolve(t.Context(), id, "demo", "main", ws, c); !errors.As(err, &unavailable) {
		t.Fatalf("Resolve judges an unreadable store as Check does: %v", err)
	}
}
