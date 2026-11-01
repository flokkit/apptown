package secret

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/flokkit/apptown/internal/database"
)

func keyExec(t *testing.T, db *database.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.SQL().ExecContext(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}

func TestVaultConcurrentFirstUseAndRequiredReceipt(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	keyExec(t, db, `CREATE FUNCTION veto_key_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	keyExec(t, db, `CREATE TRIGGER veto_key_receipt BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION veto_key_receipt()`)
	if _, err := NewVault(db).key(t.Context()); err == nil {
		t.Fatal("accepted missing initialization receipt")
	}
	if _, err := queries(db).StoreKey(context.Background()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed receipt left key: %v", err)
	}
	keyExec(t, db, `DROP TRIGGER veto_key_receipt ON audit_log`)
	const clients = 8
	keys := make([][]byte, clients)
	errs := make([]error, clients)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range clients {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; keys[i], errs[i] = NewVault(db).key(t.Context()) }()
	}
	close(start)
	wg.Wait()
	for i := range clients {
		if errs[i] != nil || len(keys[i]) != 32 || !bytes.Equal(keys[0], keys[i]) {
			t.Fatalf("first-use clients disagree: %v", errs[i])
		}
	}
	wantCount(t, "initialization receipts", len(ledger(t, db, "secrets.store-initialised")), 1)
}
