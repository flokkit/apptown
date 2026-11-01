-- What AppTown created for a preview (preview_owned): today the AI models
-- key a preview of an app needing llm gets.

-- name: RecordPreviewOwned :execrows
INSERT INTO preview_owned (id, preview, project_id, kind, ident, name, recorded_at)
  VALUES (sqlc.arg(id), sqlc.arg(preview),sqlc.arg(project_id),sqlc.arg(kind),sqlc.arg(ident),sqlc.arg(name),sqlc.arg(recorded_at))
  ON CONFLICT (preview, kind, ident) DO NOTHING;

-- name: PreviewOwnedBy :many
SELECT * FROM preview_owned WHERE preview=sqlc.arg(preview) AND kind=sqlc.arg(kind) ORDER BY id;

-- name: AllPreviewOwned :many
SELECT * FROM preview_owned ORDER BY id;

-- name: ForgetPreviewOwned :execrows
DELETE FROM preview_owned WHERE preview=sqlc.arg(preview) AND kind=sqlc.arg(kind) AND ident=sqlc.arg(ident);

-- When anyone last looked at a preview (preview_views). A look never moves
-- backwards.

-- name: RecordPreviewView :exec
INSERT INTO preview_views (preview, viewed_at) VALUES (sqlc.arg(preview), sqlc.arg(viewed_at))
  ON CONFLICT (preview) DO UPDATE SET viewed_at = GREATEST(preview_views.viewed_at, EXCLUDED.viewed_at);

-- name: PreviewView :one
SELECT * FROM preview_views WHERE preview=sqlc.arg(preview);

-- name: PreviewViews :many
SELECT * FROM preview_views ORDER BY preview;

-- name: ForgetPreviewViewBefore :execrows
DELETE FROM preview_views WHERE preview=sqlc.arg(preview) AND viewed_at < sqlc.arg(before)::timestamptz;

-- name: ForgetPreviewView :execrows
DELETE FROM preview_views WHERE preview=sqlc.arg(preview);

-- name: Reading :one
SELECT identity,payload FROM preview_readings WHERE subject=$1;

-- name: StoreReading :exec
INSERT INTO preview_readings(id,subject,identity,payload) VALUES($1,$2,$3,$4)
ON CONFLICT(subject) DO UPDATE SET identity=excluded.identity,payload=excluded.payload;

-- name: PruneReadings :exec
-- A post-act check can publish after the pass selected its subjects. Keep that
-- newer reading until a later pass has had a chance to list the preview.
DELETE FROM preview_readings WHERE NOT (subject = ANY(sqlc.arg(keep)::text[]))
AND COALESCE((payload->>'attemptedAt')::timestamptz,'-infinity'::timestamptz) < sqlc.arg(before)::timestamptz;

-- name: FailReadings :exec
UPDATE preview_readings SET payload=payload || jsonb_build_object('attemptedAt',sqlc.arg(at)::timestamptz,'failure',sqlc.arg(failure)::text)
WHERE identity=sqlc.arg(identity);
