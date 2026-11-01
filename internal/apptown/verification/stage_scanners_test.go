package verification

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/dbtest"
)

func TestStageScannerInventoryUsesAdmittedTransaction(t *testing.T) {
	t.Parallel()
	store := testDB(t)
	store.Pool().SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := store.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	scanners, err := (StageScanners{}).SnapshotIn(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(scanners) != 0 {
		t.Fatalf("fresh inventory: %+v", scanners)
	}
}

func TestStageScannerInventoryFailureDoesNotInventNoScanners(t *testing.T) {
	t.Parallel()
	store := testDB(t)
	t.Cleanup(dbtest.NextStatementFailure(store, "-- name: Bindings :many"))
	scanners, err := (StageScanners{}).SnapshotIn(context.Background(), store)
	if err == nil || scanners != nil {
		t.Fatalf("failed inventory became a scanner set: %+v %v", scanners, err)
	}
}
