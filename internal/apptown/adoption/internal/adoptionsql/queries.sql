-- name: ProjectExtension :one
SELECT * FROM project_pi_extensions WHERE project_id=$1 AND name=$2;
-- name: ProjectLane :one
SELECT * FROM project_lanes WHERE project_id=$1 AND name=$2;
-- name: AdoptionByID :one
SELECT * FROM project_adoptions WHERE id=sqlc.arg(id) AND (sqlc.narg(project_id)::uuid IS NULL OR project_id=sqlc.narg(project_id));
-- name: CurrentAdoption :one
SELECT r.* FROM project_adoptions r WHERE r.project_id=$1 AND r.revision<>0
 AND r.revision=(SELECT a.revision FROM adoptions a WHERE a.project_id=$1);
-- name: AdoptionHistory :many
SELECT * FROM project_adoptions WHERE project_id=$1 ORDER BY revision DESC LIMIT $2;
-- name: AdoptionAtRevision :one
SELECT * FROM project_adoptions WHERE project_id=$1 AND revision=$2 AND revision<>0;
-- name: PolicyByID :one
SELECT * FROM policies WHERE id=$1;
-- name: ProjectPolicies :many
SELECT * FROM policies WHERE project_id=$1 ORDER BY name;
-- name: ProjectIdentity :one
SELECT * FROM projects WHERE id=$1;

-- Locks retain the existing transaction choreography.
-- name: ProjectPoliciesForShare :many
SELECT * FROM policies WHERE project_id=$1 ORDER BY name FOR SHARE;

-- All adoption mutations lock project identity before the current aggregate.
-- name: AdoptionProjectForUpdate :one
SELECT * FROM projects WHERE id=$1 FOR UPDATE;

-- Writes are named for the operation; inserts return the entire persisted row.
-- name: InsertGuidanceImport :one
INSERT INTO guidance_imports(id, project_id,position,block_id,version,disabled)
VALUES(sqlc.arg(id), sqlc.arg(project_id),sqlc.arg(position),sqlc.arg(block_id),sqlc.arg(version),sqlc.arg(disabled)) RETURNING *;
-- name: DeleteGuidanceImportsForProject :execrows
DELETE FROM guidance_imports WHERE project_id=$1;
-- name: SetProjectLane :one
INSERT INTO project_lanes(project_id,name,enabled,updated_at) VALUES(sqlc.arg(project_id),sqlc.arg(name),sqlc.arg(enabled),sqlc.arg(at))
ON CONFLICT(project_id,name) DO UPDATE SET enabled=excluded.enabled,updated_at=excluded.updated_at RETURNING *;
-- name: DeselectProjectLanes :execrows
UPDATE project_lanes SET enabled=false,updated_at=$1 WHERE project_id=$2;
-- name: DeleteProjectLanes :execrows
DELETE FROM project_lanes WHERE project_id=$1;
-- name: EnableProjectExtension :execrows
-- Already-enabled membership is a successful no-op and returns no inserted row.
INSERT INTO project_pi_extensions(project_id,name) VALUES($1,$2) ON CONFLICT DO NOTHING;
-- name: DeleteProjectExtensions :execrows
DELETE FROM project_pi_extensions WHERE project_id=$1;
-- name: DeleteProjectPacks :execrows
DELETE FROM project_packs WHERE project_id=$1;
-- name: InsertProjectPack :one
INSERT INTO project_packs(id, project_id,pack_id,version,reason,adopted_by,adopted_by_principal,adopted_by_via,adopted_at)
VALUES(sqlc.arg(id), sqlc.arg(project_id),sqlc.arg(pack_id),sqlc.arg(version),sqlc.arg(reason),sqlc.arg(actor),sqlc.arg(actor_principal),sqlc.arg(actor_via),sqlc.arg(at)) RETURNING *;
-- name: InsertAdoption :one
INSERT INTO project_adoptions(id, project_id,revision,kind,reason,digest,created_by,created_by_principal,created_by_via,created_at,receipt_json,change_id)
VALUES(sqlc.arg(id), sqlc.arg(project_id),sqlc.arg(revision),sqlc.arg(kind),sqlc.arg(reason),sqlc.arg(digest),sqlc.arg(actor),sqlc.arg(actor_principal),sqlc.arg(actor_via),sqlc.arg(at),sqlc.arg(receipt),sqlc.narg(change_id)) RETURNING *;
-- name: DeleteProjectAdoptions :execrows
DELETE FROM project_adoptions WHERE project_id=$1;
-- name: PutProjectPolicy :execrows
-- The install participant requires exactly one affected row, including upserts.
INSERT INTO policies(id, project_id,name,description,rule,config_json,origin,created_at)
VALUES(sqlc.arg(id), sqlc.arg(project_id),sqlc.arg(name),sqlc.arg(description),sqlc.arg(rule),sqlc.arg(config),sqlc.arg(origin),sqlc.arg(at))
ON CONFLICT(project_id,name) DO UPDATE SET description=excluded.description,rule=excluded.rule,config_json=excluded.config_json,origin=excluded.origin;
-- name: DeleteProjectPolicy :execrows
DELETE FROM policies WHERE id=$1;
-- name: DeletePoliciesForProject :execrows
DELETE FROM policies WHERE project_id=$1;
-- name: RefreshAdoptedPolicyWords :execrows
UPDATE policies SET description=(SELECT pl.description FROM policy_library pl WHERE pl.name=policies.name AND pl.rule=policies.rule AND pl.config_json=policies.config_json)
WHERE EXISTS(SELECT 1 FROM policy_library pl WHERE pl.name=policies.name AND pl.rule=policies.rule AND pl.config_json=policies.config_json AND pl.description<>policies.description);

