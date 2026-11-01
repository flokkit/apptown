package secret

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// The built-in store versions values, never keeps them in the clear,
// deletes every version of one at once, and mints its key exactly once
// with a receipt.
func TestVaultVersionsAndObfuscates(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	v := NewVault(db)
	if _, err := v.Put(t.Context(), "env:p:main", "EMPTY", "  ", "bls"); !errors.Is(err, errEmpty) {
		t.Fatalf("empty value: %v", err)
	}
	if _, _, err := v.Get(t.Context(), "env:p:main", "TOKEN"); !errors.Is(err, ErrNotStored) {
		t.Fatalf("nothing stored yet: %v", err)
	}
	v1, err := v.Put(t.Context(), "env:p:main", "TOKEN", "hunter2", "bls")
	if err != nil || v1 != 1 {
		t.Fatalf("first put: %v %d", err, v1)
	}
	if v2, _ := v.Put(t.Context(), "env:p:main", "TOKEN", "hunter3", "bls"); v2 != 2 {
		t.Fatalf("second put: %d", v2)
	}
	got, ver, err := v.Get(t.Context(), "env:p:main", "TOKEN")
	if err != nil || got != "hunter3" || ver != 2 {
		t.Fatalf("get: %v %q %d", err, got, ver)
	}
	var stored string
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT value FROM secret_values WHERE scope='env:p:main' AND name='TOKEN' AND version=2`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "hunter") {
		t.Fatalf("value stored in the clear: %q", stored)
	}
	k1, err := v.key(t.Context())
	if err != nil || len(k1) != 32 {
		t.Fatalf("key: %v %d", err, len(k1))
	}
	if err := v.Delete(context.Background(), "env:p:main", "TOKEN"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.Get(t.Context(), "env:p:main", "TOKEN"); !errors.Is(err, ErrNotStored) {
		t.Fatalf("delete removes every version: %v", err)
	}
	if err := v.Delete(context.Background(), "env:p:main", "TOKEN"); err != nil {
		t.Fatalf("deleting what is not stored: %v", err)
	}
	if k2, _ := NewVault(db).key(t.Context()); string(k2) != string(k1) {
		t.Fatal("the key must be minted once and kept")
	}
	var n int
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM audit_log WHERE kind='secrets.store-initialised'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("one receipt for the minting: %v %d", err, n)
	}
}

// A built-in binding must point at a stored version; unbinding removes
// the stored versions nothing else binds, with a receipt; an Infisical
// binding never touches stored values.
func TestBindingsAndProfiles(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	v, b := NewVault(db), NewBindings(db)
	scope := Scope("p", "main", "")
	if scope != "env:p:main" || Scope("p", "main", "project") != "project:p" {
		t.Fatalf("scopes: %s", scope)
	}
	if err := b.Put(t.Context(), pid, Binding{Deployment: "main", Name: "TOKEN", Store: StoreInternal, Ref: scope, Version: "1"}); !errors.Is(err, errVersionGone) {
		t.Fatalf("binding an unstored version: %v", err)
	}
	ver, err := v.Put(t.Context(), scope, "TOKEN", "hunter2", "bls")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Put(t.Context(), pid, Binding{Deployment: "main", Name: "TOKEN", Store: StoreInternal, Ref: scope, Version: "1", Generated: true, UpdatedBy: "bls"}); err != nil || ver != 1 {
		t.Fatalf("bind: %v", err)
	}
	if err := b.Put(t.Context(), pid, Binding{Deployment: "main", Name: "STRIPE", Store: StoreInfisical, Ref: "STRIPE_KEY", Version: "infisical"}); err != nil {
		t.Fatalf("bind infisical: %v", err)
	}
	all, err := b.For(t.Context(), pid, "main")
	if err != nil || len(all) != 2 || !all["TOKEN"].Generated || all["STRIPE"].Store != StoreInfisical {
		t.Fatalf("for: %v %+v", err, all)
	}
	if err := b.Unbind(context.Background(), pid, "main", "NOPE", "bls"); !errors.Is(err, ErrBindingMissing) {
		t.Fatalf("unbinding nothing: %v", err)
	}
	if err := b.Unbind(context.Background(), pid, "main", "TOKEN", "bls"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.Get(t.Context(), scope, "TOKEN"); !errors.Is(err, ErrNotStored) {
		t.Fatalf("the stored value must go with its last binding: %v", err)
	}
	var n int
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM audit_log WHERE kind='secret.unbound' AND subject='TOKEN' AND project_id=$1`, pid).Scan(&n); err != nil || n != 1 {
		t.Fatalf("unbind receipt: %v %d", err, n)
	}
	p := newProfiles(db, nil)
	if chosen, err := p.Chosen(t.Context(), pid, "main"); err != nil || chosen != "" {
		t.Fatalf("no choice yet: %v %q", err, chosen)
	}
	if err := p.Set(context.Background(), pid, "main", "preview", "test"); err != nil {
		t.Fatal(err)
	}
	if err := p.Set(context.Background(), pid, "main", "default", "test"); err != nil {
		t.Fatal(err)
	}
	if chosen, _ := p.Chosen(t.Context(), pid, "main"); chosen != "default" {
		t.Fatalf("chosen: %q", chosen)
	}
}

