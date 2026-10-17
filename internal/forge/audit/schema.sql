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
  payload_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  authorization_decision_id UUID,
  -- The work the line names, typed (audit.Ref): derived by the writer from
  -- the subject, payload and actor, so nothing parses the text afterwards.
  refs         JSONB NOT NULL DEFAULT '[]'::jsonb
);

-- Former identities (design/decisions/mergeable-forge-data.md): every record
-- converted at the identity cutover keeps its old identity here as a fact of
-- history, so an old link, a bare old number or an old chat id resolves to
-- the record's UUID. old is text: integers and 12-hex chat ids alike.
CREATE TABLE IF NOT EXISTS former_ids (
  forge UUID NOT NULL,
  kind  TEXT NOT NULL,
  old   TEXT NOT NULL,
  id    UUID NOT NULL,
  PRIMARY KEY (forge, kind, old)
);
CREATE INDEX IF NOT EXISTS former_ids_id ON former_ids(id);
