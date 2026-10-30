-- Artifacts: what steps and runs attach as evidence.

-- name: AssertionArtifact :one
SELECT id,kind,name,ref FROM artifacts
WHERE run_id=sqlc.arg(run_id) AND kind<>'agent-reported' AND (sqlc.arg(kind)::text='' OR kind=sqlc.arg(kind)) AND (sqlc.arg(name)::text='' OR name=sqlc.arg(name))
ORDER BY id DESC LIMIT 1 FOR SHARE;

-- name: CompletionCopyChildArtifacts :execrows
-- The copies' ids are minted by the caller, one per source row in id order.
WITH src AS (SELECT a.kind,a.name,a.ref,a.body,a.body_bytes,row_number() OVER (ORDER BY a.id) AS n FROM artifacts a WHERE a.run_id=sqlc.arg(child_run_id) AND a.kind IN ('release','preview','mr','sbom'))
INSERT INTO artifacts (id,run_id,kind,name,ref,body,body_bytes,created_at)
SELECT (sqlc.arg(new_ids)::uuid[])[src.n],sqlc.arg(parent_run_id),src.kind,src.name,src.ref,src.body,src.body_bytes,sqlc.arg(created_at) FROM src ORDER BY src.n;

-- name: ContinuationCopyArtifacts :execrows
-- The copies' ids are minted by the caller, one per source row in id order.
WITH src AS (SELECT a.kind,a.name,a.ref,a.body,a.body_bytes,a.created_at,row_number() OVER (ORDER BY a.id) AS n FROM artifacts a WHERE a.run_id=sqlc.arg(source_id))
INSERT INTO artifacts(id,run_id,kind,name,ref,body,body_bytes,created_at) SELECT (sqlc.arg(new_ids)::uuid[])[src.n],sqlc.arg(run_id),src.kind,src.name,src.ref,src.body,src.body_bytes,src.created_at FROM src ORDER BY src.n;

-- name: DeleteProjectArtifacts :exec
DELETE FROM artifacts WHERE run_id IN (SELECT id FROM runs WHERE project_id=$1);

-- name: InsertBinaryArtifact :one
INSERT INTO artifacts(id, run_id,kind,name,ref,body,body_bytes,created_at)
VALUES(sqlc.arg(id), sqlc.arg(run_id),sqlc.arg(kind),sqlc.arg(name),sqlc.arg(ref),'',sqlc.arg(body_bytes),sqlc.arg(created_at)) RETURNING *;

-- name: InsertTextArtifact :one
INSERT INTO artifacts(id, run_id,kind,name,ref,body,created_at)
VALUES(sqlc.arg(id), sqlc.arg(run_id),sqlc.arg(kind),sqlc.arg(name),sqlc.arg(ref),sqlc.arg(body),sqlc.arg(created_at)) RETURNING *;

-- name: ReplaceSteeringArtifact :exec
UPDATE artifacts SET body=sqlc.arg(body),ref=sqlc.arg(ref)
WHERE run_id=sqlc.arg(run_id) AND kind='steering' AND name=sqlc.arg(name);

-- Text report, intentionally excludes binary body_bytes.
-- name: AcceptedScanReports :many
SELECT DISTINCT ON (a.name) a.id,a.run_id,a.kind,a.name,a.ref,a.body,a.created_at
FROM artifacts a JOIN runs r ON r.id=a.run_id WHERE r.project_id=$1 AND r.status=$2 AND a.kind='scan-report' ORDER BY a.name,a.id DESC;

-- Scope reads are metadata-only before evidence authorization.
-- name: ArtifactRunScopeMeta :one
SELECT a.run_id,r.project_id FROM artifacts a JOIN runs r ON r.id=a.run_id WHERE a.id=$1;

-- name: CompletionChildArtifactCount :one
SELECT count(*) FROM artifacts WHERE run_id=$1 AND kind IN ('release','preview','mr','sbom');

-- name: ContinuationArtifactCount :one
SELECT count(*) FROM artifacts WHERE run_id=$1;

