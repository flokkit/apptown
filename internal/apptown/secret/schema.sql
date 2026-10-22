-- Object capabilities (authorization-capabilities.md §11, authz_tickets.go):
-- short-lived credentials for one operation on one object, e.g. the Change
-- Git ticket. Hashed at rest, consumed by success, revocable, never a username.
-- Secrets (design/subsystems/secrets.md): the built-in store's values (obfuscated,
-- never plaintext), each deployment's bindings (name → store + ref +
-- version, never a value), and which declared profile a deployment uses.
CREATE TABLE IF NOT EXISTS secret_store_key (
  singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
  value TEXT NOT NULL CHECK (value ~ '^([0-9A-Fa-f]{32}|[0-9A-Fa-f]{48}|[0-9A-Fa-f]{64})$')
);
CREATE TABLE IF NOT EXISTS secret_values (
  id UUID PRIMARY KEY,
  scope      TEXT NOT NULL,
  name       TEXT NOT NULL,
  version    BIGINT NOT NULL,
  value      TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  created_by TEXT NOT NULL DEFAULT '',
  -- The setter's principal and way of acting (access.Actor); NULL for AppTown, or one not resolved to a person.
  created_by_principal UUID,
  created_by_via TEXT NOT NULL DEFAULT '',
  UNIQUE(scope, name, version)
);
CREATE TABLE IF NOT EXISTS secret_bindings (
  project_id UUID NOT NULL,
  deployment TEXT NOT NULL,
  name        TEXT NOT NULL,
  store       TEXT NOT NULL,
  ref         TEXT NOT NULL DEFAULT '',
  version     TEXT NOT NULL DEFAULT '',
  generated   BIGINT NOT NULL DEFAULT 0,
  updated_at  TIMESTAMPTZ NOT NULL,
  updated_by  TEXT NOT NULL DEFAULT '',
  -- The binder's principal and way of acting (access.Actor); NULL for AppTown, or one not resolved to a person.
  updated_by_principal UUID,
  updated_by_via TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (project_id, deployment, name)
);
CREATE TABLE IF NOT EXISTS secret_profiles (
  project_id UUID NOT NULL,
  deployment TEXT NOT NULL,
  profile     TEXT NOT NULL,
  PRIMARY KEY (project_id, deployment)
);
