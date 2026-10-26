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
// must satisfy schemaBaseline before any DDL runs; older installations
// are refused before any migration runs.
// New tables and columns go in the owner's schema.sql fragment and a numbered
// migration. schemaSQL is generated from those fragments; never edit it directly.
type migration struct {
	n     int
	note  string
	stmts []string
}

// schemaBaseline is the oldest supported installation: 1.0.0-rc.7.
// Earlier migration history lives in Git; the assembled schema creates new
// installations. Released migrations above this floor retain their numbers
// and statements (migrations.lock, scripts/migrations-append-only.sh).
const schemaBaseline = 167

var migrations = []migration{
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
	{n: 170, note: "record observed database engine and volume incarnation separately from deletion ownership", stmts: []string{
		`CREATE TABLE IF NOT EXISTS deployment_storage_bindings (
  id UUID PRIMARY KEY,
  deployment_id UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  need TEXT NOT NULL CHECK (need <> ''),
  volume TEXT NOT NULL CHECK (volume <> ''),
  data_path TEXT NOT NULL CHECK (data_path <> ''),
  declared_image TEXT NOT NULL CHECK (declared_image <> ''),
  image_id TEXT NOT NULL CHECK (image_id <> ''),
  repo_digest TEXT NOT NULL DEFAULT '',
  volume_created TEXT NOT NULL CHECK (volume_created <> ''),
  volume_driver TEXT NOT NULL CHECK (volume_driver <> ''),
  origin TEXT NOT NULL CHECK (origin IN ('created', 'observed', 'restored')),
  recorded_at TIMESTAMPTZ NOT NULL,
  UNIQUE (deployment_id, need, volume)
)`,
	}},
	{n: 171, note: "bind provider targets and repositories to connection identities without inventing remote scope", stmts: []string{
		`ALTER TABLE targets ADD COLUMN connection_id UUID REFERENCES integrations(id) ON DELETE RESTRICT, ADD COLUMN provider_scope_json JSONB`,
		`UPDATE targets t SET connection_id=i.id FROM integrations i WHERE t.provider<>'' AND t.provider=i.name`,
		`UPDATE targets t SET connection_id=i.id FROM integrations i WHERE t.kind='portainer' AND t.name='intranet' AND t.provider='' AND i.name='portainer-deploy' AND i.capability='deploy' AND i.provider='portainer' AND NOT jsonb_exists(t.address_json, 'urlFrom') AND NOT jsonb_exists(t.address_json, 'apiKeyFrom')`,
		`ALTER TABLE owned_repositories ADD COLUMN connection_id UUID REFERENCES integrations(id) ON DELETE RESTRICT`,
		`UPDATE owned_repositories r SET connection_id=i.id FROM integrations i WHERE r.connector=i.name`,
		`ALTER TABLE release_requests ADD COLUMN provider_binding_json JSONB`,
	}},
	{n: 172, note: "retain provider-scoped repository attachment and original merge request identity", stmts: []string{
		`CREATE TABLE repository_attachments (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL UNIQUE REFERENCES projects(id) ON DELETE CASCADE,
  connection_id UUID NOT NULL REFERENCES integrations(id),
  provider_kind TEXT NOT NULL CHECK (provider_kind='gitlab'),
  provider_base TEXT NOT NULL CHECK (provider_base<>''),
  provider_account BIGINT NOT NULL CHECK (provider_account>0),
  provider_project BIGINT NOT NULL CHECK (provider_project>0),
  clone_url TEXT NOT NULL CHECK (clone_url<>''),
  project_path TEXT NOT NULL CHECK (project_path<>''),
  verified_at TIMESTAMPTZ NOT NULL
)`,
		`CREATE TABLE repository_attachment_intents (
  id UUID PRIMARY KEY,
  request_id UUID NOT NULL,
  project_id UUID NOT NULL UNIQUE REFERENCES projects(id) ON DELETE CASCADE,
  actor_id UUID NOT NULL,
  parameters JSONB NOT NULL,
  connection_id UUID NOT NULL REFERENCES integrations(id),
  provider_base TEXT NOT NULL CHECK (provider_base<>''),
  provider_account BIGINT NOT NULL CHECK (provider_account>0),
  prior_origin TEXT NOT NULL,
  phase TEXT NOT NULL CHECK (phase IN ('intent','dispatched','identified','attached','unknown')),
  provider_result JSONB,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  CHECK ((phase IN ('identified','attached')) = (provider_result IS NOT NULL))
)`,
		`CREATE TABLE repository_mr_identities (
  artifact_id UUID PRIMARY KEY REFERENCES artifacts(id) ON DELETE CASCADE,
  identity_json JSONB NOT NULL
)`,
	}},
	{n: 173, note: "separate customer display labels and explicit project release defaults from stable identities", stmts: []string{
		`ALTER TABLE integrations ADD COLUMN display_label TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE targets ADD COLUMN display_label TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE deployments ADD COLUMN display_label TEXT NOT NULL DEFAULT ''`,
		`UPDATE integrations SET display_label=name`,
		`UPDATE targets SET display_label=name`,
		`UPDATE deployments SET display_label=name`,
		`ALTER TABLE deployments ADD CONSTRAINT deployments_project_identity UNIQUE(project_id,id)`,
		`CREATE TABLE project_release_settings (
  project_id UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  default_deployment_id UUID,
  revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
  CONSTRAINT project_release_default_membership FOREIGN KEY(project_id, default_deployment_id)
    REFERENCES deployments(project_id, id) DEFERRABLE INITIALLY DEFERRED
)`,
		`INSERT INTO project_release_settings(project_id, default_deployment_id)
SELECT d.project_id, d.id FROM deployments d JOIN targets t ON t.id=d.target_id JOIN projects p ON p.id=d.project_id
WHERE p.purpose='ordinary' AND NOT EXISTS(SELECT 1 FROM project_lifecycle l WHERE l.project_id=p.id) AND t.name='intranet' AND t.kind='portainer' AND t.status='ready'
  AND d.name=COALESCE(NULLIF(t.address_json->>'tier',''),'lan')
  AND d.kind='standing' AND d.feed<>'landing' AND d.name<>'main'`,
	}},
	{n: 174, note: "retain deployment ingress evidence and restrictive exact-host claims", stmts: []string{
		`CREATE TABLE deployment_address_settings (
  deployment_id UUID PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
  generated_hostname TEXT NOT NULL DEFAULT '',
  custom_hostname TEXT NOT NULL DEFAULT '',
  certificate_domain TEXT NOT NULL DEFAULT '',
  revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
  verification_state TEXT NOT NULL CONSTRAINT deployment_address_verification CHECK (verification_state IN ('ready','legacy-unverified')),
  legacy_evidence_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  CONSTRAINT deployment_address_ready_fields CHECK (verification_state='legacy-unverified' OR (generated_hostname<>'' AND certificate_domain<>''))
)`,
		`CREATE TABLE deployment_hostname_claims (
  hostname TEXT PRIMARY KEY
)`,
		`CREATE TABLE deployment_hostname_claimants (
  hostname TEXT NOT NULL REFERENCES deployment_hostname_claims(hostname) ON DELETE CASCADE,
  deployment_id UUID NOT NULL REFERENCES deployments(id) ON DELETE RESTRICT,
  PRIMARY KEY(hostname,deployment_id)
)`,
		`ALTER TABLE release_requests ADD COLUMN ingress_json JSONB`,
		`ALTER TABLE deployment_attempt_inputs ADD COLUMN ingress_json JSONB`,
		`INSERT INTO deployment_address_settings(deployment_id,verification_state,legacy_evidence_json)
SELECT d.id,'legacy-unverified',jsonb_build_object('projectSlug',p.slug,'projectReleaseDomain',p.release_domain,'targetDomain',t.ingress_json->>'domain','storedURL',d.url)
FROM deployments d JOIN projects p ON p.id=d.project_id JOIN targets t ON t.id=d.target_id WHERE t.kind='portainer'`,
		`CREATE TEMP TABLE apptown_legacy_hostname_candidates ON COMMIT DROP AS
WITH raw AS (
  SELECT s.deployment_id, unnest(ARRAY[
    CASE WHEN s.legacy_evidence_json->>'targetDomain'<>'' THEN (s.legacy_evidence_json->>'projectSlug')||'.'||(s.legacy_evidence_json->>'targetDomain') END,
    s.legacy_evidence_json->>'projectReleaseDomain',
    (regexp_match(s.legacy_evidence_json->>'storedURL','^https{0,1}://([A-Za-z0-9.-]+)(:[0-9]+){0,1}/{0,1}$'))[1]
  ]) AS hostname FROM deployment_address_settings s
), normalized AS (
  SELECT deployment_id,regexp_replace(lower(btrim(hostname)),'\.$','') AS hostname FROM raw
)
SELECT DISTINCT deployment_id,hostname FROM normalized
WHERE length(hostname)<=253
  AND hostname ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9]){0,1}(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9]){0,1})*$'
  AND hostname !~ '^([0-9]+|0x[0-9a-f]+)(\.([0-9]+|0x[0-9a-f]+))*$'`,
		`INSERT INTO deployment_hostname_claims(hostname) SELECT DISTINCT hostname FROM apptown_legacy_hostname_candidates`,
		`INSERT INTO deployment_hostname_claimants(hostname,deployment_id) SELECT hostname,deployment_id FROM apptown_legacy_hostname_candidates`,
	}},
	{n: 175, note: "capture immutable project serving labels without renaming routes", stmts: []string{
		`ALTER TABLE projects ADD COLUMN serving_label TEXT`,
		`UPDATE projects SET serving_label=slug`,
		`ALTER TABLE projects ALTER COLUMN serving_label SET NOT NULL`,
		`ALTER TABLE projects ADD CONSTRAINT projects_serving_label_shape CHECK (serving_label ~ '^[a-z0-9][a-z0-9-]*$')`,
		`ALTER TABLE projects ADD CONSTRAINT projects_serving_label_unique UNIQUE(serving_label)`,
	}},
	{n: 176, note: "retain the initiating authority for queued release execution", stmts: []string{
		`ALTER TABLE release_requests ADD COLUMN authority_json JSONB`,
	}},
	{n: 177, note: "preserve explicit legacy release setup and promotion policy", stmts: []string{
		`ALTER TABLE deployments ADD COLUMN require_promotion BOOLEAN NOT NULL DEFAULT false`,
		`ALTER TABLE deployments ADD COLUMN policy_revision BIGINT NOT NULL DEFAULT 1 CHECK (policy_revision > 0)`,
		`ALTER TABLE release_requests ADD COLUMN require_promotion BOOLEAN NOT NULL DEFAULT false`,
		`UPDATE release_requests q SET require_promotion=true
WHERE q.attributes_json->>'audience' IN ('partners','public') OR q.attributes_json->>'data'='production'
 OR EXISTS(SELECT 1 FROM targets t WHERE t.id=q.target_id AND t.name='intranet' AND t.kind='portainer' AND t.provider='portainer-deploy')`,
		`ALTER TABLE project_release_settings ADD COLUMN legacy_setup_json JSONB`,
		`ALTER TABLE project_release_settings ADD CONSTRAINT project_release_setup_exclusive CHECK (default_deployment_id IS NULL OR legacy_setup_json IS NULL)`,
		`UPDATE deployments d SET require_promotion=true FROM targets t
WHERE d.target_id=t.id AND t.name='intranet' AND t.kind='portainer' AND t.provider='portainer-deploy'`,
		`INSERT INTO project_release_settings(project_id,legacy_setup_json)
SELECT p.id,jsonb_build_object('version',1,'integrationId',i.id,'integrationName',i.name,
 'targetId',CASE WHEN t.provider=i.name AND t.kind='portainer' AND i.provider='portainer' AND t.connection_id=i.id
 AND COALESCE(NULLIF(t.address_json->>'tier',''),'lan')=CASE WHEN jsonb_typeof(i.config_json->'releaseDeployment')='string' THEN COALESCE(NULLIF(i.config_json->>'releaseDeployment',''),'lan') WHEN NOT jsonb_exists(i.config_json,'releaseDeployment') THEN 'lan' ELSE '' END THEN t.id ELSE NULL END,
 'deploymentName',CASE WHEN jsonb_typeof(i.config_json->'releaseDeployment')='string' THEN COALESCE(NULLIF(i.config_json->>'releaseDeployment',''),'lan') WHEN NOT jsonb_exists(i.config_json,'releaseDeployment') THEN 'lan' ELSE '' END,
 'configuration',i.config_json,'targetEvidence',CASE WHEN t.id IS NOT NULL THEN jsonb_build_object('id',t.id,'provider',t.provider,'kind',t.kind,'address',t.address_json) ELSE NULL END)
FROM projects p CROSS JOIN integrations i LEFT JOIN targets t ON t.name='intranet'
WHERE i.name='portainer-deploy' AND i.capability='deploy'
 AND p.purpose='ordinary' AND NOT EXISTS(SELECT 1 FROM project_lifecycle l WHERE l.project_id=p.id)
 AND NOT EXISTS(SELECT 1 FROM project_release_settings s WHERE s.project_id=p.id)`,
	}},
	{n: 178, note: "retain permanent project addresses and atomic local request results", stmts: []string{
		`CREATE TABLE project_addresses (address TEXT PRIMARY KEY CHECK (address ~ '^[a-z0-9][a-z0-9-]*$'), project_id UUID, UNIQUE(address,project_id))`,
		`INSERT INTO project_addresses(address,project_id) SELECT slug,id FROM projects`,
		`INSERT INTO project_addresses(address,project_id)
SELECT d.slug,CASE WHEN count(DISTINCT d.project_id)=1 THEN min(d.project_id::text)::uuid ELSE NULL END
FROM project_deletions d WHERE NOT EXISTS(SELECT 1 FROM project_addresses a WHERE a.address=d.slug)
GROUP BY d.slug`,
		`ALTER TABLE projects ADD CONSTRAINT projects_address_owner FOREIGN KEY(slug,id) REFERENCES project_addresses(address,project_id) DEFERRABLE INITIALLY DEFERRED`,
		`CREATE TABLE project_creation_requests (id UUID PRIMARY KEY,actor_id UUID NOT NULL,request_id UUID NOT NULL,project_id UUID NOT NULL,input_hash TEXT,resolved_group TEXT,result_json JSONB,UNIQUE(actor_id,request_id))`,
		`CREATE TABLE project_rename_requests (id UUID PRIMARY KEY,actor_id UUID NOT NULL,request_id UUID NOT NULL,project_id UUID NOT NULL,input_hash TEXT NOT NULL,result_json JSONB,UNIQUE(actor_id,request_id))`,
	}},
	{n: 179, note: "retain explicit standing approval revisions", stmts: []string{
		`CREATE TABLE standing_rules (id UUID PRIMARY KEY, current_revision BIGINT NOT NULL CHECK(current_revision > 0))`,
		`CREATE TABLE standing_rule_revisions (rule_id UUID NOT NULL REFERENCES standing_rules(id), revision BIGINT NOT NULL CHECK(revision > 0), enabled BOOLEAN NOT NULL, record_json JSONB NOT NULL, PRIMARY KEY(rule_id,revision))`,
	}},
	{n: 180, note: "external sign-in transactions, principal bindings and revocable sessions", stmts: []string{`-- External sign-in is evidence bound to an immutable local principal, never email.
CREATE TABLE IF NOT EXISTS external_signin_providers (
 id UUID PRIMARY KEY,
 current_slot BOOLEAN NOT NULL DEFAULT TRUE,
 revision BIGINT NOT NULL CHECK (revision > 0),
 issuer TEXT NOT NULL,
 display_name TEXT NOT NULL,
 client_id TEXT NOT NULL,
 secret_ref TEXT NOT NULL,
 callback_origin TEXT NOT NULL,
 enabled BOOLEAN NOT NULL DEFAULT FALSE,
 allow_signup BOOLEAN NOT NULL DEFAULT FALSE,
 initial_group TEXT NOT NULL DEFAULT '',
 created_by UUID NOT NULL REFERENCES principals(id),
 updated_by UUID NOT NULL REFERENCES principals(id),
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS external_signin_current ON external_signin_providers(current_slot) WHERE current_slot;
CREATE TABLE IF NOT EXISTS external_signin_bindings (
 id UUID PRIMARY KEY,
 provider_id UUID NOT NULL REFERENCES external_signin_providers(id),
 issuer TEXT NOT NULL,
 subject TEXT NOT NULL,
 principal_id UUID NOT NULL REFERENCES principals(id),
 created_by UUID NOT NULL REFERENCES principals(id),
 created_at TIMESTAMPTZ NOT NULL,
 revoked_at TIMESTAMPTZ,
 UNIQUE(provider_id,issuer,subject)
);
CREATE TABLE IF NOT EXISTS external_signin_transactions (
 id UUID PRIMARY KEY,
 state_hash TEXT NOT NULL UNIQUE,
 provider_id UUID NOT NULL REFERENCES external_signin_providers(id),
 provider_revision BIGINT NOT NULL,
 verifier_hash TEXT NOT NULL,
 nonce_hash TEXT NOT NULL,
 purpose TEXT NOT NULL CHECK (purpose IN ('login','link')),
 local_session_id UUID,
 principal_id UUID REFERENCES principals(id),
 proposed_name TEXT NOT NULL DEFAULT '',
 callback TEXT NOT NULL,
 return_path TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL,
 consumed_at TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS external_signin_sessions (
 session_id UUID PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
 binding_id UUID NOT NULL REFERENCES external_signin_bindings(id),
 provider_id UUID NOT NULL REFERENCES external_signin_providers(id),
 provider_revision BIGINT NOT NULL,
 authenticated_at TIMESTAMPTZ
);
`}},
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
		return 0, fmt.Errorf("schema: version %d is unsupported by this daemon (supported %d–%d); this build requires an installation at 1.0.0-rc.7 or newer", version, schemaBaseline, latest)
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

// Baseline is the oldest schema this build can upgrade or restore.
func Baseline() int { return schemaBaseline }

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
