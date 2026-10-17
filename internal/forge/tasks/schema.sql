-- Tasks owns execution attempts: a step attempt's task is recorded before any
-- provider effect, its provider identity before it starts, and its outcome
-- before the step settles. A settled row is kept as the attempt's receipt.
CREATE TABLE IF NOT EXISTS task_attempts (
  operation TEXT PRIMARY KEY,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  run_id UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  step_id UUID NOT NULL REFERENCES steps(id) ON DELETE CASCADE,
  attempt BIGINT NOT NULL,
  backend TEXT NOT NULL,
  container TEXT NOT NULL,
  digest TEXT NOT NULL,
  -- source is the commit a remote task was given; empty for a task that
  -- mounts the forge's workspace.
  source TEXT NOT NULL DEFAULT '',
  -- admission is the task's admission as admitted (profile, limits, network,
  -- expiry, as JSON); deadline is when its wall-clock budget ends. A
  -- recovered task keeps both.
  admission TEXT NOT NULL DEFAULT '',
  deadline TIMESTAMPTZ,
  -- invocation names one run of a program within the step attempt: a
  -- script's attempt has one, 'script'; an agent's attempt may have several
  -- in one workspace. workspace names the backend workspace the task shares
  -- with the attempt's other invocations; empty when the task owns its own.
  invocation TEXT NOT NULL DEFAULT 'script',
  workspace TEXT NOT NULL DEFAULT '',
  provider_id TEXT NOT NULL DEFAULT '',
  desired TEXT NOT NULL DEFAULT 'run',
  observed TEXT NOT NULL DEFAULT 'reserved',
  exit_code INT,
  -- timed_out: the decided outcome is a timeout (its exit code -1). Kept
  -- once recorded, through settlement.
  timed_out BOOLEAN NOT NULL DEFAULT false,
  -- detail explains the last transition to people; nothing decides by it.
  detail TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  -- released_at is when the backend confirmed the settled task released
  -- (or that nothing of it was ever held there). A settled row without it
  -- is released again until the backend confirms.
  released_at TIMESTAMPTZ,
  -- workspace_released_at is when the backend confirmed the shared
  -- workspace released, recorded on every row naming it. A workspace is
  -- released only once each of its tasks is settled and released.
  workspace_released_at TIMESTAMPTZ,
  CONSTRAINT task_attempt_tuple CHECK (operation<>'' AND backend<>'' AND container<>'' AND digest<>'' AND attempt>0 AND invocation<>''
    AND desired IN ('run','stop') AND observed IN ('reserved','started','collecting','settled','unknown','refused','held')
    AND (NOT timed_out OR exit_code IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS task_attempts_step_invocation ON task_attempts(step_id, attempt, invocation);
CREATE INDEX IF NOT EXISTS task_attempts_unsettled ON task_attempts(step_id) WHERE observed <> 'settled';
CREATE INDEX IF NOT EXISTS task_attempts_unreleased ON task_attempts(updated_at) WHERE observed = 'settled' AND released_at IS NULL;
CREATE INDEX IF NOT EXISTS task_attempts_workspace ON task_attempts(workspace) WHERE workspace <> '' AND workspace_released_at IS NULL;
