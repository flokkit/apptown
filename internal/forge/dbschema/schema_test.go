package dbschema

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"flokkit.io/forge/internal/dbtest"
)

func TestSchemaBaselineRefusesUnsupportedStateWithoutMutation(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"missing-table", "missing-row", "duplicate-row", "88", "146", "999"} {
		t.Run(version, func(t *testing.T) {
			db := emptyDB(t)
			if _, err := db.Exec(`CREATE TABLE sentinel (value TEXT)`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO sentinel VALUES ('preserve this')`); err != nil {
				t.Fatal(err)
			}
			if version != "missing-table" {
				if _, err := db.Exec(`CREATE TABLE schema_version (version BIGINT)`); err != nil {
					t.Fatal(err)
				}
				switch version {
				case "missing-row":
				case "duplicate-row":
					if _, err := db.Exec(`INSERT INTO schema_version VALUES (147),(147)`); err != nil {
						t.Fatal(err)
					}
				default:
					if _, err := db.Exec(`INSERT INTO schema_version VALUES (?)`, version); err != nil {
						t.Fatal(err)
					}
				}
			}
			var before int
			if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			var versionsBefore, versionsAfter string
			if version != "missing-table" {
				if err := db.QueryRow(`SELECT COALESCE(jsonb_agg(version ORDER BY version)::text,'[]') FROM schema_version`).Scan(&versionsBefore); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Migrate(context.Background(), db); err == nil {
				t.Fatal("unsupported schema accepted")
			}
			if version != "missing-table" {
				if err := db.QueryRow(`SELECT COALESCE(jsonb_agg(version ORDER BY version)::text,'[]') FROM schema_version`).Scan(&versionsAfter); err != nil {
					t.Fatal(err)
				}
				if versionsAfter != versionsBefore {
					t.Fatal("refused open changed schema versions")
				}
			}
			var after int
			var value string
			if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT value FROM sentinel`).Scan(&value); err != nil {
				t.Fatal(err)
			}
			if before != after || value != "preserve this" {
				t.Fatal("refused open changed the database")
			}
		})
	}
}

func TestSchemaFreshCreationAndFutureMigrationAreAtomic(t *testing.T) {
	t.Parallel()
	db := emptyDB(t)
	dbtest.FailNextStatement(t, db, "INSERT INTO schema_version")
	if _, err := Migrate(context.Background(), db); err == nil {
		t.Fatal("fresh stamp failure hidden")
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatal("failed creation left a partial schema")
	}
	if _, err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if v, err := schemaVersion(db); err != nil || v != latestSchemaVersion() {
		t.Fatalf("fresh version %d: %v", v, err)
	}
	list := append(append([]migration{}, migrations...), migration{n: latestSchemaVersion() + 1, note: "test current upgrade", stmts: []string{
		`CREATE TABLE upgrade_evidence (value TEXT)`, `INSERT INTO upgrade_evidence VALUES ('committed')`,
	}})
	for _, fragment := range []string{"INSERT INTO upgrade_evidence", "UPDATE schema_version"} {
		dbtest.FailNextStatement(t, db, fragment)
		if _, err := applyMigrations(context.Background(), db, list); err == nil {
			t.Fatalf("upgrade failure hidden: %s", fragment)
		}
		if v, err := schemaVersion(db); err != nil || v != latestSchemaVersion() {
			t.Fatal("failed migration advanced version")
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='upgrade_evidence'`).Scan(&tables); err != nil || tables != 0 {
			t.Fatal("failed migration left evidence")
		}
	}
	if n, err := applyMigrations(context.Background(), db, list); err != nil || n != 1 {
		t.Fatalf("upgrade %d: %v", n, err)
	}
	if n, err := applyMigrations(context.Background(), db, list); err != nil || n != 0 {
		t.Fatalf("replay %d: %v", n, err)
	}
	var value string
	if err := db.QueryRow(`SELECT value FROM upgrade_evidence`).Scan(&value); err != nil || value != "committed" {
		t.Fatal("upgrade evidence missing")
	}
}

// A migration runs against a populated AppTown as an earlier release left it
// (the root package's upgrade fixtures): a synthetic next migration that
// reads the release's records applies, and one that fails after it leaves
// the version and every record as they were, so the copies taken before the
// upgrade are only needed once the new build has written.
func TestAMigrationOverAReleasesRecordsAppliesOrLeavesThemAlone(t *testing.T) {
	t.Parallel()
	fixtures, err := filepath.Glob(filepath.Join("..", "testdata", "upgrade", "*", "release-schema.sql"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no upgrade fixture: %v", err)
	}
	for _, schemaPath := range fixtures {
		dir := filepath.Dir(schemaPath)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			schemaSQL, err := os.ReadFile(schemaPath)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := dbtest.ReadRows(filepath.Join(dir, "rows.jsonl.gz"))
			if err != nil {
				t.Fatal(err)
			}
			db := emptyDB(t)
			// sql-guarantee: restoring a release's storage is what no owner operation can do.
			if err := dbtest.Restore(t.Context(), db, string(schemaSQL), rows); err != nil {
				t.Fatal(err)
			}
			if _, err := Migrate(t.Context(), db); err != nil {
				t.Fatalf("upgrading the release: %v", err)
			}
			records := func(skip ...string) []dbtest.Row {
				t.Helper()
				got, err := dbtest.Snapshot(t.Context(), db, func(table string) bool { return table == "schema_version" || slices.Contains(skip, table) })
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			before := records()
			next := latestSchemaVersion() + 1
			list := append(append([]migration{}, migrations...),
				migration{n: next, note: "test: reads the release's records", stmts: []string{
					`CREATE TABLE upgrade_evidence AS SELECT id, title FROM changes`,
				}},
				migration{n: next + 1, note: "test: fails after rewriting them", stmts: []string{
					`UPDATE changes SET title = 'rewritten'`, `UPDATE schema_version SET version = version`,
				}})
			dbtest.FailNextStatement(t, db, "UPDATE schema_version SET version = version")
			if _, err := applyMigrations(t.Context(), db, list); err == nil {
				t.Fatal("the failing migration was hidden")
			}
			if v, err := schemaVersion(db); err != nil || v != next {
				t.Fatalf("version after the failure %d (want %d): %v", v, next, err)
			}
			if after := records("upgrade_evidence"); !slices.EqualFunc(before, after, func(a, b dbtest.Row) bool { return a.Table == b.Table && string(a.Row) == string(b.Row) }) {
				t.Fatal("a failed migration changed the release's records")
			}
			var copied, changes int
			if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM upgrade_evidence), (SELECT COUNT(*) FROM changes)`).Scan(&copied, &changes); err != nil || copied != changes || changes == 0 {
				t.Fatalf("the applied migration read %d of %d changes: %v", copied, changes, err)
			}
		})
	}
}
