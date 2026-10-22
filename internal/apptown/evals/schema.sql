-- Evals: is a model or a pack version better or worse? An eval re-runs
-- finished Changes in throwaway copies of their projects, one arm per
-- setup, and compares the arms.

-- One eval, as its starter started it. The meaning is what the starter
-- was shown and agreed to; the subject is the one thing that varies.
CREATE TABLE IF NOT EXISTS evals (
  id UUID PRIMARY KEY,
  -- How people name it ("eval 9"): its number within AppTown, from
  -- eval_numbers, never reused.
  number BIGINT NOT NULL,
  started_by    TEXT NOT NULL,
  -- The starter's principal and way of acting (access.Actor); NULL when
  -- the starter was not resolved to a person.
  started_by_principal UUID,
  started_by_via TEXT NOT NULL DEFAULT '',
  started_at    TIMESTAMPTZ NOT NULL,
  meaning       TEXT NOT NULL,
  subject_kind  TEXT NOT NULL,
  subject       TEXT NOT NULL,
  budget_micros BIGINT NOT NULL,
  state         TEXT NOT NULL DEFAULT 'running',
  reason        TEXT NOT NULL DEFAULT '',
  -- The incomplete spend observations the starter has said to continue
  -- past: one more holds the eval again.
  incomplete_seen BIGINT NOT NULL DEFAULT 0,
  finished_at   TIMESTAMPTZ,
  deleted_at    TIMESTAMPTZ,
  -- subject_digest names exactly what a pack-version eval tested: its
  -- resolved spec and pins at start.
  subject_digest TEXT NOT NULL DEFAULT ''
);

-- One arm: one Change re-run under one setup in its own copy. models_json
-- is the slot → model the arm runs, read when the eval started: what the
-- AppTown may call for its copy.
CREATE TABLE IF NOT EXISTS eval_arms (
  id UUID PRIMARY KEY,
  eval_id UUID NOT NULL REFERENCES evals(id),
  source_project_id UUID NOT NULL,
  source_change_id UUID NOT NULL,
  arm               TEXT NOT NULL,
  profile           TEXT NOT NULL,
  models_json       JSONB NOT NULL DEFAULT '{}'::jsonb,
  state             TEXT NOT NULL DEFAULT 'queued',
  reason            TEXT NOT NULL DEFAULT '',
  project_id UUID,
  change_id UUID,
  run_id UUID,
  landing_run_id UUID,
  started_at        TIMESTAMPTZ,
  ready_at          TIMESTAMPTZ,
  finished_at       TIMESTAMPTZ,
  score_json        JSONB NOT NULL DEFAULT '{}'::jsonb,
  UNIQUE(eval_id, source_change_id, arm)
);

-- The copy record: which eval and arm made a copy (whether a project is a
-- copy at all is Project's purpose). Written with the copy, in its creation
-- transaction, and never edited or deleted, not even with the copy or its
-- eval (project ids are never reused), so complaints and spend stay
-- attributed after both are gone.
CREATE TABLE IF NOT EXISTS eval_copies (
  project_id UUID PRIMARY KEY,
  eval_id UUID NOT NULL,
  arm_id UUID NOT NULL,
  arm        TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS evals_number ON evals(number);
-- The last number given to one of evals: the next is one more, whatever was
-- deleted since, so a number never names a second record.
CREATE TABLE IF NOT EXISTS eval_numbers (
  id   BOOLEAN PRIMARY KEY DEFAULT true CONSTRAINT eval_numbers_one CHECK (id),
  last BIGINT NOT NULL
);
