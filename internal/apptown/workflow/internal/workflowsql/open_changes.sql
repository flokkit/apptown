-- The requester's principal and current project visibility fence the selection
-- before ordering and bounding it. Historical runs still move an idle change.
-- name: PersonalOpenChanges :many
SELECT c.id,c.number,c.title,c.project_id,p.slug AS project_slug,
GREATEST(c.created_at, (SELECT MAX(r.updated_at) FROM runs r WHERE r.change_id=c.id AND r.project_id=c.project_id))::timestamptz AS updated_at,
active.id AS run_id, COUNT(*) OVER()::bigint AS total
FROM changes c JOIN projects p ON p.id=c.project_id
LEFT JOIN LATERAL (
 SELECT r.id FROM runs r WHERE r.change_id=c.id AND r.project_id=c.project_id
 AND r.status=sqlc.arg(running)::text AND r.parent_run_id IS NULL
 ORDER BY r.updated_at DESC,r.id DESC LIMIT 1
) active ON true
WHERE c.project_id=ANY(sqlc.arg(project_ids)::uuid[])
AND c.opened_by_principal=sqlc.arg(principal)::uuid
AND c.state IN (sqlc.arg(in_progress)::text,sqlc.arg(landing)::text) AND c.closed_at IS NULL
ORDER BY updated_at DESC,c.id DESC LIMIT 20;
