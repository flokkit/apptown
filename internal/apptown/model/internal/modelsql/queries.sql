-- Records, grouped by table. Reads return complete rows.
-- name: ModelSlotCell :one
SELECT * FROM model_slots
WHERE slot=sqlc.arg(slot) AND project_id IS NOT DISTINCT FROM sqlc.arg(project_id) AND profile=sqlc.arg(profile);

-- name: SlotFallbackBySlot :one
SELECT * FROM slot_fallbacks WHERE slot=sqlc.arg(slot);

-- name: ModelSlotsInstance :many
SELECT * FROM model_slots WHERE project_id IS NULL;

-- name: Fallbacks :many
SELECT * FROM slot_fallbacks;

-- name: Overrides :many
SELECT m.slot, m.pattern, p.slug AS project_slug
FROM model_slots m JOIN projects p ON p.id=m.project_id
WHERE m.project_id IS NOT NULL;

-- name: Profiles :many
SELECT DISTINCT profile AS profile_name FROM model_slots WHERE project_id IS NULL AND profile != ''
UNION SELECT DISTINCT model_profile AS profile_name FROM projects WHERE model_profile != ''
ORDER BY profile_name;

-- name: InsertFallback :exec
INSERT INTO slot_fallbacks (slot, fallback) VALUES (sqlc.arg(slot), sqlc.arg(fallback))
ON CONFLICT (slot) DO NOTHING;

-- name: SetDefaultSlot :exec
INSERT INTO model_slots (slot, project_id, profile, pattern, updated_at)
VALUES ('default', NULL, sqlc.arg(profile), sqlc.arg(pattern), sqlc.arg(updated_at))
ON CONFLICT(slot, project_id, profile) DO UPDATE
SET pattern=excluded.pattern, updated_at=excluded.updated_at;

-- name: DeleteSlotCell :execrows
DELETE FROM model_slots
WHERE slot=sqlc.arg(slot) AND project_id IS NOT DISTINCT FROM sqlc.arg(project_id) AND profile=sqlc.arg(profile);

-- name: UpsertSlotCell :execrows
INSERT INTO model_slots (slot, project_id, profile, pattern, updated_at)
VALUES (sqlc.arg(slot), sqlc.arg(project_id), sqlc.arg(profile), sqlc.arg(pattern), sqlc.arg(updated_at))
ON CONFLICT(slot, project_id, profile) DO UPDATE
SET pattern=excluded.pattern, updated_at=excluded.updated_at;

-- name: RemoveProjectSlots :execrows
DELETE FROM model_slots WHERE project_id=sqlc.arg(project_id);

-- name: ProjectOverrides :many
SELECT slot, pattern FROM model_slots WHERE project_id = $1 AND profile = '' ORDER BY slot;
