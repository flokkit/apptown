package dbschema

import "testing"

// Migrations 158 and 162: every standing deployment on a Docker host or
// Portainer that a release before 1.0 recorded keeps the names it was made
// with (forge-) until it is moved; any other deployment, and every one
// recorded afterwards, is named by its key (”).
//
// Kills: the 162 backfill clearing a Docker deployment's prefix (its data
// would be looked for under new names), or keeping it on a deployment that
// no host names; the later default left at apptown-.
func TestMigrationsKeepEachDockerDeploymentsHostNamesUntilItMoves(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	var docked, other int
	if err := db.QueryRow(`SELECT count(*) FILTER (WHERE d.kind = 'standing' AND t.kind IN ('docker-host', 'portainer')),
  count(*) FILTER (WHERE NOT (d.kind = 'standing' AND COALESCE(t.kind, '') IN ('docker-host', 'portainer')))
  FROM deployments d LEFT JOIN targets t ON t.id = d.target_id`).Scan(&docked, &other); err != nil {
		t.Fatal(err)
	}
	if docked == 0 {
		t.Fatal("the release fixture records no standing Docker deployment to keep")
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	var kept, cleared int
	if err := db.QueryRow(`SELECT count(*) FILTER (WHERE host_prefix = 'forge-'), count(*) FILTER (WHERE host_prefix = '') FROM deployments`).Scan(&kept, &cleared); err != nil {
		t.Fatal(err)
	}
	if kept != docked || cleared != other {
		t.Fatalf("%d of %d standing Docker deployments keep forge-; %d of %d others are named by key", kept, docked, cleared, other)
	}
	var project string
	if err := db.QueryRow(`SELECT project_id::text FROM deployments LIMIT 1`).Scan(&project); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: a row as the deployment owner's inserts write it,
	// naming no prefix; the default is what this migration sets.
	if _, err := db.Exec(`INSERT INTO deployments (id, project_id, name, updated_at) VALUES ('01a15e00-0000-7000-8000-000000000162', ?, 'after-162', '2026-10-22T12:00:00Z')`, project); err != nil {
		t.Fatal(err)
	}
	var prefix string
	if err := db.QueryRow(`SELECT host_prefix FROM deployments WHERE name = 'after-162'`).Scan(&prefix); err != nil || prefix != "" {
		t.Fatalf("a deployment made after 162: %q %v", prefix, err)
	}
}
