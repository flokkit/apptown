-- name: JanitorLandedChanges :many
SELECT a.*,c.number,c.title,c.type,c.target_candidate_json,c.closed_at,p.slug,p.name
FROM automated_changes a JOIN changes c ON c.id=a.active_change_id AND c.project_id=a.project_id
JOIN projects p ON p.id=a.project_id
WHERE a.project_id=ANY(sqlc.arg(project_ids)::uuid[]) AND a.outcome='shipped' AND a.queue<>''
AND (c.landed_sha<>'' OR (a.queue='packs' AND c.adopted_revision>0))
AND c.closed_at>=sqlc.arg(since)::timestamptz AND c.closed_at<sqlc.arg(until)::timestamptz
ORDER BY c.closed_at DESC,c.id DESC LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: JanitorProjectChoices :many
SELECT DISTINCT ON (project_id) project_id,record_json FROM standing_project_choices
WHERE project_id=ANY(sqlc.arg(project_ids)::uuid[]) ORDER BY project_id,at DESC,id DESC;

-- name: PreviousLandedAdoption :one
SELECT target_candidate_json FROM changes
WHERE project_id=sqlc.arg(project_id) AND type='uplift' AND adopted_revision>0
AND closed_at<sqlc.arg(before)::timestamptz
ORDER BY closed_at DESC,id DESC LIMIT 1;

-- name: JanitorRecords :many
SELECT p.id,p.slug,p.name,p.default_branch,COALESCE(j.queues,'[]'::jsonb)::jsonb AS queues,COALESCE(j.scans,'{}'::jsonb)::jsonb AS scans,COALESCE(j.problem,'')::text AS problem FROM projects p
LEFT JOIN janitor_projects j ON j.project_id=p.id
WHERE p.id=ANY(sqlc.arg(project_ids)::uuid[]) AND p.purpose='ordinary'
AND NOT EXISTS(SELECT 1 FROM project_lifecycle l WHERE l.project_id=p.id)
ORDER BY p.name,p.id;

-- name: JanitorRecord :many
SELECT * FROM janitor_projects WHERE project_id=$1;

-- name: RecordJanitorQueues :exec
INSERT INTO janitor_projects(project_id,queues,updated_at) VALUES($1,$2,$3)
ON CONFLICT(project_id) DO UPDATE SET queues=EXCLUDED.queues,updated_at=EXCLUDED.updated_at;

-- name: RecordJanitorProcedures :exec
INSERT INTO janitor_projects(project_id,scans,problem,updated_at) VALUES($1,$2,$3,$4)
ON CONFLICT(project_id) DO UPDATE SET scans=janitor_projects.scans || EXCLUDED.scans,problem=EXCLUDED.problem,updated_at=EXCLUDED.updated_at;

-- name: RecordJanitorScans :exec
INSERT INTO janitor_projects(project_id,scans,updated_at) VALUES($1,$2,$3)
ON CONFLICT(project_id) DO UPDATE SET scans=janitor_projects.scans || EXCLUDED.scans,updated_at=EXCLUDED.updated_at;

-- name: JanitorProjectIDs :many
SELECT id FROM projects ORDER BY id;

-- name: JanitorRunChange :one
SELECT project_id,change_id FROM runs WHERE id=$1;

-- name: JanitorAttemptStats :many
SELECT id, project_id, active_change_id, queue, opened_at, ended_at, outcome, reason FROM automated_changes
WHERE project_id=ANY(sqlc.arg(project_ids)::uuid[]) AND queue IN ('packs','updates','security') AND opened_at>=sqlc.arg(since)
ORDER BY opened_at DESC, id DESC;
