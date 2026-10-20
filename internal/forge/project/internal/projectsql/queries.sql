-- Project record reads and locks.
-- name: ProjectByID :one
SELECT * FROM projects WHERE id = $1;

-- name: ProjectBySlug :one
SELECT * FROM projects WHERE slug = $1;

-- name: ProjectsByNamespaceAndName :many
SELECT * FROM projects ORDER BY namespace, name;

-- name: ProjectByIDForUpdate :one
SELECT * FROM projects WHERE id = $1 FOR UPDATE;

-- name: ProjectSlugs :many
SELECT slug FROM projects ORDER BY slug;

-- Project writes.
-- name: InsertProject :one
INSERT INTO projects (id, slug, name, namespace, blurb, icon, owner_group, default_approvers, created_at, purpose)
VALUES (sqlc.arg(id), sqlc.arg(slug), sqlc.arg(name), sqlc.arg(namespace), sqlc.arg(blurb), sqlc.arg(icon), sqlc.arg(owner_group), sqlc.arg(default_approvers), sqlc.arg(created_at), sqlc.arg(purpose)) RETURNING *;

-- name: ProjectPurpose :one
SELECT purpose FROM projects WHERE id = $1;

-- name: SetProjectOwnerGroup :exec
UPDATE projects SET owner_group = $2 WHERE id=$1;

-- name: SetProjectReleaseDomain :exec
UPDATE projects SET release_domain = $2 WHERE id=$1;

-- name: SetProjectModelProfile :exec
UPDATE projects SET model_profile = $2 WHERE id=$1;

-- name: SetProjectDefaultBranch :exec
UPDATE projects SET default_branch = $2 WHERE id=$1;

-- name: RenameProject :exec
UPDATE projects SET slug = sqlc.arg(slug), name = sqlc.arg(name), settings_revision = settings_revision + 1 WHERE id=sqlc.arg(id);

-- name: SetIndependentApproval :one
UPDATE projects SET require_independent_approval = $2 WHERE id=$1 RETURNING *;

-- name: DeleteProject :execrows
DELETE FROM projects WHERE id=$1;

-- Contract snapshot records. AppYAML can be large; existence needs only the key.
-- name: ContractSnapshotExists :one
SELECT EXISTS(SELECT 1 FROM contract_snapshots WHERE project_id = $1 AND sha = $2);

-- name: SaveContractSnapshot :exec
INSERT INTO contract_snapshots (project_id, sha, status, app_yaml, source_format, error, created_at)
VALUES (sqlc.arg(project_id), sqlc.arg(sha), sqlc.arg(status), sqlc.narg(app_yaml), sqlc.arg(source_format), sqlc.narg(error), sqlc.arg(created_at))
ON CONFLICT(project_id, sha) DO UPDATE SET status=excluded.status, app_yaml=excluded.app_yaml,
 source_format=excluded.source_format, error=excluded.error;

-- name: UpdateProjectSettings :one
UPDATE projects SET slug=$2, name=$3, icon=$4, blurb=$5, namespace=$6, settings_revision=settings_revision+1 WHERE id=$1 RETURNING *;

-- name: ProjectAuthorityCatalogue :many
SELECT id, name, owner_group FROM projects
WHERE (sqlc.narg(project_id)::uuid IS NULL OR id = sqlc.narg(project_id)) ORDER BY id;

-- The project lists show ordinary projects: a throwaway eval copy appears
-- under its eval, never as a project.
-- name: VisibleProjects :many
SELECT * FROM projects WHERE id=ANY($1::uuid[]) AND purpose = 'ordinary'
AND NOT EXISTS (SELECT 1 FROM project_lifecycle l WHERE l.project_id = projects.id AND l.state = 'deleted')
ORDER BY namespace, name, id;

-- name: VisibleProjectCount :one
SELECT count(*) FROM projects WHERE id=ANY(sqlc.arg(project_ids)::uuid[]) AND purpose = 'ordinary'
AND NOT EXISTS (SELECT 1 FROM project_lifecycle l WHERE l.project_id = projects.id AND l.state = 'deleted')
AND (sqlc.arg(query)::text='' OR strpos(lower(name||' '||slug||' '||blurb||' '||namespace||' '||icon),sqlc.arg(query))>0);

-- name: ProjectPage :many
SELECT * FROM projects WHERE id=ANY(sqlc.arg(project_ids)::uuid[]) AND purpose = 'ordinary'
AND NOT EXISTS (SELECT 1 FROM project_lifecycle l WHERE l.project_id = projects.id AND l.state = 'deleted')
AND (sqlc.arg(query)::text='' OR strpos(lower(name||' '||slug||' '||blurb||' '||namespace||' '||icon),sqlc.arg(query))>0)
ORDER BY CASE WHEN sqlc.arg(ordered)::boolean THEN array_position(sqlc.arg(project_ids)::uuid[],id) END,
 CASE WHEN sqlc.arg(query)::text='' THEN 0
 WHEN strpos(lower(name),sqlc.arg(query))=1 OR strpos(lower(slug),sqlc.arg(query))=1 THEN 0
 WHEN strpos(lower(name),sqlc.arg(query))>0 OR strpos(lower(slug),sqlc.arg(query))>0 THEN 1
 WHEN strpos(lower(blurb),sqlc.arg(query))>0 THEN 2 ELSE 3 END,
 CASE WHEN sqlc.arg(query)::text='' THEN namespace END,name,id
