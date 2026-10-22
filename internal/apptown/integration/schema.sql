CREATE TABLE IF NOT EXISTS integrations (
  id UUID PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  capability  TEXT NOT NULL,
  provider    TEXT NOT NULL,
  config_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  version     BIGINT NOT NULL DEFAULT 1,
  managed     BOOLEAN NOT NULL DEFAULT false,
  created_at  TIMESTAMPTZ NOT NULL,
  updated_at  TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS integration_versions (
  id UUID PRIMARY KEY,
  integration_id UUID NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
  version        BIGINT NOT NULL,
  config_json    JSONB NOT NULL,
  created_at     TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS project_integrations (
  project_id UUID NOT NULL,
  capability    TEXT NOT NULL,
  binding       TEXT NOT NULL,
  resource_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  wire_change_id UUID,
  provenance    TEXT NOT NULL DEFAULT '',
  created_at    TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (project_id, capability)
);
CREATE TABLE IF NOT EXISTS service_kinds (
  capability  TEXT PRIMARY KEY,
  spec_yaml   TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL,
  updated_at  TIMESTAMPTZ NOT NULL
);
