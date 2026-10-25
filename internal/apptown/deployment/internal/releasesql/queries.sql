-- name: AppendReleaseEvent :execrows
INSERT INTO release_events(id, release_id, at, text) VALUES ($4, $1, $2, $3);

-- name: ReleaseForUpdate :one
SELECT id, project_id, number, deployment_id, generation, run_id, state, deployment,
  approved_by, commit_sha, external_ref, artifact_hash FROM releases WHERE id = $1 FOR UPDATE;

-- name: DeploymentInputs :one
SELECT release_id, release_run, revision, image_digest, artifact_hash, docker_source, rebuild, bundle_id, applied, ingress_json
FROM deployment_attempt_inputs WHERE attempt_id = $1;

-- name: InsertDeploymentInputs :execrows
INSERT INTO deployment_attempt_inputs(attempt_id, release_id, release_run, revision,
  image_digest, artifact_hash, docker_source, rebuild, bundle_id, applied, ingress_json) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: SettleReleaseAttempt :execrows
UPDATE releases SET state = sqlc.arg(state), deployment_id = sqlc.arg(deployment_id), generation = sqlc.arg(generation),
  released_at = CASE WHEN sqlc.arg(released)::boolean THEN COALESCE(released_at,sqlc.arg(at)::timestamptz) ELSE released_at END,
  notes = CASE WHEN sqlc.arg(released)::boolean THEN sqlc.arg(notes)::text ELSE notes END,
  artifact_hash = CASE WHEN sqlc.arg(released)::boolean THEN sqlc.arg(artifact_hash)::text ELSE artifact_hash END,
  external_ref = CASE WHEN sqlc.arg(released)::boolean THEN sqlc.arg(external_ref)::text ELSE external_ref END
WHERE id=sqlc.arg(id) AND state=sqlc.arg(open_state);

-- name: ReleaseDeploymentID :one
SELECT deployment_id FROM releases WHERE id = $1;

-- name: LockDeployment :one
SELECT id FROM deployments WHERE id = $1 FOR UPDATE;

-- name: HasReleaseAttempt :one
SELECT EXISTS(SELECT 1 FROM deployment_attempt_inputs WHERE release_id = $1);

-- name: FailUnstartedRelease :execrows
UPDATE releases SET state = sqlc.arg(state) WHERE id=sqlc.arg(id);

-- name: LockReleaseProject :one
SELECT id FROM projects WHERE id=$1 FOR UPDATE;

-- name: ReleaseInFlight :one
SELECT EXISTS(SELECT 1 FROM releases WHERE project_id=sqlc.arg(project_id) AND state=sqlc.arg(open_state)
  AND (sqlc.arg(project_wide)::boolean OR deployment_id=sqlc.arg(deployment_id)));

-- name: CreateRelease :one
INSERT INTO releases(id, project_id,number,state,deployment,approved_by,run_id,created_at,released_at,
  notes,commit_sha,source_sha,bundle_id,deployment_id)
VALUES (sqlc.arg(id), sqlc.arg(project_id),
  (SELECT COALESCE(MAX(r.number),0)+1 FROM releases r WHERE r.project_id=sqlc.arg(project_id)),
  sqlc.arg(state),
  sqlc.arg(deployment),sqlc.arg(actor),sqlc.arg(run_id),sqlc.arg(at)::timestamptz,
  CASE WHEN sqlc.arg(from_workflow)::boolean THEN sqlc.arg(at)::timestamptz ELSE NULL END,
  sqlc.arg(notes),sqlc.arg(revision),sqlc.arg(revision),
  CASE
    WHEN sqlc.arg(from_workflow)::boolean THEN (SELECT CASE WHEN (b.built_from_json->>'runId')::uuid=sqlc.arg(run_id)::uuid THEN b.id END
      FROM release_bundles b WHERE b.project_id=sqlc.arg(project_id) ORDER BY b.id DESC LIMIT 1)
    WHEN sqlc.arg(from_branch)::boolean THEN (SELECT b.id FROM release_bundles b WHERE b.project_id=sqlc.arg(project_id)
      AND b.contract_revision=sqlc.arg(revision) ORDER BY b.id DESC LIMIT 1)
    ELSE sqlc.arg(bundle_id)::uuid END,
  CASE WHEN sqlc.arg(from_workflow)::boolean THEN (SELECT d.id FROM deployments d
    WHERE d.project_id=sqlc.arg(project_id) AND d.name='main') ELSE sqlc.arg(deployment_id)::uuid END)
