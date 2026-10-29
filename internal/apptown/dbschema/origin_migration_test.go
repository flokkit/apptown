package dbschema

import (
	"context"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/version"
)

// Migration 168 records where every published workflow, block and pack
// version of a release's records came from: stock as the built-in source,
// signed "shipped with AppTown before" the build that ran the migration,
// unsigned; the instance's own as source here, with no signer. Nothing is
// left without a source, and an empty source is refused from then on.
//
// Kills: a source by name or date instead of the library's origin, stock
// without the build, a version left empty, or the column left accepting an
// empty source.
func TestMigration168RecordsEachVersionsSourceAndSigner(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	at := "'2026-10-01T00:00:00Z'"
	// sql-guarantee: versions published before this migration, stock and
	// authored, of each kind; no operation of this build writes one
	// without a label.
	stmts := []string{}
	for i, origin := range []string{"stock", ""} {
		id := func(n int) string { return fmt.Sprintf("'01a1323b-3213-7000-8853-c2d6074751%d%d'", i, n) }
		stmts = append(stmts,
			`INSERT INTO lib_workflows (id, name, origin, updated_at) VALUES (`+id(1)+`, 'm168-flow-`+fmt.Sprint(i)+`', '`+origin+`', `+at+`)`,
			`INSERT INTO lib_workflow_versions (id, lib_id, version, yaml, created_at) VALUES (`+id(2)+`, `+id(1)+`, 1, 'x', `+at+`)`,
			`INSERT INTO guidance_blocks (id, name, origin, created_at, updated_at) VALUES (`+id(3)+`, 'm168/block-`+fmt.Sprint(i)+`', '`+origin+`', `+at+`, `+at+`)`,
			`INSERT INTO guidance_block_versions (id, block_id, version, body_md, created_at) VALUES (`+id(4)+`, `+id(3)+`, 1, 'x', `+at+`)`,
			`INSERT INTO packs (id, name, origin, updated_at) VALUES (`+id(5)+`, 'm168-pack-`+fmt.Sprint(i)+`', '`+origin+`', `+at+`)`,
			`INSERT INTO pack_versions (id, pack_id, version, spec_json, created_at) VALUES (`+id(6)+`, `+id(5)+`, 1, '{}', `+at+`)`,
		)
	}
	for _, s := range stmts {
		if _, err := db.SQL().ExecContext(context.Background(), s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	signer := "shipped with AppTown before " + version.String() + ", unsigned"
	for _, q := range []struct{ table, library, key string }{
		{"lib_workflow_versions", "lib_workflows", "lib_id"},
		{"guidance_block_versions", "guidance_blocks", "block_id"},
		{"pack_versions", "packs", "pack_id"},
	} {
		var stocks, stockLabelled, others, othersLabelled int
		if err := db.SQL().QueryRowContext(context.Background(), `SELECT count(*) FILTER (WHERE l.origin = 'stock'), count(*) FILTER (WHERE l.origin = 'stock' AND v.source = 'flokkit' AND v.signer = $1),
			count(*) FILTER (WHERE l.origin <> 'stock'), count(*) FILTER (WHERE l.origin <> 'stock' AND v.source = 'here' AND v.signer = '')
			FROM `+q.table+` v JOIN `+q.library+` l ON l.id = v.`+q.key, signer).Scan(&stocks, &stockLabelled, &others, &othersLabelled); err != nil {
			t.Fatal(err)
		}
		if stocks == 0 || others == 0 || stocks != stockLabelled || others != othersLabelled {
			t.Errorf("%s: %d stock, %d flokkit signed %q; %d others, %d here", q.table, stocks, stockLabelled, signer, others, othersLabelled)
		}
		if _, err := db.SQL().ExecContext(context.Background(), `UPDATE `+q.table+` SET source = ''`); err == nil {
			t.Errorf("%s accepts an empty source", q.table)
		}
	}
}
