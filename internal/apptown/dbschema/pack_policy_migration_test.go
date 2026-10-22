package dbschema

import (
	"encoding/json"
	"testing"
)

// Migration 164 gives each pack version that names a policy without its
// definition the library's definition, which is what installing it read.
// A defined entry, a name the library lacks, the version's pins and its
// declaration stay as they were, and running the statement again changes
// nothing.
//
// Kills: a migration that rewrites defined entries, invents a definition for
// an unknown name, reorders the list, or touches pins or the declaration.
func TestMigration164WritesTheLibrarysDefinitionIntoPackVersions(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	const (
		pack    = "01a1323b-3213-7000-8853-c2d607475a90"
		version = "01a1323b-3213-7000-8853-c2d607475a91"
		spec    = `{"policies":[{"name":"p-known","rule":"","with":null,"description":""},{"name":"p-defined","rule":"requireStepBefore","with":{"uses":"deploy","requires":"scan"},"description":"its own"},{"name":"p-unknown","rule":"","with":null,"description":""}]}`
		pins    = `{"blocks":{},"workflows":{},"policies":{"p-defined":"pinned"}}`
		decl    = `{"policies":["p-known","p-defined","p-unknown"]}`
	)
	// sql-guarantee: a publication from before policies were pinned; no
	// operation of this build writes one.
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO policy_library (id, name, description, rule, config_json, origin, updated_at) VALUES ('01a1323b-3213-7000-8853-c2d607475a92', 'p-known', 'the library''s words', 'requireCapabilityBound', '{"capability":"scanner"}', 'stock', '2026-10-01T00:00:00Z')`, nil},
		{`INSERT INTO packs (id, name, updated_at) VALUES (?, 'migration-164', '2026-10-01T00:00:00Z')`, []any{pack}},
		{`INSERT INTO pack_versions (id, pack_id, version, spec_json, created_at, pins_json, declaration_json) VALUES (?, ?, 1, ?, '2026-09-01T00:00:00Z', ?, ?)`, []any{version, pack, spec, pins, decl}},
	} {
		if _, err := db.Exec(stmt.sql, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	read := func() (policies []map[string]any, pinsAfter, declAfter string) {
		t.Helper()
		var specAfter string
		if err := db.QueryRow(`SELECT spec_json::text, pins_json::text, declaration_json::text FROM pack_versions WHERE id = ?`, version).Scan(&specAfter, &pinsAfter, &declAfter); err != nil {
			t.Fatal(err)
		}
		var s struct{ Policies []map[string]any }
		if err := json.Unmarshal([]byte(specAfter), &s); err != nil {
			t.Fatal(err)
		}
		return s.Policies, pinsAfter, declAfter
	}
	policies, pinsAfter, declAfter := read()
	if len(policies) != 3 || policies[0]["name"] != "p-known" || policies[1]["name"] != "p-defined" || policies[2]["name"] != "p-unknown" {
		t.Fatalf("the list after 164: %v", policies)
	}
	if p := policies[0]; p["rule"] != "requireCapabilityBound" || p["description"] != "the library's words" || p["with"].(map[string]any)["capability"] != "scanner" {
		t.Fatalf("the named policy after 164: %v", p)
	}
	if p := policies[1]; p["rule"] != "requireStepBefore" || p["description"] != "its own" {
		t.Fatalf("the defined policy after 164: %v", p)
	}
	if p := policies[2]; p["rule"] != "" {
		t.Fatalf("a policy the library lacks after 164: %v", p)
	}
	var wantPins, wantDecl string
	if err := db.QueryRow(`SELECT ?::jsonb::text, ?::jsonb::text`, pins, decl).Scan(&wantPins, &wantDecl); err != nil {
		t.Fatal(err)
	}
	if pinsAfter != wantPins || declAfter != wantDecl {
		t.Fatalf("pins or declaration changed: %s %s", pinsAfter, declAfter)
	}
	before := pinsAfter
	var stmt string
	for _, m := range migrations {
		if m.n == 164 {
			stmt = m.stmts[0]
		}
	}
	if _, err := db.Exec(stmt); err != nil {
		t.Fatal(err)
	}
	again, pinsAgain, _ := read()
	if len(again) != 3 || again[0]["rule"] != "requireCapabilityBound" || again[2]["rule"] != "" || pinsAgain != before {
		t.Fatalf("running 164 again changed the version: %v", again)
	}
}
