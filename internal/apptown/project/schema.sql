-- Assigned AppTown addresses are permanent: purging a project does not free them.
-- A null project is a historical reservation whose owner cannot be established.
CREATE TABLE IF NOT EXISTS project_addresses (
  address TEXT PRIMARY KEY CHECK (address ~ '^[a-z0-9][a-z0-9-]*$'),
  project_id UUID,
  UNIQUE (address, project_id)
);
CREATE TABLE IF NOT EXISTS projects (
  id UUID PRIMARY KEY,
  slug           TEXT NOT NULL UNIQUE,
  name           TEXT NOT NULL,
  default_branch TEXT NOT NULL DEFAULT '',
  namespace      TEXT NOT NULL DEFAULT '',
  icon           TEXT NOT NULL DEFAULT '',
  blurb          TEXT NOT NULL DEFAULT '',
  release_domain TEXT NOT NULL DEFAULT '',
  model_profile  TEXT NOT NULL DEFAULT 'standard',
  owner_group    TEXT NOT NULL DEFAULT '',
  default_approvers TEXT NOT NULL DEFAULT '',
  created_at     TIMESTAMPTZ NOT NULL,
  require_independent_approval BOOLEAN NOT NULL DEFAULT false,
  -- Advances with every write of the name, address, group, icon or blurb, so
  -- an edit started from an older reading is refused rather than applied.
  settings_revision BIGINT NOT NULL DEFAULT 0,
  -- What the project is for, written when it is made and never changed:
  -- 'ordinary', or 'eval-copy' (a throwaway copy an eval made). Effect
  -- owners ask Project what the purpose allows (project/purpose.go).
  purpose TEXT NOT NULL DEFAULT 'ordinary' CONSTRAINT projects_purpose CHECK (purpose IN ('ordinary','eval-copy')),
  -- Captured at creation; independent of the mutable AppTown navigation address.
  serving_label TEXT NOT NULL CONSTRAINT projects_serving_label_shape CHECK (serving_label ~ '^[a-z0-9][a-z0-9-]*$') CONSTRAINT projects_serving_label_unique UNIQUE,
  CONSTRAINT projects_address_owner FOREIGN KEY (slug,id) REFERENCES project_addresses(address,project_id) DEFERRABLE INITIALLY DEFERRED
);
-- A project that is not active (design/subsystems/project-deletion.md): no
-- row is active. Importing and deleting refuse new work; deleted is kept,
-- read-only, until it is purged. The row goes with the project at purge.
CREATE TABLE IF NOT EXISTS project_lifecycle (
  project_id UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  state             TEXT NOT NULL CONSTRAINT project_lifecycle_state CHECK (state IN ('importing','deleting','deleted')),
  requested_by      TEXT NOT NULL DEFAULT '',
  -- The requester's principal and way of acting (access.Actor); NULL when not resolved to a person.
  requested_by_principal UUID,
  requested_by_via TEXT NOT NULL DEFAULT '',
  requested_at      TIMESTAMPTZ NOT NULL,
  binding_id UUID, -- the scoped system binding the deletion and purge run under
  synthetic         BOOLEAN NOT NULL DEFAULT false,     -- an eval copy: no kept period
  takedowns_started BOOLEAN NOT NULL DEFAULT false,     -- the first take-down was admitted: no cancelling now
  stalled           TEXT NOT NULL DEFAULT '',           -- why the deletion waits on a person
  deleted_at        TIMESTAMPTZ,
  kept_until        TIMESTAMPTZ,
  purge_started_at  TIMESTAMPTZ                         -- the first purge was admitted: no restoring now
);
CREATE TABLE IF NOT EXISTS contract_snapshots (
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  sha           TEXT NOT NULL,
  status        TEXT NOT NULL,            -- present | absent | invalid
  app_yaml      TEXT,                     -- canonical bytes when present
  source_format TEXT NOT NULL DEFAULT '', -- app-yaml | compose-yaml | app-json | none
  error         TEXT,
  created_at    TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (project_id, sha)
);
-- A deleted project's cleanup: what the deletion committed to remove outside
-- the database, and how far that got. The row outlives the project (ids are
-- never reused). Permanent address reservations separately prevent a new
-- project from acquiring any of its old names.
CREATE TABLE IF NOT EXISTS project_deletions (
  project_id UUID PRIMARY KEY,
  slug         TEXT NOT NULL,
  requested_by TEXT NOT NULL,
  -- The requester's principal and way of acting (access.Actor); NULL when not resolved to a person.
  requested_by_principal UUID,
  requested_by_via TEXT NOT NULL DEFAULT '',
  requested_at TIMESTAMPTZ NOT NULL,
  plan         JSONB NOT NULL,
  state        TEXT NOT NULL DEFAULT 'pending', -- pending | done | needs-attention
  detail       TEXT NOT NULL DEFAULT '',
  attempts     INT NOT NULL DEFAULT 0,
  round        INT NOT NULL DEFAULT 0,          -- advances on each deliberate retry
  finished_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS project_deletions_open ON project_deletions(slug) WHERE state <> 'done';

-- Request claims and their results commit with the corresponding local act.
-- Nullable result fields exist only during its caller's uncommitted transaction.
CREATE TABLE IF NOT EXISTS project_creation_requests (
  id UUID PRIMARY KEY,
  actor_id UUID NOT NULL,
  request_id UUID NOT NULL,
  project_id UUID NOT NULL,
  input_hash TEXT,
  resolved_group TEXT,
  result_json JSONB,
  UNIQUE (actor_id, request_id)
);
CREATE TABLE IF NOT EXISTS project_rename_requests (
  id UUID PRIMARY KEY,
  actor_id UUID NOT NULL,
  request_id UUID NOT NULL,
  project_id UUID NOT NULL,
  input_hash TEXT NOT NULL,
  result_json JSONB,
  UNIQUE (actor_id, request_id)
);
