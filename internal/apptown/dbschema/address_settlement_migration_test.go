package dbschema

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

func migrationIndex(t *testing.T, n int) int {
	t.Helper()
	for i, m := range migrations {
		if m.n == n {
			return i
		}
	}
	t.Fatalf("migration %d missing", n)
	return -1
}

// Migration 190 settles the addresses 174 left unconfirmed from the rule that
// served them before 174, keeps each settled deployment's URL, deletes rows
// whose records disagree or whose hosts another deployment claims, leaves
// ready rows alone and drops the confirmation state.
func TestMigration190SettlesLegacyAddressesFromTheRecordedRule(t *testing.T) {
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 174)]); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	target, bare := ids.New(now), ids.New(now)
	// sql-guarantee: pre-174 Portainer targets existed before the address owner; no current operation writes this historical shape.
	if _, err := db.Exec(`INSERT INTO targets(id,name,kind,ingress_json,created_at,updated_at) VALUES ($1,'settle-intranet','portainer','{"domain":"Apps.Example."}',$3,$3),($2,'settle-bare','portainer','{}',$3,$3)`, target, bare, now); err != nil {
		t.Fatal(err)
	}
	deployment := func(slug, releaseDomain, url string, on ids.ID) ids.ID {
		t.Helper()
		project, dep := ids.New(now), ids.New(now)
		insert := `INSERT INTO projects(id,slug,name,release_domain,created_at) VALUES ($1,$2,$2,$3,$4)`
		// sql-guarantee: a pre-174 project's release domain is historical input no current operation writes.
		if _, err := db.Exec(insert, project, slug, releaseDomain, now); err != nil {
			t.Fatal(err)
		}
		// sql-guarantee: the stored serving URL is the pre-174 release's record, which the settlement compares.
		if _, err := db.Exec(`INSERT INTO deployments(id,project_id,name,target_id,url,updated_at,handle_json) VALUES ($1,$2,'lan',$3,$4,$5,'{}')`, dep, project, on, url, now); err != nil {
			t.Fatal(err)
		}
		return dep
	}
	plain := deployment("plain", "", "https://plain.apps.example", target)
	// The stack may have been released under an earlier apps domain; its
	// secondary host follows the domain captured at 174 (the named limit).
	vanity := deployment("vanity", "Vanity.Example.", "https://vanity.example/", target)
	disagrees := deployment("moved", "", "https://elsewhere.example", target)
	sharedA := deployment("shared-a", "shared.example", "https://shared.example", target)
	sharedB := deployment("shared-b", "shared.example", "https://shared.example", target)
	noDomain := deployment("nodomain", "", "https://nodomain.", bare)
	neverReleased := deployment("never", "", "", target)
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 190)]); err != nil {
		t.Fatal(err)
	}
	ready := ids.New(now)
	// sql-guarantee: a second deployment of an existing project, recorded after 174 with a ready address.
	if _, err := db.Exec(`INSERT INTO deployments(id,project_id,name,target_id,url,updated_at,handle_json) SELECT $1,project_id,'current',target_id,'',$2,'{}' FROM deployments WHERE id=$3`, ready, now, plain); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: a row confirmed before this migration; the settlement must leave it as it is.
	if _, err := db.Exec(`INSERT INTO deployment_address_settings(deployment_id,generated_hostname,certificate_domain,revision,verification_state) VALUES ($1,'app-current.apps.example','apps.example',3,'ready')`, ready); err != nil {
		t.Fatal(err)
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 190)+1]); err != nil {
		t.Fatal(err)
	}

	type row struct {
		generated, custom, certificate string
		revision                       int64
	}
	read := func(dep ids.ID) (row, bool) {
		t.Helper()
		var r row
		// sql-guarantee: the settled storage is the migration's guarantee.
		err := db.QueryRow(`SELECT generated_hostname,custom_hostname,certificate_domain,revision FROM deployment_address_settings WHERE deployment_id=$1`, dep).Scan(&r.generated, &r.custom, &r.certificate, &r.revision)
		if errors.Is(err, sql.ErrNoRows) {
			return r, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return r, true
	}
	for name, c := range map[string]struct {
		dep  ids.ID
		want *row
	}{
		"no release domain":          {plain, &row{"plain.apps.example", "", "apps.example", 2}},
		"release domain kept as URL": {vanity, &row{"vanity.apps.example", "vanity.example", "apps.example", 2}},
		"stored URL disagrees":       {disagrees, nil},
		"host claimed twice, first":  {sharedA, nil},
		"host claimed twice, other":  {sharedB, nil},
		"no apps domain":             {noDomain, nil},
		"never released":             {neverReleased, nil},
		"already ready":              {ready, &row{"app-current.apps.example", "", "apps.example", 3}},
	} {
		got, ok := read(c.dep)
		if c.want == nil {
			if ok {
				t.Errorf("%s: kept %+v, want the row deleted", name, got)
			}
			continue
		}
		if !ok || got != *c.want {
			t.Errorf("%s: %+v (present %v), want %+v", name, got, ok, *c.want)
		}
	}
	var claims int
	// sql-guarantee: deleted desired settings are not a teardown; retained claims are storage the migration must keep.
	if err := db.QueryRow(`SELECT count(*) FROM deployment_hostname_claimants WHERE deployment_id=$1`, disagrees).Scan(&claims); err != nil || claims == 0 {
		t.Fatalf("a deleted row's claims were released: %d %v", claims, err)
	}
	var columns int
	// sql-guarantee: the dropped confirmation state is a storage fact.
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name='deployment_address_settings' AND column_name IN ('verification_state','legacy_evidence_json')`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("confirmation columns remain: %d %v", columns, err)
	}
	// sql-guarantee: the unconditional ready check is a storage constraint.
	if _, err := db.Exec(`INSERT INTO deployment_address_settings(deployment_id) VALUES ($1)`, neverReleased); err == nil {
		t.Fatal("an empty address row was accepted")
	}
}
