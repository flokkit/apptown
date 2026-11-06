package dbschema

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"testing"
	"time"
)

func TestMigration211MapsFeedsAndInventsNoStandingAuthorization(t *testing.T) {
	db := releaseDB(t)
	last := 0
	var selected migration
	for i, m := range migrations {
		if m.n < 211 {
			last = i + 1
		}
		if m.n == 211 {
			selected = m
		}
	}
	if selected.n != 211 {
		t.Fatal("deploy migration missing")
	}
	if _, err := applyMigrations(t.Context(), db, migrations[:last]); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		slug  string
		feeds map[string]string
		want  map[string]string
	}{
		{"both", map[string]string{"main": "manual", "prod": "manual", "other": "landing", "stage": "schedule"}, map[string]string{"main": "main", "prod": "manual", "other": "main", "stage": "promotion"}},
		{"fallback", map[string]string{"prod": "manual", "stage": "promotion"}, map[string]string{"prod": "main", "stage": "promotion"}},
		{"hand", map[string]string{"stage": "promotion", "timed": "schedule", "hand": "manual"}, map[string]string{"stage": "manual", "timed": "manual", "hand": "manual"}},
		{"named", map[string]string{"auto": "landing", "stage": "schedule"}, map[string]string{"auto": "main", "stage": "promotion"}},
	} {
		t.Run(scenario.slug, func(t *testing.T) {
			projectID, err := database.Transact(t.Context(), db, func(tx *database.Tx) (ids.ID, error) {
				return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: scenario.slug, Name: scenario.slug})
			})
			if err != nil {
				t.Fatal(err)
			}
			for name, feed := range scenario.feeds {
				// sql-guarantee: this is pre-upgrade data, including removed feed values
				// and no setter columns. Current deployment operations cannot create it.
				if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO deployments(id,project_id,name,kind,feed,updated_at) VALUES($1,$2,$3,'standing',$4,$5)`, ids.New(time.Now()), projectID, name, feed, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			// Test each fixture against the same migration without applying it twice:
			// accumulate old rows and inspect all of them below after one forward write.
		})
	}
	if err := applyMigration(context.Background(), db, selected); err != nil {
		t.Fatal(err)
	}
	rows, err := db.SQL().QueryContext(t.Context(), `SELECT p.slug,d.name,d.feed,d.feed_authority_json,d.feed_revision,COALESCE(src.feed,'') FROM deployments d JOIN projects p ON p.id=d.project_id LEFT JOIN deployments src ON src.id=d.promoted_from_id WHERE p.slug IN ('both','fallback','hand','named')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[string]string{"both/main": "main", "both/prod": "manual", "both/other": "main", "both/stage": "promotion", "fallback/prod": "main", "fallback/stage": "promotion", "hand/stage": "manual", "hand/timed": "manual", "hand/hand": "manual", "named/auto": "main", "named/stage": "promotion"}
	seen := 0
	for rows.Next() {
		var slug, name, feed, source string
		var authority []byte
		var revision int64
		if err := rows.Scan(&slug, &name, &feed, &authority, &revision, &source); err != nil {
			t.Fatal(err)
		}
		key := slug + "/" + name
		if feed != want[key] || len(authority) != 0 || revision != 1 || (feed == "promotion" && source != "main") {
			t.Errorf("%s: feed=%s setter=%s revision=%d source=%s", key, feed, authority, revision, source)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != len(want) {
		t.Fatalf("mapped %d of %d", seen, len(want))
	}
}
