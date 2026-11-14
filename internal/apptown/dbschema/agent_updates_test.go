package dbschema

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/ids"
)

func TestAgentUpdateMigrationPreservesRecordedObservations(t *testing.T) {
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:len(migrations)-1]); err != nil {
		t.Fatal(err)
	}
	pid, err := database.Transact(t.Context(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "update-history", Name: "Update history"})
	})
	if err != nil {
		t.Fatal(err)
	}
	block, err := wow.New(db, wow.Dependencies{}).CreateBlock(t.Context(), "lang/test/updates", "Dependency conventions.", "", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	old := `[{"kind":"update","ecosystem":"npm","files":["package.json"],"run":"npm update","runMajor":"major","runFor":"target","list":"list","appliesTo":["tool:node"]},{"kind":"update","ecosystem":"Go","files":["go.mod"],"run":"go get"}]`
	// sql-guarantee: current owner operations cannot write obsolete declaration
	// keys or historical listing observations. Seed only those legacy fields.
	if _, err := db.SQL().ExecContext(t.Context(), `UPDATE guidance_block_versions SET fixes_json=$1 WHERE block_id=$2`, old, block.ID); err != nil {
		t.Fatal(err)
	}
	moves := `[{"ecosystem":"npm","package":"example","from":"v2026.6","to":"v2026.7"}]`
	var changes []ids.ID
	for _, outcome := range []string{"held", "skipped"} {
		created, err := database.Transact(t.Context(), db, func(tx *database.Tx) (workflow.CreatedChange, error) {
			return workflow.CreateChangeIn(access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule", ProjectID: pid}}), tx, workflow.ChangeDraft{ProjectID: pid, Title: "Historical update", Queue: "updates"}, "fixture", time.Now())
		})
		if err != nil {
			t.Fatal(err)
		}
		changes = append(changes, created.Change.ID)
		if _, err := db.SQL().ExecContext(t.Context(), `UPDATE automated_changes SET packages=$1,outcome=$2 WHERE id=$3`, moves, outcome, created.Change.ID); err != nil {
			t.Fatal(err)
		}
	}
	// sql-guarantee: a retained summary whose Change has already been purged is
	// historical storage that current admissions cannot create.
	if _, err := db.SQL().ExecContext(t.Context(), `DELETE FROM changes WHERE id=$1`, changes[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.SQL().QueryRowContext(t.Context(), `SELECT fixes_json::text FROM guidance_block_versions WHERE block_id=$1`, block.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	ds, err := fix.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []fix.Declaration{{Kind: "update", Ecosystem: "npm", Files: []string{"package.json"}}, {Kind: "update", Ecosystem: "Go", Files: []string{"go.mod"}}}
	if !reflect.DeepEqual(ds, want) {
		t.Fatalf("order or classification changed: %+v", ds)
	}
	for _, id := range changes {
		var packages, files json.RawMessage
		if err := db.SQL().QueryRowContext(t.Context(), `SELECT packages,files FROM automated_changes WHERE id=$1`, id).Scan(&packages, &files); err != nil {
			t.Fatal(err)
		}
		var got []workflow.PackageMove
		if err := json.Unmarshal(packages, &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].From != "v2026.6" || got[0].To != "v2026.7" || string(files) != "[]" {
			t.Fatalf("lost history: %s %s", packages, files)
		}
	}
}
