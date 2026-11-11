package dbschema

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Migration 227 records what each deployment serves: the done attempt at its
// observed generation and that attempt's release, found through old
// rebuilds' keys back to the generation they rebuilt; an adoption after a
// restore serves its observed release with no attempt; no release stays
// unknown.
//
// Kills: a backfill that leaves a rebuilt deployment's release unknown or
// stops at the first rebuild, names the shipped release of an adopted one or
// keeps its attempt, takes a failed or older generation, or invents a release.
func TestMigration227RecordsWhatEachDeploymentServes(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 227)]); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)
	project, target := idstest.N(927001), idstest.N(927002)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.SQL().ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// sql-guarantee: deployments as they stood before the serving record existed.
	exec(`INSERT INTO projects(id,slug,name,serving_label,created_at) VALUES ($1,'serving','serving','serving',$2)`, project, at)
	// sql-guarantee: as above, their target.
	exec(`INSERT INTO targets(id,name,kind,created_at,updated_at) VALUES ($1,'serving-host','docker-host',$2,$2)`, target, at)
	deployment := func(name string, observed int64) ids.ID {
		t.Helper()
		id := ids.New(at) // a deployment id names its host by its last 12 digits
		// sql-guarantee: as above.
		exec(`INSERT INTO deployments(id,project_id,name,target_id,updated_at,desired_generation,observed_generation) VALUES ($1,$2,$3,$4,$5,$6,$6)`, id, project, name, target, at, observed)
		return id
	}
	n := uint64(0)
	attempt := func(on ids.ID, generation int64, status, key string, rebuild bool) ids.ID {
		t.Helper()
		n++
		id := idstest.N(927100 + n)
		if key == "" {
			key = id.String()
		}
		// sql-guarantee: an attempt as admission recorded it before this migration.
		exec(`INSERT INTO deployment_attempts(id,deployment_id,generation,idempotency_key,status,started_at) VALUES ($1,$2,$3,$4,$5,$6)`, id, on, generation, key, status, at)
		// sql-guarantee: as above, its inputs.
		exec(`INSERT INTO deployment_attempt_inputs(attempt_id,rebuild) VALUES ($1,$2)`, id, rebuild)
		return id
	}
	number := int64(0)
	release := func(on ids.ID, generation int64, state, origin string) ids.ID {
		t.Helper()
		number++
		id := idstest.N(927200 + uint64(number))
		// sql-guarantee: a release as settlement or adoption recorded it.
		exec(`INSERT INTO releases(id,project_id,number,state,deployment,created_at,deployment_id,generation,origin) VALUES ($1,$2,$3,$4,'d',$5,$6,$7,$8)`, id, project, number, state, at, on, generation, origin)
		return id
	}
	ordinary, rebuilt, adopted, none := deployment("ordinary", 3), deployment("rebuilt", 5), deployment("adopted", 2), deployment("none", 1)
	attempt(ordinary, 2, "done", "", false)
	release(ordinary, 2, "released", "shipped")
	ordinaryAttempt := attempt(ordinary, 3, "done", "", false)
	ordinaryRelease := release(ordinary, 3, "released", "shipped")

	attempt(rebuilt, 2, "done", "", false)
	rebuiltRelease := release(rebuilt, 2, "released", "shipped")
	attempt(rebuilt, 3, "failed", "", false)
	release(rebuilt, 3, "failed", "shipped")
	attempt(rebuilt, 4, "done", fmt.Sprintf("rebuild-%s-from-2", rebuilt), true)
	rebuiltAttempt := attempt(rebuilt, 5, "done", fmt.Sprintf("rebuild-%s-from-4", rebuilt), true)

	attempt(adopted, 2, "done", "", false)
	release(adopted, 2, "released", "shipped")
	adoptedRelease := release(adopted, 2, "released", "observed-after-restore")

	noneAttempt := attempt(none, 1, "done", "", false)

	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 227)+1]); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		name               string
		dep                ids.ID
		release, attemptID ids.ID
	}{
		{"ordinary", ordinary, ordinaryRelease, ordinaryAttempt},
		{"rebuilt twice", rebuilt, rebuiltRelease, rebuiltAttempt},
		{"adopted", adopted, adoptedRelease, ids.Nil},
		{"no release", none, ids.Nil, noneAttempt},
	} {
		var release, attempt sql.Null[ids.ID]
		if err := db.SQL().QueryRowContext(t.Context(), `SELECT serving_release_id, serving_attempt_id FROM deployments WHERE id=$1`, want.dep).Scan(&release, &attempt); err != nil {
			t.Fatal(err)
		}
		if release.V != want.release || attempt.V != want.attemptID {
			t.Errorf("%s serves release %s attempt %s, want %s %s", want.name, release.V, attempt.V, want.release, want.attemptID)
		}
	}
}
