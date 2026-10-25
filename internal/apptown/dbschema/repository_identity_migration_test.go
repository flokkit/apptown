package dbschema

import (
	"database/sql"
	"fmt"
	"github.com/flokkit/apptown/internal/ids"
	"testing"
	"time"
)

// Existing names, origins, MR paths and cached credentials are evidence, not
// sufficient provenance to manufacture a provider-scoped attachment during SQL
// upgrade. Lazy observation and conservative historical refusal own that choice.
func TestMigration172PreservesLegacyRecordsWithoutInventingRepositoryIdentity(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	index := -1
	for i, m := range migrations {
		if m.n == 172 {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("migration172 is missing")
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:index]); err != nil {
		t.Fatal(err)
	}
	inserted := func(result sql.Result, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		n, err := result.RowsAffected()
		if err != nil || n != 1 {
			t.Fatalf("legacy fixture needs one retained row: %d %v", n, err)
		}
	}
	// sql-guarantee: this models a released path-only MR and token resource;
	// using today's owners would create verified provenance and defeat the test.
	inserted(db.Exec(`INSERT INTO artifacts(id,run_id,kind,name,body,created_at) SELECT ?,id,'mr','!71','legacy/team',? FROM runs ORDER BY id LIMIT 1`, ids.New(time.Now()), time.Now()))
	// sql-guarantee: released token attachments did not record provider scope.
	inserted(db.Exec(`INSERT INTO project_integrations(project_id,capability,binding,resource_json,created_at) SELECT id,'scm','gitlab','{"gitlabProject":"legacy/team","profiles":{"read":{"token":"fixture-only","tokenId":71,"expiresAt":"2026-10-29T00:00:00Z"}}}'::jsonb,? FROM projects ORDER BY id LIMIT 1 ON CONFLICT (project_id,capability) DO UPDATE SET resource_json=EXCLUDED.resource_json`, time.Now()))
	snapshot := func(table string) string {
		t.Helper()
		var rows string
		// sql-guarantee: byte-equivalent released row values are the migration guarantee;
		// table names below are fixed test constants, never caller input.
		if err := db.QueryRow(fmt.Sprintf(`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM %s t`, table)).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	before := map[string]string{}
	for _, table := range []string{"projects", "artifacts", "project_integrations"} {
		before[table] = snapshot(table)
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:index+1]); err != nil {
		t.Fatal(err)
	}
	for table, old := range before {
		if snapshot(table) != old {
			t.Errorf("migration rewrote historical %s records", table)
		}
	}
	for _, table := range []string{"repository_attachments", "repository_attachment_intents", "repository_mr_identities"} {
		var count int
		// sql-guarantee: SQL upgrade must not invent remote provenance from old paths.
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Errorf("invented %s identity rows: %d %v", table, count, err)
		}
	}
}
