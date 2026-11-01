package secret

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret/internal/secretsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func unbindFixture(t *testing.T) (*database.DB, *Vault, *bindings, ids.ID, Binding) {
	t.Helper()
	db := testDB(t)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "secret-owner", Name: "Secret owner"})
	})
	if err != nil {
		t.Fatal(err)
	}
	vault, bindings := NewVault(db), NewBindings(db)
	b := Binding{Deployment: "main", Name: "TOKEN", Store: StoreInternal, Ref: "project:secret-owner", Version: "2", UpdatedBy: "alice"}
	for _, value := range []string{"private-old-value", "private-current-value"} {
		if _, err := vault.Put(t.Context(), b.Ref, b.Name, value, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	if err := bindings.Put(t.Context(), id, b); err != nil {
		t.Fatal(err)
	}
	return db, vault, bindings, id, b
}

func execUnbindTestSQL(t *testing.T, db *database.DB, query string) {
	t.Helper()
	if _, err := db.SQL().ExecContext(context.Background(), query); err != nil {
		t.Fatal(err)
	}
}

func concurrently[T any](n int, fn func(int) T) []T {
	out := make([]T, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			out[i] = fn(i)
		}()
	}
	wg.Wait()
	return out
}

func TestUnbindRollsBackMissingRecords(t *testing.T) {
	t.Parallel()
	for _, table := range []string{"secret_bindings", "secret_values", "audit_log"} {
		t.Run(table, func(t *testing.T) {
			db, _, store, id, b := unbindFixture(t)
			operation, body := "DELETE", "RETURN NULL;"
			if table == "secret_values" {
				body = "IF OLD.version=1 THEN RETURN NULL; END IF; RETURN OLD;"
			} else if table == "audit_log" {
				operation = "INSERT"
			}
			execUnbindTestSQL(t, db, `CREATE FUNCTION veto_secret_removal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`)
			execUnbindTestSQL(t, db, `CREATE TRIGGER veto_secret_removal BEFORE `+operation+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION veto_secret_removal()`)
			if err := store.Unbind(context.Background(), id, b.Deployment, b.Name, "alice"); err == nil || errors.Is(err, ErrBindingMissing) {
				t.Fatalf("missing write was accepted or reported absent: %v", err)
			}
			wantCount(t, "bindings", len(deploymentBindings(t, db, id, b.Deployment)), 1)
			wantCount(t, "stored versions", storedVersions(t, db, b.Ref, b.Name), 2)
			wantCount(t, "unbind receipts", len(ledger(t, db, "secret.unbound")), 0)
			execUnbindTestSQL(t, db, `DROP TRIGGER veto_secret_removal ON `+table)
			if err := store.Unbind(context.Background(), id, b.Deployment, b.Name, "alice"); err != nil {
				t.Fatal(err)
			}
			wantCount(t, "bindings", len(deploymentBindings(t, db, id, b.Deployment)), 0)
			wantCount(t, "stored versions", storedVersions(t, db, b.Ref, b.Name), 0)
			receipts := ledger(t, db, "secret.unbound")
			if len(receipts) != 1 {
				t.Fatalf("unbind receipts: %+v", receipts)
			}
			// A map, not a struct: encoding/json matches struct fields without
			// regard to case, and the receipt's key is exactly store.
			var payload map[string]any
			if err := json.Unmarshal(receipts[0].Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if r := receipts[0]; r.Actor != "alice" || r.Subject != "TOKEN" || payload["store"] != "internal" || strings.Contains(string(r.Payload), "private-") {
				t.Fatalf("unbind receipt: %+v", r)
			}
		})
	}
}

func TestUnbindPreservesSharedAndExternalValues(t *testing.T) {
	t.Parallel()
	db, vault, store, id, b := unbindFixture(t)
	other := b
	other.Deployment = "preview"
	if err := store.Put(t.Context(), id, other); err != nil {
		t.Fatal(err)
	}
	if err := store.Unbind(context.Background(), id, b.Deployment, b.Name, "alice"); err != nil {
		t.Fatal(err)
	}
	wantCount(t, "stored versions", storedVersions(t, db, b.Ref, b.Name), 2)
	if value, _, err := vault.Get(t.Context(), other.Ref, other.Name); err != nil || value != "private-current-value" {
		t.Fatalf("unbinding one deployment lost another deployment's value: %v", err)
	}
	if err := store.Unbind(context.Background(), id, other.Deployment, other.Name, "alice"); err != nil {
		t.Fatal(err)
	}
	wantCount(t, "stored versions", storedVersions(t, db, b.Ref, b.Name), 0)
	if err := store.Put(t.Context(), id, b); err == nil {
		t.Fatal("a stale binding was saved after its value was deleted")
	}
	// An external reference cannot authorize removal from the internal store.
	if _, err := vault.Put(t.Context(), b.Ref, b.Name, "unrelated", "alice"); err != nil {
		t.Fatal(err)
	}
	b.Store = StoreInfisical
	if err := store.Put(t.Context(), id, b); err != nil {
		t.Fatal(err)
	}
	if err := store.Unbind(context.Background(), id, b.Deployment, b.Name, "alice"); err != nil {
		t.Fatal(err)
	}
	wantCount(t, "stored versions", storedVersions(t, db, b.Ref, b.Name), 1)
}

func TestUnbindConcurrentChangesNeverLeaveDanglingBindings(t *testing.T) {
	t.Parallel()
	db, vault, store, id, b := unbindFixture(t)
	results := concurrently(8, func(int) int {
		err := store.Unbind(context.Background(), id, b.Deployment, b.Name, "alice")
		if err == nil {
			return 1
		}
		if !errors.Is(err, ErrBindingMissing) {
			t.Error(err)
		}
		return 0
	})
	winners := 0
	for _, result := range results {
		if result == 1 {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("deletion winners: %v", results)
	}
	wantCount(t, "unbind receipts", len(ledger(t, db, "secret.unbound")), 1)
	if _, err := vault.Put(t.Context(), b.Ref, b.Name, "new-value", "alice"); err != nil {
		t.Fatal(err)
	}
	b.Version = "1"
	if err := store.Put(t.Context(), id, b); err != nil {
		t.Fatal(err)
	}
	concurrently(2, func(i int) int {
		if i == 0 {
			if err := store.Unbind(context.Background(), id, b.Deployment, b.Name, "alice"); err != nil {
				t.Error(err)
			}
		} else {
			other := b
			other.Deployment = "preview"
			if err := store.Put(t.Context(), id, other); err != nil && !strings.Contains(err.Error(), "no longer stored") {
				t.Error(err)
			}
		}
		return 0
	})
	for _, deployment := range []string{b.Deployment, "preview"} {
		for _, bound := range deploymentBindings(t, db, id, deployment) {
			if bound.Store != string(StoreInternal) {
				continue
			}
			exists, err := queries(db).SecretValueVersionExists(context.Background(), secretsql.SecretValueVersionExistsParams{Scope: bound.Ref, Name: bound.Name, Column3: bound.Version})
			if err != nil || !exists {
				t.Fatalf("dangling binding %+v: %v", bound, err)
			}
		}
	}
}
