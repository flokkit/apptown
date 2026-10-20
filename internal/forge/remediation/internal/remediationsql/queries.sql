-- Remediation (design/subsystems/remediation.md): the queue that fixes
-- findings, one governed Change per project at a time. Record-store style
-- (design/architecture/record-store-style.md): whole rows by identity, writes
-- named for the operation, the page's and the scheduler's reports last.

-- ===== worker pools ===================================================

-- name: PoolByKind :one
SELECT * FROM worker_pools WHERE kind = $1;

-- name: LockPool :one
SELECT * FROM worker_pools WHERE kind = $1 FOR UPDATE;

-- name: EnsurePool :exec
INSERT INTO worker_pools (kind, count, updated_at) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING;

-- name: UpsertPool :exec
INSERT INTO worker_pools (kind, count, updated_at) VALUES ($1, $2, $3)
ON CONFLICT (kind) DO UPDATE SET count = excluded.count, updated_at = excluded.updated_at;

-- ===== the job ========================================================

-- name: NextRemediationNumber :one
-- The next number, in the creating transaction: the row's lock orders
-- concurrent creations.
INSERT INTO remediation_numbers (id, last) VALUES (true, 1)
ON CONFLICT (id) DO UPDATE SET last = remediation_numbers.last + 1
RETURNING last;

-- name: InsertRemediation :one
INSERT INTO remediations (id, number, scope, scope_ref, mode, actor, actor_principal, actor_via, status, concurrency, retries, review_id, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(number), sqlc.arg(scope), sqlc.arg(scope_ref), sqlc.arg(mode), sqlc.arg(actor), sqlc.arg(actor_principal), sqlc.arg(actor_via), sqlc.arg(status), sqlc.arg(concurrency), sqlc.arg(retries), sqlc.arg(review_id), sqlc.arg(created_at), sqlc.arg(updated_at)) RETURNING *;

-- name: RemediationForUpdate :one
SELECT * FROM remediations WHERE id = $1 FOR UPDATE;

-- A status that ends the job stamps finished_at; any other status clears it,
-- because a job that is running again has not finished.

-- name: SetRemediationStatus :execrows
UPDATE remediations SET status = $2, updated_at = $4,
  finished_at = CASE WHEN $3::boolean THEN $4::timestamptz ELSE NULL END
WHERE id=$1;

-- name: ResumeRemediation :execrows
UPDATE remediations SET status = 'running', finished_at = NULL, updated_at = $4
WHERE id=$1 AND status IN ($2, $3);

-- name: RunningRemediationIDs :many
SELECT id FROM remediations WHERE status = 'running' ORDER BY id;

-- name: ActiveRemediationExists :one
SELECT EXISTS(SELECT 1 FROM remediations WHERE status IN ($1, $2));

-- Counts and cost come from the same snapshot as the job: the lateral
-- aggregate answers even for an empty job, without a query per job in Go.

-- name: RemediationSummary :one
SELECT sqlc.embed(m), totals.counts, totals.cost_micros FROM remediations m CROSS JOIN LATERAL (
  SELECT (COALESCE(jsonb_object_agg(c.state, c.n), '{}'::jsonb) || jsonb_build_object('total', COALESCE(SUM(c.n), 0)))::text AS counts,
         COALESCE(SUM(c.cost), 0)::bigint AS cost_micros
  FROM (SELECT i.state, COUNT(*) AS n, SUM(i.cost_micros) AS cost
        FROM remediation_items i WHERE i.remediation_id = m.id GROUP BY i.state) c
) totals WHERE m.id = $1;

-- name: RemediationSummaries :many
SELECT sqlc.embed(m), totals.counts, totals.cost_micros FROM remediations m CROSS JOIN LATERAL (
  SELECT (COALESCE(jsonb_object_agg(c.state, c.n), '{}'::jsonb) || jsonb_build_object('total', COALESCE(SUM(c.n), 0)))::text AS counts,
         COALESCE(SUM(c.cost), 0)::bigint AS cost_micros
  FROM (SELECT i.state, COUNT(*) AS n, SUM(i.cost_micros) AS cost
        FROM remediation_items i WHERE i.remediation_id = m.id GROUP BY i.state) c
) totals ORDER BY m.id DESC LIMIT $1;

