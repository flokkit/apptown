-- Evals and their copies. Record-store style: whole rows by identity,
-- writes named for the operation.

-- name: RecordCopy :exec
INSERT INTO eval_copies (project_id, eval_id, arm_id, arm) VALUES ($1, $2, $3, $4);

-- name: CopyOf :one
SELECT * FROM eval_copies WHERE project_id = $1;

-- The models an arm runs, for the copy made for it.
-- name: ModelsOfCopy :one
SELECT a.models_json FROM eval_copies c JOIN eval_arms a ON a.id = c.arm_id WHERE c.project_id = $1;

-- name: NextEvalNumber :one
-- The next number, in the creating transaction: the row's lock orders
-- concurrent creations.
INSERT INTO eval_numbers (id, last) VALUES (true, 1)
ON CONFLICT (id) DO UPDATE SET last = eval_numbers.last + 1
RETURNING last;

-- name: InsertEval :one
INSERT INTO evals (id, number, started_by, started_by_principal, started_by_via, started_at, meaning, subject_kind, subject, budget_micros, subject_digest)
VALUES (sqlc.arg(id), sqlc.arg(number), sqlc.arg(started_by), sqlc.arg(started_by_principal), sqlc.arg(started_by_via), sqlc.arg(started_at), sqlc.arg(meaning), sqlc.arg(subject_kind), sqlc.arg(subject), sqlc.arg(budget_micros), sqlc.arg(subject_digest)) RETURNING *;

-- name: EvalByID :one
SELECT * FROM evals WHERE id = $1;

-- name: EvalForUpdate :one
SELECT * FROM evals WHERE id = $1 FOR UPDATE;

-- name: Evals :many
SELECT * FROM evals WHERE deleted_at IS NULL ORDER BY id DESC LIMIT $1;

-- name: RunningEvals :many
SELECT * FROM evals WHERE state IN ('running', 'held', 'stopping') AND deleted_at IS NULL ORDER BY id;

-- name: SetEvalState :one
UPDATE evals SET state = $2, reason = $3, finished_at = $4 WHERE id=$1 RETURNING *;

-- name: MarkEvalDeleted :one
UPDATE evals SET deleted_at = $2, state = CASE WHEN state IN ('running', 'held') THEN 'stopped' ELSE state END WHERE id=$1 AND deleted_at IS NULL RETURNING *;

-- name: InsertArm :one
INSERT INTO eval_arms (id, eval_id, source_project_id, source_change_id, arm, profile, models_json)
VALUES ($7, $1, $2, $3, $4, $5, $6) RETURNING *;

-- An eval's arms, with the source change's and the arm's own change's
-- numbers joined from Work's table for naming them; 0 when none or gone.
-- name: ArmsOfEval :many
SELECT sqlc.embed(a), COALESCE(sc.number, 0)::bigint AS source_change_number, COALESCE(ac.number, 0)::bigint AS change_number
FROM eval_arms a
LEFT JOIN changes sc ON sc.id = a.source_change_id AND sc.project_id = a.source_project_id
LEFT JOIN changes ac ON ac.id = a.change_id AND ac.project_id = a.project_id
WHERE a.eval_id = $1 ORDER BY a.id;

-- name: SaveArm :one
UPDATE eval_arms SET state = $2, reason = $3, project_id = $4, change_id=$5, run_id = $6, landing_run_id = $7,
  started_at = $8, ready_at = $9, finished_at = $10, score_json = $11
WHERE id=$1 RETURNING *;

-- name: CopiesOfEval :many
SELECT project_id FROM eval_copies WHERE eval_id = $1 ORDER BY project_id;

-- name: EvalCopyProjects :many
SELECT project_id FROM eval_copies ORDER BY project_id;

-- name: ContinueEval :one
UPDATE evals SET state = 'running', reason = '', incomplete_seen = $2 WHERE id=$1 RETURNING *;

-- name: HoldEval :one
UPDATE evals SET state = 'held', reason = $2 WHERE id=$1 AND state = 'running' RETURNING *;

-- name: CopyArmsOfEval :many
SELECT project_id, arm_id FROM eval_copies WHERE eval_id = $1 ORDER BY project_id;

-- The arm's copy is the arm's from the moment it is made.
-- name: ArmCopied :execrows
UPDATE eval_arms SET project_id = $2, state = 'copying' WHERE id=$1 AND state = 'queued';

-- name: StopEval :one
UPDATE evals SET state = 'stopping', reason = $2 WHERE id=$1 AND state IN ('running', 'held') RETURNING *;

-- name: EvalForShare :one
SELECT * FROM evals WHERE id = $1 FOR SHARE;

-- The newest eval of one subject, for "the newest eval of pack@N".
-- name: LatestEvalOf :one
SELECT * FROM evals WHERE subject_kind = $1 AND subject = $2 AND deleted_at IS NULL ORDER BY id DESC LIMIT 1;

-- name: LiveEvalsFromProject :many
SELECT DISTINCT a.eval_id FROM eval_arms a JOIN evals e ON e.id = a.eval_id
WHERE a.source_project_id = $1 AND e.state IN ('running', 'held', 'stopping') AND e.deleted_at IS NULL
  AND a.state NOT IN ('scored', 'failed')
ORDER BY a.eval_id;

-- The record people name by its number ("eval 9").
-- name: EvalIDByNumber :one
SELECT id FROM evals WHERE number = $1;
