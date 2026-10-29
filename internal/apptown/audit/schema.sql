CREATE TABLE IF NOT EXISTS audit_log (
  id UUID PRIMARY KEY,
  at           TIMESTAMPTZ NOT NULL,
  actor        TEXT NOT NULL DEFAULT '',
  -- Who answers for the line and how they acted (access.Actor): the actor
  -- name above is as shown when it was written.
  actor_principal UUID,
  actor_via    TEXT NOT NULL DEFAULT '',
  project_id UUID,
  kind         TEXT NOT NULL,
  subject      TEXT NOT NULL DEFAULT '',
  payload_json JSONB NOT NULL DEFAULT '{}'::jsonb
);
