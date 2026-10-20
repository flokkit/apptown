-- ADR records and the project's identity. Project is the permitted identity join;
-- the change join reads only its number, for naming it to people.
-- name: ADRByID :one
SELECT sqlc.embed(a), p.slug, p.name, COALESCE(c.number,0)::bigint AS change_number FROM adrs a JOIN projects p ON p.id=a.project_id LEFT JOIN changes c ON c.id=a.change_id AND c.project_id=a.project_id WHERE a.id=$1 AND a.project_id=$2;

-- name: ADRByIDForUpdate :one
SELECT sqlc.embed(a), p.slug, p.name, COALESCE(c.number,0)::bigint AS change_number FROM adrs a JOIN projects p ON p.id=a.project_id LEFT JOIN changes c ON c.id=a.change_id AND c.project_id=a.project_id WHERE a.id=$1 AND a.project_id=$2 FOR UPDATE OF a;

-- Routing scope is read before authorization. Avoid body_md, which can be large.
-- name: ADRScopeMeta :one
SELECT project_id FROM adrs WHERE id=$1;

-- name: ADRsMatching :many
SELECT sqlc.embed(a), p.slug, p.name, COALESCE(c.number,0)::bigint AS change_number FROM adrs a JOIN projects p ON p.id=a.project_id LEFT JOIN changes c ON c.id=a.change_id AND c.project_id=a.project_id
WHERE a.project_id=ANY(sqlc.arg(project_ids)::uuid[])
AND (sqlc.narg(project_id)::uuid IS NULL OR a.project_id=sqlc.narg(project_id))
AND (sqlc.arg(project_slug)::text='' OR p.slug=sqlc.arg(project_slug))
AND (sqlc.arg(status)::text='' OR a.status=sqlc.arg(status))
AND (sqlc.arg(tag)::text='' OR (','||a.tags||',') LIKE sqlc.arg(tag))
AND (sqlc.arg(query)::text='' OR a.title LIKE sqlc.arg(query) OR a.body_md LIKE sqlc.arg(query))
AND (sqlc.narg(change_id)::uuid IS NULL OR a.change_id=sqlc.narg(change_id))
ORDER BY CASE WHEN sqlc.arg(recent)::boolean THEN a.updated_at END DESC,
CASE WHEN NOT sqlc.arg(recent)::boolean THEN p.slug END,a.number DESC,a.id DESC
LIMIT NULLIF(sqlc.arg(limit_count)::integer,0);

-- name: ADRsForChangeForShare :many
SELECT * FROM adrs WHERE project_id=sqlc.arg(project_id) AND change_id IS NOT DISTINCT FROM sqlc.arg(change_id) AND status=sqlc.arg(status) ORDER BY id FOR SHARE;

-- name: ADRsForChange :many
SELECT * FROM adrs WHERE project_id=$1 AND change_id IS NOT DISTINCT FROM $2 ORDER BY number;

-- name: NextADRNumber :one
SELECT (COALESCE(MAX(number),0)+1)::bigint FROM adrs WHERE project_id=$1;

-- name: SuccessorID :one
SELECT (SELECT id FROM adrs WHERE supersedes_id IS NOT DISTINCT FROM sqlc.arg(supersedes_id) AND project_id=sqlc.arg(project_id) ORDER BY (status=sqlc.arg(status)) DESC,id DESC LIMIT 1)::uuid AS id;

-- name: ADRCountsByProject :many
SELECT project_id,count(*)::bigint AS count FROM adrs WHERE project_id=ANY($1::uuid[]) GROUP BY project_id;

-- name: ProjectForUpdate :one
SELECT * FROM projects WHERE id=$1 FOR UPDATE;

-- name: InsertADR :one
INSERT INTO adrs(id, project_id,number,title,status,body_md,tags,supersedes_id,change_id,created_at,updated_at)
VALUES (sqlc.arg(id), sqlc.arg(project_id),sqlc.arg(number),sqlc.arg(title),sqlc.arg(status),sqlc.arg(body_md),sqlc.arg(tags),sqlc.arg(supersedes_id),sqlc.arg(change_id),sqlc.arg(at),sqlc.arg(at)) RETURNING *;

-- name: EditADR :execrows
UPDATE adrs SET title=sqlc.arg(title),body_md=sqlc.arg(body_md),tags=sqlc.arg(tags),status=sqlc.arg(status),change_id=sqlc.arg(change_id),updated_at=sqlc.arg(at)
WHERE id=sqlc.arg(id) AND project_id=sqlc.arg(project_id) AND status IS NOT DISTINCT FROM sqlc.arg(old_status);

-- name: DeleteADRsForProject :exec
DELETE FROM adrs WHERE project_id=$1;

-- name: ADRsRemainForProject :one
SELECT EXISTS(SELECT 1 FROM adrs WHERE project_id=$1);

-- Existing same-transaction Change ownership check; see ledger boundary exception.
-- name: ChangeBelongsToProject :one
SELECT EXISTS(SELECT 1 FROM changes WHERE id=$1 AND project_id=$2);

-- A project's ADRs, row by row, for moving it to another AppTown.
-- name: ExportADRs :many
SELECT to_jsonb(a)::text FROM adrs a WHERE a.project_id = $1 ORDER BY a.number;
