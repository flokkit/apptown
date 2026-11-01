package project

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func TestContractSnapshots(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return CreateAt(t.Context(), tx, time.Now(), Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	c := NewContracts(db)
	if done, err := c.snapshotted(context.Background(), id, "abc"); err != nil || done {
		t.Fatalf("nothing yet: %v %v", err, done)
	}
	if err := c.Snapshot(context.Background(), id, "abc", "present", "app-yaml", "apiVersion: v1", ""); err != nil {
		t.Fatal(err)
	}
	if done, _ := c.snapshotted(context.Background(), id, "abc"); !done {
		t.Fatal("recorded")
	}
	if err := c.Snapshot(context.Background(), id, "abc", "invalid", "app-yaml", "", "bad yaml"); err != nil {
		t.Fatal(err)
	}
	var status string
	// sql-guarantee: The stored snapshot bytes are the evidence guarantee, and no production read returns contract_snapshots rows; adding one would be test-only API.
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT status FROM contract_snapshots WHERE project_id=$1 AND sha='abc'`, id).Scan(&status); err != nil || status != "invalid" {
		t.Fatalf("a repeat replaces: %v %q", err, status)
	}
}

// NewContracts is the snapshots on the real clock, for these tests.
func NewContracts(db *database.DB) *Contracts { return &Contracts{db: db} }
