-- Complaints: what a person or a helper said when the platform got in the
-- way. Record-store style (design/architecture/record-store-style.md):
-- whole rows by identity, writes named for the operation, the page's
-- report last.

-- name: ComplaintByID :one
SELECT * FROM complaints WHERE id = $1;

-- name: NextComplaintNumber :one
-- The next number, in the creating transaction: the row's lock orders
-- concurrent creations.
INSERT INTO complaint_numbers (id, last) VALUES (true, 1)
ON CONFLICT (id) DO UPDATE SET last = complaint_numbers.last + 1
RETURNING last;

-- name: InsertComplaint :one
INSERT INTO complaints (id, number, at, project_id, source, run_id, workflow, step, session_id, actor, actor_principal, actor_via, text, change_id, why, wish, about, pack_version)
VALUES (sqlc.arg(id), sqlc.arg(number), sqlc.arg(at), sqlc.arg(project_id), sqlc.arg(source), sqlc.arg(run_id), sqlc.arg(workflow), sqlc.arg(step), sqlc.arg(session_id), sqlc.arg(actor), sqlc.arg(actor_principal), sqlc.arg(actor_via), sqlc.arg(text), sqlc.arg(change_id), sqlc.arg(why), sqlc.arg(wish), sqlc.arg(about), sqlc.arg(pack_version)) RETURNING *;

-- name: AnswerComplaint :one
UPDATE complaints SET answer = sqlc.arg(answer), answered_by = sqlc.arg(answered_by), answered_by_principal = sqlc.arg(answered_by_principal), answered_by_via = sqlc.arg(answered_by_via), answered_at = sqlc.arg(answered_at), seen = true WHERE id=sqlc.arg(id) RETURNING *;

-- name: ArchiveComplaint :one
UPDATE complaints SET archived_at = $2, seen = true WHERE id=$1 AND archived_at IS NULL RETURNING *;

-- name: ArchiveComplaintsBefore :execrows
UPDATE complaints SET archived_at = $1, seen = true WHERE archived_at IS NULL AND at < $2;

-- name: MarkComplaintsSeen :exec
UPDATE complaints SET seen = true WHERE NOT seen;

-- name: DeleteComplaint :execrows
DELETE FROM complaints WHERE id=$1;

-- name: DeleteComplaintsBefore :execrows
DELETE FROM complaints WHERE at < $1;

-- The box as a page reads it: newest first, with the project it names and,
-- for a complaint filed in an eval's copy, the eval (from the evals
-- owner's copy record, which outlives the copy). AppTown's queue holds
-- every complaint filed outside evals and, from evals, only those about
-- AppTown; the rest are the eval's, on its page. The run and change it
-- names are joined for their numbers, the names people read. The joins are reads of
-- other domains' tables, never writes (scripts/schema-ownership.py).

-- name: ComplaintsWithProject :many
SELECT sqlc.embed(c), COALESCE(p.slug, '')::text AS project_slug, COALESCE(p.name, '')::text AS project_name,
e.eval_id AS eval_id, COALESCE(r.number, 0)::bigint AS run_number, COALESCE(ch.number, 0)::bigint AS change_number
FROM complaints c LEFT JOIN projects p ON p.id = c.project_id LEFT JOIN eval_copies e ON e.project_id = c.project_id
LEFT JOIN runs r ON r.id = c.run_id LEFT JOIN changes ch ON ch.id = c.change_id
WHERE e.project_id IS NULL OR c.about = 'apptown'
ORDER BY c.id DESC LIMIT $1;

-- An eval's complaints, whatever they are about: those filed in its
-- copies, as the evals owner's copy record says.
-- name: ComplaintsOfEval :many
SELECT sqlc.embed(c), COALESCE(p.slug, '')::text AS project_slug, COALESCE(p.name, '')::text AS project_name, e.eval_id, e.arm_id,
COALESCE(r.number, 0)::bigint AS run_number, COALESCE(ch.number, 0)::bigint AS change_number
FROM complaints c JOIN eval_copies e ON e.project_id = c.project_id LEFT JOIN projects p ON p.id = c.project_id
LEFT JOIN runs r ON r.id = c.run_id LEFT JOIN changes ch ON ch.id = c.change_id
WHERE e.eval_id = $1 ORDER BY c.id;

-- The record people name by its number ("complaint 9").
-- name: ComplaintIDByNumber :one
SELECT id FROM complaints WHERE number = $1;
