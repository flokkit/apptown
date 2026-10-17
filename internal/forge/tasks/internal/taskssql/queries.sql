-- name: ReserveAttempt :one
INSERT INTO task_attempts(operation,project_id,run_id,step_id,attempt,invocation,workspace,backend,container,digest,source,admission,deadline,created_at,updated_at)
VALUES (sqlc.arg(operation),sqlc.arg(project_id),sqlc.arg(run_id),sqlc.arg(step_id),sqlc.arg(attempt),sqlc.arg(invocation),sqlc.arg(workspace),sqlc.arg(backend),sqlc.arg(container),sqlc.arg(digest),sqlc.arg(source),sqlc.arg(admission),sqlc.arg(deadline),sqlc.arg(at),sqlc.arg(at))
ON CONFLICT (operation) DO UPDATE SET updated_at=task_attempts.updated_at
RETURNING *;

-- name: AttemptByOperation :one
SELECT * FROM task_attempts WHERE operation=$1;

-- name: RecordProvider :execrows
UPDATE task_attempts SET provider_id=sqlc.arg(provider_id), updated_at=sqlc.arg(at)
WHERE operation=sqlc.arg(operation) AND (provider_id='' OR provider_id=sqlc.arg(provider_id)) AND observed<>'settled';

-- Each transition below names the prior states it accepts. Settled is
-- final, held is final for the forge, refused only settles with nothing
-- held. A recorded exit code and timeout are never erased: a write given no
-- code keeps the recorded one, and nothing clears timed_out.

-- name: StartAttempt :execrows
UPDATE task_attempts SET observed='started', updated_at=sqlc.arg(at)
WHERE operation=sqlc.arg(operation) AND observed IN ('reserved','started','unknown');

-- A recorded timeout is never turned into an exit.
-- name: RecordExit :execrows
UPDATE task_attempts SET observed='collecting', exit_code=sqlc.arg(exit_code), updated_at=sqlc.arg(at)
WHERE operation=sqlc.arg(operation) AND observed IN ('reserved','started','unknown') AND NOT timed_out;

-- name: RecordTimeout :execrows
UPDATE task_attempts SET observed='collecting', exit_code=-1, timed_out=true, desired='stop', updated_at=sqlc.arg(at)
WHERE operation=sqlc.arg(operation) AND observed IN ('reserved','started','unknown');

-- name: RecordRefusal :execrows
UPDATE task_attempts SET observed='refused', detail=sqlc.arg(detail), updated_at=sqlc.arg(at)
WHERE operation=sqlc.arg(operation) AND observed IN ('reserved','started','unknown');

-- name: RecordUnknown :execrows
UPDATE task_attempts SET observed='unknown', detail=sqlc.arg(detail), updated_at=sqlc.arg(at)
WHERE operation=sqlc.arg(operation) AND observed IN ('reserved','started','collecting','unknown');

-- name: HoldAttempt :execrows
UPDATE task_attempts SET observed='held', detail=sqlc.arg(detail), updated_at=sqlc.arg(at)
WHERE operation=sqlc.arg(operation) AND observed IN ('reserved','started','collecting','unknown');

-- name: SettleAttempt :execrows
UPDATE task_attempts SET observed='settled', exit_code=COALESCE(sqlc.narg(exit_code), exit_code), detail=sqlc.arg(detail), updated_at=sqlc.arg(at)
WHERE operation=sqlc.arg(operation) AND observed IN ('reserved','started','collecting','unknown');

-- name: RequestStop :execrows
UPDATE task_attempts SET desired='stop', updated_at=sqlc.arg(at) WHERE operation=sqlc.arg(operation) AND observed<>'settled';

-- name: UnsettledAttempts :many
SELECT * FROM task_attempts WHERE observed<>'settled' ORDER BY created_at, operation;

-- name: HeldAttempts :many
SELECT * FROM task_attempts WHERE observed<>'settled' OR released_at IS NULL ORDER BY created_at, operation;

-- name: UnsettledSteps :many
SELECT DISTINCT step_id FROM task_attempts WHERE observed<>'settled' AND invocation<>sqlc.arg(not_continued) ORDER BY step_id;

-- name: UnsettledRuns :many
SELECT DISTINCT run_id FROM task_attempts WHERE observed<>'settled' ORDER BY run_id;

-- name: SettleNothingHeld :execrows
UPDATE task_attempts SET observed='settled', detail=sqlc.arg(detail), released_at=sqlc.arg(at), updated_at=sqlc.arg(at)
WHERE operation=sqlc.arg(operation) AND observed NOT IN ('settled','held');

-- name: ConfirmRelease :execrows
UPDATE task_attempts SET released_at=sqlc.arg(at), updated_at=sqlc.arg(at)
WHERE operation=sqlc.arg(operation) AND observed='settled' AND released_at IS NULL;

-- name: UnreleasedAttempts :many
SELECT * FROM task_attempts WHERE observed='settled' AND released_at IS NULL AND backend=sqlc.arg(backend) ORDER BY updated_at, operation LIMIT sqlc.arg(max_rows);

-- name: AttemptInvocations :many
SELECT * FROM task_attempts WHERE step_id=sqlc.arg(step_id) AND attempt=sqlc.arg(attempt) ORDER BY created_at, operation;

-- A workspace is ready for release once every task naming it is settled and
-- released, and it was not released yet.
-- name: UnreleasedWorkspaces :many
SELECT workspace::text AS workspace FROM task_attempts WHERE workspace<>'' AND backend=sqlc.arg(backend) AND workspace_released_at IS NULL
GROUP BY workspace HAVING bool_and(observed='settled' AND released_at IS NOT NULL)
ORDER BY workspace LIMIT sqlc.arg(max_rows);

-- name: ConfirmWorkspaceRelease :execrows
UPDATE task_attempts w SET workspace_released_at=sqlc.arg(at), updated_at=sqlc.arg(at)
WHERE w.workspace=sqlc.arg(workspace) AND w.backend=sqlc.arg(backend) AND w.workspace_released_at IS NULL
AND NOT EXISTS (SELECT 1 FROM task_attempts t WHERE t.workspace=sqlc.arg(workspace) AND t.backend=sqlc.arg(backend) AND (t.observed<>'settled' OR t.released_at IS NULL));

-- name: WorkspaceHeld :one
SELECT EXISTS(SELECT 1 FROM task_attempts WHERE workspace=sqlc.arg(workspace) AND backend=sqlc.arg(backend) AND (observed<>'settled' OR released_at IS NULL));
