CREATE TABLE IF NOT EXISTS lib_workflows (
  id UUID PRIMARY KEY,
  name           TEXT NOT NULL UNIQUE,
  description    TEXT NOT NULL DEFAULT '',
  latest_version BIGINT NOT NULL DEFAULT 1,
  origin         TEXT NOT NULL DEFAULT '',
  updated_at     TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS lib_workflow_versions (
  id UUID PRIMARY KEY,
  lib_id UUID NOT NULL REFERENCES lib_workflows(id) ON DELETE CASCADE,
  version    BIGINT NOT NULL,
  yaml       TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  -- Where it came from: the source it was published by (flokkit, the
  -- built-in source, or here, this AppTown) and who signed what came in
  -- ('' for here; "shipped with AppTown <version>, unsigned" for stock a
  -- release shipped; "shipped with AppTown before <build>, unsigned" for
  -- stock older than this record).
  source     TEXT NOT NULL CONSTRAINT lib_workflow_version_source CHECK (source <> ''),
  signer     TEXT NOT NULL,
  UNIQUE(lib_id, version)
);
CREATE TABLE IF NOT EXISTS packs (
  id UUID PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT '',
  spec_json   JSONB NOT NULL DEFAULT '{}'::jsonb,
  latest_version BIGINT NOT NULL DEFAULT 0,
  -- The version new projects get and projects are offered as the update;
  -- a person moves it (design/subsystems/ways-of-working-versions.md).
  recommended_version BIGINT NOT NULL DEFAULT 0,
  origin      TEXT NOT NULL DEFAULT '',
  enabled     BOOLEAN NOT NULL DEFAULT true,
  updated_at  TIMESTAMPTZ NOT NULL
);
-- A pack version (§1.2): immutable; cut whenever the pack's spec changes.
CREATE TABLE IF NOT EXISTS pack_versions (
  id UUID PRIMARY KEY,
  pack_id UUID NOT NULL REFERENCES packs(id) ON DELETE CASCADE,
  version    BIGINT NOT NULL,
  spec_json  JSONB NOT NULL,
  note       TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL,
  pins_json  JSONB NOT NULL DEFAULT '{}'::jsonb,   -- block name → exact version, pinned at publish (§1.2)
  -- What the author wrote; spec_json and pins_json are its pinned result.
  declaration_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  -- An optional name, and the version of this pack it was made from (0: none).
  name       TEXT NOT NULL DEFAULT '',
  based_on   BIGINT NOT NULL DEFAULT 0,
  -- Where it came from, as lib_workflow_versions says.
  source     TEXT NOT NULL CONSTRAINT pack_version_source CHECK (source <> ''),
  signer     TEXT NOT NULL,
  UNIQUE(pack_id, version)
);
CREATE TABLE IF NOT EXISTS policy_library (
  id UUID PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT '',
  rule        TEXT NOT NULL,
  config_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  origin      TEXT NOT NULL DEFAULT '',
  updated_at  TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS guidance_blocks (
  id UUID PRIMARY KEY,
  name           TEXT NOT NULL UNIQUE,
  tags           TEXT NOT NULL DEFAULT '',
  latest_version BIGINT NOT NULL DEFAULT 1,
  origin         TEXT NOT NULL DEFAULT '',
  created_at     TIMESTAMPTZ NOT NULL,
  updated_at     TIMESTAMPTZ NOT NULL,
  tier           TEXT NOT NULL DEFAULT '',  -- '' | platform (rides on every repo-backed project)
  conflicts      TEXT NOT NULL DEFAULT ''   -- comma list of blocks this one contradicts
);
CREATE TABLE IF NOT EXISTS guidance_block_versions (
  id UUID PRIMARY KEY,
  block_id UUID NOT NULL REFERENCES guidance_blocks(id) ON DELETE CASCADE,
  version     BIGINT NOT NULL,
  body_md     TEXT NOT NULL,
  checks_json JSONB NOT NULL DEFAULT '[]'::jsonb,
  created_at  TIMESTAMPTZ NOT NULL,
  -- Where it came from, as lib_workflow_versions says.
  source      TEXT NOT NULL CONSTRAINT guidance_block_version_source CHECK (source <> ''),
  signer      TEXT NOT NULL,
  UNIQUE(block_id, version)
);
CREATE TABLE IF NOT EXISTS system_workflows (
  stock_name TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL
);

-- Immutable local classification evidence; revocation never removes its meaning.
CREATE TABLE IF NOT EXISTS routine_publication_classifications (
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
CREATE UNIQUE INDEX IF NOT EXISTS routine_publication_active_digest ON routine_publication_classifications(digest) WHERE revocation_json = 'null'::jsonb;
