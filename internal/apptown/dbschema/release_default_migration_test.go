package dbschema

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func TestMigration173PreservesIdentitiesAndOnlyRecordedReleaseChoice(t *testing.T) {
	db := releaseDB(t)
	index := -1
	for i, m := range migrations {
		if m.n == 173 {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("migration173 missing")
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:index]); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	target := ids.New(now)
	// sql-guarantee: historical configuration before labels/default records existed; current owners cannot create this schema input.
	if err := db.SQL().QueryRowContext(context.Background(), `INSERT INTO targets(id,name,kind,status,address_json,created_at,updated_at) VALUES ($1,'intranet','portainer','ready','{"tier":"customer"}',$2,$2) ON CONFLICT(name) DO UPDATE SET kind='portainer',status='ready',address_json='{"tier":"customer"}' RETURNING id`, target, now).Scan(&target); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, tier, kind, feed, purpose, lifecycle string
		chosen                                     bool
	}{
		{"eligible", "customer", "standing", "manual", "ordinary", "", true},
		{"other-name", "lan", "standing", "manual", "ordinary", "", false},
		{"preview", "customer", "preview", "manual", "ordinary", "", false},
		{"landing", "customer", "standing", "landing", "ordinary", "", false},
		{"copy", "customer", "standing", "manual", "eval-copy", "", false},
		{"deleted", "customer", "standing", "manual", "ordinary", "deleted", false},
	}
	projects, deployments := make([]ids.ID, len(cases)), make([]ids.ID, len(cases))
	for i, c := range cases {
		projects[i], deployments[i] = ids.New(now), ids.New(now)
		// sql-guarantee: released projects and deployments predate migration173's explicit default record.
		if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO projects(id,slug,name,purpose,created_at) VALUES ($1,$2,$2,$3,$4)`, projects[i], "default-fixture-"+c.name, c.purpose, now); err != nil {
			t.Fatal(err)
		}
		// sql-guarantee: deliberate old deployment forms exercise backfill eligibility without today's owners normalizing them.
		if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO deployments(id,project_id,name,kind,feed,target_id,updated_at,handle_json) VALUES ($1,$2,$3,$4,$5,$6,$7,'{"retained":"proof"}')`, deployments[i], projects[i], c.tier, c.kind, c.feed, target, now); err != nil {
			t.Fatal(err)
		}
		if c.lifecycle != "" {
			// sql-guarantee: released lifecycle rows must not become a new release choice during upgrade.
			if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO project_lifecycle(project_id,state,requested_at) VALUES ($1,$2,$3)`, projects[i], c.lifecycle, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	snapshot := func(table string, labeled bool) string {
		t.Helper()
		expression := "to_jsonb(t)"
		if labeled {
			expression += " - 'display_label'"
		}
		var out string
		// sql-guarantee: migration must preserve every original column, including identity, credential configuration and provider receipts; tables are fixed constants.
		if err := db.SQL().QueryRowContext(context.Background(), fmt.Sprintf(`SELECT COALESCE(jsonb_agg(%s ORDER BY t.id),'[]'::jsonb)::text FROM %s t`, expression, table)).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	tables := []string{"integrations", "targets", "deployments"}
	before := map[string]string{}
	for _, table := range tables {
		before[table] = snapshot(table, false)
	}
	dbtest.FailNextStatement(t, db, "INSERT INTO project_release_settings")
	if _, err := applyMigrations(t.Context(), db, migrations[:index+1]); err == nil {
		t.Fatal("backfill failure hidden")
	}
	if version, err := schemaVersion(context.Background(), db); err != nil || version != 172 {
		t.Fatalf("partial version %d: %v", version, err)
	}
	for _, table := range tables {
		if snapshot(table, false) != before[table] {
			t.Fatalf("failed migration changed %s", table)
		}
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:index+1]); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if snapshot(table, true) != before[table] {
			t.Fatalf("migration rewrote original %s", table)
		}
		var mismatches int
		// sql-guarantee: every pre-existing label is copied exactly from its original key without changing that key.
		if err := db.SQL().QueryRowContext(context.Background(), "SELECT count(*) FROM "+table+" WHERE display_label<>name").Scan(&mismatches); err != nil || mismatches != 0 {
			t.Fatalf("%s label backfill %d: %v", table, mismatches, err)
		}
	}
	for i, c := range cases {
		var count int
		if err := db.SQL().QueryRowContext(context.Background(), `SELECT count(*) FROM project_release_settings WHERE project_id=$1`, projects[i]).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if (count == 1) != c.chosen {
			t.Errorf("%s choice count %d", c.name, count)
		}
		if c.chosen {
			var selected ids.ID
			var revision int64
			if err := db.SQL().QueryRowContext(context.Background(), `SELECT default_deployment_id,revision FROM project_release_settings WHERE project_id=$1`, projects[i]).Scan(&selected, &revision); err != nil || selected != deployments[i] || revision != 1 {
				t.Fatalf("wrong legacy choice %s revision%d: %v", selected, revision, err)
			}
		}
	}
	// sql-guarantee: the storage membership constraint independently prevents a default pointing at another project's deployment.
	if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO project_release_settings(project_id,default_deployment_id) VALUES ($1,$2)`, projects[1], deployments[0]); err == nil {
		t.Fatal("cross-project default accepted by schema")
	}
	if n, err := applyMigrations(t.Context(), db, migrations[:index+1]); err != nil || n != 0 {
		t.Fatalf("replay applied %d: %v", n, err)
	}
}
