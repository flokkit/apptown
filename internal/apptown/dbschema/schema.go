// Package dbschema embeds the assembled application DDL and owns checked migrations.
// It has no dependency on application assembly, domain services or job workers.
package dbschema

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"log"
	"strings"

	"github.com/lib/pq"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/version"
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

// schemaBaseline is the 1.0 baseline: every AppTown in service was converted
// to UUID identities (schema 147, the identity cutover) and the migrations
// up to it were collapsed into schema.sql. An older database is refused; it
// must first be converted by apptownd -identity-cutover, which only
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
		// any other AppTown.
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
	{n: 154, note: "the AppTown rename: stored names of the backup store on AppTown's disk and of complaints about the platform", stmts: []string{
		"ALTER TABLE backups ALTER COLUMN store SET DEFAULT 'apptown-disk'",
		"UPDATE backups SET store = 'apptown-disk' WHERE store = 'forge-disk'",
		"ALTER TABLE complaints ALTER COLUMN about SET DEFAULT 'apptown'",
		"UPDATE complaints SET about = 'apptown' WHERE about = 'forge'",
	}},
	{n: 155, note: "the AppTown rename: the stock platform pack is apptown, in the library and in each project's current pack", stmts: []string{
		"UPDATE packs SET name = 'apptown' WHERE name = 'forge' AND NOT EXISTS (SELECT 1 FROM packs WHERE name = 'apptown')",
		"UPDATE pack_versions SET included_pack = 'apptown' WHERE included_pack = 'forge'",
		"UPDATE adoptions SET pack = 'apptown' WHERE pack = 'forge'",
		// apptown upgrade moves main with a door of its own.
		"ALTER TABLE project_main DROP CONSTRAINT project_main_tuple",
		"ALTER TABLE project_main ADD CONSTRAINT project_main_tuple CHECK (branch<>'' AND sha<>'' AND via IN ('landing','init','seed','copy','genesis','integrity-adopt','backfill','upgrade'))",
	}},
	{n: 156, note: "a what-if records whether it moves each project's other workflows to the newest version", stmts: []string{
		"ALTER TABLE what_if_reviews ADD COLUMN IF NOT EXISTS advance_workflows BOOLEAN NOT NULL DEFAULT false",
	}},
	{n: 157, note: "a what-if records the other pack versions it moves with its own", stmts: []string{
		"ALTER TABLE what_if_reviews ADD COLUMN IF NOT EXISTS also_packs JSONB NOT NULL DEFAULT '[]'::jsonb",
	}},
	// Every deployment made before 1.0 keeps the names it was made with
	// (forge-…); one made from now on is named apptown-….
	{n: 158, note: "each deployment records the prefix its host names begin with", stmts: []string{
		"ALTER TABLE deployments ADD COLUMN IF NOT EXISTS host_prefix TEXT NOT NULL DEFAULT 'forge-'",
		"ALTER TABLE deployments ALTER COLUMN host_prefix SET DEFAULT 'apptown-'",
	}},
	{n: 159, note: "a change whose landing merged but could not ship can be closed as shipped with the later landing that superseded it", stmts: []string{
		"ALTER TABLE changes DROP CONSTRAINT IF EXISTS change_closed_as",
		"ALTER TABLE changes ADD CONSTRAINT change_closed_as CHECK(closed_as IN ('','nothing-to-change','shipped-with'))",
	}},
	// Categories that name AppTown are moved to its name. Actor and principal
	// names in history (audit, grants, usage, approvals) keep the name they
	// were recorded with: they are identifiers in the forensic record.
	{n: 160, note: "the AppTown rename: the workload kind of AppTown's own model calls and what-ifs of the platform pack say apptown", stmts: []string{
		"UPDATE inference_requests SET kind = 'apptown' WHERE kind = 'forge'",
		"UPDATE what_if_reviews SET pack = 'apptown' WHERE pack = 'forge'",
	}},
	// A token made before purposes authenticated nothing (it was refused
	// until classified by hand). Each is revoked, on the day they were
	// retired, and kept: a used one is its account's evidence of signing in.
	// The time is written here because SQL never reads the database's clock.
	{n: 161, note: "tokens made before purposes are revoked; a token without a purpose must be a revoked one", stmts: []string{
		"UPDATE auth_tokens SET revoked_at = '2026-10-22T00:00:00Z' WHERE purpose = '' AND revoked_at IS NULL",
		"ALTER TABLE auth_tokens ALTER COLUMN purpose DROP DEFAULT",
		"ALTER TABLE auth_tokens DROP CONSTRAINT auth_token_purpose",
		"ALTER TABLE auth_tokens ADD CONSTRAINT auth_token_purpose CHECK (purpose IN ('cli', 'agent') OR (purpose = '' AND revoked_at IS NOT NULL))",
	}},
	// A deployment's things on a host are named by its key, the last 12
	// hex digits of its id (deploy.HostKey), unique by this index. One made
	// before keeps its recorded prefix until it is moved (apptown
	// move-names); '' is names by
	// key. A deployment no Docker host or Portainer runs has no such names.
	{n: 162, note: "deployments are named on their hosts by their key; the prefix is recorded only for one not yet moved", stmts: []string{
		"ALTER TABLE deployments ALTER COLUMN host_prefix SET DEFAULT ''",
		`UPDATE deployments d SET host_prefix = '' WHERE d.kind <> 'standing'
  OR NOT EXISTS (SELECT 1 FROM targets t WHERE t.id = d.target_id AND t.kind IN ('docker-host', 'portainer'))`,
		"ALTER TABLE deployments ADD CONSTRAINT deployment_host_prefix CHECK (host_prefix IN ('', 'apptown-', 'forge-'))",
		"CREATE UNIQUE INDEX deployments_host_key ON deployments ((right(id::text, 12)))",
		"ALTER TABLE release_requests ADD COLUMN move_names BOOLEAN NOT NULL DEFAULT false",
	}},
	// Features nobody used are removed, and records only they wrote go with
	// them. A restored copy is always a restore test now: one opened for
	// people ran the same checks before it stayed up, so it counts as one.
	// Old data the removed readers tolerated is rewritten to the current
	// shape. Live grants of a removed capability are revoked on the day it
	// was removed (SQL never reads the database's clock) and kept as history.
	{n: 163, note: "removed features: the local-Docker start record, restored-copy lifetimes, the catalogue tables, the draft model slot; old deployment records in their current shape", stmts: []string{
		"DELETE FROM instance_settings WHERE key = 'local_docker'",
		"ALTER TABLE restored_copies DROP COLUMN expires_at, DROP COLUMN lifetime_minutes, DROP COLUMN is_test",
		"ALTER TABLE deployments DROP COLUMN expires_at",
		"DROP TABLE catalog_change_versions",
		"DROP TABLE catalog_changes",
		"DELETE FROM slot_fallbacks WHERE slot = 'draft'",
		"DELETE FROM model_slots WHERE slot = 'draft'",
		// A copy of the app's volume from before items named their volume
		// names it as the item.
		`UPDATE backups SET items_json = (
  SELECT jsonb_agg(CASE WHEN e->>'kind' = 'volume' AND COALESCE(e->>'volume', '') = ''
    THEN e || jsonb_build_object('volume', e->>'name') ELSE e END ORDER BY o)
  FROM jsonb_array_elements(items_json) WITH ORDINALITY t(e, o))
WHERE jsonb_typeof(items_json) = 'array' AND EXISTS (
  SELECT 1 FROM jsonb_array_elements(items_json) e WHERE e->>'kind' = 'volume' AND COALESCE(e->>'volume', '') = '')`,
		"UPDATE need_instances SET status = 'creating' WHERE status = 'pending'",
		"ALTER TABLE need_instances ALTER COLUMN status DROP DEFAULT",
		"UPDATE deployments SET readiness = 'uncertain' WHERE readiness = 'needs-reconcile'",
		`UPDATE grants SET revoked_at = '2026-10-23T00:00:00Z' WHERE revoked_at IS NULL AND capability IN
  ('conformance:clear-history', 'deployment:open-restored', 'standards:manage-catalog', 'project:configure-extensions', 'group:transfer-project')`,
	}},
	// A pack version published before policies were pinned names a policy
	// without its definition, and installing it read the library's. Each
	// such entry is given the library's definition now, the one installing
	// it would read, so a publication always holds what it installs. A name
	// the library lacks is left as it is.
	{n: 164, note: "pack versions published before policies were pinned hold the library's definition of each policy they name", stmts: []string{
		`UPDATE pack_versions v SET spec_json = jsonb_set(v.spec_json, '{policies}', (
  SELECT jsonb_agg(CASE WHEN COALESCE(t.x->>'rule', '') = '' AND l.name IS NOT NULL
      THEN jsonb_build_object('name', l.name, 'description', l.description, 'rule', l.rule, 'with', l.config_json)
      ELSE t.x END ORDER BY t.ord)
  FROM jsonb_array_elements(v.spec_json->'policies') WITH ORDINALITY AS t(x, ord)
  LEFT JOIN policy_library l ON l.name = t.x->>'name'))
WHERE jsonb_typeof(v.spec_json->'policies') = 'array'
  AND EXISTS (SELECT 1 FROM jsonb_array_elements(v.spec_json->'policies') x WHERE COALESCE(x->>'rule', '') = '')`,
	}},
	{n: 165, note: "the backlog is removed: a change left proposed is abandoned, with no closure time or reason since neither is known", stmts: []string{
		`UPDATE changes SET state = 'abandoned' WHERE state = 'proposed'`,
	}},
	// A whole-AppTown restore: what it leaves for the restored AppTown to
	// do. Every existing release was shipped, and no deployment is held.
	{n: 166, note: "a whole-AppTown restore: releases say whether AppTown shipped them, deployments whether their provider was checked since, and built-in deployments' fills", stmts: []string{
		`ALTER TABLE releases ADD COLUMN origin TEXT NOT NULL DEFAULT 'shipped' CONSTRAINT release_origin CHECK (origin IN ('shipped', 'observed-after-restore'))`,
		`ALTER TABLE deployments ADD COLUMN restore_check TEXT NOT NULL DEFAULT '' CONSTRAINT deployment_restore_check CHECK (restore_check IN ('', 'unconfirmed', 'gone', 'filling'))`,
		`CREATE TABLE restore_fills (
  deployment_id UUID PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
  backup_id UUID,
  state TEXT NOT NULL DEFAULT 'waiting' CONSTRAINT restore_fill_state CHECK (state IN ('waiting', 'filled', 'failed')),
  detail TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMPTZ NOT NULL
)`,
	}},
	// Every deployment is named by its key: the move from the names a
	// deployment was made with before 1.0 is over, and its record and
	// door go. A deployment still on those names that made something on a
	// host (its app container, a Portainer stack, which an update records
	// only on the handle, or anything on its list) would be looked for
	// under names it does not have, so the upgrade stops and says which,
	// and how to move each first. One that never made anything has no
	// names on a host to lose.
	{n: 167, note: "every deployment is named by its key: the recorded host prefix and the move flag go", stmts: []string{
		`DO $$
DECLARE held text;
BEGIN
  SELECT string_agg(p.slug || '/' || d.name, ', ' ORDER BY p.slug, d.name) INTO held
  FROM deployments d JOIN projects p ON p.id = d.project_id
  WHERE d.host_prefix <> '' AND (d.container <> '' OR COALESCE(d.handle_json->>'stack', '') <> ''
    OR EXISTS (SELECT 1 FROM deployment_owned o WHERE o.deployment_id = d.id));
  IF held IS NOT NULL THEN
    RAISE EXCEPTION 'these deployments are still on the names they were made with before 1.0: %. Under AppTown 1.0.0-rc.6 first, move one on a Docker host with apptown move-names; take one on Portainer down and release it again (its data is not carried over: copy it first), then upgrade', held;
  END IF;
END $$`,
		"ALTER TABLE deployments DROP COLUMN host_prefix",
		"ALTER TABLE release_requests DROP COLUMN move_names",
	}},
	// Every published workflow, block and pack version says where it came
	// from: its source and its signer. Stock is the built-in source,
	// flokkit; it was published before this record by some earlier build,
	// which was never recorded, so its signer says "shipped with AppTown
	// before <the build running this migration>, unsigned" (applyMigration
	// sets migration.build), which is all that is known. Versions authored on
	// the instance are source here, with no signer. Adoptions and receipts
	// are not touched.
	{n: 168, note: "each published workflow, block and pack version records its source and signer", stmts: []string{
		"ALTER TABLE lib_workflow_versions ADD COLUMN source TEXT NOT NULL DEFAULT '', ADD COLUMN signer TEXT NOT NULL DEFAULT ''",
		`UPDATE lib_workflow_versions v SET source = CASE WHEN l.origin = 'stock' THEN 'flokkit' ELSE 'here' END,
  signer = CASE WHEN l.origin = 'stock' THEN 'shipped with AppTown before ' || current_setting('migration.build') || ', unsigned' ELSE '' END
  FROM lib_workflows l WHERE l.id = v.lib_id`,
		"ALTER TABLE lib_workflow_versions ALTER COLUMN source DROP DEFAULT, ALTER COLUMN signer DROP DEFAULT",
		"ALTER TABLE lib_workflow_versions ADD CONSTRAINT lib_workflow_version_source CHECK (source <> '')",
		"ALTER TABLE guidance_block_versions ADD COLUMN source TEXT NOT NULL DEFAULT '', ADD COLUMN signer TEXT NOT NULL DEFAULT ''",
		`UPDATE guidance_block_versions v SET source = CASE WHEN l.origin = 'stock' THEN 'flokkit' ELSE 'here' END,
  signer = CASE WHEN l.origin = 'stock' THEN 'shipped with AppTown before ' || current_setting('migration.build') || ', unsigned' ELSE '' END
  FROM guidance_blocks l WHERE l.id = v.block_id`,
		"ALTER TABLE guidance_block_versions ALTER COLUMN source DROP DEFAULT, ALTER COLUMN signer DROP DEFAULT",
		"ALTER TABLE guidance_block_versions ADD CONSTRAINT guidance_block_version_source CHECK (source <> '')",
		"ALTER TABLE pack_versions ADD COLUMN source TEXT NOT NULL DEFAULT '', ADD COLUMN signer TEXT NOT NULL DEFAULT ''",
		`UPDATE pack_versions v SET source = CASE WHEN l.origin = 'stock' THEN 'flokkit' ELSE 'here' END,
  signer = CASE WHEN l.origin = 'stock' THEN 'shipped with AppTown before ' || current_setting('migration.build') || ', unsigned' ELSE '' END
  FROM packs l WHERE l.id = v.pack_id`,
		"ALTER TABLE pack_versions ALTER COLUMN source DROP DEFAULT, ALTER COLUMN signer DROP DEFAULT",
		"ALTER TABLE pack_versions ADD CONSTRAINT pack_version_source CHECK (source <> '')",
	}},
	// A check says what it applies to (appliesTo), and the field is
	// required: there is no default. Every check stored before it existed
	// was judged once over the whole repository, so it is given
	// ["repository"], which states that meaning and changes nothing it
	// does. The same for every scanner that runs at a scan point (one that
	// is not manual), in the binding and in each of its versions, each
	// version judged by its own manual. Nothing else stores a declaration:
	// verdicts, what-if rows, receipts and runs name a check, they do not
	// hold one. A block version whose checks are not a list of objects
	// stops the upgrade, named.
	{n: 169, note: "every stored check and scan-point scanner states what it applies to: repository, its meaning before appliesTo", stmts: []string{
		`DO $$
DECLARE bad text;
BEGIN
  SELECT string_agg(b.name || ' v' || v.version, ', ' ORDER BY b.name, v.version) INTO bad
  FROM guidance_block_versions v JOIN guidance_blocks b ON b.id = v.block_id
  WHERE jsonb_typeof(v.checks_json) <> 'array'
     OR EXISTS (SELECT 1 FROM jsonb_array_elements(v.checks_json) c WHERE jsonb_typeof(c) <> 'object');
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION 'these guidance block versions store checks that are not a list of checks, so what they apply to cannot be stated: %', bad;
  END IF;
END $$`,
		`UPDATE guidance_block_versions v SET checks_json = (
  SELECT jsonb_agg(CASE WHEN jsonb_exists(c, 'appliesTo') THEN c ELSE c || '{"appliesTo": ["repository"]}'::jsonb END ORDER BY ord)
  FROM jsonb_array_elements(v.checks_json) WITH ORDINALITY AS t(c, ord))
WHERE EXISTS (SELECT 1 FROM jsonb_array_elements(v.checks_json) c WHERE NOT jsonb_exists(c, 'appliesTo'))`,
		`UPDATE integrations SET config_json = config_json || '{"appliesTo": ["repository"]}'::jsonb
WHERE capability = 'scanner' AND jsonb_typeof(config_json) = 'object' AND NOT jsonb_exists(config_json, 'appliesTo')
  AND COALESCE(config_json->'manual', 'false'::jsonb) <> 'true'::jsonb`,
		`UPDATE integration_versions v SET config_json = v.config_json || '{"appliesTo": ["repository"]}'::jsonb
FROM integrations i
WHERE i.id = v.integration_id AND i.capability = 'scanner' AND jsonb_typeof(v.config_json) = 'object' AND NOT jsonb_exists(v.config_json, 'appliesTo')
  AND COALESCE(v.config_json->'manual', 'false'::jsonb) <> 'true'::jsonb`,
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
		return 0, fmt.Errorf("schema: version %d is unsupported by this daemon (supported %d–%d); an AppTown older than the 1.0 baseline must first be converted by apptownd -identity-cutover in a development build from before 1.0.0-rc.1", version, schemaBaseline, latest)
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
		// The build running the migration, for statements that record it
		// (current_setting('migration.build')): the numbered SQL stays fixed
		// while naming the binary that applied it. Local to the transaction.
		if _, err := tx.Exec(`SELECT set_config('migration.build', ?, true)`, version.String()); err != nil {
			return err
		}
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

// Latest is the schema version this build migrates a database to.
func Latest() int { return latestSchemaVersion() }

// Version is the schema version a database is at.
func Version(db *database.DB) (int, error) { return schemaVersion(db) }

// Queryer is what reads a database outside AppTown's own handle (a restore
// reads the database it just loaded, before AppTown opens it).
type Queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// PublicTables is how many tables the database's public schema holds: none
// in one a restore may load into.
func PublicTables(ctx context.Context, q Queryer) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&n)
	return n, err
}

// RowsIn is how many rows a table holds, named as a plain dump names it
// (schema.table, either part quoted or not).
func RowsIn(ctx context.Context, q Queryer, table string) (int64, error) {
	schema, name, ok := strings.Cut(table, ".")
	if !ok {
		schema, name = "public", table
	}
	var n int64
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+pq.QuoteIdentifier(strings.Trim(schema, `"`))+"."+pq.QuoteIdentifier(strings.Trim(name, `"`))).Scan(&n)
	return n, err
}
