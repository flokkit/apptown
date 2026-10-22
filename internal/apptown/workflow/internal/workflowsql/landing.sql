-- Landings: handoffs, continuations and merges.

-- name: StartedLanding :one
SELECT r.* FROM workflow_landings l JOIN runs w ON w.id = l.work_run_id
JOIN runs r ON r.id = l.landing_run_id
WHERE l.work_run_id = $1 AND w.project_id = $2 AND w.change_id IS NOT DISTINCT FROM $3;

-- name: LandingForUpdate :one
SELECT * FROM workflow_landings WHERE work_run_id=$1 FOR UPDATE;

-- name: ContinuationLink :execrows
INSERT INTO workflow_continuations(source_run_id,run_id) VALUES ($1,$2);

-- name: AttachAcceptedLanding :execrows
UPDATE workflow_landings SET landing_run_id=sqlc.arg(landing_run_id) WHERE work_run_id=sqlc.arg(work_run_id);

-- name: EnsureLanding :one
WITH inserted AS (
  INSERT INTO workflow_landings (work_run_id) VALUES ($1) ON CONFLICT DO NOTHING RETURNING work_run_id
)
SELECT work_run_id FROM inserted
UNION ALL SELECT work_run_id FROM workflow_landings WHERE work_run_id=$1 LIMIT 1;

-- name: LandingAcceptedWork :one
SELECT work_run_id FROM workflow_landings WHERE landing_run_id IS NOT DISTINCT FROM $1 AND acceptance_applied;

-- name: LandingMarkApplied :execrows
UPDATE workflow_landings SET acceptance_applied=true WHERE work_run_id=sqlc.arg(work_run_id) AND landing_run_id IS NOT DISTINCT FROM sqlc.arg(landing_run_id) AND NOT acceptance_applied;

-- name: ReserveLanding :exec
INSERT INTO workflow_landings(work_run_id) VALUES ($1) ON CONFLICT DO NOTHING;

-- name: LandingPending :one
SELECT l.landing_run_id AS landing_id,EXISTS(SELECT 1 FROM workflow_continuations WHERE source_run_id=r.id) AS superseded
FROM runs r LEFT JOIN workflow_landings l ON l.work_run_id=r.id WHERE r.id=$1;

-- A landing started from accepted work that has not yet taken the acceptance.
-- name: LandingAwaitingAcceptance :one
SELECT EXISTS(SELECT 1 FROM workflow_landings WHERE landing_run_id IS NOT DISTINCT FROM $1 AND NOT acceptance_applied);

-- Accepted work whose landing is reserved but not yet started, while its change
-- still waits to land.
-- name: LandingQueued :one
SELECT EXISTS(SELECT 1 FROM workflow_landings l JOIN runs r ON r.id=l.work_run_id JOIN changes c ON c.id=r.change_id
WHERE l.work_run_id=sqlc.arg(work_run_id) AND l.landing_run_id IS NULL AND c.state=sqlc.arg(change_state)::text
AND NOT EXISTS(SELECT 1 FROM workflow_continuations WHERE source_run_id=l.work_run_id));

-- name: LandingSuperseded :one
SELECT EXISTS(SELECT 1 FROM workflow_continuations WHERE source_run_id=$1);

-- name: WasRunSentBack :one
SELECT EXISTS(SELECT 1 FROM workflow_continuations WHERE source_run_id=$1);

-- name: ExportContinuations :many
SELECT to_jsonb(w)::text FROM workflow_continuations w JOIN runs r ON r.id = w.source_run_id
WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped')) AND w.run_id IN (SELECT r.id FROM runs r WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped'))) ORDER BY w.source_run_id;

-- name: ExportLandings :many
SELECT to_jsonb(w)::text FROM workflow_landings w JOIN runs r ON r.id = w.work_run_id
WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped')) AND (w.landing_run_id IS NULL OR w.landing_run_id IN (SELECT r.id FROM runs r WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped')))) ORDER BY w.work_run_id;

-- The commits moving landings merged into the default branch: a read of
-- SCM's merge record, never a write. Each must be on the bundled default
-- branch.
-- name: ExportLandedCommits :many
SELECT m.result FROM run_merges m JOIN runs r ON r.id = m.run_id
WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped')) AND m.state = 'merged' AND m.result <> '' ORDER BY m.result;
