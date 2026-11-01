package dbschema

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/ids"
)

type failedIntegrationCheck struct{ at time.Time }

func (c failedIntegrationCheck) CheckBinding(context.Context, integration.Binding) integration.Health {
	return integration.Health{Detail: "connection refused"}.At(c.at)
}

func TestMigrationPreservesAnExistingSuccessfulIntegrationBeforeItsFirstFailure(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 196)]); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: #262 stored successful checks without a lastGood member.
	// Current owner operations always write lastGood, so only the old storage
	// representation can reproduce this upgrade boundary.
	_, err := db.SQL().ExecContext(t.Context(), `INSERT INTO integrations(id,name,capability,provider,config_json,created_at,updated_at) VALUES('01a19500-0000-7000-8000-000000000001','upgrade-registry','registry','registry','{}','2026-11-01T00:00:00Z','2026-11-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.SQL().ExecContext(t.Context(), `INSERT INTO integration_readings(id,version,payload)VALUES('01a19500-0000-7000-8000-000000000001',1,'{"checked":true,"checkedAt":"2026-11-01T00:00:00Z","ok":true,"detail":"registry accepted credentials","stateLabel":"passed"}')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 0, 5, 0, 0, time.UTC)
	owner := integration.NewService(db, integration.Dependencies{Now: func() time.Time { return now }})
	if _, err := owner.TestSaved(t.Context(), "upgrade-registry", failedIntegrationCheck{now}); err != nil {
		t.Fatal(err)
	}
	h, err := owner.RecordedHealth(t.Context(), "upgrade-registry")
	if err != nil || h.StateLabel != integration.HealthFailed || h.LastGood == nil || h.LastGood.Detail != "registry accepted credentials" || h.LastGood.CheckedAt != "2026-11-01T00:00:00Z" || h.Failure != "connection refused" {
		t.Fatalf("upgrade lost good evidence: %+v %v", h, err)
	}
}

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
