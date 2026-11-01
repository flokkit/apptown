package workflow

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
)

func TestWorkAddressFilterReadsTheCurrentAddress(t *testing.T) {
	t.Parallel()
	q, root, _ := activityFixture(t)
	ctx := t.Context()
	before, err := project.ByIDIn(ctx, q.db, root.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	next := "canonical-work"
	if err := q.db.InTx(ctx, func(tx *database.Tx) error {
		_, err := project.UpdateSettingsIn(ctx, tx, before.ID, before.Slug, project.SettingsPatch{ExpectedRevision: &before.SettingsRevision, Slug: &next})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	s := newService(q.db, allowProject(root.ProjectID), Runtime{})
	rows, err := s.Runs(ctx, RunQuery{ProjectSlug: next})
	if err != nil || len(rows) != 1 || rows[0].ID != root.ID || rows[0].ProjectSlug != next {
		t.Fatalf("current address: %+v %v", rows, err)
	}
	for _, address := range []string{before.Slug, "missing-address"} {
		if rows, err := s.Runs(ctx, RunQuery{ProjectSlug: address}); err != nil || len(rows) != 0 {
			t.Fatalf("address %q names no project, but listed: %+v %v", address, rows, err)
		}
	}
	restore := dbtest.NextStatementFailure(q.db, "-- name: ProjectIDBySlug")
	rows, err = s.Runs(ctx, RunQuery{ProjectSlug: next})
	restore()
	if err == nil || rows != nil {
		t.Fatalf("failed identity read became list: %+v %v", rows, err)
	}
}
