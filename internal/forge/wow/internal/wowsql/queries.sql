-- Records, grouped by table. All identity reads carry the complete row.
-- name: BlockByID :one
SELECT * FROM guidance_blocks WHERE id=$1;
-- name: BlockByName :one
SELECT * FROM guidance_blocks WHERE name=$1;
-- name: BlockVersion :one
SELECT * FROM guidance_block_versions WHERE block_id=$1 AND version=$2;
-- name: BlockVersionAsOf :one
SELECT * FROM guidance_block_versions WHERE block_id=$1 AND created_at<=$2 ORDER BY version DESC LIMIT 1;
-- name: WorkflowByName :one
SELECT * FROM lib_workflows WHERE name=$1;
-- name: Workflows :many
SELECT * FROM lib_workflows ORDER BY origin DESC,name;
-- name: WorkflowVersion :one
SELECT v.* FROM lib_workflows l JOIN lib_workflow_versions v ON v.lib_id=l.id
 AND v.version=CASE WHEN sqlc.arg(version)::bigint>0 THEN sqlc.arg(version)::bigint ELSE l.latest_version END
WHERE l.name=sqlc.arg(name)::text;
-- name: WorkflowVersions :many
SELECT * FROM lib_workflow_versions WHERE lib_id=$1 ORDER BY version DESC;
-- name: SystemWorkflow :one
SELECT * FROM system_workflows WHERE stock_name=$1;
-- name: PackByName :one
SELECT * FROM packs WHERE name=$1;
-- name: Packs :many
SELECT * FROM packs ORDER BY origin DESC,name;
-- name: PackVersion :one
SELECT * FROM pack_versions WHERE pack_id=$1 AND version=$2;
-- name: PackVersions :many
SELECT * FROM pack_versions WHERE pack_id=$1 ORDER BY version DESC;
-- name: CatalogByName :one
SELECT * FROM catalog_changes WHERE name=$1;
-- name: CatalogChanges :many
SELECT * FROM catalog_changes ORDER BY name;
-- name: CatalogVersion :one
SELECT * FROM catalog_change_versions WHERE catalog_id=$1 AND version=$2;
-- name: LibraryPolicyByName :one
SELECT * FROM policy_library WHERE name=$1;
-- name: LibraryPolicies :many
SELECT * FROM policy_library ORDER BY origin DESC,name;

