package dbschema

import (
	"database/sql"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func TestMigration174RetainsLegacyAddressEvidenceAndEveryConflictingClaimant(t *testing.T) {
	db := releaseDB(t)
	index := -1
	for i, m := range migrations {
		if m.n == 174 {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("migration174 missing")
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:index]); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	target := ids.New(now)
	// sql-guarantee: legacy address inputs existed before the address owner and must not become live routing assertions during SQL upgrade.
	if _, err := db.Exec(`INSERT INTO targets(id,name,kind,ingress_json,created_at,updated_at) VALUES ($1,'address-migration','portainer','{"domain":"apps.example"}',$2,$2)`, target, now); err != nil {
		t.Fatal(err)
	}
	deps := make([]ids.ID, 3)
	for i, raw := range []string{" SAME.Example. ", "same.example", "https://invalid.example/path"} {
		project, dep := ids.New(now), ids.New(now)
		deps[i] = dep
		// sql-guarantee: current owners cannot write released global address input after this migration.
		if _, err := db.Exec(`INSERT INTO projects(id,slug,name,release_domain,created_at) VALUES ($1,$2,$2,$3,$4)`, project, "address-"+dep.String(), raw, now); err != nil {
			t.Fatal(err)
		}
		// sql-guarantee: stored serving URL is independent historical evidence and is preserved even when current global configuration differs.
		if _, err := db.Exec(`INSERT INTO deployments(id,project_id,name,target_id,url,updated_at,handle_json) VALUES ($1,$2,'lan',$3,'https://served.example',$4,'{"retained":"proof"}')`, dep, project, target, now); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func() string {
		t.Helper()
		var out string
		if err := db.QueryRow(`SELECT jsonb_agg(to_jsonb(d) ORDER BY id)::text FROM deployments d`).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := snapshot()
	dbtest.FailNextStatement(t, db, "INSERT INTO deployment_hostname_claimants")
	if _, err := applyMigrations(t.Context(), db, migrations[:index+1]); err == nil {
		t.Fatal("claim backfill failure hidden")
	}
	if version, err := schemaVersion(db); err != nil || version != 173 {
		t.Fatalf("partial version%d: %v", version, err)
	}
	var table sql.NullString
	if err := db.QueryRow(`SELECT to_regclass('deployment_address_settings')::text`).Scan(&table); err != nil || table.Valid {
		t.Fatalf("partial address table survived: %+v %v", table, err)
	}
	if snapshot() != before {
		t.Fatal("failed migration changed deployments")
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:index+1]); err != nil {
		t.Fatal(err)
	}
	if snapshot() != before {
		t.Fatal("address migration rewrote deployment identity or provider evidence")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM deployment_hostname_claimants WHERE hostname='same.example'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("conflict lost a claimant: %d %v", count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM deployment_hostname_claimants WHERE hostname='served.example'`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("stored URL evidence lost: %d %v", count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM deployment_hostname_claims WHERE hostname='invalid.example'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid configured input normalized into a claim: %d %v", count, err)
	}
	var raw, state, generated string
	if err := db.QueryRow(`SELECT legacy_evidence_json->>'projectReleaseDomain',verification_state,generated_hostname FROM deployment_address_settings WHERE deployment_id=$1`, deps[2]).Scan(&raw, &state, &generated); err != nil || raw != "https://invalid.example/path" || state != "legacy-unverified" || generated != "" {
		t.Fatalf("legacy evidence became verified settings: %q %q %q %v", raw, state, generated, err)
	}
	// sql-guarantee: even a conflicted membership must independently prevent purge bypassing confirmed teardown.
	if _, err := db.Exec(`DELETE FROM deployments WHERE id=$1`, deps[0]); err == nil {
		t.Fatal("conflicted claimant deleted without reservation settlement")
	}
	if n, err := applyMigrations(t.Context(), db, migrations[:index+1]); err != nil || n != 0 {
		t.Fatalf("replay applied%d: %v", n, err)
	}
}