-- ===== items ==========================================================

-- name: InsertItem :one
INSERT INTO remediation_items (id, remediation_id, project_id, project_slug, check_key, block, version, check_name,
  rank, state, origin_verdict_id, origin_sha, origin_evaluator_ver, origin_evidence_hash, origin_detail, updated_at)
VALUES ($16, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) RETURNING *;

-- name: ItemByID :one
SELECT * FROM remediation_items WHERE id = $1;

-- name: ItemByIDForUpdate :one
SELECT * FROM remediation_items WHERE id = $1 FOR UPDATE;

-- name: ItemForChange :one
SELECT * FROM remediation_items WHERE change_id IS NOT DISTINCT FROM $1 ORDER BY id DESC LIMIT 1;

-- Every state move bumps outcome_revision: a worker that has been overtaken
-- writes nothing, because the row it describes no longer exists.

-- name: RequeueItem :execrows
UPDATE remediation_items SET outcome_revision = outcome_revision + 1, state = 'queued', reason = $2,
  not_before = NULL, lease_worker = '', lease_until = NULL, updated_at = $3
WHERE id=$1;

-- name: SkipItem :execrows
UPDATE remediation_items SET outcome_revision = outcome_revision + 1, state = 'skipped', reason = $2, updated_at = $3
WHERE id=$1;

-- name: SkipQueuedItems :execrows
UPDATE remediation_items SET outcome_revision = outcome_revision + 1, state = 'skipped', reason = $2,
  lease_worker = '', lease_until = NULL, updated_at = $3
WHERE remediation_id = $1 AND state = 'queued';

-- name: SkipItemAtRevision :execrows
UPDATE remediation_items SET outcome_revision = outcome_revision + 1, state = 'skipped', reason = $2,
  lease_worker = '', lease_until = NULL, updated_at = $5
WHERE id=$1 AND state = $3 AND outcome_revision = $4;

-- name: StartItem :execrows
UPDATE remediation_items SET outcome_revision = outcome_revision + 1, state = 'working', attempt = attempt + 1,
  reason = '', not_before = NULL, lease_worker = $2, lease_until = $3, change_id=$4, run_id = $5, updated_at = $6
WHERE id=$1;

-- name: StickItem :execrows
UPDATE remediation_items SET outcome_revision = outcome_revision + 1, state = 'stuck', reason = $2,
  lease_worker = '', lease_until = NULL, updated_at = $3
WHERE id=$1;

-- name: SetItemOutcome :execrows
UPDATE remediation_items SET state = $2, reason = $3, not_before = $4, transient_retries = $5,
  verified_review_id=$6, lease_worker = $7, lease_until = $8,
  outcome_revision = outcome_revision + 1, updated_at = $9
WHERE id=$1;

-- name: SetItemState :execrows
UPDATE remediation_items SET state = $2, reason = $3, outcome_revision = outcome_revision + 1, updated_at = $4
WHERE id=$1;

-- name: ExtendItemLease :execrows
UPDATE remediation_items SET lease_until = $2 WHERE id=$1;

-- A landing that failed for a transient reason is retried against a budget,
-- and the whole condition is in the statement so two workers cannot both
-- spend the last retry.

-- name: ChargeTransientRetry :execrows
UPDATE remediation_items SET transient_retries = transient_retries + 1, reason = $2, updated_at = $8
WHERE id=$1 AND project_id=$3 AND change_id IS NOT DISTINCT FROM $4 AND state = 'landing' AND attempt = $5
  AND transient_retries = $6 AND transient_retries < $7;