-- name: BlockByIDForUpdate :one
SELECT * FROM guidance_blocks WHERE id=$1 FOR UPDATE;
-- name: BlockByNameForUpdate :one
SELECT * FROM guidance_blocks WHERE name=$1 FOR UPDATE;
-- name: WorkflowByNameForUpdate :one
SELECT * FROM lib_workflows WHERE name=$1 FOR UPDATE;
-- name: PackByNameForUpdate :one
SELECT * FROM packs WHERE name=$1 FOR UPDATE;
-- name: LockPacks :exec
LOCK TABLE packs IN SHARE ROW EXCLUSIVE MODE;
-- name: CatalogByIDForUpdate :one
SELECT * FROM catalog_changes WHERE id=$1 FOR UPDATE;
-- name: CatalogByNameForUpdate :one
SELECT * FROM catalog_changes WHERE name=$1 FOR UPDATE;
-- name: InsertBlock :one
INSERT INTO guidance_blocks(id, name,tags,latest_version,origin,created_at,updated_at,tier,conflicts)
VALUES(sqlc.arg(id), sqlc.arg(name),sqlc.arg(tags),1,sqlc.arg(origin),sqlc.arg(at),sqlc.arg(at),sqlc.arg(tier),sqlc.arg(conflicts)) RETURNING *;
-- name: InsertBlockVersion :one
INSERT INTO guidance_block_versions(id, block_id,version,body_md,checks_json,created_at)
VALUES(sqlc.arg(id), sqlc.arg(block_id),sqlc.arg(version),sqlc.arg(body),sqlc.arg(checks),sqlc.arg(at)) RETURNING *;
-- name: SetBlockTags :execrows
UPDATE guidance_blocks SET tags=sqlc.arg(tags),updated_at=sqlc.arg(at) WHERE id=sqlc.arg(id);
-- name: AdvanceBlockVersion :execrows
UPDATE guidance_blocks SET latest_version=sqlc.arg(version),updated_at=sqlc.arg(at) WHERE id=sqlc.arg(id);
-- name: AdvanceStockBlockVersion :execrows
UPDATE guidance_blocks SET latest_version=sqlc.arg(version),tags=sqlc.arg(tags),tier=sqlc.arg(tier),conflicts=sqlc.arg(conflicts),updated_at=sqlc.arg(at) WHERE id=sqlc.arg(id);
-- name: RefreshStockBlockWords :execrows
UPDATE guidance_blocks SET tags=sqlc.arg(tags),tier=sqlc.arg(tier),conflicts=sqlc.arg(conflicts),updated_at=sqlc.arg(at)
WHERE name=sqlc.arg(name) AND (tags<>sqlc.arg(tags) OR tier<>sqlc.arg(tier) OR conflicts<>sqlc.arg(conflicts));
-- name: InsertWorkflow :one
INSERT INTO lib_workflows(id, name,description,latest_version,origin,updated_at)
VALUES(sqlc.arg(id), sqlc.arg(name),sqlc.arg(description),1,sqlc.arg(origin),sqlc.arg(at)) RETURNING *;
-- name: InsertWorkflowVersion :one
INSERT INTO lib_workflow_versions(id, lib_id,version,yaml,created_at)
VALUES(sqlc.arg(id), sqlc.arg(lib_id),sqlc.arg(version),sqlc.arg(yaml),sqlc.arg(at)) RETURNING *;
-- name: AdvanceWorkflowVersion :execrows
UPDATE lib_workflows SET latest_version=sqlc.arg(version),description=sqlc.arg(description),updated_at=sqlc.arg(at) WHERE id=sqlc.arg(id);
-- name: RefreshWorkflowWords :execrows
UPDATE lib_workflows SET description=$1 WHERE name=$2 AND description<>$1;
-- name: DeleteWorkflowVersions :execrows
DELETE FROM lib_workflow_versions WHERE lib_id=$1;
-- name: DeleteWorkflow :execrows
DELETE FROM lib_workflows WHERE id=$1;
-- name: BindSystemWorkflow :one
INSERT INTO system_workflows(stock_name,name,updated_at) VALUES(sqlc.arg(stock_name),sqlc.arg(name),sqlc.arg(at))
ON CONFLICT(stock_name) DO UPDATE SET name=excluded.name,updated_at=excluded.updated_at RETURNING *;
-- name: ResetSystemWorkflow :execrows
DELETE FROM system_workflows WHERE stock_name=$1;
-- name: InsertPack :one
INSERT INTO packs(id, name,description,spec_json,origin,updated_at,latest_version,recommended_version)
VALUES(sqlc.arg(id), sqlc.arg(name),sqlc.arg(description),sqlc.arg(spec),sqlc.arg(origin),sqlc.arg(at),1,1) RETURNING *;
-- name: InsertPackVersion :one
INSERT INTO pack_versions(id, pack_id,version,spec_json,note,created_at,pins_json,declaration_json,included_pack,included_version,name,based_on)
VALUES(sqlc.arg(id), sqlc.arg(pack_id),sqlc.arg(version),sqlc.arg(spec),sqlc.arg(note),sqlc.arg(at),sqlc.arg(pins),sqlc.arg(declaration),sqlc.arg(included_pack),sqlc.arg(included_version),sqlc.arg(name),sqlc.arg(based_on)) RETURNING *;
-- name: RecommendPackVersion :execrows
UPDATE packs SET recommended_version=sqlc.arg(version),updated_at=sqlc.arg(at) WHERE id=sqlc.arg(id);
-- name: OfferedPacks :many
-- Each pack with the resolved contents of the publication it offers.
SELECT sqlc.embed(p),COALESCE(v.spec_json,p.spec_json) AS offered_spec,COALESCE(v.version,0)::bigint AS offered_version
FROM packs p LEFT JOIN pack_versions v ON v.pack_id=p.id AND v.version=p.recommended_version ORDER BY p.origin DESC,p.name;
-- name: OfferedPack :one
SELECT sqlc.embed(p),COALESCE(v.spec_json,p.spec_json) AS offered_spec,COALESCE(v.version,0)::bigint AS offered_version
FROM packs p LEFT JOIN pack_versions v ON v.pack_id=p.id AND v.version=p.recommended_version WHERE p.name=$1;
-- name: PacksIncluding :many
-- Packs whose latest publication resolved against the named pack.
SELECT p.* FROM packs p JOIN pack_versions v ON v.pack_id=p.id AND v.version=p.latest_version
WHERE v.included_pack=sqlc.arg(included)::text ORDER BY p.name FOR UPDATE OF p;
-- name: RefreshPackContents :execrows
UPDATE packs SET description=sqlc.arg(description),spec_json=sqlc.arg(spec) WHERE id=sqlc.arg(id);
-- name: AdvancePackVersion :execrows
UPDATE packs SET description=sqlc.arg(description),spec_json=sqlc.arg(spec),updated_at=sqlc.arg(at),latest_version=sqlc.arg(version) WHERE id=sqlc.arg(id);
-- name: RefreshStockPackWords :execrows
UPDATE packs SET description=$1 WHERE name=$2 AND origin='stock' AND description<>$1;
-- name: SetPackEnabled :execrows
UPDATE packs SET enabled=sqlc.arg(enabled),updated_at=sqlc.arg(at) WHERE name=sqlc.arg(name);
-- name: DeletePack :execrows
DELETE FROM packs WHERE name=$1;
-- name: InsertCatalogChange :one
INSERT INTO catalog_changes(id, name,description,latest_version,promoted_version,promoted_by,promoted_at,promotion_note,created_at,updated_at)
VALUES(sqlc.arg(id), sqlc.arg(name),sqlc.arg(description),1,0,'',NULL,'',sqlc.arg(at),sqlc.arg(at)) RETURNING *;
-- name: InsertCatalogVersion :one
INSERT INTO catalog_change_versions(id, catalog_id,version,definition_json,created_at)
VALUES(sqlc.arg(id), sqlc.arg(catalog_id),sqlc.arg(version),sqlc.arg(definition),sqlc.arg(at)) RETURNING *;
-- name: DescribeCatalogChange :execrows
UPDATE catalog_changes SET description=sqlc.arg(description),updated_at=sqlc.arg(at) WHERE id=sqlc.arg(id);
-- name: AdvanceCatalogVersion :execrows
UPDATE catalog_changes SET latest_version=sqlc.arg(version),updated_at=sqlc.arg(at) WHERE id=sqlc.arg(id);
-- name: PromoteCatalogChange :execrows
UPDATE catalog_changes SET promoted_version=sqlc.arg(version),promoted_by=sqlc.arg(actor),promoted_by_principal=sqlc.arg(actor_principal),promoted_by_via=sqlc.arg(actor_via),promoted_at=sqlc.arg(at),promotion_note=sqlc.arg(note),updated_at=sqlc.arg(at) WHERE id=sqlc.arg(id);
-- name: InsertLibraryPolicy :one
INSERT INTO policy_library(id, name,description,rule,config_json,origin,updated_at)
VALUES(sqlc.arg(id), sqlc.arg(name),sqlc.arg(description),sqlc.arg(rule),sqlc.arg(config),sqlc.arg(origin),sqlc.arg(at)) RETURNING *;
-- name: EditLibraryPolicy :execrows
UPDATE policy_library SET description=sqlc.arg(description),rule=sqlc.arg(rule),config_json=sqlc.arg(config),updated_at=sqlc.arg(at) WHERE name=sqlc.arg(name) AND origin=sqlc.arg(origin);
-- name: RefreshStockPolicyWords :execrows
UPDATE policy_library SET description=$1 WHERE name=$2 AND origin='stock' AND description<>$1;
-- name: DeleteLibraryPolicy :execrows
DELETE FROM policy_library WHERE name=$1;
-- name: BlocksMatchingMeta :many
-- Lists need the byte count, not every guidance body, which may span megabytes.
SELECT sqlc.embed(b),octet_length(v.body_md)::bigint AS body_bytes FROM guidance_blocks b
JOIN guidance_block_versions v ON v.block_id=b.id AND v.version=b.latest_version
WHERE (sqlc.arg(query)::text='' OR b.name LIKE '%'||sqlc.arg(query)||'%' OR v.body_md LIKE '%'||sqlc.arg(query)||'%')
AND (sqlc.arg(tag)::text='' OR (','||b.tags||',') LIKE '%,'||sqlc.arg(tag)||',%') ORDER BY b.name;
-- name: BlockVersionsMeta :many
-- History lists show versions and dates without copying their potentially large bodies/checks.
SELECT block_id,version,created_at FROM guidance_block_versions WHERE block_id=$1 ORDER BY version DESC;
-- name: WorkflowCatalog :many
SELECT sqlc.embed(l),COALESCE(v.yaml,'')::text AS yaml,(v.lib_id IS NOT NULL)::boolean AS has_version
FROM lib_workflows l LEFT JOIN lib_workflow_versions v ON v.lib_id=l.id AND v.version=l.latest_version ORDER BY l.origin DESC,l.name;
-- name: OtherOfferedPackCount :one
-- Keep the stored platform flag's PostgreSQL boolean interpretation (and errors).
SELECT COUNT(*) FROM packs WHERE enabled AND name<>$1 AND NOT COALESCE((spec_json->>'platform')::boolean,false);

