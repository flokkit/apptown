package dbschema

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func TestMigration178PreservesCurrentAndHistoricalAddressOwnership(t *testing.T) {
	db := releaseDB(t)
	index := -1
	for i, m := range migrations {
		if m.n == 178 {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("migration178 missing")
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:index]); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	live := ids.New(now)
	historical := ids.New(now)
	ambiguous := ids.New(now)
	// sql-guarantee: represent released pre178 rows; current creation also writes the new reservation table.
	if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO projects(id,slug,serving_label,name,purpose,created_at) VALUES ($1,'reused','reused','Current','ordinary',$2)`, live, now); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id   ids.ID
		slug string
	}{{historical, "reused"}, {ids.New(now), "retired"}, {ambiguous, "uncertain"}, {ids.New(now), "uncertain"}} {
		// sql-guarantee: historical completed purge records predate permanent address assignment and may share a previously reusable name.
		if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO project_deletions(project_id,slug,requested_by,requested_at,plan,state) VALUES ($1,$2,'historical',$3,'{}','done')`, row.id, row.slug, now); err != nil {
			t.Fatal(err)
		}
	}
	dbtest.FailNextStatement(t, db, "INSERT INTO project_addresses")
	if _, err := applyMigrations(t.Context(), db, migrations[:index+1]); err == nil {
		t.Fatal("failed reservation backfill accepted")
	}
	if version, err := schemaVersion(context.Background(), db); err != nil || version != 177 {
		t.Fatalf("partial version %d %v", version, err)
	}
	var exists bool
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT to_regclass('public.project_addresses') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("failed migration retained reservation table: %t %v", exists, err)
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:index+1]); err != nil {
		t.Fatal(err)
	}
	var owner sql.NullString
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT project_id::text FROM project_addresses WHERE address='reused'`).Scan(&owner); err != nil || owner.String != live.String() {
		t.Fatalf("current assignment lost to old deletion: %+v %v", owner, err)
	}
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT project_id::text FROM project_addresses WHERE address='retired'`).Scan(&owner); err != nil || !owner.Valid {
		t.Fatalf("known historical identity lost: %+v %v", owner, err)
	}
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT project_id::text FROM project_addresses WHERE address='uncertain'`).Scan(&owner); err != nil || owner.Valid {
		t.Fatalf("ambiguous historical owner invented: %+v %v", owner, err)
	}
	// sql-guarantee: the database boundary must independently refuse canonical names without a matching UUID assignment.
	if _, err := db.SQL().ExecContext(context.Background(), `UPDATE projects SET slug='unclaimed' WHERE id=$1`, live); err == nil {
		t.Fatal("unclaimed canonical address accepted")
	}
	// sql-guarantee: reservation survival is a schema property, independent of lifecycle cleanup effects.
	if _, err := db.SQL().ExecContext(context.Background(), `DELETE FROM projects WHERE id=$1`, live); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT project_id::text FROM project_addresses WHERE address='reused'`).Scan(&owner); err != nil || owner.String != live.String() {
		t.Fatalf("purge erased permanent address: %+v %v", owner, err)
	}
}
