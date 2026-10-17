-- Remediation as a queue (design/subsystems/remediation.md): see migration 34 for the
-- column notes. Declared worker pools by kind; jobs → items → attempts; fixes
-- are records written by the review, attributed to the Changes in range.
CREATE TABLE IF NOT EXISTS worker_pools (
  kind       TEXT PRIMARY KEY,
  count      BIGINT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS remediations (
  id UUID PRIMARY KEY,
  -- How people name it ("remediation 9"): its number within the forge, from
  -- remediation_numbers, never reused.
  number BIGINT NOT NULL,
  scope        TEXT NOT NULL,
  scope_ref    TEXT NOT NULL DEFAULT '',
  mode         TEXT NOT NULL DEFAULT 'attended',
  actor        TEXT NOT NULL,
  -- The starter's principal and way of acting (access.Actor); NULL when not resolved to a person.
  actor_principal UUID,
  actor_via TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL,
  concurrency  BIGINT NOT NULL DEFAULT 0,
  retries      BIGINT NOT NULL DEFAULT 0,
  review_id UUID,
  created_at   TIMESTAMPTZ NOT NULL,
  updated_at   TIMESTAMPTZ NOT NULL,
  finished_at  TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS remediation_items (
  id UUID PRIMARY KEY,
  remediation_id UUID NOT NULL REFERENCES remediations(id) ON DELETE CASCADE,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  project_slug         TEXT NOT NULL,
  check_key            TEXT NOT NULL,
  block                TEXT NOT NULL,
  version              BIGINT NOT NULL,
  check_name           TEXT NOT NULL,
  rank                 BIGINT NOT NULL DEFAULT 0,
  state                TEXT NOT NULL,
  reason               TEXT NOT NULL DEFAULT '',
  origin_verdict_id UUID,
  origin_sha           TEXT NOT NULL DEFAULT '',
  origin_evaluator_ver TEXT NOT NULL DEFAULT '',
  origin_evidence_hash TEXT NOT NULL DEFAULT '',
  origin_detail        TEXT NOT NULL DEFAULT '',
  attempt              BIGINT NOT NULL DEFAULT 0,
  transient_retries    BIGINT NOT NULL DEFAULT 0,
  change_id UUID,
  run_id UUID,
  lease_worker         TEXT NOT NULL DEFAULT '',
  lease_until          TIMESTAMPTZ,
  not_before           TIMESTAMPTZ,
  verified_review_id UUID,
  outcome_revision     BIGINT NOT NULL DEFAULT 0,
  cost_micros          BIGINT NOT NULL DEFAULT 0,
  updated_at           TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS remediation_attempts (
  id UUID PRIMARY KEY,
  item_id UUID NOT NULL REFERENCES remediation_items(id) ON DELETE CASCADE,
  n           BIGINT NOT NULL,
  change_id UUID,
  run_id UUID,
  base_sha    TEXT NOT NULL DEFAULT '',
  outcome     TEXT NOT NULL DEFAULT '',
  cost_micros  BIGINT NOT NULL DEFAULT 0,
  reason      TEXT NOT NULL DEFAULT '',
  started_at  TIMESTAMPTZ NOT NULL,
  finished_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS remediations_number ON remediations(number);
-- The last number given to one of remediations: the next is one more, whatever was
-- deleted since, so a number never names a second record.
CREATE TABLE IF NOT EXISTS remediation_numbers (
  id   BOOLEAN PRIMARY KEY DEFAULT true CONSTRAINT remediation_numbers_one CHECK (id),
  last BIGINT NOT NULL
);
