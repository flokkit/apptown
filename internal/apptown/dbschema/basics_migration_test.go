package dbschema

import (
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// The combined upgrades preserve standard task models and apply each profile
// before borrowing standard, without creating unset cells.
func TestBasicChoicesAndNamesUpgrade(t *testing.T) {
	for _, populated := range []bool{false, true} {
		name := "default-only"
		if populated {
			name = "multiple-profiles"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// Build the historical schema; changing a fresh database's version
			// would replay later DDL that is already installed.
			db := releaseDB(t)
			if _, err := applyMigrations(t.Context(), db, migrations[:migrationIndex(t, 198)+1]); err != nil {
				t.Fatal(err)
			}
			s := model.New(db)
			// The current owner intentionally cannot seed old fallback values.
			// sql-guarantee: isolate the released model cells and stock chain.
			if _, err := db.SQL().ExecContext(t.Context(), `DELETE FROM model_slots;
DELETE FROM slot_fallbacks;
INSERT INTO slot_fallbacks(slot,fallback) VALUES
('plan','default'),('implement','default'),('review','default'),('security','review'),
('fast','default'),('chat','default'),('worker','default'),('scout','fast')`); err != nil {
				t.Fatal(err)
			}
			// The current owner also refuses retired cell names. Seed the old
			// representation directly to exercise the upgrade.
			setOldCell := func(slot, profile, pattern string) {
				t.Helper()
				// sql-guarantee: pre-upgrade cells use the retired names, which the owner refuses.
				if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO model_slots(slot, profile, pattern, updated_at) VALUES ($1, $2, $3, '2026-01-01Z')`, slot, profile, pattern); err != nil {
					t.Fatal(err)
				}
			}
			setOldCell("default", "standard", "vendor/standard")
			if populated {
				for _, cell := range [][3]string{
					{"fast", "standard", "vendor/quick"}, {"review", "standard", "other/review"},
					{"default", "high", "vendor/high"}, {"fast", "high", "vendor/high-quick"}, {"implement", "high", "vendor/code"},
					{"default", "luna", "local/luna"}, {"review", "luna", "other/luna-review"}, {"chat", "luna", "local/chat"},
				} {
					setOldCell(cell[0], cell[1], cell[2])
				}
			}
			tasks := []string{"default", "plan", "implement", "review", "security", "fast", "chat", "worker", "scout"}
			if n, err := Migrate(t.Context(), db); err != nil || n != Latest()-198 {
				t.Fatalf("upgrade: %d %v", n, err)
			}
			if err := s.SeedFallbacks(t.Context()); err != nil {
				t.Fatal(err)
			}
			selection := model.NewSelection(s)
			projects := project.New(db, nil)
			for _, profile := range []string{"standard", "high", "luna"} {
				projectID, err := database.Transact(t.Context(), db, func(tx *database.Tx) (ids.ID, error) {
					return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "upgrade-" + profile, Name: profile})
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := projects.SetModelProfile(t.Context(), projectID, profile); err != nil {
					t.Fatal(err)
				}
				for _, task := range append(tasks, "secondary-fast") {
					requested := task
					if task == "default" {
						requested = "main-strong"
					}
					want := "vendor/standard"
					if populated {
						switch profile {
						case "standard":
							if task == "review" || task == "security" {
								want = "other/review"
							}
							if task == "fast" || task == "scout" || task == "secondary-fast" {
								want = "vendor/quick"
							}
						case "high":
							want = "vendor/high"
							if task == "implement" {
								want = "vendor/code"
							}
							if task == "fast" || task == "scout" || task == "secondary-fast" {
								want = "vendor/high-quick"
							}
						case "luna":
							want = "local/luna"
							if task == "review" || task == "security" {
								want = "other/luna-review"
							}
							if task == "chat" {
								want = "local/chat"
							}
						}
					}
					if got, _, _, err := selection.Resolve(t.Context(), projectID, requested); err != nil || got != want {
						t.Errorf("%s/%s: %q, want %q (%v)", profile, requested, got, want, err)
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
			if len(cells["main-fast"]) != want || cells["main-fast"]["luna"] != "" {
				t.Fatalf("only explicit fast cells should move: %v", cells["main-fast"])
			}
		})
	}
}
