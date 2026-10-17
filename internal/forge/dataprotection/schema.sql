-- A set of copies of a deployment's data: where its files are (the forge's
-- disk or the backup store), what each item is and its digest, and the
-- data class it was taken from. It outlives the deployment it was taken of
-- and is kept with its project until the project is purged.
CREATE TABLE IF NOT EXISTS backups (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL,
  deployment  TEXT NOT NULL,
  taken_at     TIMESTAMPTZ NOT NULL,
  trigger      TEXT NOT NULL DEFAULT 'manual',        -- manual | scheduled | release
  actor        TEXT NOT NULL DEFAULT '',
  -- The taker's principal and way of acting (access.Actor); NULL for the forge, or one not resolved to a person.
  actor_principal UUID,
  actor_via TEXT NOT NULL DEFAULT '',
  path         TEXT NOT NULL DEFAULT '',              -- the set's directory under <data>/backups
  bytes        BIGINT NOT NULL DEFAULT 0,
  items_json   JSONB NOT NULL DEFAULT '[]'::jsonb,    -- what was taken: kind, name, file, bytes, sha256
  status       TEXT NOT NULL DEFAULT 'ok',            -- ok | partial | failed | pruned
  error        TEXT NOT NULL DEFAULT '',
  -- Where the set's files are: forge-disk (path is a directory on the
  -- forge's disk) or store (path is the object prefix in the backup store).
  store        TEXT NOT NULL DEFAULT 'forge-disk',
  -- The data class of the deployment the set was taken of; '' on sets from
  -- before 8 October 2026. A production copy goes only into production.
  data_class   TEXT NOT NULL DEFAULT '',
  -- Where a set in the store was written: it is read and pruned there.
  store_endpoint TEXT NOT NULL DEFAULT '',
  store_bucket   TEXT NOT NULL DEFAULT ''
);
-- The one backup store an administrator attached: a private bucket in an
-- S3-compatible store, reached with the key pair the named secrets hold.
CREATE TABLE IF NOT EXISTS backup_store (
  id              INT PRIMARY KEY CHECK (id = 1),
  endpoint        TEXT NOT NULL,
  region          TEXT NOT NULL,
  bucket          TEXT NOT NULL,
  access_key_from TEXT NOT NULL,
  secret_key_from TEXT NOT NULL,
  attached_by     TEXT NOT NULL,
  -- The attacher's principal and way of acting (access.Actor); NULL when not resolved to a person.
  attached_by_principal UUID,
  attached_by_via TEXT NOT NULL DEFAULT '',
  attached_at     TIMESTAMPTZ NOT NULL,
  checked_at      TIMESTAMPTZ NOT NULL
);
-- A copy filled into a new, empty place and checked there: whether it came
-- back, from which set, on which target, into which place, as whom.
CREATE TABLE IF NOT EXISTS backup_drills (
  id UUID PRIMARY KEY,
  backup_id UUID NOT NULL,
  project_id UUID NOT NULL,
  deployment   TEXT NOT NULL,
  drilled_at   TIMESTAMPTZ NOT NULL,
  passed       BOOLEAN NOT NULL,
  target       TEXT NOT NULL,
  place        TEXT NOT NULL,
  as_user      TEXT NOT NULL,
  detail       TEXT NOT NULL DEFAULT '',
  operation_id UUID,
  deployment_id UUID -- the deployment drilled, by id; 0: not provably one
);
-- A restore rehearsal (design/subsystems/deployment.md): a
-- throwaway place started from a copy, the app booted on it, checked and
-- taken down. Its facts are only what is safe to show a project reader:
-- step names, status codes, names of what was withheld; never output.
CREATE TABLE IF NOT EXISTS restore_rehearsals (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL,
  deployment     TEXT NOT NULL,
  operation_id UUID NOT NULL,
  backup_id UUID NOT NULL,
  release_number BIGINT NOT NULL,
  host           TEXT NOT NULL,
  outcome        TEXT NOT NULL,
  failed_step    TEXT NOT NULL DEFAULT '',
  asked_by       TEXT NOT NULL,
  -- The asker's principal and way of acting (access.Actor); NULL for the forge, or one not resolved to a person.
  asked_by_principal UUID,
  asked_by_via TEXT NOT NULL DEFAULT '',
  facts_json     JSONB NOT NULL DEFAULT '{}'::jsonb,
  rehearsed_at   TIMESTAMPTZ NOT NULL,
  deployment_id UUID -- the deployment rehearsed, by id
);
-- What a rehearsal makes on the rehearsal host, recorded before it is
-- made, and when it was seen removed: the sweep removes what an ended
-- rehearsal still owns (one the forge stopped in the middle of, or whose
-- take-down left something).
CREATE TABLE IF NOT EXISTS rehearsal_places (
  operation_id UUID PRIMARY KEY,
  project_id UUID NOT NULL,
  place_json   JSONB NOT NULL,
  recorded_at  TIMESTAMPTZ NOT NULL,
  cleared_at   TIMESTAMPTZ
);
-- A restored copy (design/subsystems/deployment.md): a temporary
-- deployment (kind restored-copy) of a production deployment's current
-- release, started from a copy on the source's own target, behind the
-- forge's access gate. The row outlives the deployment it made: it is the
-- evidence of the check, who opened it, and when it was taken down. It
-- holds only safe facts (step names, status codes, names of what was
-- withheld), never output, and the gate secret only as its SHA-256.
-- app_marker is the copy's own random mark, written before anything is
-- made and rendered into its app: an app found by name is deleted only
-- when it carries it (RTB2-01).
CREATE TABLE IF NOT EXISTS restored_copies (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL,
  -- Within its project: names the copy's deployment (copy-<number>), which
  -- is also its app's name on a platform with short names.
  number             BIGINT NOT NULL,
  source             TEXT NOT NULL,
  source_id UUID NOT NULL,
  deployment         TEXT NOT NULL DEFAULT '',
  deployment_id UUID,
  operation_id UUID NOT NULL,
  backup_id UUID NOT NULL,
  release_number     BIGINT NOT NULL DEFAULT 0,
  target             TEXT NOT NULL DEFAULT '',
  is_test            BOOLEAN NOT NULL,
  lifetime_minutes   BIGINT NOT NULL DEFAULT 0,
  expires_at         TIMESTAMPTZ,
  gate_url           TEXT NOT NULL DEFAULT '',
  gate_secret_sha256 TEXT NOT NULL DEFAULT '',
  app_marker         TEXT NOT NULL DEFAULT '',
  asked_by           TEXT NOT NULL,
  -- The asker's principal and way of acting (access.Actor); NULL for the forge, or one not resolved to a person.
  asked_by_principal UUID,
  asked_by_via TEXT NOT NULL DEFAULT '',
  outcome            TEXT NOT NULL DEFAULT '',
  failed_step        TEXT NOT NULL DEFAULT '',
  facts_json         JSONB NOT NULL DEFAULT '{}'::jsonb,
  started_at         TIMESTAMPTZ NOT NULL,
  checked_at         TIMESTAMPTZ,
  ended_at           TIMESTAMPTZ,
  end_detail         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS restored_copies_source ON restored_copies (source_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS restored_copies_number ON restored_copies (project_id, number);
-- A one-use ticket a person's Open issues for a restored copy's gate, kept
-- by its SHA-256 only. Redeeming it (within a minute, once) is the person's
-- entry: the redeemed rows with check_operation 0 are who opened the copy
-- and when. check_operation names the copy's own start when the ticket is
-- the forge's check of its gate: issued to the forge, redeemed by that
-- operation while it runs, and never a person's entry.
CREATE TABLE IF NOT EXISTS restored_copy_tickets (
  ticket_sha256   TEXT PRIMARY KEY,
  copy_id UUID NOT NULL REFERENCES restored_copies(id) ON DELETE CASCADE,
  person          TEXT NOT NULL,
  -- The holder's principal and way of acting (access.Actor); NULL for the forge's own gate check, or one not resolved to a person.
  person_principal UUID,
  person_via TEXT NOT NULL DEFAULT '',
  issued_at       TIMESTAMPTZ NOT NULL,
  expires_at      TIMESTAMPTZ NOT NULL,
  redeemed_at     TIMESTAMPTZ,
  check_operation UUID
);
