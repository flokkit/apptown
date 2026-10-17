package database_test

import (
	"context"
	"errors"
	"testing"

	"flokkit.io/forge/internal/database"
)

func TestTransactionResultsRequireCommitAndAlwaysRelease(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	execTest(t, db, `CREATE TABLE transaction_probe(n int UNIQUE DEFERRABLE INITIALLY DEFERRED)`)
	value, err := database.Transact(context.Background(), db, func(tx *database.Tx) (string, error) {
		_, err := tx.Exec(`INSERT INTO transaction_probe VALUES (1)`)
		return "committed", err
	})
	if value != "committed" || err != nil {
		t.Fatalf("committed result: %q, %v", value, err)
	}
	for _, commitFailure := range []bool{false, true} {
		bodyErr := errors.New("callback refused")
		value, err := database.Transact(context.Background(), db, func(tx *database.Tx) (string, error) {
			n := 2
			if commitFailure {
				n = 1 // duplicate is rejected only at commit
			}
			if _, err := tx.Exec(`INSERT INTO transaction_probe VALUES (?)`, n); err != nil {
				t.Fatalf("failure must occur after the insert: %v", err)
			}
			if !commitFailure {
				return "uncommitted", bodyErr
			}
			return "uncommitted", nil
		})
		if value != "" || err == nil || (!commitFailure && !errors.Is(err, bodyErr)) {
			t.Fatalf("commit failure=%v exposed result=%q, err=%v", commitFailure, value, err)
		}
		countTest(t, db, 1, `SELECT COUNT(*) FROM transaction_probe`)
	}
	func() {
		defer func() {
			if got := recover(); got != "operation panicked" {
				t.Fatalf("panic changed or swallowed: %v", got)
			}
		}()
		_ = db.InTx(context.Background(), func(tx *database.Tx) error {
			if _, err := tx.Exec(`INSERT INTO transaction_probe VALUES (3)`); err != nil {
				t.Fatal(err)
			}
			panic("operation panicked")
		})
	}()
	if n := db.Pool().Stats().InUse; n != 0 {
		t.Fatalf("transaction retained %d pool connections", n)
	}
	countTest(t, db, 1, `SELECT COUNT(*) FROM transaction_probe`)
}
