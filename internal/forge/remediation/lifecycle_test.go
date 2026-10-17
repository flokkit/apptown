package remediation

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
)

// The remediation pass counts a project that is not active as busy, so it
// skips it rather than starting work its doors would refuse; a person in
// the workshop still makes an active project busy.
//
// Kills: a pass that keeps trying a project being deleted.
func TestAProjectNotActiveIsBusyForRemediation(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	alpha := createTestProject(t, db, "alpha", "Alpha", "")
	s := &Service{db: db}
	if s.busy(context.Background(), alpha) {
		t.Fatal("an active, idle project is busy")
	}
	s.deps.Busy = func(_ context.Context, id ids.ID) bool { return id == alpha }
	if !s.busy(context.Background(), alpha) {
		t.Fatal("a project with a person in its workshop is not busy")
	}
	s.deps.Busy = nil
	ctx := context.Background()
	if err := db.InTx(ctx, func(tx *database.Tx) error {
		return project.MarkDeletingIn(ctx, tx, alpha, access.Actor{Name: "boss"}, ids.Nil, false, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	if !s.busy(ctx, alpha) {
		t.Fatal("a project being deleted is not busy")
	}
}
