-- A principal is a person or the forge itself (design/decisions/mergeable-forge-data.md):
-- what answers for an act, by UUID. Its name is what it is called today; a
-- removed principal keeps its row (removed_at) so history still names it,
-- and its name is free for a new principal that inherits nothing.
CREATE TABLE IF NOT EXISTS principals (
  id           UUID PRIMARY KEY,
  kind         TEXT NOT NULL CONSTRAINT principal_kind CHECK (kind IN ('person', 'system')),
  name         TEXT NOT NULL,
  -- the forge a principal came from with a moved project; NULL is this one
  origin_forge UUID,
  created_at   TIMESTAMPTZ NOT NULL,
  removed_at   TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS principals_live_name ON principals(name) WHERE removed_at IS NULL AND origin_forge IS NULL;
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  principal_id UUID NOT NULL REFERENCES principals(id),
  created_at TIMESTAMPTZ NOT NULL,
  last_used  TIMESTAMPTZ,
  expires_at TIMESTAMPTZ NOT NULL,
  -- A non-secret reference an operation keeps to ask later whether the
  -- session that started it is still live.
  id UUID NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS groups (
  name        TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT '',
  kind        TEXT NOT NULL DEFAULT 'ordinary',   -- ordinary | personal | built-in
  created_by  TEXT NOT NULL DEFAULT '',
  -- The creator's principal and way of acting (access.Actor); NULL for the forge, or one not resolved to a person.
  created_by_principal UUID,
  created_by_via TEXT NOT NULL DEFAULT '',
  retired_at  TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS group_members (
  group_name TEXT NOT NULL REFERENCES groups(name) ON DELETE CASCADE,
  principal_id UUID NOT NULL REFERENCES principals(id),
  created_by TEXT NOT NULL DEFAULT '',
  -- The adder's principal and way of acting (access.Actor); NULL when not resolved to a person.
  created_by_principal UUID,
  created_by_via TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL,
  expires_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  PRIMARY KEY (group_name, principal_id)
);
-- Authorization (design/subsystems/authorization-capabilities.md §14). A grant gives a
-- subject (a person principal's id, group:<name>, service-account:<name>,
-- mandate:<id>@<v>, or anonymous) either a stock role bundle or one
-- capability, at a scope. Revocation stamps the row; history is kept.
CREATE TABLE IF NOT EXISTS grants (
  id UUID PRIMARY KEY,
  subject_type    TEXT NOT NULL,                 -- user | group | service-account | mandate | anonymous
  subject         TEXT NOT NULL,
  role_name       TEXT NOT NULL DEFAULT '',
  capability      TEXT NOT NULL DEFAULT '',
  scope_type      TEXT NOT NULL,                 -- instance | group | project | change | run | dimension | release-target
  scope_id        TEXT NOT NULL DEFAULT '',
  origin          TEXT NOT NULL DEFAULT '',      -- '' | migrated | migrated-open | profile:<name> | bootstrap
  granted_by      TEXT NOT NULL DEFAULT '',
  -- The granter's principal and way of acting (access.Actor); NULL for the forge, or one not resolved to a person.
  granted_by_principal UUID,
  granted_by_via TEXT NOT NULL DEFAULT '',
  reason          TEXT NOT NULL DEFAULT '',
  created_at      TIMESTAMPTZ NOT NULL,
  expires_at      TIMESTAMPTZ,
  review_at       TIMESTAMPTZ,
  revoked_at      TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS grants_subject ON grants(subject_type, subject, revoked_at);
CREATE INDEX IF NOT EXISTS grants_scope ON grants(scope_type, scope_id, revoked_at);
CREATE UNIQUE INDEX IF NOT EXISTS grants_live_identity ON grants(subject_type, subject, role_name, capability, scope_type, scope_id) WHERE revoked_at IS NULL;
-- One row per consequential decision (allow or deny) on a protected act:
-- the receipt an audit row points at. Identifiers, never names, are the
-- truth here; the actor string on audit_log is a display snapshot.
CREATE TABLE IF NOT EXISTS authorization_decisions (
  id UUID PRIMARY KEY,
  decided_at           TIMESTAMPTZ NOT NULL,
  outcome              TEXT NOT NULL,            -- allow | deny
  reason_code          TEXT NOT NULL DEFAULT '',
  principal_type       TEXT NOT NULL,
  principal            TEXT NOT NULL,
  delegation_kind      TEXT NOT NULL DEFAULT '',
  delegation_id        TEXT NOT NULL DEFAULT '',
  capability           TEXT NOT NULL,
  resource_type        TEXT NOT NULL DEFAULT '',
  resource_id          TEXT NOT NULL DEFAULT '',
  project_id UUID,
  grant_ids_json       JSONB NOT NULL DEFAULT '[]'::jsonb,
  roles_json   JSONB NOT NULL DEFAULT '[]'::jsonb,
  membership_paths_json JSONB NOT NULL DEFAULT '[]'::jsonb,
  constraints_json     JSONB NOT NULL DEFAULT '{}'::jsonb,
  context_json         JSONB NOT NULL DEFAULT '{}'::jsonb,
  reason               TEXT NOT NULL DEFAULT ''
);
-- Small instance-wide settings the code reads by key (the authorization
-- profile, migration flags).
CREATE TABLE IF NOT EXISTS instance_settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
);
-- OAuth for the embedded MCP (design/subsystems/mcp.md §3, authorization-capabilities
-- §10). A registered client is a public client (PKCE, no secret). A
-- delegation is what a person consented to: client, audience, scopes,
-- bounds, expiry — an upper bound on their live authority, never a copy of
-- it. Tokens point at a delegation and are hashed at rest.
CREATE TABLE IF NOT EXISTS oauth_clients (
  client_id     TEXT PRIMARY KEY,
  name          TEXT NOT NULL DEFAULT '',
  redirect_uris TEXT NOT NULL DEFAULT '[]',
  created_at    TIMESTAMPTZ NOT NULL,
  last_used     TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS oauth_codes (
  code_hash      TEXT PRIMARY KEY,
  client_id      TEXT NOT NULL,
  principal_id   UUID NOT NULL REFERENCES principals(id),
  redirect_uri   TEXT NOT NULL,
  scopes         TEXT NOT NULL DEFAULT '',
  resource       TEXT NOT NULL DEFAULT '',
  code_challenge TEXT NOT NULL,
  auth_time      TIMESTAMPTZ NOT NULL,
  created_at     TIMESTAMPTZ NOT NULL,
  expires_at     TIMESTAMPTZ NOT NULL,
  used_at        TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS oauth_consents (
  token_hash   TEXT PRIMARY KEY,
  principal_id UUID NOT NULL REFERENCES principals(id),
  browser_hash TEXT NOT NULL,
  request_json JSONB NOT NULL,
  expires_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS oauth_consents_expiry ON oauth_consents (expires_at);
CREATE TABLE IF NOT EXISTS delegations (
  id UUID PRIMARY KEY,
  principal_id        UUID NOT NULL REFERENCES principals(id),
  client_id           TEXT NOT NULL,
  client_name         TEXT NOT NULL DEFAULT '',
  audience            TEXT NOT NULL,                 -- /mcp/user | /mcp/admin
  kind                TEXT NOT NULL DEFAULT 'oauth', -- oauth | full-breadth | legacy-token
  scopes_json         JSONB NOT NULL DEFAULT '[]'::jsonb,
  group_bounds_json   JSONB NOT NULL DEFAULT '[]'::jsonb,
  project_bounds_json JSONB NOT NULL DEFAULT '[]'::jsonb,
  auth_time           TIMESTAMPTZ,
  auth_method         TEXT NOT NULL DEFAULT '',
  created_at          TIMESTAMPTZ NOT NULL,
  last_used           TIMESTAMPTZ,
  expires_at          TIMESTAMPTZ,
  revoked_at          TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS object_capabilities (
  id UUID PRIMARY KEY,
  kind             TEXT NOT NULL,
  principal_id     UUID NOT NULL REFERENCES principals(id),
  delegation_id UUID,
  resource_type    TEXT NOT NULL,
  resource_id      TEXT NOT NULL,
  constraints_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  token_hash       TEXT NOT NULL UNIQUE,
  created_at       TIMESTAMPTZ NOT NULL,
  expires_at       TIMESTAMPTZ NOT NULL,
  consumed_at      TIMESTAMPTZ,
  revoked_at       TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS oauth_tokens (
  token_hash    TEXT PRIMARY KEY,
  kind          TEXT NOT NULL,                       -- access | refresh
  delegation_id UUID NOT NULL REFERENCES delegations(id) ON DELETE CASCADE,
  created_at    TIMESTAMPTZ NOT NULL,
  expires_at    TIMESTAMPTZ NOT NULL,
  revoked_at    TIMESTAMPTZ
);
-- A person principal's account: its sign-in name (the principal's name
-- today), password and state.
CREATE TABLE IF NOT EXISTS users (
  username   TEXT PRIMARY KEY,
  principal_id  UUID NOT NULL UNIQUE REFERENCES principals(id),
  password_hash TEXT NOT NULL DEFAULT '',
  created_at    TIMESTAMPTZ NOT NULL,
  last_seen     TIMESTAMPTZ,
  disabled_at   TIMESTAMPTZ,
  default_group TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS auth_tokens (
  name       TEXT PRIMARY KEY,
  principal_id UUID NOT NULL REFERENCES principals(id),
  role       TEXT NOT NULL DEFAULT 'member',
  token_hash TEXT NOT NULL UNIQUE,
  created_at TIMESTAMPTZ NOT NULL,
  last_used  TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  -- cli | agent, chosen when made; '' is a token made before purposes,
  -- refused until it is classified by hand.
  purpose    TEXT NOT NULL DEFAULT '' CONSTRAINT auth_token_purpose CHECK (purpose IN ('', 'cli', 'agent'))
);
-- A project's read-only git tokens, one per environment given one. Only the
-- token's hash is kept; the token itself is shown to that environment alone.
CREATE TABLE IF NOT EXISTS git_pull_tokens (
  token_hash TEXT PRIMARY KEY,
  project_id UUID NOT NULL,
  created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS git_pull_tokens_project ON git_pull_tokens (project_id);
-- An invite lets one person set the password of one account they were
-- invited to (auth.md). The link is stored only as a hash; a used, revoked
-- or expired invite stays as the record of who invited whom, with what role.
-- A password reset link (purpose 'password') is the same kind of link for
-- an account that already has a password: it sets a new one and grants
-- nothing, so it carries no role.
CREATE TABLE IF NOT EXISTS invites (
  id UUID PRIMARY KEY,
  username    TEXT NOT NULL,
  -- The person the link is for: it sets a password on this principal's
  -- account and no other, whatever its name is when it is used. NULL only
  -- on a link made before principals that named no account.
  principal_id UUID REFERENCES principals(id),
  label       TEXT NOT NULL DEFAULT '',
  role        TEXT NOT NULL DEFAULT 'member',
  invited_by  TEXT NOT NULL,
  -- The inviter's principal and way of acting (access.Actor); NULL when not resolved to a person.
  invited_by_principal UUID,
  invited_by_via TEXT NOT NULL DEFAULT '',
  token_hash  TEXT NOT NULL UNIQUE,
  created_at  TIMESTAMPTZ NOT NULL,
  expires_at  TIMESTAMPTZ NOT NULL,
  accepted_at TIMESTAMPTZ,
  revoked_at  TIMESTAMPTZ,
  revoked_by  TEXT NOT NULL DEFAULT '',
  -- The revoker's principal and way of acting (access.Actor); NULL unless revoked by a resolved person.
  revoked_by_principal UUID,
  revoked_by_via TEXT NOT NULL DEFAULT '',
  purpose     TEXT NOT NULL DEFAULT 'invite',
  CONSTRAINT invite_role CHECK ((purpose = 'invite' AND role IN ('member','admin')) OR (purpose = 'password' AND role = ''))
);
CREATE INDEX IF NOT EXISTS invites_pending ON invites(username) WHERE accepted_at IS NULL AND revoked_at IS NULL;
-- A scoped system binding: authority the forge holds for one purpose on
-- one project, granted by a person's decision (a project's deletion and
-- its purge after the kept period). It permits only what its purpose
-- names, is checked before each effect like any credential, and stops
-- everything it runs when revoked.
CREATE TABLE IF NOT EXISTS system_bindings (
  id UUID PRIMARY KEY,
  purpose     TEXT NOT NULL,
  project_id UUID NOT NULL,
  granted_by  TEXT NOT NULL,
  -- The granter's principal and way of acting (access.Actor); NULL when not resolved to a person.
  granted_by_principal UUID,
  granted_by_via TEXT NOT NULL DEFAULT '',
  decision    JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at  TIMESTAMPTZ NOT NULL,
  revoked_at  TIMESTAMPTZ,
  revoked_by  TEXT NOT NULL DEFAULT '',
  -- The revoker's principal and way of acting (access.Actor); NULL unless revoked by a resolved person.
  revoked_by_principal UUID,
  revoked_by_via TEXT NOT NULL DEFAULT '',
  revoked_why TEXT NOT NULL DEFAULT ''
);
-- Credentials and memberships belong to a principal; these views name it by
-- its account's name today. A credential whose principal has no account
-- (removed) is in none of them, so it authenticates nothing.
CREATE OR REPLACE VIEW sessions_named AS
  SELECT s.*, u.username FROM sessions s JOIN users u ON u.principal_id = s.principal_id;
CREATE OR REPLACE VIEW auth_tokens_named AS
  SELECT t.*, u.username FROM auth_tokens t JOIN users u ON u.principal_id = t.principal_id;
CREATE OR REPLACE VIEW oauth_codes_named AS
  SELECT c.*, u.username FROM oauth_codes c JOIN users u ON u.principal_id = c.principal_id;
CREATE OR REPLACE VIEW delegations_named AS
  SELECT d.*, u.username FROM delegations d JOIN users u ON u.principal_id = d.principal_id;
CREATE OR REPLACE VIEW object_capabilities_named AS
  SELECT o.*, u.username FROM object_capabilities o JOIN users u ON u.principal_id = o.principal_id;
CREATE OR REPLACE VIEW group_members_named AS
  SELECT m.*, u.username FROM group_members m JOIN users u ON u.principal_id = m.principal_id;
-- A grant to a person names their principal; read, it names the account
-- (a removed principal's grants are revoked with it).
CREATE OR REPLACE VIEW grants_named AS
  SELECT g.id, g.subject_type,
    (CASE WHEN g.subject_type = 'user' THEN COALESCE(u.username, g.subject) ELSE g.subject END)::text AS subject,
    g.role_name, g.capability, g.scope_type, g.scope_id, g.origin, g.granted_by, g.granted_by_principal, g.granted_by_via, g.reason,
    g.created_at, g.expires_at, g.review_at, g.revoked_at
  FROM grants g LEFT JOIN users u ON g.subject_type = 'user' AND u.principal_id::text = g.subject;
