-- Secrets: the built-in store's obfuscated
-- values, each deployment's bindings, and the declared profile a deployment
-- chose. Record-store style (design/architecture/record-store-style.md).

-- ===== locks ==========================================================

-- name: LockSecretValue :exec
SELECT pg_advisory_xact_lock(hashtextextended(jsonb_build_array($1::text, $2::text)::text, 0));

-- The project row is locked before a binding changes, so a shared generated
-- value is safe while another deployment binds or unbinds it. A read of
-- another domain's table, never a write (scripts/schema-ownership.py).

-- name: LockProjectForBindings :one
SELECT id FROM projects WHERE id = $1 FOR UPDATE;

-- ===== the store's key ================================================

-- name: StoreKey :one
SELECT * FROM secret_store_key WHERE singleton;

-- name: InsertStoreKey :execrows
INSERT INTO secret_store_key (singleton, value) VALUES (true, $1) ON CONFLICT (singleton) DO NOTHING;

-- ===== values =========================================================

-- name: LatestSecretValue :one
SELECT * FROM secret_values WHERE scope = $1 AND name = $2 ORDER BY version DESC LIMIT 1;

-- Every secret's current value, sealed: what masking replaces.

-- name: CurrentSecretValues :many
SELECT DISTINCT ON (scope, name) value FROM secret_values ORDER BY scope, name, version DESC;

-- The next version is computed in the statement so two writers holding the
-- scope lock in turn cannot mint the same one.

-- name: InsertSecretValue :one
INSERT INTO secret_values (id, scope, name, version, value, created_at, created_by, created_by_principal, created_by_via)
SELECT sqlc.arg(id)::uuid, sqlc.arg(scope)::text, sqlc.arg(name)::text, COALESCE(MAX(version), 0) + 1, sqlc.arg(value)::text, sqlc.arg(created_at)::timestamptz, sqlc.arg(created_by)::text, sqlc.narg(created_by_principal)::uuid, sqlc.arg(created_by_via)::text
FROM secret_values WHERE scope = sqlc.arg(scope)::text AND name = sqlc.arg(name)::text
RETURNING *;

-- A value AppTown minted for something it owns (an AI models key) goes
-- with its owner: every version of it.

-- name: DeleteSecretValue :execrows
DELETE FROM secret_values WHERE scope = $1 AND name = $2;

-- name: SecretValueVersionExists :one
SELECT EXISTS(SELECT 1 FROM secret_values WHERE scope = $1 AND name = $2 AND version::text = $3::text);

-- ===== bindings =======================================================

-- name: BindingsForDeployment :many
SELECT * FROM secret_bindings WHERE project_id = $1 AND deployment = $2;

-- name: BindingByName :one
SELECT * FROM secret_bindings WHERE project_id = $1 AND deployment = $2 AND name = $3;

-- name: UpsertBinding :one
INSERT INTO secret_bindings (project_id, deployment, name, store, ref, version, generated, updated_at, updated_by, updated_by_principal, updated_by_via)
VALUES (sqlc.arg(project_id), sqlc.arg(deployment), sqlc.arg(name), sqlc.arg(store), sqlc.arg(ref), sqlc.arg(version), sqlc.arg(generated), sqlc.arg(updated_at), sqlc.arg(updated_by), sqlc.arg(updated_by_principal), sqlc.arg(updated_by_via))
ON CONFLICT (project_id, deployment, name) DO UPDATE SET store = excluded.store, ref = excluded.ref,
  version = excluded.version, generated = excluded.generated, updated_at = excluded.updated_at,
  updated_by = excluded.updated_by, updated_by_principal = excluded.updated_by_principal, updated_by_via = excluded.updated_by_via
RETURNING *;

-- name: DeleteBinding :one
DELETE FROM secret_bindings WHERE project_id=$1 AND deployment = $2 AND name = $3 RETURNING *;

-- Unbinding the last holder of a built-in value removes its stored
-- versions. The counts are compared by the caller: a selected row that was
-- not removed means the delete did not do what the select promised.

-- name: DeleteUnboundSecretValues :one
WITH selected AS MATERIALIZED (
  SELECT v.id FROM secret_values v WHERE v.scope = $1 AND v.name = $2 AND NOT EXISTS (
    SELECT 1 FROM secret_bindings b WHERE b.store = 'internal' AND b.ref = $1 AND b.name = $2)
), removed AS (
  DELETE FROM secret_values USING selected WHERE secret_values.id = selected.id RETURNING secret_values.id
) SELECT (SELECT COUNT(*) FROM selected)::bigint AS selected, (SELECT COUNT(*) FROM removed)::bigint AS removed;

-- ===== profiles =======================================================

-- name: ChosenProfile :one
SELECT * FROM secret_profiles WHERE project_id = $1 AND deployment = $2;

-- name: SetProfile :exec
INSERT INTO secret_profiles (project_id, deployment, profile) VALUES ($1, $2, $3)
ON CONFLICT (project_id, deployment) DO UPDATE SET profile = excluded.profile;

-- ===== a project leaves ===============================================

-- name: DeleteProjectSecretValues :exec
DELETE FROM secret_values v USING secret_bindings b
WHERE b.project_id=$1 AND b.store = 'internal' AND v.scope = b.ref AND v.name = b.name
  AND NOT EXISTS (SELECT 1 FROM secret_bindings o WHERE o.store = 'internal' AND o.ref = b.ref AND o.name = b.name AND o.project_id <> b.project_id);

-- name: DeleteProjectBindings :exec
DELETE FROM secret_bindings WHERE project_id=$1;

-- name: DeleteProjectProfiles :exec
DELETE FROM secret_profiles WHERE project_id=$1;

-- Which secret names each deployment had bound, and where, for moving a
-- project to another AppTown: names only, never a value or a store's ref.
-- name: ExportBindingNames :many
SELECT deployment, name, store, generated FROM secret_bindings WHERE project_id = $1 ORDER BY deployment, name;
