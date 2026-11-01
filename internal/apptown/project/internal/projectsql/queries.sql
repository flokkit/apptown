-- Project record reads and locks.
-- name: ProjectByID :one
SELECT * FROM projects WHERE id = $1;

-- name: ProjectBySlug :one
SELECT * FROM projects WHERE slug = $1;

-- name: ProjectByServingLabel :one
SELECT * FROM projects WHERE serving_label = $1;

-- name: ProjectsByNamespaceAndName :many
SELECT * FROM projects ORDER BY namespace, name;

-- name: ProjectByIDForUpdate :one
SELECT * FROM projects WHERE id = $1 FOR UPDATE;

-- name: ProjectSlugs :many
SELECT slug FROM projects ORDER BY slug;

-- Project writes.
-- name: InsertProject :one
INSERT INTO projects (id, slug, name, namespace, blurb, blurb_source, icon, owner_group, default_approvers, created_at, purpose, serving_label)
VALUES (sqlc.arg(id), sqlc.arg(slug), sqlc.arg(name), sqlc.arg(namespace), sqlc.arg(blurb), sqlc.arg(blurb_source), sqlc.arg(icon), sqlc.arg(owner_group), sqlc.arg(default_approvers), sqlc.arg(created_at), sqlc.arg(purpose), sqlc.arg(serving_label)) RETURNING *;

-- name: ProjectPurpose :one
SELECT purpose FROM projects WHERE id = $1;

-- name: SetProjectModelProfile :exec
UPDATE projects SET model_profile = $2 WHERE id=$1;

-- name: SetProjectDefaultBranch :exec
UPDATE projects SET default_branch = $2 WHERE id=$1;

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
UPDATE projects SET slug=$2, name=$3, icon=$4, blurb=$5, namespace=$6, blurb_source=$7, settings_revision=settings_revision+1 WHERE id=$1 RETURNING *;

-- name: ProjectAuthorityCatalogue :many
SELECT id, name, owner_group FROM projects
WHERE (sqlc.narg(project_id)::uuid IS NULL OR id = sqlc.narg(project_id)) ORDER BY id;

-- The project lists show ordinary projects: a throwaway eval copy appears
-- under its eval, never as a project.
-- name: VisibleProjectCount :one
SELECT count(*) FROM projects WHERE id=ANY(sqlc.arg(project_ids)::uuid[]) AND purpose = 'ordinary'
AND NOT EXISTS (SELECT 1 FROM project_lifecycle l WHERE l.project_id = projects.id AND l.state = 'deleted')
AND (sqlc.arg(query)::text='' OR strpos(lower(name||' '||slug||' '||blurb||' '||namespace||' '||icon),sqlc.arg(query))>0);

-- name: VisibleProjects :many
SELECT * FROM projects
WHERE id=ANY(sqlc.arg(project_ids)::uuid[]) AND purpose='ordinary'
AND NOT EXISTS (SELECT 1 FROM project_lifecycle l WHERE l.project_id=projects.id AND l.state='deleted')
ORDER BY namespace, name, id;

-- name: ProjectPage :many
SELECT p.* FROM projects p
WHERE p.id=ANY(sqlc.arg(project_ids)::uuid[]) AND p.purpose = 'ordinary'
AND NOT EXISTS (SELECT 1 FROM project_lifecycle l WHERE l.project_id = p.id AND l.state = 'deleted')
AND (sqlc.arg(query)::text='' OR strpos(lower(p.name||' '||p.slug||' '||p.blurb||' '||p.namespace||' '||p.icon),sqlc.arg(query))>0)
ORDER BY (p.id=ANY(sqlc.arg(priority_ids)::uuid[])) DESC,
 CASE WHEN sqlc.arg(query)::text='' THEN 0
 WHEN strpos(lower(name),sqlc.arg(query))=1 OR strpos(lower(slug),sqlc.arg(query))=1 THEN 0
 WHEN strpos(lower(name),sqlc.arg(query))>0 OR strpos(lower(slug),sqlc.arg(query))>0 THEN 1
 WHEN strpos(lower(blurb),sqlc.arg(query))>0 THEN 2 ELSE 3 END,
 CASE WHEN sqlc.arg(query)::text='' THEN namespace END,p.name,p.id
LIMIT sqlc.arg(row_limit)::bigint OFFSET sqlc.arg(row_offset)::bigint;

