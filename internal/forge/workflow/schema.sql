CREATE TABLE IF NOT EXISTS changes (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  -- The change's number within its project, for people: "egg-catch#12".
  -- Allocated in the creating transaction from project_numbers; never an
  -- identity (design/decisions/mergeable-forge-data.md).
  number       BIGINT NOT NULL,
  type         TEXT NOT NULL,
  title        TEXT NOT NULL,
  body         TEXT NOT NULL DEFAULT '',
  run_id UUID,
  state        TEXT NOT NULL DEFAULT 'in_progress',
  branch       TEXT NOT NULL DEFAULT '',
  base_sha     TEXT NOT NULL DEFAULT '',
  landed_sha   TEXT NOT NULL DEFAULT '', -- the commit its landing merged as; '' until it lands
  external_ref TEXT NOT NULL DEFAULT '',
  summary      TEXT NOT NULL DEFAULT '',
  provenance   TEXT NOT NULL DEFAULT '',
  title_ownership TEXT NOT NULL DEFAULT 'human' CONSTRAINT change_title_ownership CHECK(title_ownership IN ('human','awaiting_chat','generated')),
  created_at   TIMESTAMPTZ NOT NULL,
  closed_at    TIMESTAMPTZ,
  target_adoption_id UUID, -- an uplift's target revision (ways-of-working-versions.md §11)
  acceptance_json    JSONB NOT NULL DEFAULT '{}'::jsonb, -- what the change must prove, structurally
  expected_revision  BIGINT NOT NULL DEFAULT 0,        -- the revision the target was prepared from
  adopted_revision   BIGINT NOT NULL DEFAULT 0,        -- set when the landing moved the project
  target_candidate_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  -- Advances with every human edit of the title, body or external reference,
  -- so an edit started from an older reading is refused, not applied.
  edit_revision      BIGINT NOT NULL DEFAULT 0,
  -- Who opened the change: the actor of its change.created receipt, written
  -- with it, so a change read need not search the ledger. '' when unknown.
  opened_by          TEXT NOT NULL DEFAULT '',
  -- The opener's principal and how they acted (access.Actor); NULL and ''
  -- when the opener was not resolved to a person.
  opened_by_principal UUID,
  opened_by_via      TEXT NOT NULL DEFAULT '',
  -- Whether the change does what was asked, as the narrative writer read the
  -- request against the work, with the shortfall in words when it does not.
  -- Written with the summary; 'unknown' until a narrative says.
  request_met        TEXT NOT NULL DEFAULT 'unknown' CONSTRAINT change_request_met CHECK(request_met IN ('yes','partly','no','unknown')),
  request_gap        TEXT NOT NULL DEFAULT '',
  -- How a closed change was closed, when that is more than its state: the
  -- engine's nothing-to-change close (an abandoned change whose run found
  -- nothing to change) is 'nothing-to-change'; '' otherwise. Only the
  -- engine writes it. Last, where migration 148 added it.
  closed_as          TEXT NOT NULL DEFAULT '' CONSTRAINT change_closed_as CHECK(closed_as IN ('','nothing-to-change','shipped-with')),
  UNIQUE(project_id, number)
);
-- The last number given to a change and to a run in each project. A
-- creating transaction takes the next under this row's lock, so numbers are
-- allocated in commit order within a project.
CREATE TABLE IF NOT EXISTS project_numbers (
  project_id UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  changes    BIGINT NOT NULL DEFAULT 0,
  runs       BIGINT NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS runs (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  number           BIGINT NOT NULL, -- within its project, as a change's

  workflow_name    TEXT NOT NULL,
  workflow_version BIGINT NOT NULL,
  kind             TEXT NOT NULL,
  status           TEXT NOT NULL DEFAULT 'running',
  current_stage    TEXT NOT NULL,
  definition_json  JSONB NOT NULL,
  change_id UUID,
  workflow_origin  TEXT NOT NULL DEFAULT '',
  parent_run_id UUID,
  parent_step_id UUID,
  parent_step_attempt BIGINT NOT NULL DEFAULT 0,
  inputs_json      JSONB NOT NULL DEFAULT '{}'::jsonb,
  retry_revision   BIGINT NOT NULL DEFAULT 0,
  base_sha         TEXT NOT NULL DEFAULT '',
  created_at       TIMESTAMPTZ NOT NULL,
  updated_at       TIMESTAMPTZ NOT NULL,
  adoption_id UUID, -- the adoption revision this run reads
  -- Who admitted this root run while holding change:land on its change, ''
  -- when nobody did. One of the two facts a merge or deploy may rest on
  -- (assent.go); the other is a person's approval recorded in the run.
  landing_authority TEXT NOT NULL DEFAULT '',
  -- The landing authority's principal and way of acting (access.Actor); NULL for none, an eval's authority, or one not resolved to a person.
  landing_authority_principal UUID,
  landing_authority_via TEXT NOT NULL DEFAULT '',
  UNIQUE(project_id, number)
);
-- Only new runs have a start record. Legacy runs retain their recovery rules.
CREATE TABLE IF NOT EXISTS run_starts (
  run_id UUID PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
  ready BOOLEAN NOT NULL DEFAULT false
);
-- The landing created from a particular accepted working run. Linking it in
-- the run-creation transaction makes retries after a crash reuse that run.
-- A send-back gets a new completion identity; old landing jobs cannot ship it.
CREATE TABLE IF NOT EXISTS workflow_continuations (
  source_run_id UUID PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
  run_id UUID NOT NULL UNIQUE REFERENCES runs(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS workflow_landings (
  work_run_id UUID PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
  landing_run_id UUID UNIQUE REFERENCES runs(id) ON DELETE CASCADE,
  acceptance_applied BOOLEAN NOT NULL DEFAULT false
);
CREATE TABLE IF NOT EXISTS steps (
  attempt BIGINT NOT NULL DEFAULT 0,
  id UUID PRIMARY KEY,
  run_id UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  stage           TEXT NOT NULL,
  name            TEXT NOT NULL,
  type            TEXT NOT NULL,
  config_json     JSONB NOT NULL DEFAULT '{}'::jsonb CONSTRAINT steps_config_object CHECK (jsonb_typeof(config_json)='object'),
  status          TEXT NOT NULL DEFAULT 'pending',
  evidence_json   JSONB NOT NULL DEFAULT '{}'::jsonb,
  override_by     TEXT NOT NULL DEFAULT '',
  override_by_principal UUID,
  override_by_via TEXT NOT NULL DEFAULT '',
  override_reason TEXT NOT NULL DEFAULT '',
  started_at      TIMESTAMPTZ,
  finished_at     TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS run_events (
  id UUID PRIMARY KEY,
  run_id UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  at           TIMESTAMPTZ NOT NULL,
  kind         TEXT NOT NULL,
  payload_json JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE TABLE IF NOT EXISTS artifacts (
  id UUID PRIMARY KEY,
  run_id UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL,
  name       TEXT NOT NULL DEFAULT '',
  ref        TEXT NOT NULL DEFAULT '',
  body       TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL,
  -- body_bytes holds a body that is not text (a file an agent produced,
  -- a log with raw bytes) faithfully; body is then empty. Never both.
  body_bytes BYTEA
);
-- A project's scheduled workflow: when it last fired and what that firing
-- did. Arming a schedule seen for the first time writes the same time as a
-- firing, so last_outcome says which it was: armed (never fired yet),
-- opened (last_change_id is the change it opened), skipped (its previous
-- run was still active), failed (its run could not be started), or unknown
-- (recorded before outcomes were kept, schema 144).
CREATE TABLE IF NOT EXISTS workflow_schedules (
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  workflow_name TEXT NOT NULL,
  last_fired_at TIMESTAMPTZ NOT NULL,
  last_outcome TEXT NOT NULL DEFAULT 'unknown',
  last_change_id UUID,
  PRIMARY KEY (project_id, workflow_name),
  CONSTRAINT workflow_schedule_outcome CHECK (last_outcome IN ('armed','opened','skipped','failed','unknown')
    AND (last_outcome = 'opened') = (last_change_id IS NOT NULL))
);
CREATE TABLE IF NOT EXISTS run_inference_owners (
  run_id UUID PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
  actor TEXT NOT NULL,
  -- The owner's principal and way of acting (access.Actor); NULL for AppTown, a run, or one not resolved to a person.
  actor_principal UUID,
  actor_via TEXT NOT NULL DEFAULT ''
);
-- An agent attempt's kept history: the digest of its session's transcript,
-- stored immutably under dataDir/transcripts, or why it was not kept. One row
-- per invocation of the attempt, so every stored file keeps its reference;
-- the attempt's history is its latest invocation's. The attempt's start
-- receipt names the session.
CREATE TABLE IF NOT EXISTS agent_transcripts (
  run_id UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  step_id UUID NOT NULL REFERENCES steps(id) ON DELETE CASCADE,
  attempt BIGINT NOT NULL,
  session_id TEXT NOT NULL,
  invocation BIGINT NOT NULL,
  digest TEXT NOT NULL DEFAULT '',
  unavailable TEXT NOT NULL DEFAULT '',
  recorded_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (step_id, attempt, invocation),
  CONSTRAINT agent_transcript_tuple CHECK (attempt>0 AND invocation>0 AND session_id<>'' AND ((digest<>'') <> (unavailable<>'')))
);
CREATE INDEX IF NOT EXISTS agent_transcripts_digest ON agent_transcripts(digest) WHERE digest <> '';

-- Project summaries and run-scoped step reads must not rescan all history.
CREATE INDEX IF NOT EXISTS changes_project ON changes(project_id);
CREATE INDEX IF NOT EXISTS runs_project ON runs(project_id);
CREATE INDEX IF NOT EXISTS steps_run_stage ON steps(run_id, stage);

-- Dashboard active work is bounded by recency, independently of project totals.
CREATE INDEX IF NOT EXISTS runs_status_updated ON runs(status, updated_at DESC, id DESC);