LIMIT sqlc.arg(row_limit)::bigint OFFSET sqlc.arg(row_offset)::bigint;

-- name: InsertProjectDeletion :exec
INSERT INTO project_deletions (project_id, slug, requested_by, requested_by_principal, requested_by_via, requested_at, plan)
VALUES (sqlc.arg(project_id), sqlc.arg(slug), sqlc.arg(requested_by), sqlc.arg(requested_by_principal), sqlc.arg(requested_by_via), sqlc.arg(requested_at), sqlc.arg(plan));

-- name: SlugHeldByDeletion :one
SELECT EXISTS(SELECT 1 FROM project_deletions WHERE slug=$1 AND state <> 'done');

-- name: ProjectDeletion :one
SELECT * FROM project_deletions WHERE project_id=$1;

-- name: ProjectDeletions :many
SELECT * FROM project_deletions ORDER BY requested_at DESC, project_id DESC LIMIT 100;

-- name: OutstandingProjectDeletions :many
SELECT * FROM project_deletions WHERE state <> 'done' ORDER BY project_id;

-- name: SettleProjectDeletionAttempt :execrows
UPDATE project_deletions SET attempts=attempts+1, state=sqlc.arg(state), detail=sqlc.arg(detail),
 finished_at=CASE WHEN sqlc.arg(state)::text='pending' THEN NULL ELSE sqlc.arg(at)::timestamptz END
WHERE project_id=sqlc.arg(project_id) AND round=sqlc.arg(round) AND state='pending';

-- name: ProjectDeletionForUpdate :one
SELECT * FROM project_deletions WHERE project_id=$1 FOR UPDATE;

-- name: RestartProjectDeletion :one
UPDATE project_deletions SET state='pending', attempts=0, round=round+1, detail='', finished_at=NULL
WHERE project_id=$1 AND state<>'done' RETURNING round;

-- Lifecycle (lifecycle.go). The project row is locked first by the caller.
-- name: ProjectRowForShare :one
SELECT id FROM projects WHERE id = $1 FOR SHARE;

-- name: ProjectLifecycle :one
SELECT * FROM project_lifecycle WHERE project_id = $1;

-- requested_at is the deletion owner's clock, which also ends its grace.
-- name: InsertProjectLifecycle :exec
INSERT INTO project_lifecycle (project_id, state, requested_by, requested_by_principal, requested_by_via, binding_id, synthetic, requested_at)
VALUES (sqlc.arg(project_id), sqlc.arg(state), sqlc.arg(requested_by), sqlc.arg(requested_by_principal), sqlc.arg(requested_by_via), sqlc.arg(binding_id), sqlc.arg(synthetic), sqlc.arg(requested_at));

-- name: DeleteProjectLifecycle :execrows
DELETE FROM project_lifecycle WHERE project_id=$1;

-- name: NoteTakeDownsStarted :execrows
UPDATE project_lifecycle SET takedowns_started = true WHERE project_id=$1 AND state = 'deleting';

-- name: StallDeletion :execrows
UPDATE project_lifecycle SET stalled = sqlc.arg(stalled) WHERE project_id=sqlc.arg(project_id) AND state = 'deleting';

-- name: ReconfirmDeletion :execrows
UPDATE project_lifecycle SET stalled = '', requested_by = sqlc.arg(requested_by), requested_by_principal = sqlc.arg(requested_by_principal), requested_by_via = sqlc.arg(requested_by_via), binding_id = sqlc.arg(binding_id)
WHERE project_id=sqlc.arg(project_id) AND state = 'deleting';

-- name: MarkProjectDeleted :execrows
UPDATE project_lifecycle SET state = 'deleted', deleted_at = sqlc.arg(at), kept_until = sqlc.arg(kept_until), stalled = ''
WHERE project_id=sqlc.arg(project_id) AND state = 'deleting';

-- name: StartPurge :execrows
UPDATE project_lifecycle SET purge_started_at = COALESCE(purge_started_at, sqlc.arg(at))
WHERE project_id=sqlc.arg(project_id) AND state = 'deleted';

-- name: DeletingProjects :many
SELECT * FROM project_lifecycle WHERE state = 'deleting' ORDER BY project_id;

-- name: PurgesDue :many
SELECT * FROM project_lifecycle WHERE state = 'deleted' AND kept_until <= sqlc.arg(now) ORDER BY project_id;

-- name: LifecyclesOf :many
SELECT sqlc.embed(l), p.slug, p.name FROM project_lifecycle l JOIN projects p ON p.id = l.project_id ORDER BY l.project_id;

-- A project's own row, for moving it to another AppTown.
-- name: ExportProjectRow :one
SELECT to_jsonb(p)::text FROM projects p WHERE p.id = $1;
