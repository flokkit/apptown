-- name: LockBaseline :exec
SELECT pg_advisory_xact_lock(74217933);
-- name: HasBaseline :one
SELECT EXISTS(SELECT 1 FROM notification_baseline);
-- name: Baseline :exec
INSERT INTO notification_baseline(recorded_at) VALUES ($1) ON CONFLICT DO NOTHING;
-- name: Items :many
SELECT * FROM notification_items ORDER BY opened_at, id;
-- name: OpenItem :exec
INSERT INTO notification_items(id,key,title,detail,href,severity,unknown,stale,urgent,opened_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10);
-- name: RefreshItem :exec
UPDATE notification_items SET title=$2,detail=$3,href=$4,severity=$5,unknown=$6,stale=$7,urgent=$8 WHERE id=$1;
-- name: Announce :exec
UPDATE notification_items SET announced=true WHERE id=$1;
-- name: Resolve :exec
UPDATE notification_items SET resolved_at=$2 WHERE id=$1;
-- name: CreateDelivery :execrows
INSERT INTO notification_deliveries(id,channel,kind,item_key,opened_at,digest_date,body,state,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,'pending',$8) ON CONFLICT DO NOTHING;
-- name: Delivery :one
SELECT * FROM notification_deliveries WHERE id=$1 FOR UPDATE;
-- name: Recent :many
SELECT * FROM notification_deliveries ORDER BY created_at DESC,id DESC LIMIT 50;
-- name: LatestFinished :many
SELECT DISTINCT ON (channel) * FROM notification_deliveries WHERE finished_at IS NOT NULL ORDER BY channel,finished_at DESC,id DESC;
-- name: LatestStates :many
SELECT DISTINCT ON (channel) channel,state FROM notification_deliveries ORDER BY channel,created_at DESC,id DESC;
-- name: LastDigest :one
SELECT COALESCE(MAX(created_at),'epoch'::timestamptz)::timestamptz AS at FROM notification_deliveries WHERE channel=$1 AND kind='digest';
-- name: SetDeliveryState :exec
UPDATE notification_deliveries SET state=$2,attempts=$3,reason=$4,next_at=$5,finished_at=$6 WHERE id=$1;
-- name: RetainDeliveries :exec
DELETE FROM notification_deliveries WHERE created_at < $1;
-- name: RetainItems :exec
DELETE FROM notification_items WHERE resolved_at < $1;
