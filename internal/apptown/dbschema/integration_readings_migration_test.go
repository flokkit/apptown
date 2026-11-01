package dbschema

import (
	"github.com/flokkit/apptown/internal/ids"
	"testing"
	"time"
)

func TestMigration195KeepsIntegrationsAndStartsWithNoHealthAssertion(t *testing.T) {
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 195)]); err != nil {
		t.Fatal(err)
	}
	id := ids.New(time.Now())
	// sql-guarantee: model an existing integration before retained checks existed.
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO integrations(id,name,capability,provider,created_at,updated_at) VALUES($1,'registry','registry','npm',$2,$2)`, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 195)+1]); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM integrations WHERE id=$1`, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("integration: %d %v", n, err)
	}
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM integration_readings`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("invented health result: %d %v", n, err)
	}
}