-- name: ProjectHasActiveItem :one
SELECT EXISTS(SELECT 1 FROM remediation_items WHERE project_id = $1
  AND state IN ('working', 'accepting', 'landing', 'verifying'));

-- name: ActiveItemCounts :one
SELECT COUNT(*)::bigint AS active, COUNT(*) FILTER (WHERE remediation_id = $1)::bigint AS in_job
FROM remediation_items WHERE state IN ('working', 'accepting', 'landing', 'verifying');

-- name: JobItemCounts :one
SELECT COUNT(*) FILTER (WHERE state IN ('queued', 'working', 'accepting', 'landing', 'verifying'))::bigint AS open,
       COUNT(*) FILTER (WHERE state = 'stuck')::bigint AS stuck
FROM remediation_items WHERE remediation_id = $1;

-- The change and run an item names are joined for their numbers, the
-- names people read (reads of Work's tables); 0 when none or gone.
-- name: ItemsForPage :many
SELECT sqlc.embed(i), COALESCE(ch.number, 0)::bigint AS change_number, COALESCE(r.number, 0)::bigint AS run_number
FROM remediation_items i
LEFT JOIN changes ch ON ch.id = i.change_id AND ch.project_id = i.project_id
LEFT JOIN runs r ON r.id = i.run_id AND r.project_id = i.project_id
WHERE (sqlc.narg('job')::uuid IS NOT NULL AND i.remediation_id = sqlc.narg('job')::uuid) OR
      (sqlc.narg('job')::uuid IS NULL AND i.state IN ('working', 'accepting', 'landing', 'verifying'))
ORDER BY CASE WHEN sqlc.narg('job')::uuid IS NULL THEN i.updated_at END, i.project_slug, i.rank, i.id;

-- An item is reconciled when it is in flight, or stuck with its own last
-- attempt recorded stuck: the pair is what the worker decides from.

-- name: OpenItems :many
SELECT * FROM remediation_items i
WHERE i.state IN ('working', 'accepting', 'landing', 'verifying')
   OR (i.state = 'stuck' AND EXISTS(SELECT 1 FROM remediation_attempts a
        WHERE a.item_id = i.id AND a.n = i.attempt AND a.outcome = 'stuck'))
ORDER BY i.id;

-- An item waits until not_before by AppTown's clock (at), which wrote
-- it and judges it again at admission.
-- name: QueuedItemsForJob :many
SELECT * FROM remediation_items
WHERE remediation_id = sqlc.arg(remediation_id) AND state = 'queued' AND (not_before IS NULL OR not_before <= sqlc.arg(at)::timestamptz)
ORDER BY rank, id;

-- ===== attempts =======================================================

-- name: InsertAttempt :exec
INSERT INTO remediation_attempts (id, item_id, n, change_id, run_id, base_sha, started_at)
VALUES ($7, $1, $2, $3, $4, $5, $6);

-- name: AttemptOutcome :one
SELECT * FROM remediation_attempts WHERE item_id = $1 AND n = $2 AND change_id IS NOT DISTINCT FROM $3 AND run_id = $4;

-- name: FinishAttempt :execrows
UPDATE remediation_attempts SET reason = sqlc.arg(reason),
  finished_at = CASE WHEN outcome = sqlc.arg(outcome) THEN COALESCE(finished_at, sqlc.arg(at)::timestamptz) ELSE sqlc.arg(at)::timestamptz END, outcome = sqlc.arg(outcome_2)
WHERE item_id = sqlc.arg(item_id) AND n = sqlc.arg(n) AND change_id IS NOT DISTINCT FROM sqlc.arg(change_id) AND run_id IS NOT DISTINCT FROM sqlc.arg(run_id);

-- An item that was attempted keeps its entry even when no history row
-- survives; an unattempted item has none.

-- name: AttemptsForJob :many
SELECT i.id AS item_id, a.id AS id, COALESCE(a.n, 0)::bigint AS n,
  a.change_id AS change_id, a.run_id AS run_id,
  COALESCE(a.base_sha, '')::text AS base_sha, COALESCE(a.outcome, '')::text AS outcome,
  COALESCE(a.reason, '')::text AS reason, a.started_at, a.finished_at,
  COALESCE(a.cost_micros, 0)::bigint AS cost_micros
FROM remediation_items i LEFT JOIN remediation_attempts a ON a.item_id = i.id
WHERE i.remediation_id = $1 AND i.attempt > 0 ORDER BY i.id, a.n, a.id;

-- Cost is rolled up from the usage the run recorded, then from the attempts
-- to the item. token_usage is another domain's table, read and never written.

-- name: RollUpAttemptCost :exec
UPDATE remediation_attempts a SET cost_micros = COALESCE((
  SELECT SUM(u.cost_micros) FROM token_usage u JOIN remediation_items i ON i.id = a.item_id
  WHERE u.project_id = i.project_id AND a.run_id IS NOT NULL AND (u.run_id = a.run_id OR u.root_run_id = a.run_id)), 0)
WHERE a.item_id = $1;

-- name: RollUpItemCost :exec
UPDATE remediation_items i SET cost_micros = COALESCE((
  SELECT SUM(a.cost_micros) FROM remediation_attempts a WHERE a.item_id = i.id), 0)
WHERE i.id=$1;

-- ===== the scheduler's reports ========================================

-- name: FreeWorkerSlots :one
SELECT (COALESCE((SELECT p.count FROM worker_pools p WHERE p.kind = $1), $2) -
  (SELECT COUNT(*) FROM remediation_items WHERE state IN ('working', 'accepting', 'landing', 'verifying')))::bigint;

-- name: RunningJobCapacity :many
SELECT m.id, (CASE WHEN m.concurrency > 0 THEN m.concurrency -
  (SELECT COUNT(*) FROM remediation_items i WHERE i.remediation_id = m.id
     AND i.state IN ('working', 'accepting', 'landing', 'verifying'))
  ELSE $1 END)::bigint AS free
FROM remediations m WHERE m.status = 'running' ORDER BY m.id;

-- name: PoolAndQueueDepth :one
SELECT COALESCE((SELECT p.count FROM worker_pools p WHERE p.kind = $1 AND p.count >= 0), $2)::bigint AS declared,
  (SELECT COUNT(*) FROM remediation_items i JOIN remediations r ON r.id = i.remediation_id
     WHERE i.state = 'queued' AND r.status = 'running')::bigint AS queued;

-- ===== another domain's rows, read never written ======================

-- name: LockProjectForRemediation :one
SELECT id FROM projects WHERE id = $1 FOR UPDATE;

-- ===== history maintenance ============================================

-- name: LockRemediationHistory :exec
LOCK TABLE remediations, remediation_items, remediation_attempts IN ACCESS EXCLUSIVE MODE NOWAIT;

-- name: RemediationRecordCounts :one
SELECT (SELECT count(*) FROM remediation_attempts)::bigint AS attempts,
       (SELECT count(*) FROM remediation_items)::bigint AS items,
       (SELECT count(*) FROM remediations)::bigint AS remediations;

-- name: DeleteAllAttempts :execrows
DELETE FROM remediation_attempts;

-- name: DeleteAllItems :execrows
DELETE FROM remediation_items;

-- name: DeleteAllRemediations :execrows
DELETE FROM remediations;

-- name: SettledAttemptsForCheck :many
-- Every settled attempt to fix one block's check, at any version.
SELECT a.outcome, a.cost_micros, a.started_at, a.finished_at::timestamptz AS finished_at
FROM remediation_attempts a JOIN remediation_items i ON i.id = a.item_id
WHERE i.block = $1 AND i.check_name = $2 AND a.outcome <> '' AND a.finished_at IS NOT NULL
ORDER BY a.id;

-- The record people name by its number ("remediation 9").
-- name: RemediationIDByNumber :one
SELECT id FROM remediations WHERE number = $1;
