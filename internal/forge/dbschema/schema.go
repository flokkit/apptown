// Package dbschema embeds the assembled application DDL and owns checked migrations.
// It has no dependency on application assembly, domain services or job workers.
package dbschema

import (
	"context"
	_ "embed"
	"fmt"
	"log"

	"flokkit.io/forge/internal/database"
)

// schemaSQL creates a fresh PostgreSQL database atomically. Existing databases
// must satisfy schemaBaseline before any DDL runs; older installations require
// the explicit maintenance cutover, never a startup compatibility path.
// New tables and columns go in the owner's schema.sql fragment and a numbered
// migration. schemaSQL is generated from those fragments; never edit it directly.
type migration struct {
	n     int
	note  string
	stmts []string
}

// schemaBaseline is the 1.0 baseline: every forge in service was converted
// to UUID identities (schema 147, the identity cutover) and the migrations
// up to it were collapsed into schema.sql. An older database is refused; it
// must first be converted by forged -identity-cutover, which only
// development builds from before the baseline carry: 1.0.0-rc.1, the first
// release candidate, already starts at the baseline.
//
// From here the chain only appends: a released migration keeps its number
// and statements for ever (migrations.lock, scripts/migrations-append-only.sh).
const schemaBaseline = 147

var migrations = []migration{
	{n: 148, note: "changes.closed_as: a change closed because there was nothing to change says so", stmts: []string{
		`ALTER TABLE changes ADD COLUMN closed_as TEXT NOT NULL DEFAULT '' CONSTRAINT change_closed_as CHECK(closed_as IN ('','nothing-to-change'))`,
		// One change on an early installation was closed by hand, before the
		// engine's close existed, for exactly this reason ("Nothing to update:
		// the project declares no dependencies"). Its id matches nothing on
		// any other forge.
		`UPDATE changes SET closed_as = 'nothing-to-change' WHERE id = '01a0f60c-aaae-7000-9de8-0b5d465313ba' AND state = 'abandoned'`,
	}},
	{n: 149, note: "deployment profiles: each generation's applied record, and operators' resource overrides", stmts: []string{
		`ALTER TABLE deployment_attempt_inputs ADD COLUMN applied JSONB NOT NULL DEFAULT '{}'::jsonb`,
		`CREATE TABLE deployment_overrides (
  deployment_id UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  part TEXT NOT NULL,
  memory_bytes BIGINT NOT NULL DEFAULT 0,
  cpus DOUBLE PRECISION NOT NULL DEFAULT 0,
  reason TEXT NOT NULL,
  set_by TEXT NOT NULL DEFAULT '',
  set_by_principal UUID,
  set_by_via TEXT NOT NULL DEFAULT '',
  set_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY(deployment_id, part)
)`,
	}},
	{n: 150, note: "deployment limit events: out-of-memory kills and exits per part and need", stmts: []string{
		`CREATE TABLE deployment_limit_events (
  id UUID PRIMARY KEY,
  deployment_id UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  part TEXT NOT NULL,
  kind TEXT NOT NULL CONSTRAINT deployment_limit_event_kind CHECK(kind IN ('out-of-memory','exited')),
  container TEXT NOT NULL,
  at TIMESTAMPTZ NOT NULL,
  exit_code INTEGER,
  UNIQUE(deployment_id, container, kind, at)
)`,
		`CREATE INDEX deployment_limit_events_at ON deployment_limit_events(deployment_id, at)`,
	}},
	{n: 151, note: "explicit unlimited memory resource overrides", stmts: []string{"ALTER TABLE deployment_overrides ADD COLUMN memory_unlimited BOOLEAN NOT NULL DEFAULT false"}},
	{n: 152, note: "deployment limit events: the generation each happened under, when known", stmts: []string{"ALTER TABLE deployment_limit_events ADD COLUMN generation BIGINT"}},
	{n: 153, note: "backup drills: the item of the set each filled and checked", stmts: []string{
		"ALTER TABLE backup_drills ADD COLUMN need TEXT NOT NULL DEFAULT ''",
		// Earlier rows name their item only in their place: a bucket's new
		// store ("a new s3 store on <runner>"), or a new database on the
		// set item's instance ("<database> on <instance id>"), which now
		// reads as the cluster it was made on.
		`UPDATE backup_drills SET need = substring(place from '^a new (.+) store on ') WHERE place ~ '^a new .+ store on '`,
		`UPDATE backup_drills d SET need = e->>'name', place = 'a new database on the deployment''s cluster'
  FROM backups b, jsonb_array_elements(b.items_json) e
  WHERE d.need = '' AND b.id = d.backup_id AND e->>'kind' = 'need' AND COALESCE(e->>'bucket', '') = ''
    AND COALESCE(e->>'instance', '') <> '' AND right(d.place, length(e->>'instance') + 4) = ' on ' || (e->>'instance')`,
	}},
}

