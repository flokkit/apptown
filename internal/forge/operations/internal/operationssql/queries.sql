-- Every admission and every hold check serialises on one advisory lock,
-- taken after the owner's own row locks (the plan's locking order).
-- name: LockAdmission :exec
SELECT pg_advisory_xact_lock(hashtext('operations-admission'));

-- A live operation holding a path that is, or is an ancestor or a
-- descendant of, one of the given paths (segment ancestry, so project:7
-- never meets project:70).
-- name: LiveHolder :one
SELECT * FROM operations o
  WHERE o.state IN ('queued','running')
    AND EXISTS (SELECT 1 FROM jsonb_array_elements_text(o.holds) h, jsonb_array_elements_text(sqlc.arg(paths)::jsonb) w
                WHERE h = w OR starts_with(h, w || '/') OR starts_with(w, h || '/'))
  ORDER BY o.id LIMIT 1;

-- name: NextOperationNumber :one
-- The next number, in the creating transaction: the row's lock orders
-- concurrent creations.
INSERT INTO operation_numbers (id, last) VALUES (true, 1)
ON CONFLICT (id) DO UPDATE SET last = operation_numbers.last + 1
RETURNING last;

-- name: InsertOperation :one
INSERT INTO operations (id, number, kind, project_id, subject, holds, asked_by, asked_by_principal, authority, input, state, created_at, touched_at)
  VALUES (sqlc.arg(id), sqlc.arg(number), sqlc.arg(kind), sqlc.arg(project_id), sqlc.narg(subject), sqlc.arg(holds), sqlc.arg(asked_by), sqlc.narg(asked_by_principal), sqlc.arg(authority), sqlc.arg(input), 'queued',
          sqlc.arg(at), sqlc.arg(at))
  RETURNING *;

-- name: SetOperationJob :execrows
UPDATE operations SET job_id=sqlc.arg(job_id) WHERE id=sqlc.arg(id);

-- name: OperationByID :one
SELECT * FROM operations WHERE id=sqlc.arg(id);

-- Duplicate delivery never executes a running or ended operation: only a
-- queued one is claimed, once.
-- name: ClaimOperation :one
UPDATE operations SET state='running', started_at=sqlc.arg(at)::timestamptz, touched_at=sqlc.arg(at)::timestamptz
  WHERE id=sqlc.arg(id) AND state='queued' RETURNING *;

-- name: RecordOperationSteps :execrows
UPDATE operations SET steps=sqlc.arg(steps), touched_at=sqlc.arg(at) WHERE id=sqlc.arg(id) AND state='running';

-- name: RecordOperationResult :execrows
UPDATE operations SET result=sqlc.arg(result), touched_at=sqlc.arg(at) WHERE id=sqlc.arg(id) AND state='running';

-- name: FinishOperation :execrows
UPDATE operations SET state=sqlc.arg(state), steps=sqlc.arg(steps), result=sqlc.arg(result), detail=sqlc.arg(detail),
    touched_at=sqlc.arg(at), finished_at=sqlc.arg(at)
  WHERE id=sqlc.arg(id) AND state=sqlc.arg(from_state);

-- name: RequestOperationCancel :execrows
UPDATE operations SET cancel_requested=true, touched_at=sqlc.arg(at) WHERE id=sqlc.arg(id) AND state='running';

-- name: OperationsInStateForUpdate :many
SELECT * FROM operations WHERE state=sqlc.arg(state) ORDER BY id FOR UPDATE;

-- name: OperationsOnSubject :many
SELECT * FROM operations WHERE subject=sqlc.arg(subject) ORDER BY id DESC LIMIT sqlc.arg(row_limit)::bigint;

-- name: LiveOnSubject :one
SELECT * FROM operations WHERE subject=sqlc.arg(subject) AND state IN ('queued','running') ORDER BY id DESC LIMIT 1;

-- name: LatestEndedOfKind :one
SELECT * FROM operations WHERE subject=sqlc.arg(subject) AND kind=sqlc.arg(kind) AND state NOT IN ('queued','running')
  ORDER BY id DESC LIMIT 1;

-- name: EndedFor :many
SELECT * FROM operations WHERE (asked_by_principal = sqlc.narg(asked_by_principal) OR (asked_by_principal IS NULL AND asked_by=sqlc.arg(asked_by))) AND state NOT IN ('queued','running') AND dismissed_at IS NULL
  ORDER BY id DESC LIMIT 50;

-- name: DismissOperation :execrows
UPDATE operations SET dismissed_at=sqlc.arg(at)::timestamptz
  WHERE id=sqlc.arg(id) AND (asked_by_principal = sqlc.narg(asked_by_principal) OR (asked_by_principal IS NULL AND asked_by=sqlc.arg(asked_by))) AND state NOT IN ('queued','running') AND dismissed_at IS NULL;

-- name: OperationsUnderProject :many
SELECT * FROM operations WHERE project_id = sqlc.arg(project_id) AND subject IS NOT NULL
  AND (subject = sqlc.arg(root) OR starts_with(subject, sqlc.arg(root) || '/'))
ORDER BY id DESC LIMIT sqlc.arg(row_limit)::bigint;

-- The record people name by its number ("operation 9").
-- name: OperationIDByNumber :one
SELECT id FROM operations WHERE number = $1;

-- What a forge restart would interrupt: every running operation, oldest
-- first (forge executing).
-- name: RunningOperations :many
SELECT * FROM operations WHERE state = 'running' ORDER BY id;
