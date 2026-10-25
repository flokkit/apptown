package workflow

import (
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"testing"
)

func TestWorkAddressAliasesFilterByPermanentProjectIdentity(t *testing.T) {
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
	for _, address := range []string{before.Slug, next} {
		rows, err := s.Runs(ctx, RunQuery{ProjectSlug: address})
		if err != nil || len(rows) != 1 || rows[0].ID != root.ID || rows[0].ProjectSlug != next {
			t.Fatalf("address %q: %+v %v", address, rows, err)
		}
	}
	if rows, err := s.Runs(ctx, RunQuery{ProjectSlug: "missing-address"}); err != nil || len(rows) != 0 {
		t.Fatalf("missing became unfiltered: %+v %v", rows, err)
	}
	restore := q.db.FailNextStatement("-- name: ProjectIDByAddress")
	rows, err := s.Runs(ctx, RunQuery{ProjectSlug: before.Slug})
	restore()
	if err == nil || rows != nil {
		t.Fatalf("failed identity read became list: %+v %v", rows, err)
	}
}
