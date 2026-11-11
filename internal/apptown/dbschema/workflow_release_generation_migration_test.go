package dbschema

import (
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Migration 226 gives a release the ship workflow's deploy step recorded the
// generation its run's attempt applied, so the deployment's serving release
// (the release at its observed generation) is found again. Only a released
// row whose run has a done, non-rebuild attempt on its deployment changes.
//
// Kills: a migration that leaves those releases at generation 0, takes a
// rebuild's or a failed attempt's generation, or touches other releases.
func TestMigration226RecordsGenerationOfWorkflowRecordedReleases(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 226)]); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 4, 23, 58, 57, 0, time.UTC)
	project, target, dep, other := idstest.N(922001), idstest.N(922002), ids.New(at), ids.New(at) // a deployment id names its host by its last 12 digits
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.SQL().ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// sql-guarantee: releases the removed workflow deploy step recorded exist only in old storage.
	exec(`INSERT INTO projects(id,slug,name,serving_label,created_at) VALUES ($1,'workflow-releases','workflow-releases','workflow-releases',$2)`, project, at)
	// sql-guarantee: as above, the deployment those releases ran on.
	exec(`INSERT INTO targets(id,name,kind,created_at,updated_at) VALUES ($1,'workflow-host','docker-host',$2,$2)`, target, at)
	// sql-guarantee: as above.
	exec(`INSERT INTO deployments(id,project_id,name,target_id,url,updated_at,handle_json,desired_generation,observed_generation) VALUES ($1,$2,'main',$4,'',$5,'{}',7,7),($3,$2,'other',$4,'',$5,'{}',9,9)`, dep, project, other, target, at)
	attempt := func(n uint64, on ids.ID, generation int64, status string, run ids.ID, rebuild bool) {
		t.Helper()
		id := idstest.N(922100 + n)
		// sql-guarantee: an attempt the workflow deploy step admitted, naming its run and no release.
		exec(`INSERT INTO deployment_attempts(id,deployment_id,generation,idempotency_key,status,started_at) VALUES ($1,$2,$3,$4,$5,$6)`, id, on, generation, id.String(), status, at)
		// sql-guarantee: as above, the attempt's inputs.
		exec(`INSERT INTO deployment_attempt_inputs(attempt_id,release_run,rebuild) VALUES ($1,$2,$3)`, id, run, rebuild)
	}
	release := func(number int64, state string, run *ids.ID, generation int64) ids.ID {
		t.Helper()
		id := idstest.N(922200 + uint64(number))
		// sql-guarantee: a release row as the workflow deploy step or a hand release recorded it.
		exec(`INSERT INTO releases(id,project_id,number,state,deployment,run_id,created_at,deployment_id,generation) VALUES ($1,$2,$3,$4,'main',$5,$6,$7,$8)`, id, project, number, state, run, at, dep, generation)
		return id
	}
	once, retried, unmatched, failedRun, recorded := idstest.N(922301), idstest.N(922302), idstest.N(922303), idstest.N(922304), idstest.N(922305)
	attempt(1, dep, 2, "done", once, false)
	attempt(2, dep, 7, "failed", once, false)      // a newer failed attempt of the run
	attempt(3, other, 9, "done", unmatched, false) // its run on another deployment only
	attempt(4, dep, 3, "done", retried, false)     // a retried step: deployed, then deployed again
	attempt(5, dep, 4, "done", retried, false)
	attempt(6, dep, 5, "done", retried, true) // a later rebuild carries the run
	attempt(7, dep, 6, "done", failedRun, false)
	attempt(8, dep, 7, "done", recorded, false)
	want := map[ids.ID]int64{
		release(1, "released", nil, 1):        1, // a hand release keeps its generation
		release(2, "released", &once, 0):      2, // not the newer failed attempt's 7
		release(3, "released", &retried, 0):   4, // the newest deployment, not the earlier 3 or the rebuild's 5
		release(4, "released", &unmatched, 0): 0, // no attempt on its deployment names the run
		release(5, "failed", &failedRun, 0):   0, // only a released row serves
		release(6, "released", &recorded, 1):  1, // a recorded generation is kept, though its run has another
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 226)+1]); err != nil {
		t.Fatal(err)
	}
	for id, generation := range want {
		var got int64
		if err := db.SQL().QueryRowContext(t.Context(), `SELECT generation FROM releases WHERE id=$1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != generation {
			t.Errorf("release %s generation = %d, want %d", id, got, generation)
		}
	}
}
