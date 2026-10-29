package adrs

import (
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"testing"
)

func TestADRAddressFilterReadsTheCurrentAddress(t *testing.T) {
	t.Parallel()
	s, p := fixture(t)
	db := s.records.db
	ctx := testCaller()
	a := create(t, s, p, "local")
	foreign := projectFixture(t, db, "foreign", "Foreign")
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
	rows, err := s.List(ctx, Filter{ProjectSlug: next})
	if err != nil || len(rows) != 1 || rows[0].ID != a.ID || rows[0].ProjectSlug != next {
		t.Fatalf("current address: %+v %v", rows, err)
	}
	for _, f := range []Filter{{ProjectSlug: before.Slug}, {ProjectSlug: "missing-address"}, {ProjectSlug: next, ProjectID: foreign}} {
		rows, err := s.List(ctx, f)
		if err != nil || len(rows) != 0 {
			t.Fatalf("missing/mixed scope became unfiltered: %+v %v", rows, err)
		}
	}
	restore := db.FailNextStatement("-- name: ProjectIDBySlug")
	rows, err = s.List(ctx, Filter{ProjectSlug: next})
	restore()
	if err == nil || rows != nil {
		t.Fatalf("failed identity read became list: %+v %v", rows, err)
	}
}
