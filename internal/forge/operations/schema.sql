-- Operations (design/subsystems/jobs.md): one row per long
-- action someone asked for. Who asked, with which credential (a reference,
-- never a secret), on which subject, what it holds, which steps happened
-- and how it ended. No cascade from the subject: a receipt outlives what
-- it describes. The id is BY DEFAULT so the cutover from
-- deployment_operations kept every legacy id.
CREATE TABLE IF NOT EXISTS operations (
  id UUID PRIMARY KEY,
  -- How people name it ("operation 9"): its number within the forge, from
  -- operation_numbers, never reused.
  number BIGINT NOT NULL,
  kind             TEXT NOT NULL,
  project_id UUID,
  subject          TEXT,                                -- a hold path; null for unattributed history
  holds            JSONB NOT NULL DEFAULT '[]'::jsonb,  -- every path held while queued or running
  asked_by         TEXT NOT NULL,
  -- The asker's principal (access.Actor); NULL for the forge, or one not
  -- resolved to a person.
  asked_by_principal UUID,
  authority        JSONB NOT NULL DEFAULT '{}'::jsonb,  -- principal, credential reference, captured bounds
  input            JSONB NOT NULL DEFAULT '{}'::jsonb,  -- what was asked, fixed at admission
  state            TEXT NOT NULL CONSTRAINT operation_state CHECK (state IN ('queued','running','succeeded','failed','unknown','cancelled')),
  steps            JSONB NOT NULL DEFAULT '[]'::jsonb,
  result           JSONB NOT NULL DEFAULT '{}'::jsonb,
  detail           TEXT NOT NULL DEFAULT '',
  cancel_requested BOOLEAN NOT NULL DEFAULT false,
  job_id           BIGINT NOT NULL DEFAULT 0,           -- the River job, for diagnosis; never authority
  created_at       TIMESTAMPTZ NOT NULL,
  started_at       TIMESTAMPTZ,
  touched_at       TIMESTAMPTZ NOT NULL,                -- last persisted update, not a health claim
  finished_at      TIMESTAMPTZ,
  dismissed_at     TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS operations_live ON operations (id) WHERE state IN ('queued','running');
CREATE INDEX IF NOT EXISTS operations_subject ON operations (subject, id);
CREATE INDEX IF NOT EXISTS operations_asked_by ON operations (asked_by, id) WHERE dismissed_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS operations_number ON operations(number);
-- The last number given to one of operations: the next is one more, whatever was
-- deleted since, so a number never names a second record.
CREATE TABLE IF NOT EXISTS operation_numbers (
  id   BOOLEAN PRIMARY KEY DEFAULT true CONSTRAINT operation_numbers_one CHECK (id),
  last BIGINT NOT NULL
);
