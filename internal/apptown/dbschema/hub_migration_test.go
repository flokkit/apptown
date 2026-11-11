package dbschema

import (
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
)

func TestHubUpgradeRemovesAttachmentsAndRefusesAmbiguousConnectors(t *testing.T) {
	for _, scenario := range []string{"one", "two", "gitlab", "gitlab in config"} {
		t.Run(scenario, func(t *testing.T) {
			db := releaseDB(t)
			if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 224)]); err != nil {
				t.Fatal(err)
			}
			// sql-guarantee: recreate the old per-project rows before the upgrade.
			if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO project_integrations(project_id,capability,binding,created_at) VALUES($1,'container-registry','hub',now())`, ids.New(time.Now())); err != nil {
				t.Fatal(err)
			}
			provider, config := "direct", `{}`
			if scenario == "gitlab" {
				provider = "gitlab"
			}
			if scenario == "gitlab in config" {
				// The shape older AppTowns recorded: the service kind's connector
				// names GitLab in its configuration.
				provider, config = "service", `{"provider":"gitlab","url":"registry.example","group":"team"}`
			}
			if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO integrations(id,name,capability,provider,config_json,created_at,updated_at) VALUES($1,'hub','container-registry',$2,$3,now(),now())`, ids.New(time.Now()), provider, config); err != nil {
				t.Fatal(err)
			}
			if scenario == "two" {
				if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO integrations(id,name,capability,provider,created_at,updated_at) VALUES($1,'other','container-registry','direct',now(),now())`, ids.New(time.Now())); err != nil {
					t.Fatal(err)
				}
			}
			_, err := applyMigrations(t.Context(), db, migrations)
			if scenario != "one" {
				if err == nil || !strings.Contains(err.Error(), "hub") {
					t.Fatalf("ambiguous upgrade: %v", err)
				}
				if scenario == "two" && !strings.Contains(err.Error(), "other") {
					t.Fatalf("upgrade did not name both connectors: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var n int
			if err := db.SQL().QueryRowContext(t.Context(), `SELECT count(*) FROM project_integrations WHERE capability='container-registry'`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("attachments remain: %d %v", n, err)
			}
		})
	}
}
