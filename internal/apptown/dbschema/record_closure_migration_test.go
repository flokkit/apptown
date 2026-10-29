package dbschema

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

// Migration 191 closes as 'record' only a shipped change with nothing of a
// landing recorded: not one a root landing run shipped (main redeployed,
// no merge of its own), not one with a merge receipt or a landed commit,
// not one closed some other way, not one still open.
func TestMigration191MarksOnlyChangesARecordRunShipped(t *testing.T) {
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 191)]); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	project := ids.New(now)
	// sql-guarantee: changes shipped before 191 are history no current operation writes in this shape.
	if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO projects(id,slug,name,serving_label,created_at) VALUES ($1,'closures','closures','closures',$2)`, project, now); err != nil {
		t.Fatal(err)
	}
	n := int64(0)
	change := func(state, closedAs, landedSHA, role, event string) ids.ID {
		t.Helper()
		n++
		id := ids.New(now)
		// sql-guarantee: as above, a pre-191 change.
		if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO changes(id,project_id,number,type,title,state,closed_as,landed_sha,created_at) VALUES ($1,$2,$3,'feature','c',$4,$5,$6,$7)`, id, project, n, state, closedAs, landedSHA, now); err != nil {
			t.Fatal(err)
		}
		if role == "" {
			return id
		}
		run := ids.New(now)
		// sql-guarantee: the change's root run as it was admitted, with the role its definition snapshot holds.
		if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO runs(id,project_id,number,workflow_name,workflow_version,kind,status,current_stage,definition_json,change_id,created_at,updated_at) VALUES ($1,$2,$3,'w',1,'pipeline','done','s',$4,$5,$6,$6)`,
			run, project, n, `{"kind":"pipeline","role":"`+role+`"}`, id, now); err != nil {
			t.Fatal(err)
		}
		if event != "" {
			// sql-guarantee: the run's merge receipt as the landing recorded it.
			if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO run_events(id,run_id,at,kind) VALUES ($1,$2,$3,$4)`, ids.New(now), run, now, event); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	record := change("shipped", "", "", "record", "")
	noRun := change("shipped", "", "", "", "")
	landingNoMerge := change("shipped", "", "", "landing", "")
	merged := change("shipped", "", "", "working", "merge.completed")
	landed := change("shipped", "", "abc123", "working", "")
	shippedWith := change("shipped", "shipped-with", "", "record", "")
	open := change("in_progress", "", "", "record", "")
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 191)+1]); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[ids.ID]string{record: "record", noRun: "record", landingNoMerge: "", merged: "", landed: "", shippedWith: "shipped-with", open: ""} {
		var got string
		if err := db.SQL().QueryRowContext(context.Background(), `SELECT closed_as FROM changes WHERE id=$1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("change %s closed as %q, want %q", id, got, want)
		}
	}
}
