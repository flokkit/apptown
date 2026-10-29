-- name: RecordUsage :exec
INSERT INTO token_usage(id, at,kind,project_id,run_id,root_run_id,step_id,workflow_name,workflow_version,session_id,actor,actor_principal,actor_via,provider,model,input,output,cache_read,cache_write,total,cost_micros,incomplete)
VALUES (sqlc.arg(id), sqlc.arg(at),sqlc.arg(kind),sqlc.arg(project_id),sqlc.arg(run_id),sqlc.arg(root_run_id),sqlc.arg(step_id),sqlc.arg(workflow_name),sqlc.arg(workflow_version),sqlc.arg(session_id),sqlc.arg(actor),sqlc.arg(actor_principal),sqlc.arg(actor_via),sqlc.arg(provider),sqlc.arg(model),sqlc.arg(input),sqlc.arg(output),sqlc.arg(cache_read),sqlc.arg(cache_write),sqlc.arg(total),sqlc.arg(cost_micros),sqlc.arg(incomplete));

-- name: RecordUsageOnce :exec
INSERT INTO token_usage(id, at,kind,project_id,run_id,root_run_id,step_id,workflow_name,workflow_version,session_id,actor,actor_principal,actor_via,provider,model,input,output,cache_read,cache_write,total,cost_micros,observation,incomplete)
VALUES (sqlc.arg(id), sqlc.arg(at),sqlc.arg(kind),sqlc.arg(project_id),sqlc.arg(run_id),sqlc.arg(root_run_id),sqlc.arg(step_id),sqlc.arg(workflow_name),sqlc.arg(workflow_version),sqlc.arg(session_id),sqlc.arg(actor),sqlc.arg(actor_principal),sqlc.arg(actor_via),sqlc.arg(provider),sqlc.arg(model),sqlc.arg(input),sqlc.arg(output),sqlc.arg(cache_read),sqlc.arg(cache_write),sqlc.arg(total),sqlc.arg(cost_micros),sqlc.arg(observation),sqlc.arg(incomplete))
ON CONFLICT (observation) WHERE observation <> '' DO NOTHING;

-- name: ObservationByKey :one
SELECT input, output, cache_read, cache_write, total, cost_micros, incomplete FROM token_usage WHERE observation=sqlc.arg(observation) AND observation <> '';

-- name: Cursor :one
SELECT * FROM usage_cursors WHERE path=$1;

-- name: SetCursor :exec
INSERT INTO usage_cursors(path,byte_offset) VALUES ($1,$2) ON CONFLICT(path) DO UPDATE SET byte_offset=excluded.byte_offset;

-- name: RunTotal :one
SELECT COALESCE(SUM(total),0)::bigint AS total, COALESCE(SUM(cost_micros),0)::bigint AS cost_micros FROM token_usage WHERE root_run_id=ANY(sqlc.arg(root_run_ids)::uuid[]);

-- A report reads the whole instance, or only the listed projects; an empty
-- list selects nothing.
-- name: UsageTotals :one
SELECT COALESCE(SUM(input),0)::bigint AS input, COALESCE(SUM(output),0)::bigint AS output,
COALESCE(SUM(cache_read),0)::bigint AS cache_read, COALESCE(SUM(cache_write),0)::bigint AS cache_write,
COALESCE(SUM(total),0)::bigint AS total, COALESCE(SUM(cost_micros),0)::bigint AS cost_micros,
COUNT(*) FILTER (WHERE incomplete <> '' OR pending_until <= sqlc.arg(now)::timestamptz)::bigint AS incomplete
FROM token_usage
WHERE at>=sqlc.arg(since) AND (sqlc.arg(whole_instance)::boolean OR project_id=ANY(sqlc.arg(project_ids)::uuid[]))
AND (sqlc.arg(workflow)::text='' OR workflow_name=sqlc.arg(workflow));

-- Each supported report dimension uses the same filter and aggregation. Keep
-- the top-50 limit before discarding empty keys, matching the report contract.
-- name: UsageGroups :many
SELECT COALESCE(CASE sqlc.arg(dimension)::text
 WHEN 'kind' THEN u.kind
 WHEN 'model' THEN u.provider||'/'||u.model
 WHEN 'day' THEN to_char(u.at AT TIME ZONE 'UTC','YYYY-MM-DD')
 WHEN 'workflow' THEN CASE WHEN u.kind='console' THEN '(interactive console)' ELSE u.workflow_name END
 WHEN 'project' THEN CASE WHEN EXISTS (SELECT 1 FROM eval_copies e WHERE e.project_id = u.project_id) THEN 'evals' ELSE p.slug END END,'')::text AS key,
