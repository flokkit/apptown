package dbschema

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

// Upgrade preserves configured beta providers, including retired targets whose
// retained records still need their pages, without enabling unused providers.
func TestFeatureMigrationPreservesConfiguredBetaAreas(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	index := 0
	for index < len(migrations) && migrations[index].n != 218 {
		index++
	}
	if index == len(migrations) {
		t.Fatal("feature migration missing")
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:index]); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// sql-guarantee: historical records preceding feature settings; current owner
	// admission cannot reproduce an upgrade's source schema and missing switches.
	if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO targets(id,name,kind,status,created_at,updated_at) VALUES ($1,'kept-platform','do-app','retired',$2,$2)`, ids.New(now), now); err != nil {
		t.Fatal(err)
	}
	// A deleted eval and an authorized agent configuration are recorded use,
	// even with no live eval or MCP session. These pre-switch facts must survive.
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO audit_log(id,at,kind,subject) VALUES ($1,$2,'eval.deleted','kept-history')`, ids.New(now), now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO oauth_clients(client_id,name,redirect_uris,created_at) VALUES ('outside-agent','Outside agent','["http://127.0.0.1/callback"]',$1)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := applyMigrations(t.Context(), db, migrations); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT value FROM instance_settings WHERE key='feature.app-platform'`).Scan(&value); err != nil || value != "true" {
		t.Fatalf("provider lost: %q %v", value, err)
	}
	for _, area := range []string{"evals", "mcp"} {
		if err := db.SQL().QueryRowContext(t.Context(), `SELECT value FROM instance_settings WHERE key=$1`, "feature."+area).Scan(&value); err != nil || value != "true" {
			t.Fatalf("%s use lost: %q %v", area, value, err)
		}
	}
	var unused int
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM instance_settings WHERE key IN ('feature.portainer','feature.bucket-site')`).Scan(&unused); err != nil || unused != 0 {
		t.Fatalf("unused beta enabled: %d %v", unused, err)
	}
}
