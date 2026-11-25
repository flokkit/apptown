package dbschema

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/check"
)

// Migration 249 gives each old tool check the appliesTo its block's newest
// version states for the same-named check, so a lockfile check stored before
// appliesTo existed judges the parts built with its tool, not the whole
// repository. The shape is live's lang/selection/typescript-for-web: v11 from
// before appliesTo (given ["repository"] by migration 169), v12 scoped to
// tool:node. An sh check keeps ["repository"] (its stored script behaves as it
// always did), as does a tool check the newest version dropped, one with no
// newer version, and one already scoped. Other fields and order are kept, and
// a second run changes nothing.
//
// Kills: the migration removed, the evaluator filter dropped (the sh check
// changes), the oldest version taken as the donor (nothing changes), or an
// existing appliesTo overwritten.
func TestMigration249GivesOldToolChecksTheirBlocksAppliesTo(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 249)]); err != nil {
		t.Fatal(err)
	}
	at := "'2026-10-01T00:00:00Z'"
	id := func(n int) string { return fmt.Sprintf("'01a21250-3213-7000-8853-c2d607475%03d'", n) }
	old := `[{"name":"lockfile-in-step","expects":"e","severity":"medium","evaluator":"tool","tool":"npm-lock-check","appliesTo":["repository"]},` +
		`{"name":"readme","expects":"e","evaluator":"sh","sh":"test -f README.md","appliesTo":["repository"]},` +
		`{"name":"dropped","expects":"e","evaluator":"tool","tool":"t","appliesTo":["repository"]},` +
		`{"name":"scoped","expects":"e","evaluator":"tool","tool":"go-mod-tidy","appliesTo":["tool:go"]}]`
	newest := `[{"name":"lockfile-in-step","expects":"e2","severity":"medium","evaluator":"tool","tool":"npm-lock-check","appliesTo":["tool:node"]},` +
		`{"name":"readme","expects":"e","evaluator":"sh","sh":"test -f README.md","appliesTo":["tool:node"]},` +
		`{"name":"scoped","expects":"e","evaluator":"tool","tool":"go-mod-tidy","appliesTo":["tool:node"]}]`
	alone := `[{"name":"lockfile-in-step","expects":"e","evaluator":"tool","tool":"uv-lock-check","appliesTo":["repository"]}]`
	ins := func(v, block, n int, checks string) string {
		return `INSERT INTO guidance_block_versions (id, block_id, version, body_md, checks_json, created_at, source, signer) VALUES (` +
			id(v) + `, ` + id(block) + `, ` + fmt.Sprint(n) + `, 'x', '` + checks + `', ` + at + `, 'flokkit', '')`
	}
	// sql-guarantee: block versions stored before appliesTo existed, as
	// migration 169 left them; no operation of this build stores one.
	for _, s := range []string{
		`INSERT INTO guidance_blocks (id, name, origin, created_at, updated_at) VALUES (` + id(1) + `, 'lang/selection/web', 'stock', ` + at + `, ` + at + `)`,
		ins(2, 1, 11, old),
		ins(3, 1, 12, newest),
		`INSERT INTO guidance_blocks (id, name, origin, created_at, updated_at) VALUES (` + id(4) + `, 'own/block', '', ` + at + `, ` + at + `)`,
		ins(5, 4, 1, alone),
	} {
		if _, err := db.SQL().ExecContext(t.Context(), s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	read := func() map[string]string {
		out := map[string]string{}
		rows, err := db.SQL().QueryContext(t.Context(), `SELECT b.name || ' v' || v.version, v.checks_json::text FROM guidance_block_versions v JOIN guidance_blocks b ON b.id = v.block_id WHERE b.id IN (`+id(1)+`, `+id(4)+`)`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatal(err)
			}
			out[k] = v
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	newestBefore := read()["lang/selection/web v12"]
	if _, err := applyMigrations(t.Context(), db, migrations); err != nil {
		t.Fatal(err)
	}
	after := read()
	want := map[string]map[string]string{
		"lang/selection/web v11": {"lockfile-in-step": "tool:node", "readme": "repository", "dropped": "repository", "scoped": "tool:go"},
		"own/block v1":           {"lockfile-in-step": "repository"},
	}
	for version, checks := range want {
		cs, err := check.ParseDeclarations(after[version])
		if err != nil {
			t.Fatalf("%s does not read through the strict reader: %v", version, err)
		}
		var names []string
		for _, c := range cs {
			names = append(names, c.Name)
			if got := strings.Join(c.AppliesTo, ","); got != checks[c.Name] {
				t.Errorf("%s %s applies to %s, want %s", version, c.Name, got, checks[c.Name])
			}
		}
		if len(cs) != len(checks) {
			t.Errorf("%s holds %d checks, want %d", version, len(cs), len(checks))
		}
		if version == "lang/selection/web v11" && strings.Join(names, ",") != "lockfile-in-step,readme,dropped,scoped" {
			t.Errorf("%s: checks reordered: %v", version, names)
		}
	}
	// Apart from appliesTo, the old lockfile check is what it was.
	var list []map[string]any
	if err := json.Unmarshal([]byte(after["lang/selection/web v11"]), &list); err != nil {
		t.Fatal(err)
	}
	if c := list[0]; c["expects"] != "e" || c["severity"] != "medium" || c["tool"] != "npm-lock-check" || len(c) != 6 {
		t.Errorf("the old lockfile check's other fields changed: %v", c)
	}
	if after["lang/selection/web v12"] != newestBefore {
		t.Errorf("the newest version changed:\n%s\nwas\n%s", after["lang/selection/web v12"], newestBefore)
	}
	if _, err := db.SQL().ExecContext(t.Context(), migrations[migrationIndex(t, 249)].stmts[0]); err != nil {
		t.Fatal(err)
	}
	if again := read(); fmt.Sprint(again) != fmt.Sprint(after) {
		t.Errorf("a second run changed the checks:\n%v\nwas\n%v", again, after)
	}
}