// Independent Vault instances must allocate distinct versions of one secret.
func TestSecretVersionsDistinctUnderRace(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	clients := []*Vault{NewVault(db), NewVault(db)}
	const writes = 8
	start := make(chan struct{})
	errors := make([]error, writes)
	var wg sync.WaitGroup
	for i := range writes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errors[i] = clients[i%len(clients)].Put(t.Context(), "env:p:main", "KEY", fmt.Sprintf("value %d", i), "bls")
		}()
	}
	close(start)
	wg.Wait()
	for _, err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var versions int
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(DISTINCT version) FROM secret_values WHERE scope='env:p:main' AND name='KEY'`).Scan(&versions); err != nil || versions != writes {
		t.Fatalf("distinct versions: %d %v", versions, err)
	}
	_, version, err := clients[0].Get(t.Context(), "env:p:main", "KEY")
	if err != nil || version != writes {
		t.Fatalf("latest version: %d %v", version, err)
	}
}

// The store's receipts and a binding carry the service's clock, however
// far it is from the machine's.
func TestTheStoreStampsByItsClock(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-72 * time.Hour).Truncate(time.Second).UTC()
	s := NewService(db, Config{DataDir: t.TempDir(), Now: func() time.Time { return at }})
	scope := Scope("p", "main", "")
	if _, err := s.Vault.Put(t.Context(), scope, "TOKEN", "hunter2", "bls"); err != nil {
		t.Fatal(err)
	}
	// The first value made the store's key, with its receipt, on the vault's clock.
	if lines, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: "secrets.store-initialised", Limit: 1}); err != nil || len(lines) != 1 || lines[0].At != at.Format(time.RFC3339) {
		t.Fatalf("store receipt %+v %v, want it stamped %v", lines, err, at)
	}
	if err := s.Bindings.Put(t.Context(), pid, Binding{Deployment: "main", Name: "TOKEN", Store: StoreInternal, Ref: scope, Version: "1", UpdatedBy: "bls"}); err != nil {
		t.Fatal(err)
	}
	all, err := s.Bindings.For(t.Context(), pid, "main")
	if err != nil || all["TOKEN"].UpdatedAt != at.Format(time.RFC3339) {
		t.Fatalf("bound %+v %v, want it updated at %v", all, err, at)
	}
	if err := s.Bindings.Unbind(context.Background(), pid, "main", "TOKEN", "bls"); err != nil {
		t.Fatal(err)
	}
	lines, err := audit.Recent(context.Background(), db, audit.Filter{KindPrefix: "secret.unbound", Limit: 1})
	if err != nil || len(lines) != 1 || lines[0].At != at.Format(time.RFC3339) {
		t.Fatalf("unbinding receipt %+v %v, want it stamped %v", lines, err, at)
	}
}

// NewVault and NewBindings are the store's parts on the real clock, for
// these tests; Service's carry AppTown's.
func NewVault(db *database.DB) *Vault       { return &Vault{db: db} }
func NewBindings(db *database.DB) *bindings { return &bindings{db: db} }
