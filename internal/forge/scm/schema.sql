-- SCM owns merge operations; absence is idle, never an incomplete operation.
CREATE TABLE IF NOT EXISTS run_merges (
  run_id UUID PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
  state TEXT NOT NULL,
  operation TEXT NOT NULL,
  expected TEXT NOT NULL,
  result TEXT NOT NULL DEFAULT '',
  CONSTRAINT run_merge_tuple CHECK (operation<>'' AND expected<>'' AND
    ((state='merging' AND result='') OR (state='merged' AND result<>'')))
);
CREATE INDEX IF NOT EXISTS run_merges_pending ON run_merges(run_id) WHERE state='merging';

-- SCM owns who may move a project's branch. The generation increases at every
-- handover or replaced attempt; a publisher presents the generation it was
-- admitted under. Absence is generation 0 with no holder.
CREATE TABLE IF NOT EXISTS branch_authority (
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  branch TEXT NOT NULL,
  generation BIGINT NOT NULL,
  holder TEXT NOT NULL,
  PRIMARY KEY (project_id, branch),
  CONSTRAINT branch_authority_tuple CHECK (branch<>'' AND generation>0 AND holder<>'')
);

-- A branch publication admitted but not yet settled. Settled publications are
-- deleted; a row is either in flight or unknown after a crash, and blocks the
-- branch's next handover until it is settled.
CREATE TABLE IF NOT EXISTS branch_publications (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  branch TEXT NOT NULL,
  repository TEXT NOT NULL,
  generation BIGINT NOT NULL,
  expected TEXT NOT NULL,
  proposed TEXT NOT NULL,
  state TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  CONSTRAINT branch_publication_tuple CHECK (branch<>'' AND repository<>'' AND proposed<>'' AND state IN ('pending','unknown'))
);
CREATE UNIQUE INDEX IF NOT EXISTS branch_publications_one ON branch_publications(project_id, branch);

-- Where a project's main stood when the forge last moved it: the commit read
-- back from the hub under the repository lock, recorded in the transaction
-- that settled the move. It exists and is an ancestor of (or equal to) the
-- hub's main unless main was rewritten outside the forge. via names the door
-- (landing, init, seed, copy, genesis, integrity-adopt, backfill); run_id is
-- the landing run, 0 for other doors. Absence is unknown.
CREATE TABLE IF NOT EXISTS project_main (
  project_id UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  branch TEXT NOT NULL,
  sha TEXT NOT NULL,
  via TEXT NOT NULL,
  run_id UUID,
  recorded_at TIMESTAMPTZ NOT NULL,
  CONSTRAINT project_main_tuple CHECK (branch<>'' AND sha<>'' AND via IN ('landing','init','seed','copy','genesis','integrity-adopt','backfill'))
);
