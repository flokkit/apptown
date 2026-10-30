package dbschema

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

// Migration 192 drops the single primary pack name and nothing else: the
// project's current revision is kept, and every receipt keeps the JSON
// (and digest) it was recorded with, since a digest is the hash of the Go
// form and SQL cannot recompute it. Projects are recorded again through
// the product after the release's stock syncs.
//
// Kills: a migration that rewrites receipts, or one that loses the
// current revision with the column.
func TestMigration192DropsThePrimaryPackAndKeepsReceipts(t *testing.T) {
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 192)]); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	project := ids.New(now)
	ctx := context.Background()
	// sql-guarantee: a project and its adoption as a release before 192 recorded them, with the primary pack name it no longer has.
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO projects(id,slug,name,serving_label,created_at) VALUES ($1,'packs','packs','packs',$2)`, project, now); err != nil {
		t.Fatal(err)
	}
	receipt := `{"resolver":1,"pack":"go-small","guidance":[],"workflows":[],"policies":[],"extensions":[]}`
	// sql-guarantee: as above.
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO adoptions(project_id,revision,pack,drift_note) VALUES ($1,3,'go-small','')`, project); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: as above.
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO project_adoptions(id,project_id,revision,kind,digest,created_at,receipt_json) VALUES ($1,$2,3,'edit','d1',$3,$4)`, ids.New(now), project, now, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 192)+1]); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := db.SQL().QueryRowContext(ctx, `SELECT revision FROM adoptions WHERE project_id=$1`, project).Scan(&revision); err != nil || revision != 3 {
		t.Fatalf("current revision after 192: %d, %v", revision, err)
	}
	var column int
	if err := db.SQL().QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name='adoptions' AND column_name='pack'`).Scan(&column); err != nil || column != 0 {
		t.Fatalf("adoptions.pack after 192: %d columns, %v", column, err)
	}
	var kept, digest string
	if err := db.SQL().QueryRowContext(ctx, `SELECT receipt_json->>'pack', digest FROM project_adoptions WHERE project_id=$1`, project).Scan(&kept, &digest); err != nil || kept != "go-small" || digest != "d1" {
		t.Fatalf("the receipt after 192 says pack %q, digest %q (%v); it is kept as recorded", kept, digest, err)
	}
}
