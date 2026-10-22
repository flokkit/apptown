-- Dependency catalog records (design/architecture/record-store-style.md).

-- name: DependencyKindByCapability :one
SELECT * FROM dependency_kinds WHERE capability = $1;

-- name: DependencyKinds :many
SELECT * FROM dependency_kinds ORDER BY capability;

-- name: SyncStockDependencyKind :exec
INSERT INTO dependency_kinds (capability, spec_yaml, origin, updated_at)
VALUES (sqlc.arg(capability), sqlc.arg(spec_yaml), sqlc.arg(origin), sqlc.arg(updated_at))
ON CONFLICT (capability) DO UPDATE SET spec_yaml = excluded.spec_yaml, updated_at = excluded.updated_at
WHERE dependency_kinds.origin = sqlc.arg(origin);

-- name: SaveCustomDependencyKind :one
INSERT INTO dependency_kinds (capability, spec_yaml, origin, updated_at)
VALUES (sqlc.arg(capability), sqlc.arg(spec_yaml), sqlc.arg(origin), sqlc.arg(updated_at))
ON CONFLICT (capability) DO UPDATE SET spec_yaml = excluded.spec_yaml, updated_at = excluded.updated_at
RETURNING *;

-- name: DeleteStockDependencyKindsNotShipped :many
DELETE FROM dependency_kinds
WHERE origin = sqlc.arg(origin) AND NOT (capability = ANY(sqlc.arg(shipped)::text[]))
RETURNING capability;
