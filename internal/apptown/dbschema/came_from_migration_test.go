package dbschema

import (
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/version"
)

// Migration 168 labels every published workflow, block and pack version of
// a release's records with where it came from: stock as "AppTown before"
// the build that ran the migration, the instance's own as "authored here".
// Nothing is left unlabelled, and an empty label is refused from then on.
//
// Kills: a label by name or date instead of the library's origin, stock
// labelled without the build, a version left empty, or the column left
// accepting an empty label.
func TestMigration168LabelsWhereEachVersionCameFrom(t *testing.T) {
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
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	stock := "AppTown before " + version.String()
	for _, q := range []struct{ table, library, key string }{
		{"lib_workflow_versions", "lib_workflows", "lib_id"},
		{"guidance_block_versions", "guidance_blocks", "block_id"},
		{"pack_versions", "packs", "pack_id"},
	} {
		var stocks, stockLabelled, others, othersLabelled int
		if err := db.QueryRow(`SELECT count(*) FILTER (WHERE l.origin = 'stock'), count(*) FILTER (WHERE l.origin = 'stock' AND v.came_from = ?),
			count(*) FILTER (WHERE l.origin <> 'stock'), count(*) FILTER (WHERE l.origin <> 'stock' AND v.came_from = 'authored here')
			FROM `+q.table+` v JOIN `+q.library+` l ON l.id = v.`+q.key, stock).Scan(&stocks, &stockLabelled, &others, &othersLabelled); err != nil {
			t.Fatal(err)
		}
		if stocks == 0 || others == 0 || stocks != stockLabelled || others != othersLabelled {
			t.Errorf("%s: %d stock, %d labelled %q; %d others, %d authored here", q.table, stocks, stockLabelled, stock, others, othersLabelled)
		}
		if _, err := db.Exec(`UPDATE ` + q.table + ` SET came_from = ''`); err == nil {
			t.Errorf("%s accepts an empty label", q.table)
		}
	}
}
