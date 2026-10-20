-- Which pack versions a project is on (§1.1): placed at migration from the
-- evidence (§7), moved by adoption afterwards. An observation at placement,
-- never a claim about history.
CREATE TABLE IF NOT EXISTS project_packs (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  pack_id UUID NOT NULL REFERENCES packs(id) ON DELETE CASCADE,
  version    BIGINT NOT NULL,
  reason     TEXT NOT NULL DEFAULT '',
  adopted_by TEXT NOT NULL DEFAULT '',
  -- The adopter's principal and way of acting (access.Actor); NULL for AppTown, or one not resolved to a person.
  adopted_by_principal UUID,
  adopted_by_via TEXT NOT NULL DEFAULT '',
  adopted_at TIMESTAMPTZ NOT NULL,
  UNIQUE(project_id, pack_id)
);
-- Adoption owns a project's current selection separately from its immutable
-- receipt history. A zero or unavailable revision remains observable without
-- manufacturing a receipt; current is never inferred from the newest history.
CREATE TABLE IF NOT EXISTS adoptions (
  project_id UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  revision BIGINT NOT NULL DEFAULT 0,
  pack TEXT NOT NULL DEFAULT '',
  drift_note TEXT NOT NULL DEFAULT ''
);
-- An adoption revision (§1.1, §4.1): the exact way of working a project is
-- on, as an immutable receipt with a digest. adoptions.revision selects the
-- current one within its project; target revisions are independent history.
CREATE TABLE IF NOT EXISTS project_adoptions (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  revision     BIGINT NOT NULL,
  kind         TEXT NOT NULL,                 -- legacy-snapshot | edit | drift
  reason       TEXT NOT NULL DEFAULT '',
  digest       TEXT NOT NULL,
  created_by   TEXT NOT NULL DEFAULT '',
  -- The author's principal and way of acting (access.Actor); NULL for AppTown, or one not resolved to a person.
  created_by_principal UUID,
  created_by_via TEXT NOT NULL DEFAULT '',
  created_at   TIMESTAMPTZ NOT NULL,
  receipt_json JSONB NOT NULL,
  -- An uplift's target (kind target) belongs to one change: its change_id,
  -- with revision 0. Every other row is a revision of the project.
  change_id    UUID
);
CREATE UNIQUE INDEX IF NOT EXISTS project_adoptions_revision ON project_adoptions(project_id, revision) WHERE change_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS project_adoptions_target ON project_adoptions(project_id, change_id) WHERE change_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS policies (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  rule        TEXT NOT NULL,
  config_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  origin      TEXT NOT NULL DEFAULT '',
  created_at  TIMESTAMPTZ NOT NULL,
  UNIQUE(project_id, name)
);
CREATE TABLE IF NOT EXISTS guidance_imports (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  position   BIGINT NOT NULL,
  block_id UUID NOT NULL REFERENCES guidance_blocks(id) ON DELETE CASCADE,
  version    BIGINT NOT NULL,
  disabled   BOOLEAN NOT NULL DEFAULT false
);
-- Workflows as lanes (design/decisions/workflows-as-lanes.md): a project SELECTS
-- lanes from the library; system workflows are bound per instance.
CREATE TABLE IF NOT EXISTS project_lanes (
  project_id UUID NOT NULL,
  name       TEXT NOT NULL,
  enabled    BOOLEAN NOT NULL DEFAULT true,
  updated_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (project_id, name)
);
-- Opt-in pi extensions enabled per project (extensions.go): the blessed
-- baked extension set is off by default; a row here turns one on.
CREATE TABLE IF NOT EXISTS project_pi_extensions (
  project_id UUID NOT NULL,
  name       TEXT NOT NULL,
  PRIMARY KEY (project_id, name)
);
