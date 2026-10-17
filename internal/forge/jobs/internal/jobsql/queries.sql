-- Queue projections and exclusive-startup interruption settlement.
-- All normal queue state changes remain River API calls.
-- Metadata reports deliberately omit potentially large job arguments and errors.
-- name: RecentOperationsMeta :many
SELECT id, (args->>'operation')::text AS operation, (args->>'key')::text AS key, state::text AS state, attempt,
 COALESCE(errors[array_length(errors,1)]->>'error','')::text AS error,
 COALESCE(metadata->'output','{}'::jsonb)::jsonb AS receipt,
 created_at,attempted_at,finalized_at,scheduled_at
FROM river_job WHERE kind='forge.operation'
ORDER BY (state IN ('available','running','retryable','scheduled','pending')) DESC,id DESC LIMIT $1;

-- name: RunOperationsForUpdate :many
SELECT id,state::text AS state FROM river_job
WHERE kind=sqlc.arg(kind) AND args->>'operation'=sqlc.arg(operation)::text AND args->'values'->>'runId'=sqlc.arg(run_id)::text
ORDER BY id FOR UPDATE;

-- name: UnfinishedKeys :many
SELECT (args->>'key')::text AS key FROM river_job
WHERE args->>'operation'=$1::text AND state NOT IN ('completed','cancelled','discarded');

-- name: IncompleteKeys :many
SELECT (args->>'key')::text AS key FROM river_job
WHERE args->>'operation'=$1::text AND state <> 'completed';

-- name: InterruptedOperationsForUpdate :many
SELECT id,args,attempt,
 COALESCE(attempted_by[array_length(attempted_by,1)],'')::text AS previous_client,
 (metadata ? 'cancel_attempted_at')::boolean AS cancel_requested
FROM river_job
WHERE kind='forge.operation' AND state='running'
 AND attempted_by[array_length(attempted_by,1)] IS DISTINCT FROM sqlc.arg(current_client)::text
ORDER BY id FOR UPDATE;

-- name: SettleInterruptedOperation :many
UPDATE river_job SET
 state=sqlc.arg(outcome)::river_job_state,
 attempt=CASE WHEN sqlc.arg(outcome)::river_job_state='available' THEN GREATEST(attempt-1,0) ELSE attempt END,
 scheduled_at=CASE WHEN sqlc.arg(outcome)::river_job_state='available' THEN sqlc.arg(settled_at)::timestamptz ELSE scheduled_at END,
 finalized_at=CASE WHEN sqlc.arg(outcome)::river_job_state='cancelled' THEN sqlc.arg(settled_at)::timestamptz ELSE NULL END,
 errors=CASE WHEN sqlc.arg(append_error)::boolean THEN array_append(errors,sqlc.arg(error_json)::jsonb) ELSE errors END
WHERE id=sqlc.arg(id) AND kind='forge.operation' AND state='running'
 AND attempted_by[array_length(attempted_by,1)]=sqlc.arg(previous_client)::text
 AND attempted_by[array_length(attempted_by,1)]<>sqlc.arg(current_client)::text
RETURNING id;
