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

-- name: RelocatePublication :execrows
UPDATE branch_publications SET repository=sqlc.arg(destination)
WHERE id=sqlc.arg(id) AND project_id=sqlc.arg(project_id)
 AND branch=sqlc.arg(branch) AND expected=sqlc.arg(expected) AND proposed=sqlc.arg(proposed)
 AND repository IN (sqlc.arg(source),sqlc.arg(destination));

-- name: RepositoryAttachment :one
SELECT * FROM repository_attachments WHERE project_id=$1;

-- name: RecordRepositoryAttachment :execrows
INSERT INTO repository_attachments(id,project_id,connection_id,provider_kind,provider_base,provider_account,provider_project,clone_url,project_path,verified_at)
VALUES ($1,$2,$3,'gitlab',$4,$5,$6,$7,$8,$9) ON CONFLICT(project_id) DO NOTHING;

-- name: RepositoryAttachmentIntent :one
SELECT * FROM repository_attachment_intents WHERE project_id=$1;

-- name: AdmitRepositoryAttachment :execrows
INSERT INTO repository_attachment_intents(id,request_id,project_id,actor_id,parameters,connection_id,provider_base,provider_account,prior_origin,phase,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'intent',$10,$10) ON CONFLICT(project_id) DO NOTHING;

-- name: DispatchRepositoryAttachment :execrows
UPDATE repository_attachment_intents SET phase='dispatched',updated_at=$3 WHERE id=$1 AND project_id=$2 AND phase='intent';

-- name: IdentifyRepositoryAttachment :execrows
UPDATE repository_attachment_intents SET phase='identified',provider_result=$3,updated_at=$4 WHERE id=$1 AND project_id=$2 AND phase IN ('intent','dispatched','unknown');

-- name: CompleteRepositoryAttachment :execrows
UPDATE repository_attachment_intents SET phase='attached',updated_at=$3 WHERE id=$1 AND project_id=$2 AND phase='identified';

-- name: RecordMergeRequestIdentity :exec
INSERT INTO repository_mr_identities(artifact_id,identity_json) VALUES ($1,$2);

-- name: MergeRequestIdentity :one
SELECT identity_json FROM repository_mr_identities WHERE artifact_id=$1;
