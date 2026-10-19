package dbschema

import "testing"

// Migration 153 gives each earlier drill row its item: a bucket's row by
// the new store its place names, a database's by the set item whose
// instance its place ends with, whose place then reads as the cluster it
// was made on. A row whose place names no item of its set keeps no item.
//
// Kills: a backfill that gives every row of a set one item, matches a
// database row by its set alone, or leaves the instance id as its place.
func TestMigration153GivesEachEarlierDrillItsItem(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	const (
		project = "00000000-0001-7000-8000-000000000000"
		set     = "01a151d0-4cbf-7001-8410-7a24bfc0dc3b"
		cluster = "8bd23f19-4241-4c4a-9d0e-5a5a5a5a5a5a"
	)
	// sql-guarantee: the release's records before the migration; the 1.0
	// build that wrote them is not this one, so no owner operation can.
	if _, err := db.Exec(`INSERT INTO backups (id, project_id, deployment, taken_at, items_json, store) VALUES (?, ?, 'cloud', '2026-10-19T01:39:21Z', ?::jsonb, 'store')`, set, project,
		`[{"kind":"need","name":"postgres","file":"postgres.dump","instance":"`+cluster+`","database":"app"},{"kind":"need","name":"s3","file":"s3.tar","bucket":"forge-bkproof-cloud-f913f61e"}]`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, place string }{
		{"01a151d1-0000-7000-8000-000000000001", "app_check_b0e0aef05a1c on " + cluster},
		{"01a151d1-0000-7000-8000-000000000002", "a new s3 store on demo-docker"},
		{"01a151d1-0000-7000-8000-000000000003", "app_check_b0e0aef05a1c on another-cluster"},
	} {
		if _, err := db.Exec(`INSERT INTO backup_drills (id, backup_id, project_id, deployment, drilled_at, passed, target, place, as_user) VALUES (?, ?, ?, 'cloud', '2026-10-19T01:39:59Z', true, 'do-syd', ?, '')`, row.id, set, project, row.place); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		"01a151d1-0000-7000-8000-000000000001": {"postgres", "a new database on the deployment's cluster"},
		"01a151d1-0000-7000-8000-000000000002": {"s3", "a new s3 store on demo-docker"},
		"01a151d1-0000-7000-8000-000000000003": {"", "app_check_b0e0aef05a1c on another-cluster"},
	}
	rows, err := db.Query(`SELECT id::text, need, place FROM backup_drills WHERE backup_id = ?`, set)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var id, need, place string
		if err := rows.Scan(&id, &need, &place); err != nil {
			t.Fatal(err)
		}
		seen++
		if got := [2]string{need, place}; got != want[id] {
			t.Errorf("drill %s after migration 153: %q, want %q", id, got, want[id])
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != len(want) {
		t.Fatalf("read %d drills, want %d", seen, len(want))
	}
}