-- name: ExistingArtifactKinds :many
SELECT DISTINCT a.kind FROM artifacts a JOIN runs r ON r.id=a.run_id
WHERE r.project_id=sqlc.arg(project_id) AND a.kind<>'agent-reported' AND a.kind=ANY(sqlc.arg(kinds)::text[]);

-- Artifact identity only; sbom bodies can be large.
-- name: LatestSBOMIDBeforeMeta :many
SELECT a.id FROM artifacts a JOIN runs r ON r.id=a.run_id
WHERE r.project_id=$1 AND a.kind='sbom' AND a.created_at<=$2 ORDER BY a.id DESC LIMIT 1;

-- name: NarrativeArtifacts :many
SELECT a.kind,a.body FROM artifacts a JOIN runs r ON a.run_id=r.id WHERE r.change_id IS NOT DISTINCT FROM $1 AND r.project_id=$2 AND (a.kind LIKE '%report' OR a.kind='plan') AND a.kind NOT LIKE '%scan%' ORDER BY a.id DESC LIMIT 2;

-- name: NarrativeAgentReport :one
-- The agent's own report: a claim the summary may read, never evidence.
SELECT a.body FROM artifacts a JOIN runs r ON a.run_id=r.id WHERE r.change_id IS NOT DISTINCT FROM $1 AND r.project_id=$2 AND a.kind='agent-reported' ORDER BY a.id DESC LIMIT 1;

-- name: ReadArtifacts :many
SELECT id,run_id,kind,name,ref,
 (CASE WHEN sqlc.arg(preview_chars)::integer>0 THEN left(body,sqlc.arg(preview_chars)::integer) ELSE body END)::text AS body,
 COALESCE(octet_length(body_bytes),octet_length(body))::bigint AS body_size,
 created_at,(body_bytes IS NOT NULL)::boolean AS binary,
 (CASE WHEN sqlc.arg(include_binary)::boolean THEN body_bytes ELSE NULL::bytea END)::bytea AS raw_body
FROM artifacts
WHERE (sqlc.narg(artifact_id)::uuid IS NULL OR id=sqlc.narg(artifact_id))
  AND (sqlc.narg(run_id)::uuid IS NULL OR run_id=sqlc.narg(run_id))
ORDER BY id;

-- name: SelectedTextArtifacts :many
SELECT a.id,a.run_id,a.kind,a.name,a.ref,a.body,a.created_at
FROM artifacts a JOIN runs r ON r.id=a.run_id
WHERE r.project_id=sqlc.arg(project_id) AND (sqlc.narg(run_id)::uuid IS NULL OR r.id=sqlc.narg(run_id))
AND (sqlc.narg(change_id)::uuid IS NULL OR (r.change_id=sqlc.narg(change_id) AND EXISTS(SELECT 1 FROM changes c WHERE c.id=sqlc.narg(change_id) AND c.project_id=sqlc.arg(project_id))))
AND (cardinality(sqlc.arg(kinds)::text[])=0 OR a.kind=ANY(sqlc.arg(kinds)::text[])) AND (sqlc.arg(name)::text='' OR a.name=sqlc.arg(name))
ORDER BY CASE WHEN sqlc.arg(latest)::boolean THEN a.id END DESC,CASE WHEN NOT sqlc.arg(latest)::boolean THEN a.id END ASC LIMIT NULLIF(sqlc.arg(limit_count)::integer,0);

-- Presence-only prerequisite report; never load artifact bodies. Array indexes
-- preserve duplicates/order, as WITH ORDINALITY did before sqlc conversion.
-- name: MissingArtifactKinds :many
SELECT (sqlc.arg(kinds)::text[])[i]::text AS kind
FROM generate_subscripts(sqlc.arg(kinds)::text[],1) AS requested(i)
WHERE NOT EXISTS(SELECT 1 FROM artifacts a JOIN runs r ON r.id=a.run_id
 WHERE r.project_id=sqlc.arg(project_id) AND a.kind<>'agent-reported' AND a.kind=(sqlc.arg(kinds)::text[])[i]) ORDER BY i;
