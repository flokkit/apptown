package projectlifecycle

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// An export sent in full to a caller that then went away keeps its
// receipt: the data has left, so its record is written though the caller
// has gone.
//
// Kills: the receipt transaction on the caller's context.
func TestASentExportKeepsItsReceiptThoughTheCallerWent(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	id, err := database.Transact(t.Context(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "sent", Name: "Sent"})
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "spool", "move.tar")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("the move file"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &ExportFile{Path: path, Size: 13, db: db, project: &project.Project{ID: id, Slug: "sent"}, actor: "owner", now: time.Now}
	ctx, cancel := context.WithCancel(t.Context())
	var out bytes.Buffer
	cancel() // the caller goes once the file has been written to it
	_ = f.Send(ctx, &out)
	got, err := audit.Recent(t.Context(), db, audit.Filter{ProjectID: id, KindPrefix: "project.exported"})
	if out.String() != "the move file" || err != nil || len(got) != 1 {
		t.Fatalf("sent %q; receipts %d %v", out.String(), len(got), err)
	}
}
