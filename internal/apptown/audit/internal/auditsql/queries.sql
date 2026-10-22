-- name: AppendReceipt :execrows
INSERT INTO audit_log(id, at,actor,actor_principal,actor_via,project_id,kind,subject,payload_json,authorization_decision_id,refs)
VALUES (sqlc.arg(id), sqlc.arg(at)::text::timestamptz,sqlc.arg(actor),sqlc.arg(actor_principal),sqlc.arg(actor_via),sqlc.arg(project_id),sqlc.arg(kind),sqlc.arg(subject),sqlc.arg(payload_json),sqlc.arg(authorization_decision_id),sqlc.arg(refs));

-- name: RecentReceipts :many
SELECT a.*, COALESCE(p.slug,'')::text AS project_slug
FROM audit_log a LEFT JOIN projects p ON p.id=a.project_id
WHERE (sqlc.narg(project_id)::uuid IS NULL OR a.project_id=sqlc.narg(project_id) OR a.project_id IS NULL)
AND (sqlc.arg(kind_prefix)::text='' OR a.kind LIKE sqlc.arg(kind_prefix)||'%')
AND (sqlc.arg(subject)::text='' OR a.subject=sqlc.arg(subject))
ORDER BY a.id DESC LIMIT sqlc.arg(row_limit);

-- Which of these people any ledger line names as its actor, directly or
-- acting through something ("alice via console/<s>"): one pass over the
-- ledger, whatever the number of names.
-- name: ActorsAmong :many
SELECT DISTINCT split_part(actor, ' via ', 1)::text AS name FROM audit_log
WHERE split_part(actor, ' via ', 1) = ANY(sqlc.arg(names)::text[]);

-- The record a former identity names: this AppTown's row for the converted
-- table (kind) and the identity it had before the cutover (old).
-- name: FormerID :one
SELECT id FROM former_ids WHERE forge = sqlc.arg(forge) AND kind = sqlc.arg(kind) AND old = sqlc.arg(old);
