package dbschema

import "testing"

// Migration 160 moves two categories that name AppTown to its name: the
// workload kind of AppTown's own model calls, and what-ifs of the platform
// pack (renamed by 155). The actor recorded on the same model call is an
// identifier in the forensic record and keeps the name it was written with.
//
// Kills: either UPDATE removed, or an UPDATE widened to the actor column.
func TestMigration160MovesTheCategoriesAndKeepsTheActors(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	// sql-guarantee: the release's records before the migration; the build
	// that wrote them is not this one, so no owner operation can.
	for _, row := range []struct{ id, kind string }{
		{"01a15f00-0000-7000-8000-000000000001", "forge"},
		{"01a15f00-0000-7000-8000-000000000002", "step"},
	} {
		if _, err := db.Exec(`INSERT INTO inference_requests (id, started_at, kind, actor, provider, model) VALUES (?, '2026-10-20T01:00:00Z', ?, 'forge', 'openrouter', 'm')`, row.id, row.kind); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct{ id, pack string }{
		{"01a15f00-0000-7000-8000-0000000000a1", "forge"},
		{"01a15f00-0000-7000-8000-0000000000a2", "go-small"},
	} {
		if _, err := db.Exec(`INSERT INTO what_if_reviews (id, pack, pack_version, requested_by, created_at) VALUES (?, ?, 31, 'bls', '2026-10-20T01:00:00Z')`, row.id, row.pack); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	actors := map[string]string{}
	rows, err := db.Query(`SELECT id::text, kind, actor FROM inference_requests`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, kind, actor string
		if err := rows.Scan(&id, &kind, &actor); err != nil {
			t.Fatal(err)
		}
		kinds[id], actors[id] = kind, actor
	}
	_ = rows.Close()
	if kinds["01a15f00-0000-7000-8000-000000000001"] != "apptown" || kinds["01a15f00-0000-7000-8000-000000000002"] != "step" {
		t.Fatalf("kinds after 160: %v", kinds)
	}
	for id, actor := range actors {
		if actor != "forge" {
			t.Fatalf("the actor of %s was rewritten to %q; history keeps the name it was recorded with", id, actor)
		}
	}
	packs := map[string]string{}
	rows, err = db.Query(`SELECT id::text, pack FROM what_if_reviews`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, pack string
		if err := rows.Scan(&id, &pack); err != nil {
			t.Fatal(err)
		}
		packs[id] = pack
	}
	_ = rows.Close()
	if packs["01a15f00-0000-7000-8000-0000000000a1"] != "apptown" || packs["01a15f00-0000-7000-8000-0000000000a2"] != "go-small" {
		t.Fatalf("what-if packs after 160: %v", packs)
	}
}
