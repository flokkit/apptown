CREATE TABLE IF NOT EXISTS token_usage (
  id UUID PRIMARY KEY,
  at               TIMESTAMPTZ NOT NULL,
  kind             TEXT NOT NULL,
  project_id UUID,
  run_id UUID,
  root_run_id UUID,
  step_id UUID,
  workflow_name    TEXT NOT NULL DEFAULT '',
  workflow_version BIGINT NOT NULL DEFAULT 0,
  session_id       TEXT NOT NULL DEFAULT '',
  actor            TEXT NOT NULL DEFAULT '',
  -- The spender's principal and way of acting (access.Actor); NULL for AppTown, or one not resolved to a person.
  actor_principal UUID,
  actor_via TEXT NOT NULL DEFAULT '',
  provider         TEXT NOT NULL DEFAULT '',
  model            TEXT NOT NULL DEFAULT '',
  input            BIGINT NOT NULL DEFAULT 0,
  output           BIGINT NOT NULL DEFAULT 0,
  cache_read       BIGINT NOT NULL DEFAULT 0,
  cache_write      BIGINT NOT NULL DEFAULT 0,
  total            BIGINT NOT NULL DEFAULT 0,
  cost_micros      BIGINT NOT NULL DEFAULT 0,
  observation      TEXT NOT NULL DEFAULT '',
  incomplete       TEXT NOT NULL DEFAULT '',
  -- An AppTown model call's line is written before the call is sent and is
  -- pending until settled; past this time an unsettled line counts as
  -- unknown spend. NULL for every settled or ordinary line.
  pending_until    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_token_usage_at ON token_usage(at);
CREATE UNIQUE INDEX IF NOT EXISTS token_usage_observation ON token_usage(observation) WHERE observation <> '';
CREATE INDEX IF NOT EXISTS idx_token_usage_project ON token_usage(project_id, at);
CREATE TABLE IF NOT EXISTS usage_cursors (
  path        TEXT PRIMARY KEY,
  byte_offset BIGINT NOT NULL DEFAULT 0
);
