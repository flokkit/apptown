-- Bindings are a small instance catalog. Whole records are filtered in Go for
-- capability/provider selections; identity reads retain their unique lookup.
-- name: BindingByName :one
SELECT * FROM integrations WHERE name=$1;

-- name: Bindings :many
SELECT * FROM integrations ORDER BY capability, name;

-- name: ProjectIntegration :one
SELECT * FROM project_integrations WHERE project_id=$1 AND capability=$2;

-- name: ServiceKinds :many
SELECT * FROM service_kinds ORDER BY capability;

-- name: ServiceKindByCapability :one
SELECT * FROM service_kinds WHERE capability=$1;

-- Locks preserve the operation's existing choreography.
-- name: BindingByNameForUpdate :one
SELECT * FROM integrations WHERE name=$1 FOR UPDATE;

-- name: BindingsForShare :many
SELECT * FROM integrations ORDER BY id FOR SHARE;

-- name: InsertBinding :one
INSERT INTO integrations (id, name, capability, provider, config_json, version, managed, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(name),sqlc.arg(capability),sqlc.arg(provider),sqlc.arg(config_json),1,sqlc.arg(managed),sqlc.arg(created_at),sqlc.arg(updated_at)) RETURNING *;

-- name: InsertBindingVersion :one
INSERT INTO integration_versions (id, integration_id, version, config_json, created_at)
VALUES (sqlc.arg(id), sqlc.arg(integration_id),sqlc.arg(version),sqlc.arg(config_json),sqlc.arg(created_at)) RETURNING *;

-- name: SetBindingConfiguration :one
UPDATE integrations SET config_json=sqlc.arg(config_json), version=sqlc.arg(version), updated_at=sqlc.arg(updated_at)
WHERE id=sqlc.arg(id) RETURNING *;

-- name: DeleteBinding :execrows
DELETE FROM integrations WHERE name=$1;

-- name: SaveServiceKind :one
INSERT INTO service_kinds (capability, spec_yaml, created_at, updated_at)
VALUES (sqlc.arg(capability),sqlc.arg(spec_yaml),sqlc.arg(created_at),sqlc.arg(updated_at))
ON CONFLICT(capability) DO UPDATE SET spec_yaml=excluded.spec_yaml, updated_at=excluded.updated_at RETURNING *;

-- name: DeleteServiceKind :execrows
DELETE FROM service_kinds WHERE capability=$1;

-- name: AttachIntegration :one
INSERT INTO project_integrations (project_id, capability, binding, resource_json, provenance, created_at)
VALUES (sqlc.arg(project_id),sqlc.arg(capability),sqlc.arg(binding),sqlc.arg(resource_json),sqlc.arg(provenance),sqlc.arg(created_at))
ON CONFLICT(project_id, capability) DO UPDATE SET binding=excluded.binding, resource_json=excluded.resource_json, provenance=excluded.provenance RETURNING *;

-- name: RememberIntegration :one
INSERT INTO project_integrations (project_id, capability, binding, resource_json, created_at)
VALUES (sqlc.arg(project_id),sqlc.arg(capability),sqlc.arg(binding),sqlc.arg(resource_json),sqlc.arg(created_at))
ON CONFLICT(project_id, capability) DO UPDATE SET resource_json=excluded.resource_json RETURNING *;

-- name: DetachIntegration :execrows
DELETE FROM project_integrations WHERE project_id=$1 AND capability=$2 AND provenance!='';

-- The wiring admission locks the attachment and reads its current wiring
-- change, so two requests cannot both link fresh work to one attachment.
-- name: AttachmentWireForUpdate :one
SELECT p.wire_change_id, COALESCE(c.state,'')::text AS change_state, COALESCE(c.number,0)::bigint AS change_number
FROM project_integrations p
LEFT JOIN changes c ON c.id=p.wire_change_id AND c.project_id=p.project_id
WHERE p.project_id=sqlc.arg(project_id) AND p.capability=sqlc.arg(capability) AND p.provenance<>''
FOR UPDATE OF p;

-- name: LinkIntegrationWire :execrows
UPDATE project_integrations SET wire_change_id=sqlc.arg(wire_change_id) WHERE project_id=sqlc.arg(project_id) AND capability=sqlc.arg(capability) AND provenance<>'';

-- name: DeleteProjectIntegrations :execrows
DELETE FROM project_integrations WHERE project_id=$1;

-- Reports cover attachment references across this domain's own records.
-- name: BindingAttachmentCount :one
SELECT COUNT(*) FROM project_integrations WHERE binding=$1 AND provenance!='';

-- name: KindAttachmentCount :one
SELECT COUNT(*) FROM project_integrations WHERE capability=$1 AND provenance!='';

-- name: AttachedCapabilitiesForShare :many
-- A required capability must have a real project attachment and a live matching binding.
SELECT p.capability FROM project_integrations p
JOIN integrations i ON i.name=p.binding AND i.capability=p.capability
WHERE p.project_id=sqlc.arg(project_id) AND p.capability=ANY(sqlc.arg(capabilities)::text[]) AND p.provenance<>''
ORDER BY i.id,p.capability FOR SHARE OF i,p;

-- name: MarkScannerManaged :exec
UPDATE integrations SET managed=true WHERE name=$1;

-- name: RetireConnector :one
WITH selected AS MATERIALIZED (
 SELECT i.id FROM integrations i WHERE i.name=sqlc.arg(name) AND i.capability=sqlc.arg(capability)
), removed AS (
 DELETE FROM integrations USING selected WHERE integrations.id=selected.id RETURNING integrations.id
) SELECT (SELECT COUNT(*) FROM selected) AS selected_count,(SELECT COUNT(*) FROM removed) AS removed_count;

-- This attachment report preserves the existing scoped wire-change display.
-- name: AttachedIntegrations :many
SELECT p.*, COALESCE(i.config_json,'null'::jsonb)::jsonb AS config_json,
 c.id AS change_id, COALESCE(c.number,0)::bigint AS change_number, COALESCE(c.state,'')::text AS change_state,
 COALESCE(c.title,'')::text AS change_title, COALESCE(k.spec_yaml,'')::text AS kind_yaml
FROM project_integrations p
LEFT JOIN integrations i ON i.name=p.binding AND i.capability=p.capability
LEFT JOIN changes c ON c.id=p.wire_change_id AND c.project_id=p.project_id
LEFT JOIN service_kinds k ON k.capability=p.capability
WHERE p.project_id=$1 AND p.provenance!='' ORDER BY p.capability;

-- Every project attachment of a live binding, with the project it belongs to.
-- name: BindingUsers :many
SELECT i.name AS binding, p.capability, pr.slug, pr.name AS project_name
FROM project_integrations p
JOIN integrations i ON i.name=p.binding AND i.capability=p.capability
JOIN projects pr ON pr.id=p.project_id
WHERE p.provenance<>'' ORDER BY i.name, pr.slug;

-- name: BindingByID :one
SELECT * FROM integrations WHERE id=$1;

-- name: BindingByIDForShare :one
SELECT * FROM integrations WHERE id=$1 FOR SHARE;
