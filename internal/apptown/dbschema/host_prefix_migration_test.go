package dbschema

import "testing"

// Migration 158: every deployment a release before 1.0 recorded keeps the
// names it was made with (forge-), and a deployment recorded afterwards
// without naming one is apptown-.
//
// Kills: the backfill default left at apptown-, or the later default left
// at forge-.
func TestMigration158KeepsEachDeploymentsHostNames(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	var before int
	if err := db.QueryRow(`SELECT count(*) FROM deployments`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("the release fixture records no deployment to keep")
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	var kept int
	if err := db.QueryRow(`SELECT count(*) FROM deployments WHERE host_prefix = 'forge-'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != before {
		t.Fatalf("%d of %d earlier deployments keep forge-", kept, before)
	}
	var project string
	if err := db.QueryRow(`SELECT project_id::text FROM deployments LIMIT 1`).Scan(&project); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: a row as the deployment owner's inserts write it,
	// naming no prefix; the default is what this migration sets.
	if _, err := db.Exec(`INSERT INTO deployments (id, project_id, name, updated_at) VALUES ('01a15e00-0000-7000-8000-000000000158', ?, 'after-158', '2026-10-21T12:00:00Z')`, project); err != nil {
		t.Fatal(err)
	}
	var prefix string
	if err := db.QueryRow(`SELECT host_prefix FROM deployments WHERE name = 'after-158'`).Scan(&prefix); err != nil || prefix != "apptown-" {
		t.Fatalf("a deployment made after 158: %q %v", prefix, err)
	}
}
