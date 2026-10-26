package projectlifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func TestProjectPurgeInventoryIsInsideRollback(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	projectID, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, Dependencies{}, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.removeRecords(ctx, projectID, "boss", func(*database.Tx, projectRemoval) error { return nil }); err == nil {
		t.Fatal("cancelled purge succeeded")
	}
	if _, err := project.ByIDIn(t.Context(), db, projectID); err != nil {
		t.Fatalf("cancelled removal lost project: %v", err)
	}
	if _, err := s.removeRecords(context.Background(), projectID, "", func(*database.Tx, projectRemoval) error { return nil }); err == nil {
		t.Fatal("purge without receipt actor succeeded")
	}
}
