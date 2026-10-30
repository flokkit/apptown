package dbschema

import (
	"github.com/flokkit/apptown/internal/ids"
	"strings"
	"testing"
	"time"
)

func TestMigration193PreservesExistingDescriptionsAsPersonsWords(t *testing.T) {
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 193)]); err != nil {
		t.Fatal(err)
	}
	id := ids.New(time.Now())
	// sql-guarantee: this is an existing row in the schema before description ownership.
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO projects(id,slug,name,serving_label,blurb,created_at) VALUES($1,'old','Old','old','Original words',$2)`, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 193)+1]); err != nil {
		t.Fatal(err)
	}
	var text, source string
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT blurb,blurb_source FROM projects WHERE id=$1`, id).Scan(&text, &source); err != nil || text != "Original words" || source != "person" {
		t.Fatalf("existing description: %q %q %v", text, source, err)
	}
	// sql-guarantee: upgrades install the partial index used by project detail reads.
	var definition string
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT indexdef FROM pg_indexes WHERE schemaname='public' AND tablename='audit_log' AND indexname='audit_log_project_created'`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(definition, "(project_id)") || !strings.Contains(definition, "WHERE (kind = 'project.created'::text)") {
		t.Fatalf("creation request index: %s", definition)
	}

}