// latestSchemaVersion is the number a fresh database is stamped with.
func latestSchemaVersion() int {
	if len(migrations) == 0 {
		return schemaBaseline
	}
	return migrations[len(migrations)-1].n
}

// Migrate creates an empty database, or validates and upgrades a supported one.
func Migrate(ctx context.Context, db *database.DB) (applied int, err error) {
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&tables); err != nil {
		return 0, fmt.Errorf("schema: %w", err)
	}
	if tables != 0 {
		applied, err := applyMigrations(ctx, db, migrations)
		if err != nil {
			return applied, err
		}
		return applied, nil
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	// Native execution is required for the complete multi-statement DDL.
	if _, err := tx.Native().Exec(schemaSQL); err != nil {
		return 0, fmt.Errorf("schema: create: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, latestSchemaVersion()); err != nil {
		return 0, fmt.Errorf("schema: stamp fresh database: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return 0, nil
}

func schemaVersion(db *database.DB) (int, error) {
	var count, version int
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(MIN(version), 0) FROM schema_version`).Scan(&count, &version); err != nil {
		return 0, fmt.Errorf("existing database has no readable schema version: %w", err)
	}
	if count != 1 {
		return 0, fmt.Errorf("existing database must have exactly one schema version; found %d", count)
	}
	return version, nil
}

func applyMigrations(ctx context.Context, db *database.DB, list []migration) (applied int, err error) {
	version, err := schemaVersion(db)
	if err != nil {
		return 0, fmt.Errorf("schema: %w", err)
	}
	latest := schemaBaseline
	for _, m := range list {
		if m.n != latest+1 {
			return 0, fmt.Errorf("schema: migration %d must follow %d", m.n, latest)
		}
		latest = m.n
	}
	if version < schemaBaseline || version > latest {
		return 0, fmt.Errorf("schema: version %d is unsupported by this daemon (supported %d–%d); an AppTown older than the 1.0 baseline must first be converted by forged -identity-cutover in a development build from before 1.0.0-rc.1", version, schemaBaseline, latest)
	}
	for _, m := range list {
		if m.n <= version {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return applied, fmt.Errorf("schema: migration %d (%s): %w", m.n, m.note, err)
		}
		log.Printf("schema: migration %d applied: %s", m.n, m.note)
		version = m.n
		applied++
	}
	return applied, nil
}

// applyMigration commits application DDL, data and version together.
func applyMigration(ctx context.Context, db *database.DB, m migration) error {
	return db.InTx(ctx, func(tx *database.Tx) error {
		for _, q := range m.stmts {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE schema_version SET version=?`, m.n); err != nil {
			return err
		}
		return nil
	})
}

//go:embed schema.sql
var schemaSQL string

// Open opens a database and installs or validates the canonical application
// schema. Application data seeding and job-library migrations belong to assembly.
func Open(ctx context.Context, url string) (*database.DB, error) {
	db, err := database.Open(url)
	if err != nil {
		return nil, err
	}
	if _, err := Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// LatestVersion is the schema version this build migrates to: what a move
// file records of the forge it came from.
func LatestVersion() int { return latestSchemaVersion() }
