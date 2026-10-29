package dbschema

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/check"
)

// Migration 169 states what every stored check and scan-point scanner
// applies to. appliesTo is required and has no default, so each one saved
// before it existed is given ["repository"] (what it meant: judged once
// over the whole repository), with everything else about it unchanged:
// stock and an instance's own block versions, old versions too, and a
// scan-point scanner in its binding and in each of its versions, a version
// judged by its own manual. A manual scanner takes its check's
// applicability and is left alone. A second run changes nothing.
//
// Kills: the migration removed, either scanner location left out, a manual
// scanner given an appliesTo, an existing appliesTo overwritten, or a
// check's other fields or order rewritten.
func TestMigration169StatesWhatEachStoredCheckAppliesTo(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	at := "'2026-10-01T00:00:00Z'"
	id := func(n int) string { return fmt.Sprintf("'01a1323b-3213-7000-8853-c2d607475%03d'", n) }
	checks := `[{"name":"sh-one","expects":"e","evaluator":"sh","sh":"exit 0","severity":"high"},` +
		`{"name":"tool-one","expects":"e","evaluator":"tool","tool":"t"},` +
		`{"name":"rubric-one","expects":"e","evaluator":"llm","rubric":"r","scope":"tree","files":["README.md"]},` +
		`{"name":"probe-one","expects":"e","evaluator":"runtime","probe":"page"},` +
		`{"name":"already","expects":"e","sh":"exit 0","appliesTo":["tool:go"]}]`
	scanPoint := `{"name":"semgrep","image":"semgrep/semgrep:1.99.0","command":["semgrep"],"report":".apptown/s.sarif"}`
	manual := `{"name":"lister","image":"x","command":["ls"],"manual":true}`
	// sql-guarantee: declarations stored by a release before appliesTo
	// existed; no operation of this build writes one without it.
	stmts := []string{
		`INSERT INTO guidance_blocks (id, name, origin, created_at, updated_at) VALUES (` + id(1) + `, 'stock/block', 'stock', ` + at + `, ` + at + `)`,
		`INSERT INTO guidance_block_versions (id, block_id, version, body_md, checks_json, created_at) VALUES (` + id(2) + `, ` + id(1) + `, 1, 'x', '` + checks + `', ` + at + `)`,
		`INSERT INTO guidance_block_versions (id, block_id, version, body_md, checks_json, created_at) VALUES (` + id(3) + `, ` + id(1) + `, 2, 'x', '` + checks + `', ` + at + `)`,
		`INSERT INTO guidance_blocks (id, name, origin, created_at, updated_at) VALUES (` + id(4) + `, 'own/block', '', ` + at + `, ` + at + `)`,
		`INSERT INTO guidance_block_versions (id, block_id, version, body_md, checks_json, created_at) VALUES (` + id(5) + `, ` + id(4) + `, 1, 'x', '` + checks + `', ` + at + `)`,
		`INSERT INTO guidance_block_versions (id, block_id, version, body_md, checks_json, created_at) VALUES (` + id(6) + `, ` + id(4) + `, 2, 'x', '[]', ` + at + `)`,
		`INSERT INTO integrations (id, name, capability, provider, config_json, version, created_at, updated_at) VALUES (` + id(7) + `, 'semgrep', 'scanner', 'tool', '` + scanPoint + `', 2, ` + at + `, ` + at + `)`,
		`INSERT INTO integration_versions (id, integration_id, version, config_json, created_at) VALUES (` + id(8) + `, ` + id(7) + `, 1, '` + scanPoint + `', ` + at + `)`,
		// An older version of a binding that is manual now was not then:
		// each version is judged by its own manual.
		`INSERT INTO integrations (id, name, capability, provider, config_json, version, created_at, updated_at) VALUES (` + id(9) + `, 'lister', 'scanner', 'tool', '` + manual + `', 2, ` + at + `, ` + at + `)`,
		`INSERT INTO integration_versions (id, integration_id, version, config_json, created_at) VALUES (` + id(10) + `, ` + id(9) + `, 1, '` + strings.Replace(manual, `,"manual":true`, ``, 1) + `', ` + at + `)`,
		`INSERT INTO integration_versions (id, integration_id, version, config_json, created_at) VALUES (` + id(11) + `, ` + id(9) + `, 2, '` + manual + `', ` + at + `)`,
	}
	for _, s := range stmts {
		if _, err := db.SQL().ExecContext(context.Background(), s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	// Through 169 only: a later migration drops integration_versions, whose
	// rows this released migration still rewrites.
	through169 := slices.IndexFunc(migrations, func(m migration) bool { return m.n == 169 }) + 1
	if _, err := applyMigrations(t.Context(), db, migrations[:through169]); err != nil {
		t.Fatal(err)
	}
	read := func() (blocks map[string]string, scanners map[string]string) {
		blocks, scanners = map[string]string{}, map[string]string{}
		rows, err := db.SQL().QueryContext(context.Background(), `SELECT b.name || ' v' || v.version, v.checks_json::text FROM guidance_block_versions v JOIN guidance_blocks b ON b.id = v.block_id WHERE b.name IN ('stock/block', 'own/block')`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatal(err)
			}
			blocks[k] = v
		}
		rows.Close()
		rows, err = db.SQL().QueryContext(context.Background(), `SELECT i.name || ' binding', i.config_json::text FROM integrations i WHERE i.name IN ('semgrep', 'lister')
			UNION ALL SELECT i.name || ' v' || v.version, v.config_json::text FROM integration_versions v JOIN integrations i ON i.id = v.integration_id WHERE i.name IN ('semgrep', 'lister')`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatal(err)
			}
			scanners[k] = v
		}
		rows.Close()
		return blocks, scanners
	}
	blocks, scanners := read()
	checked := 0
	for name, raw := range blocks {
		cs, err := check.ParseDeclarations(raw)
		if err != nil {
			t.Errorf("%s does not read through the strict reader: %v", name, err)
			continue
		}
		for _, c := range cs {
			checked++
			want := []string{check.AppliesRepository}
			if c.Name == "already" {
				want = []string{"tool:go"}
			}
			if strings.Join(c.AppliesTo, ",") != strings.Join(want, ",") {
				t.Errorf("%s %s applies to %v, want %v", name, c.Name, c.AppliesTo, want)
			}
		}
		if raw != "[]" {
			var names []string
			var list []map[string]any
			if err := json.Unmarshal([]byte(raw), &list); err != nil {
				t.Fatal(err)
			}
			for _, c := range list {
				names = append(names, c["name"].(string))
			}
			if got := strings.Join(names, ","); got != "sh-one,tool-one,rubric-one,probe-one,already" {
				t.Errorf("%s: checks reordered: %s", name, got)
			}
			if !strings.Contains(raw, `"sh": "exit 0"`) || !strings.Contains(raw, `"severity": "high"`) {
				t.Errorf("%s: a check's other fields changed: %s", name, raw)
			}
		}
	}
	if checked != 15 || len(blocks) != 4 {
		t.Fatalf("read %d checks in %d block versions, want 15 in 4", checked, len(blocks))
	}
	for name, raw := range scanners {
		var s check.Scanner
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			t.Fatal(err)
		}
		if err := s.ValidateApplicability(); err != nil {
			t.Errorf("%s: %v (%s)", name, err, raw)
		}
		wantStated := !s.Manual
		if (len(s.AppliesTo) > 0) != wantStated {
			t.Errorf("%s: appliesTo %v with manual %v", name, s.AppliesTo, s.Manual)
		}
	}
	if len(scanners) != 5 {
		t.Fatalf("read %d scanner configurations, want 5", len(scanners))
	}
	// Again: nothing changes.
	for _, s := range migrations {
		if s.n != 169 {
			continue
		}
		for _, stmt := range s.stmts {
			if _, err := db.SQL().ExecContext(context.Background(), stmt); err != nil {
				t.Fatal(err)
			}
		}
	}
	againBlocks, againScanners := read()
	for k, v := range blocks {
		if againBlocks[k] != v {
			t.Errorf("a second run changed %s", k)
		}
	}
	for k, v := range scanners {
		if againScanners[k] != v {
			t.Errorf("a second run changed %s", k)
		}
	}
}