RETURNING id,number;

-- name: LinkReleaseChanges :one
WITH selected AS MATERIALIZED (
  SELECT c.id FROM changes c WHERE c.project_id=sqlc.arg(project_id) AND c.state='shipped' AND
  CASE
    WHEN sqlc.arg(from_workflow)::boolean THEN c.id=sqlc.arg(change_id)::uuid
    WHEN sqlc.arg(from_bundle)::boolean THEN c.closed_at<=(SELECT b.created_at FROM release_bundles b WHERE b.id=sqlc.arg(bundle_id)::uuid)
    WHEN sqlc.arg(from_branch)::boolean THEN c.closed_at>COALESCE((SELECT MAX(r.created_at) FROM releases r
      WHERE r.project_id=sqlc.arg(project_id) AND r.deployment=sqlc.arg(deployment)::text AND r.state=sqlc.arg(released_state)),'epoch'::timestamptz)
    ELSE false END
), inserted AS (
  INSERT INTO release_changes (release_id,change_id) SELECT sqlc.arg(release_id)::uuid,id FROM selected RETURNING change_id
)
SELECT (SELECT COUNT(*) FROM selected) AS selected_count, (SELECT COUNT(*) FROM inserted) AS inserted_count;

-- name: FillChangeReleaseNotes :one
WITH selected AS MATERIALIZED (
  SELECT r.id FROM releases r WHERE r.project_id=sqlc.arg(project_id) AND r.notes=''
    AND EXISTS(SELECT 1 FROM release_changes rc WHERE rc.release_id=r.id AND rc.change_id IS NOT DISTINCT FROM sqlc.arg(change_id))
  FOR UPDATE
), updated AS (
  UPDATE releases SET notes=sqlc.arg(note)::text WHERE id IN(SELECT id FROM selected) RETURNING id
)
SELECT (SELECT COUNT(*) FROM selected) AS selected_count, (SELECT COUNT(*) FROM updated) AS updated_count;

-- name: AppliedByRelease :one
SELECT applied FROM deployment_attempt_inputs WHERE release_id = $1;

-- What the newest released release on a deployment says runs there.
-- name: LatestReleasedRef :one
SELECT external_ref FROM releases WHERE deployment_id=sqlc.arg(deployment_id) AND state=sqlc.arg(released)
ORDER BY number DESC LIMIT 1;

-- A release found running after AppTown was restored, which AppTown did
-- not ship: no bundle, run or approver, released when observed.
-- name: RecordObservedRelease :one
INSERT INTO releases(id, project_id, number, state, deployment, deployment_id, generation, external_ref, notes, created_at, released_at, origin)
VALUES (sqlc.arg(id), sqlc.arg(project_id),
  (SELECT COALESCE(MAX(r.number),0)+1 FROM releases r WHERE r.project_id=sqlc.arg(project_id)),
  sqlc.arg(state), sqlc.arg(deployment), sqlc.arg(deployment_id), sqlc.arg(generation), sqlc.arg(external_ref), sqlc.arg(notes),
  sqlc.arg(at)::timestamptz, sqlc.arg(at)::timestamptz, sqlc.arg(origin))
RETURNING number;

-- name: NewestReleasedOrigin :one
SELECT origin FROM releases WHERE deployment_id=sqlc.arg(deployment_id) AND state=sqlc.arg(released)
ORDER BY number DESC LIMIT 1;

-- name: NewestShippedBundle :one
SELECT bundle_id FROM releases WHERE deployment_id=sqlc.arg(deployment_id) AND state=sqlc.arg(released)
  AND origin=sqlc.arg(shipped) AND bundle_id IS NOT NULL
ORDER BY number DESC LIMIT 1;
