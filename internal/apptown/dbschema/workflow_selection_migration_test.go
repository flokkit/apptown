package dbschema

import "testing"

// Selection data is discarded, including disabled rows, without replacement storage.
func TestMigration200DropsWorkflowSelection(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 200)]); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: reproduce the retired selection records before applying the drop.
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO project_lanes(project_id,name,enabled,updated_at) VALUES
 ('00000000-0000-7000-8000-000000000001','implement',true,now()),
 ('00000000-0000-7000-8000-000000000001','research',false,now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	var count int
	// sql-guarantee: only the schema catalog can prove that selection storage is gone.
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='project_lanes'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("selection table remains: %d %v", count, err)
	}
}
