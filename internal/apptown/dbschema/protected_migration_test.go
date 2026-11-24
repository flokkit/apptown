package dbschema

import (
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

func TestProtectionMigrationPreservesStartingRuleAndReason(t *testing.T) {
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 246)]); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	pid := ids.New(now)
	// Historical rows precede explicit protection; current constructors cannot create them.
	if _, err := db.SQL().ExecContext(t.Context(), "INSERT INTO projects(id,slug,name,serving_label,created_at) VALUES($1,'protected','Protected','protected',$2)", pid, now); err != nil {
		t.Fatal(err)
	}
	type example struct {
		id                     ids.ID
		audience, data, reason string
	}
	cases := []example{}
	for _, audience := range []string{"team", "organisation", "partners", "public"} {
		for _, data := range []string{"synthetic", "non-production", "production"} {
			reason := ""
			if data == "production" {
				reason = "production data"
			}
			if audience == "partners" {
				reason = "partners audience"
			}
			if audience == "public" {
				reason = "public audience"
			}
			id := ids.New(now)
			// sql-guarantee: migrate each combination of historical classification values.
			if _, err := db.SQL().ExecContext(t.Context(), "INSERT INTO deployments(id,project_id,name,audience,data_class,updated_at) VALUES($1,$2,$3,$4,$5,$6)", id, pid, fmt.Sprintf("d%d", len(cases)), audience, data, now); err != nil {
				t.Fatal(err)
			}
			cases = append(cases, example{id, audience, data, reason})
		}
	}
	if _, err := applyMigrations(t.Context(), db, migrations); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		var protected bool
		var reason string
		if err := db.SQL().QueryRowContext(t.Context(), "SELECT protected,protected_reason FROM deployments WHERE id=$1", c.id).Scan(&protected, &reason); err != nil {
			t.Fatal(err)
		}
		if protected != (c.reason != "") || reason != c.reason {
			t.Fatalf("%s/%s: protection=%t reason=%q, want %q", c.audience, c.data, protected, reason, c.reason)
		}
	}
}
