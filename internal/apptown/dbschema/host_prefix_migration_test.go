package dbschema

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/database"
)

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
	// Through 166: 167 drops the column this reads.
	migrateThrough(t, db, 166)
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

// migrateThrough applies the migrations numbered up to n, as a release that
// stopped there did.
func migrateThrough(t *testing.T, db *database.DB, n int) {
	t.Helper()
	var list []migration
	for _, m := range migrations {
		if m.n <= n {
			list = append(list, m)
		}
	}
	if _, err := applyMigrations(t.Context(), db, list); err != nil {
		t.Fatal(err)
	}
}

// Migration 167 drops the recorded host prefix once no deployment is on
// the names it was made with before 1.0. It refuses, changing nothing, while
// a deployment with a prefix made something on a host: its app container,
// a Portainer stack (recorded only on the handle when an update reused it),
// or anything on its list; and the refusal names it. A deployment with a
// prefix that made nothing (the release fixture's, never booted) and a
// moved one go through.
//
// Kills: a refusal that misses any of the three signals, refuses a
// deployment that holds nothing, half-applies, or names no deployment.
func TestMigration167RefusesWhileADeploymentIsOnItsOldNamesAndThenDropsThePrefix(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	migrateThrough(t, db, 166)
	var dep, project, slug, prefix string
	if err := db.QueryRow(`SELECT d.id::text, d.project_id::text, p.slug, d.host_prefix FROM deployments d JOIN projects p ON p.id = d.project_id
  WHERE d.container = '' AND d.handle_json = '{}'::jsonb AND NOT EXISTS (SELECT 1 FROM deployment_owned o WHERE o.deployment_id = d.id) ORDER BY d.id LIMIT 1`).Scan(&dep, &project, &slug, &prefix); err != nil || prefix == "" {
		t.Fatalf("the release fixture records a deployment with a prefix that made nothing: %q %v", prefix, err)
	}
	hasColumn := func() bool {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'deployments' AND column_name = 'host_prefix'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// sql-guarantee: each case sets the one record a release before 1.0
	// left, which no current owner operation can write (the prefix is gone
	// from the code); each is undone before the next.
	for _, held := range []struct{ name, set, undo string }{
		{"its app container", `UPDATE deployments SET container = 'forge-env-` + slug + `-main' WHERE id = '` + dep + `'`, `UPDATE deployments SET container = '' WHERE id = '` + dep + `'`},
		{"a Portainer stack on its handle", `UPDATE deployments SET handle_json = '{"stack": "forge-` + slug + `"}'::jsonb WHERE id = '` + dep + `'`, `UPDATE deployments SET handle_json = '{}'::jsonb WHERE id = '` + dep + `'`},
		{"a volume on its list", `INSERT INTO deployment_owned (id, deployment_id, kind, ident, name, holds_data, need, recorded_at, note)
  VALUES ('01a16900-0000-7000-8000-000000000166', '` + dep + `', 'volume', 'forge-data-forge-env-` + slug + `-main', 'forge-data-forge-env-` + slug + `-main', true, '', '2026-10-23T12:00:00Z', '')`,
			`DELETE FROM deployment_owned WHERE id = '01a16900-0000-7000-8000-000000000166'`},
	} {
		exec(held.set)
		_, err := Migrate(t.Context(), db)
		if err == nil || !strings.Contains(err.Error(), slug+"/") || !strings.Contains(err.Error(), "move-names") {
			t.Fatalf("a deployment on its old names holding %s was not refused by name: %v", held.name, err)
		}
		if v, err := schemaVersion(db); err != nil || v != 166 || !hasColumn() {
			t.Fatalf("the refused migration changed the schema (holding %s): version %d %v, column %v", held.name, v, err, hasColumn())
		}
		exec(held.undo)
	}
	// A moved deployment that holds a container goes through beside the
	// fixture's, which holds nothing.
	exec(`INSERT INTO deployments (id, project_id, name, container, host_prefix, updated_at) VALUES ('01a16900-0000-7000-8000-000000000167', ?, 'moved', 'apptown-dep-000000000167', '', '2026-10-23T12:00:00Z')`, project)
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("nothing on its old names, yet the migration refused: %v", err)
	}
	if hasColumn() {
		t.Fatal("deployments.host_prefix is still there")
	}
	var moveFlag int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'release_requests' AND column_name = 'move_names'`).Scan(&moveFlag); err != nil || moveFlag != 0 {
		t.Fatalf("release_requests.move_names is still there: %d %v", moveFlag, err)
	}
}