-- name: SaveCurrentAdoption :one
UPDATE adoptions SET revision=sqlc.arg(revision),drift_note=sqlc.arg(drift)
WHERE project_id=sqlc.arg(project_id) AND revision=sqlc.arg(expected_revision) RETURNING *;

-- Reports. Meta projections deliberately avoid returning potentially large version bodies.
-- name: BlockUses :many
SELECT p.slug,gi.version,gi.disabled FROM guidance_imports gi JOIN projects p ON p.id=gi.project_id WHERE gi.block_id=$1 ORDER BY p.slug;
-- name: GuidanceImportsMeta :many
-- The manifest needs pinned body byte counts, not all guidance bodies.
SELECT sqlc.embed(gi),sqlc.embed(b),COALESCE(octet_length(v.body_md),0)::bigint AS body_bytes
FROM guidance_imports gi JOIN guidance_blocks b ON b.id=gi.block_id
LEFT JOIN guidance_block_versions v ON v.block_id=gi.block_id AND v.version=gi.version
WHERE gi.project_id=$1 ORDER BY gi.position,gi.block_id;
-- name: MissingPlatformBlocks :many
SELECT b.* FROM guidance_blocks b WHERE b.tier=$1 AND NOT EXISTS(SELECT 1 FROM guidance_imports gi WHERE gi.project_id=$2 AND gi.block_id=b.id) ORDER BY b.name;
-- name: NextImportPosition :one
SELECT COALESCE(MAX(position)+1,0)::bigint FROM guidance_imports WHERE project_id=$1;
-- name: ImportersBehind :one
SELECT COUNT(DISTINCT project_id) FROM guidance_imports WHERE block_id=$1 AND NOT disabled AND version<$2;
-- name: WorkflowSelections :many
SELECT pl.name,p.slug FROM project_lanes pl JOIN projects p ON p.id=pl.project_id WHERE pl.enabled ORDER BY pl.name,p.slug;
-- name: WorkflowsForProject :many
SELECT sqlc.embed(l),COALESCE(v.yaml,'')::text AS yaml,
 COALESCE((SELECT enabled FROM project_lanes WHERE project_id=$1 AND name=l.name),false)::boolean AS selected
