package adrs

import (
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"testing"
)

func TestADRAddressAliasesFilterByPermanentProjectIdentity(t *testing.T) {
	t.Parallel()
	s, p := fixture(t)
	db := s.records.db
	ctx := testCaller()
	a := create(t, s, p, "local")
	foreign := projectFixture(t, db, "foreign-alias", "Foreign")
	create(t, s, foreign, "foreign")
	before, err := project.ByIDIn(ctx, db, p)
	if err != nil {
		t.Fatal(err)
	}
	next := "canonical-decisions"
	if err := db.InTx(ctx, func(tx *database.Tx) error {
		_, err := project.UpdateSettingsIn(ctx, tx, p, before.Slug, project.SettingsPatch{ExpectedRevision: &before.SettingsRevision, Slug: &next})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{before.Slug, next} {
		rows, err := s.List(ctx, Filter{ProjectSlug: address})
		if err != nil || len(rows) != 1 || rows[0].ID != a.ID || rows[0].ProjectSlug != next {
			t.Fatalf("address %q: %+v %v", address, rows, err)
		}
	}
	for _, f := range []Filter{{ProjectSlug: "missing-address"}, {ProjectSlug: before.Slug, ProjectID: foreign}} {
		rows, err := s.List(ctx, f)
		if err != nil || len(rows) != 0 {
			t.Fatalf("missing/mixed scope became unfiltered: %+v %v", rows, err)
		}
	}
	restore := db.FailNextStatement("-- name: ProjectIDByAddress")
	rows, err := s.List(ctx, Filter{ProjectSlug: before.Slug})
	restore()
	if err == nil || rows != nil {
		t.Fatalf("failed identity read became list: %+v %v", rows, err)
	}
}