SUM(u.input)::bigint AS input, SUM(u.output)::bigint AS output, SUM(u.total)::bigint AS total, SUM(u.cost_micros)::bigint AS cost_micros
FROM token_usage u LEFT JOIN projects p ON p.id=u.project_id
WHERE u.at>=sqlc.arg(since) AND (sqlc.arg(whole_instance)::boolean OR u.project_id=ANY(sqlc.arg(project_ids)::uuid[]))
AND (sqlc.arg(workflow)::text='' OR u.workflow_name=sqlc.arg(workflow))
GROUP BY key ORDER BY SUM(u.total) DESC LIMIT 50;

-- name: UsageDaysByKind :many
SELECT to_char(at AT TIME ZONE 'UTC','YYYY-MM-DD')::text AS day, kind,
SUM(total)::bigint AS total, SUM(cost_micros)::bigint AS cost_micros
FROM token_usage
WHERE at>=sqlc.arg(since) AND (sqlc.arg(whole_instance)::boolean OR project_id=ANY(sqlc.arg(project_ids)::uuid[]))
AND (sqlc.arg(workflow)::text='' OR workflow_name=sqlc.arg(workflow))
GROUP BY 1,2 ORDER BY 1;

-- All the spending of the listed projects, whenever it was recorded, with
-- how many observations were incomplete: an eval copy's cost.
-- name: ProjectsTotal :one
SELECT COALESCE(SUM(input),0)::bigint AS input, COALESCE(SUM(output),0)::bigint AS output,
COALESCE(SUM(total),0)::bigint AS total, COALESCE(SUM(cost_micros),0)::bigint AS cost_micros,
COUNT(*) FILTER (WHERE incomplete <> '' OR pending_until <= sqlc.arg(now)::timestamptz)::bigint AS incomplete
FROM token_usage WHERE project_id = ANY(sqlc.arg(project_ids)::uuid[]);

-- An AppTown model call's spend line, opened before the call is sent: keyed by
-- its call's id, zero until settled, pending until pending_until.
-- name: OpenCallLine :exec
INSERT INTO token_usage(id, at,kind,project_id,run_id,actor,actor_principal,actor_via,provider,model,observation,pending_until)
VALUES (sqlc.arg(id), sqlc.arg(at),sqlc.arg(kind),sqlc.arg(project_id),sqlc.arg(run_id),sqlc.arg(actor),sqlc.arg(actor_principal),sqlc.arg(actor_via),sqlc.arg(provider),sqlc.arg(model),sqlc.arg(observation),sqlc.arg(pending_until)::timestamptz);

-- Settles a pending line, only while it is still pending and its time has
-- not run out. A line already locked in as unknown is left as it is.
-- name: SettleCallLine :execrows
UPDATE token_usage SET input=sqlc.arg(input), output=sqlc.arg(output), cache_read=sqlc.arg(cache_read), cache_write=sqlc.arg(cache_write),
total=sqlc.arg(total), cost_micros=sqlc.arg(cost_micros), incomplete=sqlc.arg(incomplete), pending_until=NULL
WHERE observation=sqlc.arg(observation) AND observation <> '' AND pending_until > sqlc.arg(now)::timestamptz;

-- Locks in the listed projects' expired pending lines as unknown spend that
-- can never change. The row locks decide against a concurrent settlement.
-- name: LockInExpiredLines :exec
UPDATE token_usage SET incomplete=sqlc.arg(incomplete), pending_until=NULL
WHERE project_id = ANY(sqlc.arg(project_ids)::uuid[]) AND pending_until <= sqlc.arg(now)::timestamptz;

-- The listed projects' unknown spend lines that can never change.
-- name: LockedInIncomplete :one
SELECT COUNT(*)::bigint FROM token_usage WHERE project_id = ANY(sqlc.arg(project_ids)::uuid[]) AND incomplete <> '';

-- At startup, before serving: every pending line belongs to a call the
-- restart cut off.
-- name: LockInAllPendingLines :execrows
UPDATE token_usage SET incomplete=sqlc.arg(incomplete), pending_until=NULL WHERE pending_until IS NOT NULL;