FROM lib_workflows l LEFT JOIN lib_workflow_versions v ON v.lib_id=l.id AND v.version=l.latest_version ORDER BY l.name;
-- name: ProjectPacks :many
SELECT sqlc.embed(p),sqlc.embed(pp),COALESCE(v.spec_json,'{}'::jsonb) AS version_spec
FROM project_packs pp JOIN packs p ON p.id=pp.pack_id
LEFT JOIN pack_versions v ON v.pack_id=pp.pack_id AND v.version=pp.version WHERE pp.project_id=$1 ORDER BY p.name;
-- name: FleetPackMemberships :many
SELECT pp.pack_id, pp.version, pr.slug FROM project_packs pp
JOIN projects pr ON pr.id=pp.project_id
WHERE pr.id=ANY(sqlc.arg(project_ids)::uuid[]) AND pr.purpose = 'ordinary'
ORDER BY pp.pack_id,pp.version,pr.slug;
-- name: ProjectsOnPackBelow :many
SELECT pr.id, pr.slug FROM project_packs pp
JOIN packs pk ON pk.id=pp.pack_id JOIN projects pr ON pr.id=pp.project_id
WHERE pk.name=sqlc.arg(pack) AND pp.version < sqlc.arg(version)::bigint
AND pr.purpose = 'ordinary' AND pr.default_branch <> ''
ORDER BY pr.slug;
-- name: PolicyAdoptions :many
SELECT DISTINCT pol.name,pol.project_id,pr.slug,pr.name AS project_name FROM policies pol JOIN projects pr ON pr.id=pol.project_id ORDER BY pr.name;

-- name: EnabledExtensions :many
SELECT name FROM project_pi_extensions WHERE project_id=$1 ORDER BY name;

-- name: CurrentAggregate :one
SELECT * FROM adoptions WHERE project_id=$1;
-- name: CurrentAggregateForUpdate :one
SELECT * FROM adoptions WHERE project_id=$1 FOR UPDATE;
-- name: InsertCurrentAdoption :one
INSERT INTO adoptions(project_id,revision,drift_note) VALUES($1,$2,$3) RETURNING *;
-- name: SetCurrentDrift :one
UPDATE adoptions SET drift_note=$2 WHERE project_id=$1 RETURNING *;
-- name: DeleteCurrentAdoption :execrows
DELETE FROM adoptions WHERE project_id=$1;
-- name: ProjectsWithRevisions :many
SELECT project_id FROM adoptions WHERE revision>0 ORDER BY project_id;
-- name: CurrentReceiptID :one
-- Pointer observation deliberately does not decode receipt contents. Known projects
-- without a matching revision remain known, with no current receipt ID.
SELECT r.id AS id FROM projects p
LEFT JOIN adoptions a ON a.project_id=p.id
LEFT JOIN project_adoptions r ON r.project_id=a.project_id AND r.revision=a.revision
WHERE p.id=$1;

-- name: TargetOwned :one
SELECT EXISTS(SELECT 1 FROM project_adoptions WHERE id=sqlc.arg(receipt_id) AND project_id=sqlc.arg(project_id)
AND change_id IS NOT DISTINCT FROM sqlc.arg(change_id) AND kind=sqlc.arg(kind) AND digest=sqlc.arg(digest));

-- name: LiveChecksForProjects :many
-- Existing fleet report: one statement retains the pinned import snapshot.
SELECT gi.project_id,b.name,gi.version,COALESCE(v.checks_json,'[]'::jsonb)::text AS checks,(v.id IS NULL)::boolean AS missing_definition
FROM guidance_imports gi JOIN guidance_blocks b ON b.id=gi.block_id
LEFT JOIN guidance_block_versions v ON v.block_id=gi.block_id AND v.version=gi.version
WHERE gi.project_id=ANY(sqlc.arg(project_ids)::uuid[]) AND NOT gi.disabled
ORDER BY gi.project_id,gi.position;

-- name: DeletePackMembership :exec
DELETE FROM project_packs WHERE project_id=sqlc.arg(project_id)
AND pack_id = (SELECT id FROM packs WHERE name=sqlc.arg(pack)::text);
