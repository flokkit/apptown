
-- name: Reading :one
SELECT identity, generation, invalidated, payload FROM maintenance_readings WHERE subject=$1 AND kind=$2;

-- name: StoreReading :exec
INSERT INTO maintenance_readings (id, subject, kind, identity, payload)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (subject,kind) DO UPDATE SET identity=EXCLUDED.identity, payload=EXCLUDED.payload
WHERE maintenance_readings.identity=EXCLUDED.identity OR maintenance_readings.generation=0;

