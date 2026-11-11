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
	data  func(context.Context, *database.Tx) error
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
	{n: 181, note: "retain revocable human publication classification", stmts: []string{`CREATE TABLE IF NOT EXISTS routine_publication_classifications (
 id UUID PRIMARY KEY,
 publication_kind TEXT NOT NULL CHECK (publication_kind IN ('block','pack')),
 publication_id UUID NOT NULL,
 from_version BIGINT NOT NULL CHECK (from_version > 0),
 to_version BIGINT NOT NULL CHECK (to_version > from_version),
 digest TEXT NOT NULL CHECK (digest <> ''),
 issuer_id UUID NOT NULL,
 request_id UUID NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 record_json JSONB NOT NULL,
 revocation_json JSONB NOT NULL DEFAULT 'null'::jsonb,
 UNIQUE(issuer_id,request_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS routine_publication_active_digest ON routine_publication_classifications(digest) WHERE revocation_json = 'null'::jsonb;`}},
	{n: 182, note: "drop records nothing reads: decision receipts, inference request rows, integration config history, need reconciliations", stmts: []string{
		`DROP TABLE IF EXISTS authorization_decisions`,
		`ALTER TABLE audit_log DROP COLUMN IF EXISTS authorization_decision_id`,
		`DROP TABLE IF EXISTS inference_requests`,
		`DROP TABLE IF EXISTS integration_versions`,
		`DROP TABLE IF EXISTS reconciliations`,
	}},
	{n: 183, note: "drop the target secrets delivery setting nothing reads", stmts: []string{
		`ALTER TABLE targets DROP COLUMN IF EXISTS secrets_delivery`,
	}},
	{n: 184, note: "hold a restored deployment whose data did not come back", stmts: []string{
		`ALTER TABLE deployments DROP CONSTRAINT deployment_restore_check`,
		`ALTER TABLE deployments ADD CONSTRAINT deployment_restore_check CHECK (restore_check IN ('', 'unconfirmed', 'gone', 'filling', 'not-filled'))`,
	}},
	{n: 185, note: "drop pre-1.0 former identities and derived ledger refs", stmts: []string{
		`DROP TABLE IF EXISTS former_ids`,
		`ALTER TABLE audit_log DROP COLUMN IF EXISTS refs`,
	}},
	{n: 186, note: "drop project address aliases and rename request results", stmts: []string{
		`ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_address_owner`,
		`DROP TABLE IF EXISTS project_addresses`,
		`DROP TABLE IF EXISTS project_rename_requests`,
	}},
	{n: 187, note: "drop roles nobody is offered, grant scopes nothing grants, and unused delegation fields", stmts: []string{
		`DELETE FROM grants WHERE role_name IN ('integration-administrator','group-viewer','group-project-creator','group-maintainer','group-auditor') OR scope_type IN ('change','run','dimension')`,
		`DROP VIEW IF EXISTS delegations_named`,
		`ALTER TABLE delegations DROP COLUMN IF EXISTS kind, DROP COLUMN IF EXISTS group_bounds_json, DROP COLUMN IF EXISTS project_bounds_json`,
		`CREATE OR REPLACE VIEW delegations_named AS
  SELECT d.*, u.username FROM delegations d JOIN users u ON u.principal_id = d.principal_id`,
	}},
	// An imported release destination was never chosen (the exclusivity check
	// kept its default empty), so its row goes and the project has no
	// preference until one is chosen. Promotion permission follows data and
	// audience classification only; admitted releases keep their recorded need.
	{n: 188, note: "settle imported release setup and drop the explicit promotion policy", stmts: []string{
		`DELETE FROM project_release_settings WHERE legacy_setup_json IS NOT NULL`,
		`ALTER TABLE project_release_settings DROP CONSTRAINT project_release_setup_exclusive`,
		`ALTER TABLE project_release_settings DROP COLUMN legacy_setup_json`,
		`ALTER TABLE deployments DROP COLUMN require_promotion, DROP COLUMN policy_revision`,
	}},
	// Nothing ordered or gated promotion by it; admitted requests' attribute
	// JSON keeps the old key, which decoding ignores.
	{n: 189, note: "drop the deployment promote-after setting nothing reads", stmts: []string{
		`ALTER TABLE deployments DROP COLUMN promote_after`,
	}},
	// Settles the Portainer addresses migration 174 left unconfirmed from
	// AppTown's own records of the rule that served them before 174: hosts
	// <slug>.<apps domain> and the project's release domain, the URL the
	// last of them. A row settles only when the deployment's stored URL
	// agrees with that rule and no other deployment claims either host;
	// otherwise it is deleted and the next release selects a generated host.
	{n: 190, note: "settle unconfirmed Portainer addresses from the recorded pre-174 rule and drop the confirmation state", stmts: []string{
		`CREATE TEMP TABLE apptown_address_settlement ON COMMIT DROP AS
WITH legacy AS (
  SELECT s.deployment_id,
    regexp_replace(lower(btrim(COALESCE(s.legacy_evidence_json->>'targetDomain',''))),'\.$','') AS domain,
    lower(btrim(COALESCE(s.legacy_evidence_json->>'projectSlug',''))) AS slug,
    regexp_replace(lower(btrim(COALESCE(s.legacy_evidence_json->>'projectReleaseDomain',''))),'\.$','') AS vanity,
    regexp_replace(lower(COALESCE((regexp_match(COALESCE(s.legacy_evidence_json->>'storedURL',''),'^https{0,1}://([A-Za-z0-9.-]+)(:[0-9]+){0,1}/{0,1}$'))[1],'')),'\.$','') AS served
  FROM deployment_address_settings s WHERE s.verification_state='legacy-unverified'
), shaped AS (
  SELECT deployment_id, domain, served, slug||'.'||domain AS generated,
    CASE WHEN vanity=slug||'.'||domain THEN '' ELSE vanity END AS custom,
    CASE WHEN vanity<>'' THEN vanity ELSE slug||'.'||domain END AS url_host
  FROM legacy WHERE domain<>'' AND slug<>''
), valid AS (
  SELECT * FROM shaped
  WHERE length(generated)<=253 AND generated ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9]){0,1}(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9]){0,1})*$' AND generated !~ '^([0-9]+|0x[0-9a-f]+)(\.([0-9]+|0x[0-9a-f]+))*$'
    AND domain ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9]){0,1}(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9]){0,1})*$' AND domain !~ '^([0-9]+|0x[0-9a-f]+)(\.([0-9]+|0x[0-9a-f]+))*$'
    AND (custom='' OR (length(custom)<=253 AND custom ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9]){0,1}(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9]){0,1})*$' AND custom !~ '^([0-9]+|0x[0-9a-f]+)(\.([0-9]+|0x[0-9a-f]+))*$'))
    AND served=url_host
)
SELECT v.deployment_id, v.domain, v.generated, v.custom FROM valid v
WHERE NOT EXISTS(SELECT 1 FROM deployment_hostname_claimants c
  WHERE c.hostname IN (v.generated, NULLIF(v.custom,'')) AND c.deployment_id<>v.deployment_id)`,
		`INSERT INTO deployment_hostname_claims(hostname)
SELECT generated FROM apptown_address_settlement UNION SELECT custom FROM apptown_address_settlement WHERE custom<>''
ON CONFLICT DO NOTHING`,
		`INSERT INTO deployment_hostname_claimants(hostname,deployment_id)
SELECT generated,deployment_id FROM apptown_address_settlement UNION SELECT custom,deployment_id FROM apptown_address_settlement WHERE custom<>''
ON CONFLICT DO NOTHING`,
		`UPDATE deployment_address_settings s SET generated_hostname=a.generated, custom_hostname=a.custom, certificate_domain=a.domain,
  verification_state='ready', revision=s.revision+1
FROM apptown_address_settlement a WHERE s.deployment_id=a.deployment_id`,
		`DELETE FROM deployment_address_settings WHERE verification_state='legacy-unverified'`,
		`ALTER TABLE deployment_address_settings DROP CONSTRAINT deployment_address_ready_fields`,
		`ALTER TABLE deployment_address_settings DROP COLUMN verification_state`,
		`ALTER TABLE deployment_address_settings DROP COLUMN legacy_evidence_json`,
		`ALTER TABLE deployment_address_settings ADD CONSTRAINT deployment_address_ready_fields CHECK (generated_hostname<>'' AND certificate_domain<>'')`,
	}},
	// A change a record run (research, a specification) shipped with
	// nothing to land is closed as 'record', so every read of it says
	// finished, not shipped. A shipped change is marked only when nothing
	// of a landing is recorded for it: no root landing run (one that ships
	// main with no code of its own included), no merge receipt and no
	// landed commit.
	{n: 191, note: "record how a change a record run shipped was closed", stmts: []string{
		`ALTER TABLE changes DROP CONSTRAINT change_closed_as`,
		`ALTER TABLE changes ADD CONSTRAINT change_closed_as CHECK(closed_as IN ('','nothing-to-change','shipped-with','record'))`,
		`UPDATE changes c SET closed_as='record'
WHERE c.state='shipped' AND c.closed_as='' AND c.landed_sha=''
  AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.change_id=c.id AND r.project_id=c.project_id AND r.parent_run_id IS NULL AND r.definition_json->>'role'='landing')
  AND NOT EXISTS(SELECT 1 FROM run_events e JOIN runs r ON r.id=e.run_id WHERE r.change_id=c.id AND r.project_id=c.project_id AND e.kind='merge.completed')`,
	}},
	// A project is on several packs together, none above another: its
	// memberships (project_packs) and its receipt's packs say which. The
	// single "primary" pack name goes. Receipts keep the JSON they were
	// recorded with; each project is recorded again through the product
	// after the release's stock has synced (design/subsystems/ways-of-working-versions.md).
	{n: 192, note: "drop the single primary pack name: a project's packs are its memberships", stmts: []string{
		`ALTER TABLE adoptions DROP COLUMN pack`,
		// Packs no longer include one another; no pack on any instance did.
		`ALTER TABLE pack_versions DROP COLUMN included_pack, DROP COLUMN included_version`,
	}},
	{n: 193, note: "track whether a project description belongs to a person", stmts: []string{
		"ALTER TABLE projects ADD COLUMN blurb_source TEXT NOT NULL DEFAULT 'person' CONSTRAINT projects_blurb_source CHECK (blurb_source IN ('generated','person'))",
		"ALTER TABLE projects ALTER COLUMN blurb_source SET DEFAULT 'generated'",
		"CREATE INDEX audit_log_project_created ON audit_log(project_id) WHERE kind='project.created'",
	}},
	{n: 194, note: "persist background infrastructure readings", stmts: []string{
		`CREATE TABLE deployment_readings (
    id UUID PRIMARY KEY,
    subject TEXT NOT NULL,
    kind TEXT NOT NULL,
    identity TEXT NOT NULL,
    generation BIGINT NOT NULL DEFAULT 0,
    invalidated BOOLEAN NOT NULL DEFAULT FALSE,
    payload JSONB NOT NULL DEFAULT '{}',
    UNIQUE (subject, kind)
);`,
		`CREATE TABLE protection_readings (
    id UUID PRIMARY KEY,
    subject TEXT NOT NULL,
    kind TEXT NOT NULL,
    identity TEXT NOT NULL,
    generation BIGINT NOT NULL DEFAULT 0,
    invalidated BOOLEAN NOT NULL DEFAULT FALSE,
    payload JSONB NOT NULL DEFAULT '{}',
    UNIQUE (subject, kind)
);`,
		`CREATE TABLE maintenance_readings (
    id UUID PRIMARY KEY,
    subject TEXT NOT NULL,
    kind TEXT NOT NULL,
    identity TEXT NOT NULL,
    generation BIGINT NOT NULL DEFAULT 0,
    invalidated BOOLEAN NOT NULL DEFAULT FALSE,
    payload JSONB NOT NULL DEFAULT '{}',
    UNIQUE (subject, kind)
);`,
	}},
	{n: 195, note: "retain integration check results for shared operational status", stmts: []string{
		`CREATE TABLE integration_readings (
  id UUID PRIMARY KEY REFERENCES integrations(id) ON DELETE CASCADE,
  version BIGINT NOT NULL,
  payload JSONB NOT NULL
);`,
	}},
	{n: 196, note: "retain preview and project integration observations", stmts: []string{
		`UPDATE integration_readings SET payload=jsonb_set(payload,'{lastGood}',payload) WHERE payload->>'stateLabel'='passed'`,
		`CREATE TABLE integration_resource_readings (
 id UUID PRIMARY KEY,
 project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 capability TEXT NOT NULL,
 identity TEXT NOT NULL,
 payload JSONB NOT NULL,
 UNIQUE(project_id,capability)
);`,
		`CREATE TABLE preview_readings (
 id UUID PRIMARY KEY,
 subject TEXT NOT NULL UNIQUE,
 identity TEXT NOT NULL,
 payload JSONB NOT NULL
);`,
	}},
	{n: 197, note: "index personal project chat receipts", stmts: []string{
		"CREATE INDEX audit_log_personal_chat ON audit_log(actor_principal, project_id, at) WHERE kind IN ('console.say','console.upload')",
	}},
	{n: 198, note: "model slots use profiles only", stmts: []string{
		"DELETE FROM model_slots WHERE project_id IS NOT NULL",
		"ALTER TABLE model_slots DROP COLUMN project_id",
		"ALTER TABLE model_slots ALTER COLUMN profile SET DEFAULT 'standard'",
		"ALTER TABLE model_slots ADD PRIMARY KEY (slot, profile)",
	}},
	{n: 199, note: "basic model profile choices", stmts: []string{
		"UPDATE slot_fallbacks SET fallback='secondary-strong' WHERE slot='review' AND fallback='default'",
		"UPDATE slot_fallbacks SET fallback='primary-fast' WHERE slot='fast' AND fallback='default'",
		"UPDATE model_slots SET slot='primary-fast' WHERE slot='fast'",
	}},
	{n: 200, note: "remove per-project workflow selection", stmts: []string{"DROP TABLE IF EXISTS project_lanes"}},
	{n: 201, note: "name the main model choices", stmts: []string{
		"UPDATE model_slots SET slot=CASE slot WHEN 'default' THEN 'main-strong' WHEN 'primary-fast' THEN 'main-fast' END WHERE slot IN ('default','primary-fast')",
		"UPDATE slot_fallbacks SET slot=CASE slot WHEN 'default' THEN 'main-strong' WHEN 'primary-fast' THEN 'main-fast' END WHERE slot IN ('default','primary-fast')",
		"UPDATE slot_fallbacks SET fallback=CASE fallback WHEN 'default' THEN 'main-strong' WHEN 'primary-fast' THEN 'main-fast' END WHERE fallback IN ('default','primary-fast')",
	}},
	{n: 202, note: "replace Docker inventory readings with host snapshots", stmts: []string{
		`DELETE FROM deployment_readings r USING targets t WHERE t.kind='docker-host' AND r.kind='inventory' AND r.subject=t.name`,
		`DO $$ BEGIN IF to_regclass('river_job') IS NOT NULL THEN
 DELETE FROM river_job j USING targets t
 WHERE t.kind='docker-host' AND j.args->>'operation'='inventory'
 AND (j.args->>'key'=t.name OR j.args->'values'->>'target'=t.name)
 AND j.state IN ('available','pending','retryable','scheduled');
 END IF; END $$`,
	}},
	{n: 203, note: "host reader loops replace snapshot jobs", stmts: []string{
		`ALTER TABLE deployment_readings ADD COLUMN act_counter BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE deployment_readings ADD COLUMN complete_act_counter BIGINT NOT NULL DEFAULT 0`,
		`UPDATE deployment_readings SET act_counter=1 WHERE kind='host-snapshot' AND invalidated`,
		`DO $$ BEGIN IF to_regclass('river_job') IS NOT NULL THEN
 DELETE FROM river_job WHERE args->>'operation' IN ('host-snapshot','host-snapshot-pass')
 AND state IN ('available','pending','retryable','scheduled');
 END IF; END $$`,
	}},
	{n: 204, note: "record landing conformance sources", stmts: []string{
		`ALTER TABLE conformance_reviews ADD COLUMN source_run UUID`,
		`ALTER TABLE conformance_reviews ADD COLUMN candidate TEXT NOT NULL DEFAULT ''`,
	}},
	{n: 205, note: "record model price sources", stmts: []string{
		`ALTER TABLE token_usage ADD COLUMN provider_reported BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE token_usage ADD COLUMN catalog BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE token_usage ADD COLUMN unpriced BIGINT NOT NULL DEFAULT 0`,
		// Old observations cannot establish provenance; keep their amounts as recorded.
		`UPDATE token_usage SET unpriced=1 WHERE total>0`,
	}},
	{n: 206, note: "link workflow releases to their pre-release backup", stmts: []string{
		`ALTER TABLE deployment_attempt_inputs ADD COLUMN backup_id UUID`,
		`ALTER TABLE releases ADD COLUMN backup_id UUID`,
	}},
	{n: 207, note: "allow explicit agent access to preview secret bindings", stmts: []string{
		`ALTER TABLE secret_bindings ADD COLUMN agent_visible BOOLEAN NOT NULL DEFAULT false CHECK (NOT agent_visible OR deployment = 'preview')`,
	}},
	{n: 208, note: "retain conversation request summaries and their model inputs", stmts: []string{
		`ALTER TABLE changes ADD COLUMN chat_request BOOLEAN NOT NULL DEFAULT false, ADD COLUMN request_summary JSONB NOT NULL DEFAULT '{}'`,
		`UPDATE changes SET chat_request=true WHERE title_ownership IN ('awaiting_chat','generated')`,
	}},
	{n: 209, note: "attribute narrative shortfalls to the person's words", stmts: []string{
		`ALTER TABLE changes ADD COLUMN request_gap_quote TEXT NOT NULL DEFAULT ''`,
		`UPDATE changes SET request_met='unknown', request_gap='' WHERE request_met IN ('partly','no')`,
	}},
	{n: 210, note: "retain check session evidence and recheck admission", stmts: []string{
		`ALTER TABLE conformance_verdicts ADD COLUMN agent_evidence JSONB`,
		`ALTER TABLE conformance_reviews ADD COLUMN recheck BOOLEAN NOT NULL DEFAULT false`,
	}},
	{n: 211, note: "separate landing builds from authorized follows-main releases", stmts: []string{
		`ALTER TABLE deployments ADD COLUMN feed_revision BIGINT NOT NULL DEFAULT 1 CHECK (feed_revision > 0), ADD COLUMN feed_authority_json JSONB, ADD COLUMN promoted_from_id UUID REFERENCES deployments(id)`,
		`UPDATE deployments d SET feed = 'main' WHERE name = 'main' OR (name = 'prod' AND NOT EXISTS (SELECT 1 FROM deployments m WHERE m.project_id=d.project_id AND m.name='main')) OR feed='landing'`,
		`UPDATE deployments d SET feed = CASE WHEN EXISTS (SELECT 1 FROM deployments m WHERE m.project_id=d.project_id AND m.feed='main') THEN 'promotion' ELSE 'manual' END, promoted_from_id = (SELECT m.id FROM deployments m WHERE m.project_id=d.project_id AND m.feed='main' ORDER BY (m.name='main') DESC, (m.name='prod') DESC, m.id LIMIT 1) WHERE feed IN ('promotion','schedule')`,
		`ALTER TABLE deployments ALTER COLUMN feed SET DEFAULT 'main', ADD CONSTRAINT deployments_feed_check CHECK (feed IN ('main','promotion','manual'))`,
		`ALTER TABLE release_requests ADD COLUMN feed_revision BIGINT, ADD COLUMN landing_approvers TEXT NOT NULL DEFAULT '', ADD COLUMN backup_evidence_json JSONB`,
		`ALTER TABLE release_requests ADD COLUMN uncopied_acceptance_json JSONB`,
	}},
	{n: 212, note: "record promotion source on releases", stmts: []string{
		`ALTER TABLE releases ADD COLUMN from_deployment_id UUID`,
	}},
	{n: 213, note: "require human approval before a deployment takes a release", stmts: []string{
		`ALTER TABLE deployments ADD COLUMN approval_role TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE releases ADD COLUMN approval_role TEXT NOT NULL DEFAULT '', ADD COLUMN approval_json JSONB, ADD COLUMN superseded_by UUID`,
	}},
	{n: 214, note: "record rollback selection and backup-time release evidence", stmts: []string{
		`ALTER TABLE releases ADD COLUMN rollback_of UUID, ADD COLUMN restore_backup_id UUID`,
		`ALTER TABLE backups ADD COLUMN running_release_id UUID`,
	}},
	{n: 215, note: "remove retired deployment workflows and their selections", data: func(ctx context.Context, tx *database.Tx) error {
		rows, err := tx.SQL().QueryContext(ctx, `SELECT l.name,l.origin,v.yaml,v.version,l.latest_version FROM lib_workflows l JOIN lib_workflow_versions v ON v.lib_id=l.id`)
		if err != nil {
			return err
		}
		var definitions []workflow215
		for rows.Next() {
			var w workflow215
			if err := rows.Scan(&w.name, &w.origin, &w.yaml, &w.version, &w.latest); err != nil {
				rows.Close()
				return err
			}
			definitions = append(definitions, w)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		removed, err := removedWorkflows215(definitions)
		if err != nil {
			return err
		}
		if _, err := tx.SQL().ExecContext(ctx, `CREATE TEMP TABLE retired_workflows215(name TEXT PRIMARY KEY) ON COMMIT DROP; CREATE TEMP TABLE retired_publications215(name TEXT,version BIGINT,PRIMARY KEY(name,version)) ON COMMIT DROP; CREATE TEMP TABLE workflow_receipts215(id UUID PRIMARY KEY,body JSONB,digest TEXT) ON COMMIT DROP; CREATE TEMP TABLE workflow_candidates215(id UUID PRIMARY KEY,body JSONB) ON COMMIT DROP`); err != nil {
			return err
		}
		for pub := range removed.publications {
			if _, err := tx.SQL().ExecContext(ctx, `INSERT INTO retired_publications215(name,version) VALUES($1,$2)`, pub.name, pub.version); err != nil {
				return err
			}
		}
		for name := range removed.names {
			if _, err := tx.SQL().ExecContext(ctx, `INSERT INTO retired_workflows215(name) VALUES($1)`, name); err != nil {
				return err
			}
		}
		patches, err := receiptPatches215(ctx, tx, removed)
		if err != nil {
			return err
		}
		for _, p := range patches {
			if _, err := tx.SQL().ExecContext(ctx, `INSERT INTO workflow_receipts215(id,body,digest) VALUES($1,$2,$3)`, p.id, string(p.body), p.digest); err != nil {
				return err
			}
		}
		candidates, err := candidatePatches215(ctx, tx, removed)
		if err != nil {
			return err
		}
		for _, p := range candidates {
			if _, err := tx.SQL().ExecContext(ctx, `INSERT INTO workflow_candidates215(id,body) VALUES($1,$2)`, p.id, string(p.body)); err != nil {
				return err
			}
		}
		return nil
	}, stmts: []string{
		`CREATE FUNCTION pg_temp.clean_workflow_refs215(doc JSONB) RETURNS JSONB LANGUAGE plpgsql AS $$ BEGIN
          IF jsonb_typeof(doc->'workflows')='array' THEN
            doc := jsonb_set(doc,'{workflows}',COALESCE((SELECT jsonb_agg(w ORDER BY ord) FROM jsonb_array_elements(doc->'workflows') WITH ORDINALITY a(w,ord) WHERE NOT EXISTS(SELECT 1 FROM retired_workflows215 r WHERE r.name=w#>>'{}')),'[]'));
          END IF;
          IF jsonb_typeof(doc->'schedules')='array' THEN
            doc := jsonb_set(doc,'{schedules}',COALESCE((SELECT jsonb_agg(w ORDER BY ord) FROM jsonb_array_elements(doc->'schedules') WITH ORDINALITY a(w,ord) WHERE NOT EXISTS(SELECT 1 FROM retired_workflows215 r WHERE r.name=w#>>'{}')),'[]'));
          END IF;
          RETURN doc;
        END $$`,
		`UPDATE packs SET spec_json=pg_temp.clean_workflow_refs215(spec_json)`,
		`CREATE FUNCTION pg_temp.clean_pinned_workflow_refs215(doc JSONB,pins JSONB) RETURNS JSONB LANGUAGE plpgsql AS $$ DECLARE field TEXT; BEGIN
          FOR field IN SELECT unnest(ARRAY['workflows','schedules']) LOOP
            IF jsonb_typeof(doc->field)='array' THEN
              doc := jsonb_set(doc,ARRAY[field],COALESCE((SELECT jsonb_agg(w ORDER BY ord) FROM jsonb_array_elements(doc->field) WITH ORDINALITY a(w,ord) WHERE NOT EXISTS(SELECT 1 FROM retired_publications215 r WHERE r.name=w#>>'{}' AND r.version=(pins->'workflows'->>(w#>>'{}'))::bigint) AND NOT EXISTS(SELECT 1 FROM retired_workflows215 r WHERE r.name=w#>>'{}')),'[]'));
            END IF;
          END LOOP;
          RETURN doc;
        END $$`,
		`UPDATE pack_versions SET spec_json=pg_temp.clean_pinned_workflow_refs215(spec_json,pins_json), declaration_json=pg_temp.clean_pinned_workflow_refs215(declaration_json,pins_json), pins_json=CASE WHEN jsonb_typeof(pins_json->'workflows')='object' THEN jsonb_set(pins_json,'{workflows}',COALESCE((SELECT jsonb_object_agg(key,value) FROM jsonb_each(pins_json->'workflows') WHERE NOT EXISTS(SELECT 1 FROM retired_publications215 r WHERE r.name=key AND r.version=(value#>>'{}')::bigint) AND NOT EXISTS(SELECT 1 FROM retired_workflows215 r WHERE r.name=key)),'{}')) ELSE pins_json END`,
		`UPDATE project_adoptions a SET receipt_json=p.body,digest=p.digest FROM workflow_receipts215 p WHERE a.id=p.id`,
		`UPDATE changes c SET target_candidate_json=p.body FROM workflow_candidates215 p WHERE c.id=p.id`,
		`DELETE FROM workflow_schedules WHERE workflow_name IN (SELECT name FROM retired_workflows215)`,
		`DELETE FROM system_workflows WHERE name IN (SELECT name FROM retired_workflows215) OR stock_name='ship'`,
		`DELETE FROM lib_workflow_versions v USING lib_workflows l,retired_publications215 r WHERE v.lib_id=l.id AND l.name=r.name AND v.version=r.version`,
		`DELETE FROM lib_workflows WHERE origin='stock' AND name IN (SELECT name FROM retired_workflows215)`,
		`UPDATE lib_workflows l SET latest_version=(SELECT max(v.version) FROM lib_workflow_versions v WHERE v.lib_id=l.id) WHERE EXISTS(SELECT 1 FROM retired_publications215 r WHERE r.name=l.name AND r.version=l.latest_version)`,
	}},
	{n: 216, note: "retain deployment runtime intent and attribution", stmts: []string{
		`ALTER TABLE deployments ADD COLUMN runtime_state TEXT NOT NULL DEFAULT 'running' CHECK (runtime_state IN ('running','stopped','starting','stopping','restarting')), ADD COLUMN runtime_actor TEXT NOT NULL DEFAULT '', ADD COLUMN runtime_at TIMESTAMPTZ, ADD COLUMN runtime_error TEXT NOT NULL DEFAULT ''`,
	}},
	{n: 217, note: "remove unused target preview setting", stmts: []string{
		`UPDATE targets SET capabilities_json=capabilities_json - 'previews' WHERE capabilities_json ? 'previews'`,
	}},
	// Memory lives in imported repositories too; every existing project keeps
	// its memory pages. An empty new database receives only catalogue defaults.
	{n: 218, note: "preserve beta areas used by existing installations", stmts: []string{
		`INSERT INTO instance_settings (key, value) SELECT 'feature.evals', 'true' WHERE EXISTS (SELECT 1 FROM evals) OR EXISTS (SELECT 1 FROM audit_log WHERE kind LIKE 'eval.%') ON CONFLICT (key) DO NOTHING`,
		`INSERT INTO instance_settings (key, value) SELECT 'feature.what-ifs', 'true' WHERE EXISTS (SELECT 1 FROM what_if_reviews) ON CONFLICT (key) DO NOTHING`,
		`INSERT INTO instance_settings (key, value) SELECT 'feature.restore-rehearsals', 'true' WHERE EXISTS (SELECT 1 FROM restore_rehearsals) OR EXISTS (SELECT 1 FROM restored_copies) OR EXISTS (SELECT 1 FROM rehearsal_places) OR EXISTS (SELECT 1 FROM operations WHERE kind IN ('deployment.rehearsal', 'deployment.restored-copy', 'deployment.restored-copy-end')) ON CONFLICT (key) DO NOTHING`,
		`INSERT INTO instance_settings (key, value) SELECT 'feature.conformance-estate', 'true' WHERE EXISTS (SELECT 1 FROM projects) OR EXISTS (SELECT 1 FROM conformance_reviews) ON CONFLICT (key) DO NOTHING`,
		`INSERT INTO instance_settings (key, value) SELECT 'feature.app-platform', 'true' WHERE EXISTS (SELECT 1 FROM targets WHERE kind = 'do-app') OR EXISTS (SELECT 1 FROM audit_log WHERE kind IN ('target.attached', 'target.retired') AND payload_json->>'kind' = 'do-app') OR EXISTS (SELECT 1 FROM integrations WHERE capability = 'cloud-account' AND provider = 'digitalocean') ON CONFLICT (key) DO NOTHING`,
		`INSERT INTO instance_settings (key, value) SELECT 'feature.portainer', 'true' WHERE EXISTS (SELECT 1 FROM targets WHERE kind = 'portainer') OR EXISTS (SELECT 1 FROM audit_log WHERE kind IN ('target.attached', 'target.retired') AND payload_json->>'kind' = 'portainer') OR EXISTS (SELECT 1 FROM integrations WHERE provider = 'portainer') ON CONFLICT (key) DO NOTHING`,
		`INSERT INTO instance_settings (key, value) SELECT 'feature.bucket-site', 'true' WHERE EXISTS (SELECT 1 FROM targets WHERE kind = 's3-bucket') OR EXISTS (SELECT 1 FROM audit_log WHERE kind IN ('target.attached', 'target.retired') AND payload_json->>'kind' = 's3-bucket') ON CONFLICT (key) DO NOTHING`,
		`INSERT INTO instance_settings (key, value) SELECT 'feature.mcp', 'true' WHERE EXISTS (SELECT 1 FROM audit_log WHERE kind LIKE 'mcp.%') OR EXISTS (SELECT 1 FROM oauth_consents) OR EXISTS (SELECT 1 FROM oauth_clients) ON CONFLICT (key) DO NOTHING`,
		`INSERT INTO instance_settings (key, value) SELECT 'feature.memory', 'true' WHERE EXISTS (SELECT 1 FROM projects) ON CONFLICT (key) DO NOTHING`,
	}},
	{n: 219, note: "record release deployment progress", stmts: []string{
		`ALTER TABLE releases ADD COLUMN stage TEXT NOT NULL DEFAULT '', ADD COLUMN stage_entered_at TIMESTAMPTZ, ADD COLUMN failed_stage TEXT NOT NULL DEFAULT '', ADD COLUMN stage_finished_at TIMESTAMPTZ`,
	}},
	{n: 220, note: "make core working areas always on", stmts: []string{
		`INSERT INTO instance_settings (key, value) VALUES ('feature.ways','true'), ('feature.previews','true'), ('feature.complaints','true'), ('feature.usage','true') ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value`,
	}},
	{n: 221, note: "record a deployment's other acts", stmts: []string{
		`ALTER TABLE deployments ADD COLUMN act_at TIMESTAMPTZ`,
	}},
	{n: 222, note: "enable container data on existing Fly targets", stmts: []string{
		`UPDATE targets SET needs_mode='emulators', capabilities_json=capabilities_json || '{"persistentVolumes":true,"needs":["postgres"],"rollout":["replace","rolling","rollback"]}'::jsonb, settings_revision=settings_revision+1 WHERE kind='fly-machines' AND needs_mode='managed' AND COALESCE(capabilities_json->'needs','[]'::jsonb)='[]'::jsonb`,
	}},
	{n: 223, note: "name target data-directory retention explicitly", stmts: []string{
		`UPDATE targets SET capabilities_json=(capabilities_json - 'persistentVolumes') || jsonb_build_object('keepsDataDir', capabilities_json->'persistentVolumes'), settings_revision=settings_revision+1 WHERE capabilities_json ? 'persistentVolumes'`,
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
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&tables); err != nil {
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
	if _, err := tx.SQL().ExecContext(ctx, `INSERT INTO schema_version (version) VALUES ($1)`, latestSchemaVersion()); err != nil {
		return 0, fmt.Errorf("schema: stamp fresh database: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return 0, nil
}

func schemaVersion(ctx context.Context, db *database.DB) (int, error) {
	var count, version int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MIN(version), 0) FROM schema_version`).Scan(&count, &version); err != nil {
		return 0, fmt.Errorf("existing database has no readable schema version: %w", err)
	}
	if count != 1 {
		return 0, fmt.Errorf("existing database must have exactly one schema version; found %d", count)
	}
	return version, nil
}

func applyMigrations(ctx context.Context, db *database.DB, list []migration) (applied int, err error) {
	version, err := schemaVersion(ctx, db)
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
		if _, err := tx.SQL().ExecContext(ctx, `SELECT set_config('migration.build', $1, true)`, version.String()); err != nil {
			return err
		}
		if m.data != nil {
			if err := m.data(ctx, tx); err != nil {
				return err
			}
		}
		for _, q := range m.stmts {
			if _, err := tx.SQL().ExecContext(ctx, q); err != nil {
				return err
			}
		}

		if _, err := tx.SQL().ExecContext(ctx, `UPDATE schema_version SET version=$1`, m.n); err != nil {
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
func Version(ctx context.Context, db *database.DB) (int, error) { return schemaVersion(ctx, db) }

// queryer is what reads a database outside AppTown's own handle (a restore
// reads the database it just loaded, before AppTown opens it).
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// PublicTables is how many tables the database's public schema holds: none
// in one a restore may load into.
func PublicTables(ctx context.Context, q queryer) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&n)
	return n, err
}

// RowsIn is how many rows a table holds, named as a plain dump names it
// (schema.table, either part quoted or not).
func RowsIn(ctx context.Context, q queryer, table string) (int64, error) {
	schema, name, ok := strings.Cut(table, ".")
	if !ok {
		schema, name = "public", table
	}
	var n int64
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+pq.QuoteIdentifier(strings.Trim(schema, `"`))+"."+pq.QuoteIdentifier(strings.Trim(name, `"`))).Scan(&n)
	return n, err
}
