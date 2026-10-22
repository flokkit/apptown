package audit_test

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func TestRequiredReceiptRejectsUnserializablePayload(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := audit.At(time.Now()).RecordIn(context.Background(), tx, "alice", ids.Nil, "test", "subject", make(chan int)); err == nil {
		t.Fatal("required audit replaced its payload with an empty object")
	}
	// Inspect the caller's transaction: an outside connection could not see a
	// mistakenly inserted, uncommitted receipt.
	if rows, err := audit.Recent(context.Background(), tx, audit.Filter{Limit: audit.MaxLimit}); err != nil || len(rows) != 0 {
		t.Fatalf("invalid payload left %d receipts in caller transaction: %v", len(rows), err)
	}
}

// A ledger line carries the time its writer's owner read on its own clock,
// however far that clock is from the machine's; the ledger has none.
func TestALedgerLineIsStampedByItsWritersClock(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	skewed := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Second)
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return audit.At(skewed).RecordIn(context.Background(), tx, "alice", ids.Nil, "test.required", "subject", nil)
	}); err != nil {
		t.Fatal(err)
	}
	audit.At(skewed.Add(time.Hour)).BestEffort(context.Background(), db, "alice", ids.Nil, "test.informational", "subject", nil)
	rows, err := audit.Recent(context.Background(), db, audit.Filter{Limit: audit.MaxLimit})
	if err != nil || len(rows) != 2 {
		t.Fatalf("lines: %v %v", rows, err)
	}
	want := map[string]time.Time{"test.required": skewed, "test.informational": skewed.Add(time.Hour)}
	for _, r := range rows {
		if at, err := time.Parse(time.RFC3339Nano, r.At); err != nil || !at.Equal(want[r.Kind]) {
			t.Fatalf("%s is stamped %s, want the writer's %s", r.Kind, r.At, want[r.Kind])
		}
	}
}

// A best-effort receipt records what has happened: a caller that stopped
// after the act still leaves its receipt.
//
// Kills: the receipt written on the stopped caller.
func TestABestEffortReceiptIsWrittenThoughTheCallerStopped(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	stopped, stop := context.WithCancel(t.Context())
	stop()
	audit.At(time.Now()).BestEffort(stopped, db, "alice", ids.Nil, "test.after-the-act", "subject", nil)
	if rows, err := audit.Recent(t.Context(), db, audit.Filter{KindPrefix: "test.after-the-act"}); err != nil || len(rows) != 1 {
		t.Fatalf("receipts after a stopped caller: %d %v", len(rows), err)
	}
}
