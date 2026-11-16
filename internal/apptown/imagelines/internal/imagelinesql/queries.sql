-- name: Reading :one
SELECT tag, digest, registry, read_at FROM image_line_readings WHERE line = $1;

-- name: Record :exec
INSERT INTO image_line_readings (id, line, tag, digest, registry, read_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (line) DO UPDATE SET tag = EXCLUDED.tag, digest = EXCLUDED.digest,
registry = EXCLUDED.registry, read_at = EXCLUDED.read_at;