-- name: PersonalProjectPage :many
WITH personal_events AS (
 SELECT project_id, created_at AS at FROM changes
 WHERE project_id=ANY(sqlc.arg(project_ids)::uuid[]) AND opened_by_principal=sqlc.arg(principal)::uuid
 UNION ALL
 SELECT project_id, at FROM audit_log
 WHERE project_id=ANY(sqlc.arg(project_ids)::uuid[]) AND actor_principal=sqlc.arg(principal)::uuid
 AND kind IN ('console.say','console.upload')
), personal AS (
 SELECT project_id, max(at) AS last_worked_at FROM personal_events GROUP BY project_id
), recent AS (
 SELECT project_id, max(updated_at) AS last_run_at FROM runs
 WHERE project_id=ANY(sqlc.arg(project_ids)::uuid[]) GROUP BY project_id
)
SELECT sqlc.embed(p), COALESCE(personal.last_worked_at,'epoch'::timestamptz)::timestamptz AS last_worked_at,
 (personal.last_worked_at IS NOT NULL)::boolean AS has_last_worked_at FROM projects p
LEFT JOIN personal ON personal.project_id=p.id
LEFT JOIN recent ON recent.project_id=p.id
WHERE p.id=ANY(sqlc.arg(project_ids)::uuid[]) AND p.purpose = 'ordinary'
AND NOT EXISTS (SELECT 1 FROM project_lifecycle l WHERE l.project_id = p.id AND l.state = 'deleted')
AND (sqlc.arg(query)::text='' OR strpos(lower(p.name||' '||p.slug||' '||p.blurb||' '||p.namespace||' '||p.icon),sqlc.arg(query))>0)
ORDER BY personal.last_worked_at DESC NULLS LAST, recent.last_run_at DESC NULLS LAST, p.name, p.id
LIMIT sqlc.arg(row_limit)::bigint OFFSET sqlc.arg(row_offset)::bigint;

-- name: InsertProjectDeletion :exec
INSERT INTO project_deletions (project_id, slug, requested_by, requested_by_principal, requested_by_via, requested_at, plan)
VALUES (sqlc.arg(project_id), sqlc.arg(slug), sqlc.arg(requested_by), sqlc.arg(requested_by_principal), sqlc.arg(requested_by_via), sqlc.arg(requested_at), sqlc.arg(plan));


-- name: ProjectDeletion :one
SELECT * FROM project_deletions WHERE project_id=$1;

-- name: ProjectDeletions :many
SELECT * FROM project_deletions ORDER BY requested_at DESC, project_id DESC LIMIT 100;

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

-- name: RepositoryLayoutOwners :many
SELECT p.id,p.slug,p.default_branch,COALESCE(l.state,'')::text AS lifecycle,false::boolean AS retained
FROM projects p LEFT JOIN project_lifecycle l ON l.project_id=p.id
UNION ALL
SELECT d.project_id,d.slug,''::text,'deleted'::text,true::boolean
FROM project_deletions d WHERE d.state<>'done' AND NOT EXISTS(SELECT 1 FROM projects p WHERE p.id=d.project_id)
ORDER BY id;

-- name: ProjectIDBySlug :one
SELECT id FROM projects WHERE slug=$1;

-- An address is taken while another project has it as its address or its
-- serving label, or a deleted project's unfinished cleanup still names it.
-- name: ProjectAddressTaken :one
SELECT (EXISTS(SELECT 1 FROM projects p WHERE (p.slug=sqlc.arg(address) OR p.serving_label=sqlc.arg(address)) AND p.id<>sqlc.arg(id))
 OR EXISTS(SELECT 1 FROM project_deletions d WHERE d.state<>'done' AND d.project_id<>sqlc.arg(id)
  AND (d.slug=sqlc.arg(address) OR d.plan->'releasePreviews'->>'servingLabel'=sqlc.arg(address))))::boolean;

-- name: ClaimProjectCreation :execrows
INSERT INTO project_creation_requests(id,actor_id,request_id,project_id) VALUES($1,$2,$3,$4)
ON CONFLICT(actor_id,request_id) DO NOTHING;

-- name: ProjectCreationRequest :one
SELECT * FROM project_creation_requests WHERE actor_id=$1 AND request_id=$2;

-- name: CompleteProjectCreation :execrows
UPDATE project_creation_requests SET input_hash=$2,resolved_group=$3,result_json=$4 WHERE id=$1 AND result_json IS NULL;


-- name: PublishGeneratedDescription :exec
UPDATE projects SET blurb=$2, settings_revision=settings_revision+1
WHERE id=$1 AND blurb_source='generated' AND blurb<>$2;
