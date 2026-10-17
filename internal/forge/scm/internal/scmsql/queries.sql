-- name: MergeByRun :one
SELECT * FROM run_merges WHERE run_id=$1;

-- name: RemoveMerge :execrows
DELETE FROM run_merges WHERE run_id=sqlc.arg(run_id) AND state=sqlc.arg(state) AND operation=sqlc.arg(operation) AND expected=sqlc.arg(expected) AND result=sqlc.arg(result);

-- name: BeginMerge :execrows
INSERT INTO run_merges(run_id,state,operation,expected,result)
VALUES (sqlc.arg(run_id),sqlc.arg(state),sqlc.arg(operation),sqlc.arg(expected),sqlc.arg(result)) ON CONFLICT(run_id) DO NOTHING;

-- name: ReplaceMerge :execrows
UPDATE run_merges SET state=sqlc.arg(state),operation=sqlc.arg(operation),expected=sqlc.arg(expected),result=sqlc.arg(result)
WHERE run_id=sqlc.arg(run_id) AND state=sqlc.arg(previous_state) AND operation=sqlc.arg(previous_operation) AND expected=sqlc.arg(previous_expected) AND result=sqlc.arg(previous_result);

-- name: PendingMerges :many
SELECT * FROM run_merges WHERE state=$1 ORDER BY run_id;

-- name: AuthorityForUpdate :one
SELECT generation, holder FROM branch_authority WHERE project_id=$1 AND branch=$2 FOR UPDATE;

-- name: Authority :one
SELECT generation, holder FROM branch_authority WHERE project_id=$1 AND branch=$2;

-- name: EnsureAuthority :exec
INSERT INTO branch_authority(project_id,branch,generation,holder) VALUES (sqlc.arg(project_id),sqlc.arg(branch),1,'initial')
ON CONFLICT(project_id,branch) DO NOTHING;

-- name: AdvanceAuthority :one
UPDATE branch_authority SET generation=generation+1, holder=sqlc.arg(holder)
WHERE project_id=sqlc.arg(project_id) AND branch=sqlc.arg(branch)
RETURNING generation, holder;

-- name: PublicationForBranch :one
SELECT * FROM branch_publications WHERE project_id=$1 AND branch=$2;

-- name: BeginPublication :one
INSERT INTO branch_publications(id, project_id,branch,repository,generation,expected,proposed,state,created_at)
VALUES (sqlc.arg(id), sqlc.arg(project_id),sqlc.arg(branch),sqlc.arg(repository),sqlc.arg(generation),sqlc.arg(expected),sqlc.arg(proposed),'pending',sqlc.arg(created_at))
ON CONFLICT (project_id, branch) DO NOTHING RETURNING id;

-- name: MarkPublicationUnknown :execrows
UPDATE branch_publications SET state='unknown' WHERE id=$1;

-- name: SettlePublication :execrows
DELETE FROM branch_publications WHERE id=$1;

-- name: UnsettledPublications :many
SELECT * FROM branch_publications ORDER BY id;

-- name: RecordedMain :one
SELECT * FROM project_main WHERE project_id=$1;

-- name: InsertMain :execrows
INSERT INTO project_main(project_id,branch,sha,via,run_id,recorded_at)
VALUES (sqlc.arg(project_id),sqlc.arg(branch),sqlc.arg(sha),sqlc.arg(via),sqlc.arg(run_id),sqlc.arg(at)::timestamptz)
ON CONFLICT(project_id) DO NOTHING;

-- name: AdvanceMain :execrows
UPDATE project_main SET branch=sqlc.arg(branch),sha=sqlc.arg(sha),via=sqlc.arg(via),run_id=sqlc.arg(run_id),recorded_at=sqlc.arg(at)::timestamptz
WHERE project_id=sqlc.arg(project_id) AND sha=sqlc.arg(previous);
