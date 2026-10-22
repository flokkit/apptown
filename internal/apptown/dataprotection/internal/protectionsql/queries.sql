-- Data protection's records: backup sets and the backup store, drills,
-- rehearsals and their places, restored copies and their tickets.

-- name: BackupByID :one
SELECT * FROM backups WHERE project_id=sqlc.arg(project_id) AND deployment=sqlc.arg(deployment) AND id=sqlc.arg(id);

-- name: BackupInProject :one
SELECT * FROM backups WHERE project_id=sqlc.arg(project_id) AND id=sqlc.arg(id);

-- name: BackupsForDeployment :many
SELECT * FROM backups WHERE project_id=sqlc.arg(project_id) AND deployment=sqlc.arg(deployment) ORDER BY id DESC LIMIT sqlc.arg(row_limit)::bigint;

-- name: PrunableBackups :many
-- Counted across both places (AppTown's disk and the backup store): a
-- deployment keeps its newest sets wherever they are.
SELECT * FROM backups WHERE project_id=sqlc.arg(project_id) AND deployment=sqlc.arg(deployment) AND status IN (sqlc.arg(ok_state),sqlc.arg(partial_state)) AND trigger<>sqlc.arg(final_trigger) ORDER BY id DESC OFFSET sqlc.arg(keep_rows)::bigint;

-- name: FinalStoreSets :many
SELECT * FROM backups WHERE project_id=sqlc.arg(project_id) AND store=sqlc.arg(store) AND trigger=sqlc.arg(final_trigger) AND status=sqlc.arg(ok_state) ORDER BY id DESC;

-- name: SetsOnDisk :many
SELECT * FROM backups WHERE store=sqlc.arg(disk) AND status IN (sqlc.arg(ok_state),sqlc.arg(partial_state)) ORDER BY id;

-- name: MoveBackupToStore :execrows
UPDATE backups SET store=sqlc.arg(store), path=sqlc.arg(prefix), store_endpoint=sqlc.arg(endpoint), store_bucket=sqlc.arg(bucket)
  WHERE id=sqlc.arg(id) AND store=sqlc.arg(disk) AND path=sqlc.arg(path) AND status IN (sqlc.arg(ok_state),sqlc.arg(partial_state));

-- name: BackupByIDAnywhere :one
SELECT * FROM backups WHERE id=sqlc.arg(id);

-- name: BackupStore :one
SELECT * FROM backup_store WHERE id=1;

-- name: PutBackupStore :one
INSERT INTO backup_store (id, endpoint, region, bucket, access_key_from, secret_key_from, attached_by, attached_by_principal, attached_by_via, attached_at, checked_at)
  VALUES (1, sqlc.arg(endpoint), sqlc.arg(region), sqlc.arg(bucket), sqlc.arg(access_key_from), sqlc.arg(secret_key_from), sqlc.arg(attached_by), sqlc.arg(attached_by_principal), sqlc.arg(attached_by_via), sqlc.arg(at), sqlc.arg(at))
  ON CONFLICT (id) DO UPDATE SET endpoint=excluded.endpoint, region=excluded.region, bucket=excluded.bucket, access_key_from=excluded.access_key_from,
    secret_key_from=excluded.secret_key_from, attached_by=excluded.attached_by, attached_by_principal=excluded.attached_by_principal, attached_by_via=excluded.attached_by_via, attached_at=excluded.attached_at, checked_at=excluded.checked_at
  RETURNING *;

-- drilled_at is AppTown's clock, which judges when the next drill is due.
-- name: InsertBackupDrill :one
INSERT INTO backup_drills (id, backup_id, project_id, deployment, need, passed, target, place, as_user, detail, operation_id, deployment_id, drilled_at)
  VALUES (sqlc.arg(id), sqlc.arg(backup_id), sqlc.arg(project_id), sqlc.arg(deployment), sqlc.arg(need), sqlc.arg(passed), sqlc.arg(target), sqlc.arg(place), sqlc.arg(as_user), sqlc.arg(detail), sqlc.arg(operation_id), sqlc.arg(deployment_id), sqlc.arg(drilled_at))
  RETURNING *;

-- An item's own last drill: a drill fills each item of a set in turn, so
-- the deployment's newest row is only its last item's.
-- name: LatestDrillOf :one
SELECT * FROM backup_drills WHERE deployment_id=sqlc.arg(deployment_id) AND deployment_id IS NOT NULL AND need=sqlc.arg(need) ORDER BY id DESC LIMIT 1;

-- An item's last drill of one set: whether that copy of the item was last
-- shown to come back.
-- name: LatestDrillOfSet :one
SELECT * FROM backup_drills WHERE backup_id=sqlc.arg(backup_id) AND need=sqlc.arg(need) ORDER BY id DESC LIMIT 1;

-- name: InsertRestoreRehearsal :one
INSERT INTO restore_rehearsals (id, project_id, deployment, deployment_id, operation_id, backup_id, release_number, host, outcome, failed_step, asked_by, asked_by_principal, asked_by_via, facts_json, rehearsed_at)
  VALUES (sqlc.arg(id), sqlc.arg(project_id), sqlc.arg(deployment), sqlc.arg(deployment_id), sqlc.arg(operation_id), sqlc.arg(backup_id), sqlc.arg(release_number), sqlc.arg(host), sqlc.arg(outcome), sqlc.arg(failed_step), sqlc.arg(asked_by), sqlc.arg(asked_by_principal), sqlc.arg(asked_by_via), sqlc.arg(facts_json), sqlc.arg(at))
  RETURNING *;

-- A deployment's rehearsals by the deployment the operation ran on, not
-- its name: one made again under a taken-down one's name has none.
-- name: LatestRestoreRehearsal :one
SELECT * FROM restore_rehearsals WHERE deployment_id=sqlc.arg(deployment_id) AND deployment_id IS NOT NULL ORDER BY id DESC LIMIT 1;

-- name: RecordRehearsalPlace :exec
INSERT INTO rehearsal_places (operation_id, project_id, place_json, recorded_at) VALUES (sqlc.arg(operation_id), sqlc.arg(project_id), sqlc.arg(place_json), sqlc.arg(at));

-- name: RehearsalPlaceOf :one
SELECT * FROM rehearsal_places WHERE operation_id=sqlc.arg(operation_id);

-- name: UnclearedRehearsalPlaces :many
SELECT * FROM rehearsal_places WHERE cleared_at IS NULL ORDER BY operation_id;

-- name: MarkRehearsalCleared :execrows
UPDATE rehearsal_places SET cleared_at=sqlc.arg(at)::timestamptz WHERE operation_id=sqlc.arg(operation_id) AND cleared_at IS NULL;

-- name: InsertBackup :one
INSERT INTO backups (id, project_id, deployment, taken_at, trigger, actor, actor_principal, actor_via, path, bytes, items_json, status, error, store, data_class, store_endpoint, store_bucket) VALUES (sqlc.arg(id), sqlc.arg(project_id),sqlc.arg(deployment),sqlc.arg(taken_at)::text::timestamptz,sqlc.arg(trigger),sqlc.arg(actor),sqlc.arg(actor_principal),sqlc.arg(actor_via),sqlc.arg(path),sqlc.arg(bytes),sqlc.arg(items_json),sqlc.arg(status),sqlc.arg(error),sqlc.arg(store),sqlc.arg(data_class),sqlc.arg(store_endpoint),sqlc.arg(store_bucket)) RETURNING *;

-- name: NoteBackupError :execrows
UPDATE backups SET error=btrim(error || ' ' || sqlc.arg(note)::text) WHERE id=sqlc.arg(id);

-- name: StoreSetsElsewhere :one
SELECT count(*) FROM backups WHERE store=sqlc.arg(store) AND status IN (sqlc.arg(ok_state),sqlc.arg(partial_state))
  AND NOT (store_endpoint=sqlc.arg(endpoint) AND store_bucket=sqlc.arg(bucket));

-- name: PruneBackup :one
UPDATE backups SET status=sqlc.arg(status) WHERE id=sqlc.arg(id) AND path=sqlc.arg(path) AND status IN (sqlc.arg(ok_state),sqlc.arg(partial_state),sqlc.arg(pruned_state)) RETURNING *;

-- name: ProjectBackups :many
SELECT sqlc.embed(b),p.slug FROM backups b JOIN projects p ON p.id=b.project_id ORDER BY b.id DESC LIMIT $1::bigint;

-- name: InsertRestoredCopy :one
-- The copy's number is the project's next; a concurrent copy of the same
-- project is refused by the unique number and fails before anything is made.
INSERT INTO restored_copies (id, project_id, number, source, source_id, operation_id, backup_id, asked_by, asked_by_principal, asked_by_via, app_marker, started_at)
  VALUES (sqlc.arg(id), sqlc.arg(project_id), (SELECT COALESCE(MAX(number),0)+1 FROM restored_copies WHERE project_id=sqlc.arg(project_id)), sqlc.arg(source), sqlc.arg(source_id), sqlc.arg(operation_id), sqlc.arg(backup_id), sqlc.arg(asked_by), sqlc.arg(asked_by_principal), sqlc.arg(asked_by_via), sqlc.arg(app_marker), sqlc.arg(at))
  RETURNING *;

-- name: RestoredCopyByID :one
SELECT * FROM restored_copies WHERE id=$1;

-- name: RestoredCopyByOperation :one
SELECT * FROM restored_copies WHERE operation_id=$1;

-- name: RestoredCopyByDeployment :one
SELECT * FROM restored_copies WHERE deployment_id=$1 AND deployment_id IS NOT NULL;

-- name: RestoredCopyByGateSecret :one
SELECT * FROM restored_copies WHERE gate_secret_sha256=sqlc.arg(secret_sha256) AND gate_secret_sha256<>'' AND ended_at IS NULL;

-- A source's restored copies, newest first: every one standing, however
-- many ended since (its doors are on this card only), and the
-- newest row_limit ended ones.
-- name: RestoredCopiesOf :many
SELECT c.* FROM restored_copies c WHERE c.source_id=sqlc.arg(source_id)::uuid AND (c.ended_at IS NULL OR c.id IN (
  SELECT e.id FROM restored_copies e WHERE e.source_id=sqlc.arg(source_id)::uuid AND e.ended_at IS NOT NULL ORDER BY e.id DESC LIMIT sqlc.arg(row_limit)::bigint))
  ORDER BY c.id DESC;

-- name: LatestRestoreTest :one
SELECT * FROM restored_copies WHERE source_id=sqlc.arg(source_id) AND (outcome<>'' OR ended_at IS NOT NULL) ORDER BY id DESC LIMIT 1;

-- name: PlaceRestoredCopy :execrows
UPDATE restored_copies SET deployment=sqlc.arg(deployment), deployment_id=sqlc.arg(deployment_id), release_number=sqlc.arg(release_number),
  target=sqlc.arg(target), gate_secret_sha256=sqlc.arg(secret_sha256)
  WHERE id=sqlc.arg(id) AND ended_at IS NULL;
-- name: RecordRestoredCopyURL :execrows
UPDATE restored_copies SET gate_url=sqlc.arg(gate_url) WHERE id=sqlc.arg(id) AND ended_at IS NULL;

-- name: RecordRestoredCopyCheck :execrows
UPDATE restored_copies SET outcome=sqlc.arg(outcome), failed_step=sqlc.arg(failed_step), facts_json=sqlc.arg(facts_json), checked_at=sqlc.arg(at)::timestamptz
  WHERE id=sqlc.arg(id);

-- name: EndRestoredCopy :execrows
UPDATE restored_copies SET ended_at=sqlc.arg(at)::timestamptz, end_detail=sqlc.arg(end_detail), gate_secret_sha256='' WHERE id=sqlc.arg(id) AND ended_at IS NULL;

-- name: NoteRestoredCopyLeft :execrows
UPDATE restored_copies SET end_detail=sqlc.arg(end_detail) WHERE id=sqlc.arg(id) AND ended_at IS NULL;

-- Restored copies not yet ended: the sweep reads each one's start from
-- the operations ledger and takes down those whose start ended without
-- taking them down.
-- name: OpenRestoredCopies :many
SELECT * FROM restored_copies WHERE ended_at IS NULL ORDER BY id;

-- A ticket's expiry is AppTown's clock plus its lifetime, and redemption
-- judges it by AppTown's clock too, so both sides are one clock.
-- name: InsertGateTicket :exec
INSERT INTO restored_copy_tickets (ticket_sha256, copy_id, person, person_principal, person_via, expires_at, check_operation, issued_at)
  VALUES (sqlc.arg(ticket_sha256), sqlc.arg(copy_id), sqlc.arg(person), sqlc.arg(person_principal), sqlc.arg(person_via), sqlc.arg(expires_at), sqlc.arg(check_operation), sqlc.arg(issued_at));

-- Redeeming is single-use: only an unredeemed, unexpired ticket of this
-- copy is marked, so of two redemptions at once at most one succeeds.
-- name: RedeemGateTicket :one
UPDATE restored_copy_tickets SET redeemed_at=sqlc.arg(at)::timestamptz
  WHERE ticket_sha256=sqlc.arg(ticket_sha256) AND copy_id=sqlc.arg(copy_id) AND redeemed_at IS NULL AND expires_at>sqlc.arg(at)::timestamptz
  RETURNING person, person_principal, check_operation;

-- name: StandingRestoredCopies :one
SELECT count(*) FROM restored_copies WHERE source_id=$1 AND ended_at IS NULL;

-- A purged project's sets (purge.go): every set not yet removed, and the
-- records of those removed.
-- name: ProjectBackupsToPurge :many
SELECT * FROM backups WHERE project_id = sqlc.arg(project_id) AND status <> sqlc.arg(pruned_state) ORDER BY id;

-- name: RemovePurgedBackups :execrows
DELETE FROM backups WHERE project_id=sqlc.arg(project_id) AND status = sqlc.arg(pruned_state);

-- Sets of projects deleted before projects were kept (their records gone),
-- 30 days after the deletion; a project with no deletion record counts from
-- the set itself.
-- name: OrphanBackupsDue :many
SELECT b.* FROM backups b LEFT JOIN project_deletions d ON d.project_id = b.project_id
WHERE NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = b.project_id) AND b.status <> sqlc.arg(pruned_state)
  AND COALESCE(d.requested_at, b.taken_at) + interval '30 days' <= sqlc.arg(now)
ORDER BY b.id;

-- name: RemoveOrphanBackupRecords :execrows
DELETE FROM backups b WHERE b.status = sqlc.arg(pruned_state) AND NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = b.project_id);