-- name: PackPublication :one
-- A single snapshot distinguishes a missing pack from a missing immutable
-- publication while retaining the complete publication record. JSON columns
-- coalesce at the nullable join boundary; v.id distinguishes absence.
SELECT p.id AS owner_id,v.id,v.pack_id,v.version,
 COALESCE(v.spec_json,'{}'::jsonb) AS spec_json,v.note,v.created_at,
 COALESCE(v.pins_json,'{}'::jsonb) AS pins_json,
 COALESCE(v.included_pack,'')::text AS included_pack,COALESCE(v.included_version,0)::bigint AS included_version FROM packs p
LEFT JOIN pack_versions v ON v.pack_id=p.id AND v.version=$2 WHERE p.name=$1;

-- name: CheckDefinitionsAtVersions :many
SELECT b.name,v.version,v.checks_json::text AS checks_json FROM guidance_blocks b
JOIN guidance_block_versions v ON v.block_id=b.id
WHERE (b.name,v.version) IN (SELECT unnest(sqlc.arg(blocks)::text[]),unnest(sqlc.arg(versions)::bigint[]));

-- One library version's content, for moving a project that uses it to
-- another forge, where library numbering is local: the item and the version.
-- name: ExportGuidanceVersion :one
SELECT jsonb_build_object('item', to_jsonb(b), 'version', to_jsonb(v))::text
FROM guidance_blocks b JOIN guidance_block_versions v ON v.block_id = b.id
WHERE b.name = $1 AND v.version = $2;

-- name: ExportWorkflowVersion :one
SELECT jsonb_build_object('item', to_jsonb(w), 'version', to_jsonb(v))::text
FROM lib_workflows w JOIN lib_workflow_versions v ON v.lib_id = w.id
WHERE w.name = $1 AND v.version = $2;

-- name: ExportPackVersion :one
SELECT jsonb_build_object('item', to_jsonb(p), 'version', to_jsonb(v))::text
FROM packs p JOIN pack_versions v ON v.pack_id = p.id
WHERE p.id = $1 AND v.version = $2;
