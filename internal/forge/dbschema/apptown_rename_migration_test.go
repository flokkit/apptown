package dbschema

import "testing"

// Migration 154 renames two stored values the AppTown rename changed: a
// backup set kept on AppTown's own disk is 'apptown-disk', and a complaint
// about the platform is about 'apptown'. Other values are left as they are,
// and a row written afterwards without either column gets the new default.
//
// Kills: either UPDATE removed, or either default left at its old value.
func TestMigration154RenamesTheStoredPlatformNames(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	const project = "00000000-0001-7000-8000-000000000000"
	// sql-guarantee: the release's records before the migration; the build
	// that wrote them is not this one, so no owner operation can.
	for _, row := range []struct{ id, store string }{
		{"01a151d0-0000-7000-8000-000000000001", "forge-disk"},
		{"01a151d0-0000-7000-8000-000000000002", "store"},
	} {
		if _, err := db.Exec(`INSERT INTO backups (id, project_id, deployment, taken_at, store) VALUES (?, ?, 'main', '2026-10-19T01:39:21Z', ?)`, row.id, project, row.store); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		id    string
		n     int
		about string
	}{
		{"01a151d0-0000-7000-8000-0000000000a1", 1, "forge"},
		{"01a151d0-0000-7000-8000-0000000000a2", 2, "project"},
	} {
		if _, err := db.Exec(`INSERT INTO complaints (id, number, at, text, about) VALUES (?, ?, '2026-10-19T01:00:00Z', 'x', ?)`, row.id, row.n, row.about); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	stores := map[string]string{}
	rows, err := db.Query(`SELECT id::text, store FROM backups`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, store string
		if err := rows.Scan(&id, &store); err != nil {
			t.Fatal(err)
		}
		stores[id] = store
	}
	_ = rows.Close()
	if stores["01a151d0-0000-7000-8000-000000000001"] != "apptown-disk" || stores["01a151d0-0000-7000-8000-000000000002"] != "store" {
		t.Fatalf("stores after 154: %v", stores)
	}
	abouts := map[int]string{}
	rows, err = db.Query(`SELECT number, about FROM complaints`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n int
		var about string
		if err := rows.Scan(&n, &about); err != nil {
			t.Fatal(err)
		}
		abouts[n] = about
	}
	_ = rows.Close()
	if abouts[1] != "apptown" || abouts[2] != "project" {
		t.Fatalf("complaints after 154: %v", abouts)
	}
	if _, err := db.Exec(`INSERT INTO backups (id, project_id, deployment, taken_at) VALUES ('01a151d0-0000-7000-8000-000000000003', ?, 'main', '2026-10-20T01:00:00Z')`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO complaints (id, number, at, text) VALUES ('01a151d0-0000-7000-8000-0000000000a3', 3, '2026-10-20T01:00:00Z', 'x')`); err != nil {
		t.Fatal(err)
	}
	var store, about string
	if err := db.QueryRow(`SELECT store FROM backups WHERE id = '01a151d0-0000-7000-8000-000000000003'`).Scan(&store); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT about FROM complaints WHERE number = 3`).Scan(&about); err != nil {
		t.Fatal(err)
	}
	if store != "apptown-disk" || about != "apptown" {
		t.Fatalf("defaults after 154: store %q, about %q", store, about)
	}
}
