package model

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dbschema"
)

// The upgrade preserves every old stock task and the quick checks that now
// request secondary-fast, for default-only and populated multi-profile installs.
func TestBasicChoicesUpgradePreservesStockTaskModels(t *testing.T) {
	for _, populated := range []bool{false, true} {
		name := "default-only"
		if populated {
			name = "multiple-profiles"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := testDB(t)
			s := New(db)
			// The current owner intentionally cannot seed old fallback values.
			// sql-guarantee: reproduce the released stock chain and schema version.
			if _, err := db.SQL().ExecContext(t.Context(), `UPDATE schema_version SET version=198;
INSERT INTO slot_fallbacks(slot,fallback) VALUES
('plan','default'),('implement','default'),('review','default'),('security','review'),
('fast','default'),('chat','default'),('worker','default'),('scout','fast')`); err != nil {
				t.Fatal(err)
			}
			if err := s.SetDefault(t.Context(), "vendor/standard"); err != nil {
				t.Fatal(err)
			}
			if populated {
				for _, cell := range [][3]string{
					{"fast", "standard", "vendor/quick"}, {"review", "standard", "other/review"},
					{"default", "high", "vendor/high"}, {"fast", "high", "vendor/high-quick"}, {"implement", "high", "vendor/code"},
					{"default", "luna", "local/luna"}, {"review", "luna", "other/luna-review"}, {"chat", "luna", "local/chat"},
				} {
					if err := s.Set(t.Context(), cell[0], cell[1], cell[2], "fixture"); err != nil {
						t.Fatal(err)
					}
				}
			}
			tasks := []string{"default", "plan", "implement", "review", "security", "fast", "chat", "worker", "scout"}
			before := map[string]map[string]string{}
			for _, profile := range []string{"standard", "high", "luna"} {
				before[profile] = map[string]string{}
				for _, task := range tasks {
					got, _, _, err := s.resolve(t.Context(), profile, task)
					if err != nil {
						t.Fatal(err)
					}
					before[profile][task] = got
				}
			}
			if n, err := dbschema.Migrate(t.Context(), db); err != nil || n != 1 {
				t.Fatalf("upgrade: %d %v", n, err)
			}
			if err := s.SeedFallbacks(t.Context()); err != nil {
				t.Fatal(err)
			}
			for profile, old := range before {
				for _, task := range append(tasks, "secondary-fast") {
					oldTask := task
					if task == "secondary-fast" {
						oldTask = "fast"
					}
					if got, _, _, err := s.resolve(t.Context(), profile, task); err != nil || got != old[oldTask] {
						t.Errorf("%s/%s: %q, was %q (%v)", profile, task, got, old[oldTask], err)
					}
				}
			}
			cells, err := s.Cells(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(cells["fast"]) != 0 {
				t.Fatalf("fast cells were not moved: %v", cells["fast"])
			}
			want := 0
			if populated {
				want = 2
			}
			if len(cells["primary-fast"]) != want || cells["primary-fast"]["luna"] != "" {
				t.Fatalf("only explicit fast cells should move: %v", cells["primary-fast"])
			}
		})
	}
}
