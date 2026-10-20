-- Whole-row records

-- name: ChangeByID :one
SELECT * FROM changes WHERE id=sqlc.arg(id) AND (sqlc.narg(project_id)::uuid IS NULL OR project_id=sqlc.narg(project_id));

-- name: RunAbandonmentReason :one
SELECT COALESCE((SELECT payload_json->>'reason' FROM run_events
WHERE run_id=$1 AND kind='run.abandoned' ORDER BY id DESC LIMIT 1),'')::text AS reason;

-- name: VisibleChanges :many
SELECT * FROM changes WHERE project_id=ANY(sqlc.arg(project_ids)::uuid[]) AND id=ANY(sqlc.arg(ids)::uuid[]) ORDER BY id;

-- name: ProjectByID :one
SELECT * FROM projects WHERE id=$1;

-- name: RunInferenceOwnerByID :one
SELECT * FROM run_inference_owners WHERE run_id=$1;

-- name: RunStartByID :one
SELECT * FROM run_starts WHERE run_id=$1;

-- name: ChangeRuns :many
SELECT r.* FROM runs r JOIN changes c ON c.id=r.change_id AND c.project_id=r.project_id
WHERE r.project_id=sqlc.arg(project_id) AND r.change_id IS NOT DISTINCT FROM sqlc.arg(change_id)
AND (NOT sqlc.arg(roots_only)::boolean OR r.parent_run_id IS NULL)
ORDER BY r.number DESC LIMIT NULLIF(sqlc.arg(run_limit)::integer,0);

-- name: RunByID :one
SELECT * FROM runs WHERE id = sqlc.arg(id) AND (sqlc.narg(project_id)::uuid IS NULL OR project_id=sqlc.narg(project_id));

-- name: StartedChild :one
SELECT * FROM runs WHERE parent_run_id IS NOT DISTINCT FROM $1 AND parent_step_id IS NOT DISTINCT FROM $2 AND project_id = $3
  AND change_id IS NOT DISTINCT FROM $4 AND parent_step_attempt = $5 AND status != 'abandoned'
ORDER BY number DESC LIMIT 1;

-- name: ContinuationAgentStep :one
SELECT * FROM steps WHERE run_id=sqlc.arg(run_id) AND stage=sqlc.arg(stage) AND name=sqlc.arg(name) AND type='agent/pi';

-- name: LatestFeedbackStep :one
SELECT s.* FROM steps s JOIN runs r ON r.id=s.run_id
WHERE r.id=sqlc.arg(run_id) AND r.project_id=sqlc.arg(project_id)
AND ((s.type IN ('approval','agent/review','preview','guidance/conformance','tests') AND s.status=sqlc.arg(failed_state))
 OR (s.type='approval' AND s.evidence_json #>> '{lastRejection,at}' IS NOT NULL))
ORDER BY COALESCE(s.finished_at,NULLIF(s.evidence_json #>> '{lastRejection,at}','')::timestamptz) DESC,s.id DESC LIMIT 1;

-- name: LatestPersonFeedback :one
-- What a person last asked of a run's work: a send-back note, or an
-- approver's rejection comment, newest first.
SELECT f.note::text AS note, f.who::text AS who FROM (
  SELECT COALESCE(e.payload_json->>'note','') AS note, COALESCE(e.payload_json->>'by','') AS who, e.at AS at, e.id AS ord
  FROM run_events e JOIN runs r ON r.id=e.run_id
  WHERE r.id=sqlc.arg(run_id) AND r.project_id=sqlc.arg(project_id) AND e.kind='step.sendback'
  UNION ALL
  SELECT COALESCE(s.evidence_json #>> '{lastRejection,comment}',''), COALESCE(s.evidence_json #>> '{lastRejection,approver}',''),
    NULLIF(s.evidence_json #>> '{lastRejection,at}','')::timestamptz, s.id
  FROM steps s JOIN runs r ON r.id=s.run_id
  WHERE r.id=sqlc.arg(run_id) AND r.project_id=sqlc.arg(project_id) AND s.type='approval' AND s.evidence_json #>> '{lastRejection,at}' IS NOT NULL
  UNION ALL
  SELECT COALESCE(s.evidence_json->>'comment',''), COALESCE(s.evidence_json->>'approver',''), s.finished_at, s.id
  FROM steps s JOIN runs r ON r.id=s.run_id
  WHERE r.id=sqlc.arg(run_id) AND r.project_id=sqlc.arg(project_id) AND s.type='approval' AND s.status=sqlc.arg(failed_state)
) f WHERE f.note<>'' ORDER BY f.at DESC NULLS LAST, f.ord DESC LIMIT 1;

-- name: RunSteps :many
SELECT * FROM steps WHERE run_id = sqlc.arg(run_id) AND (NOT sqlc.arg(stage_only)::boolean OR stage = sqlc.arg(stage))
AND (sqlc.narg(project_id)::uuid IS NULL OR EXISTS(SELECT 1 FROM runs WHERE runs.id=steps.run_id AND runs.project_id=sqlc.narg(project_id))) ORDER BY id;

-- name: StepByID :one
SELECT * FROM steps WHERE id = $1;

-- name: StartedLanding :one
SELECT r.* FROM workflow_landings l JOIN runs w ON w.id = l.work_run_id
JOIN runs r ON r.id = l.landing_run_id
WHERE l.work_run_id = $1 AND w.project_id = $2 AND w.change_id IS NOT DISTINCT FROM $3;

-- name: WorkflowScheduleByIdentity :many
SELECT * FROM workflow_schedules WHERE project_id=$1 AND workflow_name=$2;

-- Locking reads

-- name: AssertionArtifact :one
SELECT id,kind,name,ref FROM artifacts
WHERE run_id=sqlc.arg(run_id) AND (sqlc.arg(kind)::text='' OR kind=sqlc.arg(kind)) AND (sqlc.arg(name)::text='' OR name=sqlc.arg(name))
ORDER BY id DESC LIMIT 1 FOR SHARE;

-- name: ChangeForUpdate :one
SELECT * FROM changes WHERE id=sqlc.arg(id) AND (sqlc.narg(project_id)::uuid IS NULL OR project_id=sqlc.narg(project_id)) FOR UPDATE;

-- Project removal inventories IDs without loading potentially large Change bodies.
-- Run-step previews name their stacks by step id (pv<step>); a deletion
-- records them before the rows go.
-- name: ProjectPreviewStepsForRemovalMeta :many
SELECT s.id FROM steps s JOIN runs r ON r.id=s.run_id WHERE r.project_id=$1 AND s.type='preview' ORDER BY s.id;

-- name: ProjectChangesForRemovalMeta :many
SELECT id FROM changes WHERE project_id=$1 ORDER BY id FOR UPDATE;

-- Project identity/configuration is the existing project-first admission lock.
-- name: ProjectForUpdate :one
SELECT * FROM projects WHERE id=$1 FOR UPDATE;

-- name: SupersessionChangeStateForUpdate :one
WITH project AS MATERIALIZED (SELECT projects.id FROM projects WHERE projects.id=sqlc.arg(project_id) FOR UPDATE)
SELECT c.state FROM changes c JOIN project p ON p.id=c.project_id WHERE c.id=sqlc.arg(change_id) FOR UPDATE OF c;

-- name: AbandonActiveChangeTrees :many
WITH RECURSIVE tree(id) AS (SELECT root.id FROM runs root WHERE root.change_id IS NOT DISTINCT FROM sqlc.arg(change_id) AND root.project_id=sqlc.arg(project_id) UNION SELECT r.id FROM runs r JOIN tree t ON r.parent_run_id=t.id WHERE r.project_id=sqlc.arg(project_id))
SELECT r.* FROM runs r WHERE r.id IN (SELECT id FROM tree) AND r.status=sqlc.arg(status) ORDER BY r.id FOR UPDATE;

-- name: AbandonActiveRunTree :many
WITH RECURSIVE tree(id) AS (SELECT root.id FROM runs root WHERE root.id=sqlc.arg(root_id) AND root.project_id=sqlc.arg(project_id) UNION SELECT r.id FROM runs r JOIN tree t ON r.parent_run_id=t.id WHERE r.project_id=sqlc.arg(project_id))
SELECT r.* FROM runs r WHERE r.id IN (SELECT id FROM tree) AND r.status=sqlc.arg(status) ORDER BY r.id FOR UPDATE;

-- name: ActiveLandingForUpdate :one
SELECT * FROM runs WHERE change_id IS NOT DISTINCT FROM $1 AND project_id = $2 AND parent_run_id IS NULL
  AND workflow_name = 'land' AND status = $3
ORDER BY number DESC LIMIT 1 FOR UPDATE;

-- name: ParentCallForUpdate :one
SELECT s.name,s.evidence_json,s.status,s.stage,s.attempt,r.project_id AS parent_project_id,r.status AS parent_status,r.current_stage AS parent_stage
FROM runs r JOIN steps s ON s.run_id=r.id WHERE r.id=sqlc.arg(parent_run_id) AND s.id=sqlc.arg(parent_step_id) FOR UPDATE OF r,s;

-- name: ProjectRunsForRemoval :many
SELECT * FROM runs WHERE project_id=$1 ORDER BY id FOR UPDATE;

-- name: RunForUpdate :one
SELECT * FROM runs WHERE id = $1 FOR UPDATE;

-- name: ScopedRunForUpdate :one
SELECT * FROM runs WHERE id=$1 AND project_id=$2 FOR UPDATE;

-- name: WorkingRunForUpdate :one
SELECT * FROM runs WHERE change_id IS NOT DISTINCT FROM $1 AND parent_run_id IS NULL
  AND status IN ('running','done') AND COALESCE(definition_json->>'role','working') != 'landing'
ORDER BY number DESC LIMIT 1 FOR UPDATE;

-- name: LandingAcceptedActorEvidence :one
SELECT evidence_json FROM steps WHERE run_id=sqlc.arg(run_id) AND type='approval' AND status=sqlc.arg(status) ORDER BY finished_at DESC NULLS LAST,id DESC LIMIT 1 FOR UPDATE;

-- name: RunAgentForUpdate :one
SELECT * FROM steps WHERE run_id=$1 AND type='agent/pi' ORDER BY id DESC LIMIT 1 FOR UPDATE;

-- name: StageAgentForUpdate :one
SELECT * FROM steps WHERE run_id=$1 AND stage=$2 AND type='agent/pi' ORDER BY id DESC LIMIT 1 FOR UPDATE;

-- name: StageStepsForUpdate :many
SELECT * FROM steps WHERE run_id = $1 AND stage = $2 ORDER BY id FOR UPDATE;

-- name: StaleChecksForUpdate :many
SELECT * FROM steps WHERE run_id = $1 AND stage = $2 AND override_by = '' AND finished_at < $3
  AND ((type = 'approval' AND status = 'fail')
    OR (type IN ('agent/review','scan','tests','preview','guidance/conformance') AND status IN ('pass','fail','skipped')))
ORDER BY id FOR UPDATE;

-- name: StepForUpdate :one
SELECT * FROM steps WHERE id=sqlc.arg(id) AND (sqlc.narg(run_id)::uuid IS NULL OR run_id=sqlc.narg(run_id)) FOR UPDATE;

-- name: StepsOfTypeForUpdate :many
SELECT * FROM steps WHERE run_id=$1 AND type=$2 ORDER BY id FOR UPDATE;

-- name: LandingForUpdate :one
SELECT * FROM workflow_landings WHERE work_run_id=$1 FOR UPDATE;

-- Operation writes and atomic settlements

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

-- name: AttachUpliftTarget :execrows
UPDATE changes SET target_adoption_id=sqlc.arg(target_adoption_id),target_candidate_json=sqlc.arg(target_candidate_json)
WHERE id=sqlc.arg(id) AND project_id=sqlc.arg(project_id) AND target_adoption_id IS NULL;

-- name: DeleteProjectChanges :exec
DELETE FROM changes WHERE project_id=$1;

-- name: EditChangeDescription :execrows
UPDATE changes SET body=COALESCE(sqlc.narg(body)::text,body), external_ref=COALESCE(sqlc.narg(external_ref)::text,external_ref),
  edit_revision=edit_revision+1
WHERE id=sqlc.arg(id) AND project_id=sqlc.arg(project_id);

-- name: EditChangeTitle :execrows
UPDATE changes SET title=sqlc.arg(title),title_ownership=sqlc.arg(title_ownership)
WHERE id=sqlc.arg(id) AND project_id=sqlc.arg(project_id) AND title IS NOT DISTINCT FROM sqlc.arg(old_title) AND title_ownership IS NOT DISTINCT FROM sqlc.arg(old_title_ownership);

-- name: InsertChange :one
INSERT INTO changes(id,number,project_id,type,title,body,external_ref,acceptance_json,state,title_ownership,branch,base_sha,provenance,expected_revision,opened_by,opened_by_principal,opened_by_via,created_at)
VALUES(sqlc.arg(id),sqlc.arg(number),sqlc.arg(project_id),sqlc.arg(type),sqlc.arg(title),sqlc.arg(body),sqlc.arg(external_ref),sqlc.arg(acceptance_json),sqlc.arg(state),sqlc.arg(title_ownership),sqlc.arg(branch),sqlc.arg(base_sha),sqlc.arg(provenance),sqlc.arg(expected_revision),sqlc.arg(opened_by),sqlc.arg(opened_by_principal),sqlc.arg(opened_by_via),sqlc.arg(created_at)::timestamptz) RETURNING *;

-- name: NameChangeWorkspaceBranch :execrows
UPDATE changes SET branch=sqlc.arg(branch)
WHERE id=sqlc.arg(change_id) AND project_id=sqlc.arg(project_id) AND branch='';

-- name: PublishChangeNarrative :execrows
UPDATE changes SET summary=sqlc.arg(summary),request_met=sqlc.arg(request_met),request_gap=sqlc.arg(request_gap) WHERE id=sqlc.arg(id) AND project_id=sqlc.arg(project_id) AND body=sqlc.arg(body) AND state=sqlc.arg(state) AND run_id IS NOT DISTINCT FROM sqlc.arg(run_id) AND branch=sqlc.arg(branch) AND base_sha=sqlc.arg(base_sha) AND summary IS NOT DISTINCT FROM sqlc.arg(old_summary);

-- name: RecordSupersededChange :execrows
UPDATE changes SET summary=sqlc.arg(summary) WHERE id=sqlc.arg(id) AND project_id=sqlc.arg(project_id) AND state=sqlc.arg(state);

-- name: SetChangeBranch :execrows
UPDATE changes SET branch=sqlc.arg(branch) WHERE id=sqlc.arg(id) AND project_id=sqlc.arg(project_id);

-- name: SetChangeLandedSHA :execrows
UPDATE changes SET landed_sha=sqlc.arg(landed_sha) WHERE id=sqlc.arg(change_id) AND project_id=sqlc.arg(project_id);

-- name: SetChangeWorkspaceBase :execrows
UPDATE changes SET base_sha=sqlc.arg(tip),branch=sqlc.arg(branch)
WHERE id=sqlc.arg(change_id) AND project_id=sqlc.arg(project_id) AND branch IS NOT DISTINCT FROM sqlc.arg(old_branch)
AND base_sha IS NOT DISTINCT FROM sqlc.arg(old_base_sha) AND landed_sha IS NOT DISTINCT FROM sqlc.arg(old_landed_sha)
AND state IS NOT DISTINCT FROM sqlc.arg(old_state) AND run_id IS NOT DISTINCT FROM sqlc.arg(old_run_id);

-- name: StampUpliftAdoption :execrows
UPDATE changes SET adopted_revision=sqlc.arg(adopted_revision) WHERE id=sqlc.arg(id) AND project_id=sqlc.arg(project_id) AND type='uplift' AND state=sqlc.arg(state) AND adopted_revision=0;

-- name: MarkChangeClosedAs :execrows
-- Only the engine's nothing-to-change close calls this, with the change's
-- abandonment, in the transaction that closes it.
UPDATE changes SET closed_as=sqlc.arg(closed_as) WHERE id=sqlc.arg(id) AND project_id=sqlc.arg(project_id) AND state=sqlc.arg(state);

-- name: TransitionChange :execrows
UPDATE changes SET state=sqlc.arg(state),run_id=sqlc.arg(run_id),branch=sqlc.arg(branch),closed_at=CASE WHEN sqlc.arg(closed)::boolean THEN sqlc.arg(at)::timestamptz ELSE closed_at END
WHERE id=sqlc.arg(id) AND project_id=sqlc.arg(project_id) AND state IS NOT DISTINCT FROM sqlc.arg(old_state) AND run_id IS NOT DISTINCT FROM sqlc.arg(old_run_id);

-- name: ContinuationCopyPins :execrows
-- The copies' ids are minted by the caller, one per source row in id order.
WITH src AS (SELECT e.kind,e.payload_json,row_number() OVER (ORDER BY e.id) AS n FROM run_events e WHERE e.run_id=sqlc.arg(source_id) AND e.kind IN ('adoption.resolved','guidance.pinned','integration.resolved'))
INSERT INTO run_events(id,run_id,at,kind,payload_json) SELECT (sqlc.arg(new_ids)::uuid[])[src.n],sqlc.arg(run_id),sqlc.arg(at)::timestamptz,src.kind,src.payload_json FROM src ORDER BY src.n;

-- name: DeleteProjectRunEvents :exec
DELETE FROM run_events WHERE run_id IN (SELECT id FROM runs WHERE project_id=$1);

-- name: InsertRunEvent :execrows
INSERT INTO run_events (id, run_id, at, kind, payload_json) VALUES ($5, $1,$2,$3,$4);

-- name: InsertWorkspacePreparedEvent :execrows
INSERT INTO run_events(id, run_id,at,kind,payload_json) VALUES(sqlc.arg(id), sqlc.arg(run_id),sqlc.arg(at)::timestamptz,'workspace.prepared',sqlc.arg(payload_json));

-- name: ContinuationOwner :execrows
INSERT INTO run_inference_owners(run_id,actor,actor_principal,actor_via)
SELECT sqlc.arg(run_id), COALESCE(o.actor, sqlc.arg(fallback_actor)::text), o.actor_principal, COALESCE(o.actor_via, '')
FROM (SELECT 1) one LEFT JOIN run_inference_owners o ON o.run_id = sqlc.arg(source_id);

-- name: InsertRunInferenceOwner :one
INSERT INTO run_inference_owners(run_id,actor,actor_principal,actor_via) VALUES (sqlc.arg(run_id),sqlc.arg(actor),sqlc.arg(actor_principal),sqlc.arg(actor_via)) RETURNING *;

-- name: ContinuationStart :execrows
INSERT INTO run_starts(run_id) VALUES ($1);

-- name: InsertRunStart :one
INSERT INTO run_starts(run_id) VALUES ($1) RETURNING *;

-- name: MarkRunReady :execrows
UPDATE run_starts SET ready=true WHERE run_id=$1 AND NOT ready;

-- name: ContinuationInsertRun :one
INSERT INTO runs(id,number,project_id,workflow_name,workflow_version,workflow_origin,kind,status,current_stage,definition_json,change_id,inputs_json,adoption_id,base_sha,created_at,updated_at)
SELECT sqlc.arg(id),sqlc.arg(number),src.project_id,src.workflow_name,src.workflow_version,src.workflow_origin,src.kind,sqlc.arg(status),sqlc.arg(stage),src.definition_json,src.change_id,src.inputs_json,src.adoption_id,'',sqlc.arg(at)::timestamptz,sqlc.arg(at)::timestamptz FROM runs src WHERE src.id=sqlc.arg(source_id) RETURNING *;

-- name: DeleteProjectRuns :exec
DELETE FROM runs WHERE project_id=$1;

-- name: InsertRun :one
INSERT INTO runs(id,number,project_id,workflow_name,workflow_version,workflow_origin,kind,status,current_stage,definition_json,change_id,parent_run_id,parent_step_id,inputs_json,adoption_id,created_at,updated_at)
VALUES (sqlc.arg(id),sqlc.arg(number),sqlc.arg(project_id),sqlc.arg(workflow_name),sqlc.arg(workflow_version),sqlc.arg(workflow_origin),sqlc.arg(kind),sqlc.arg(status),sqlc.arg(current_stage),sqlc.arg(definition_json),sqlc.arg(change_id),sqlc.arg(parent_run_id),sqlc.arg(parent_step_id),sqlc.arg(inputs_json),sqlc.arg(adoption_id),sqlc.arg(created_at),sqlc.arg(updated_at)) RETURNING *;

-- name: InvalidateRunDecision :execrows
UPDATE runs SET retry_revision=retry_revision+1,updated_at=sqlc.arg(at)::timestamptz
WHERE id=sqlc.arg(id) AND status='running' AND current_stage=sqlc.arg(current_stage) AND retry_revision=sqlc.arg(retry_revision);

-- name: SetParentStepAttempt :execrows
UPDATE runs SET parent_step_attempt=$1 WHERE id=$2;

-- name: SetRunWorkspaceBase :execrows
UPDATE runs SET base_sha=sqlc.arg(tip)
WHERE id=sqlc.arg(run_id) AND project_id=sqlc.arg(project_id) AND base_sha IS NOT DISTINCT FROM sqlc.arg(old_base_sha)
AND status IS NOT DISTINCT FROM sqlc.arg(old_status) AND current_stage IS NOT DISTINCT FROM sqlc.arg(old_stage) AND retry_revision IS NOT DISTINCT FROM sqlc.arg(old_revision);

-- name: TransitionRun :execrows
UPDATE runs SET current_stage=sqlc.arg(next_stage),status=sqlc.arg(next_status),updated_at=sqlc.arg(at)::timestamptz
WHERE id=sqlc.arg(id) AND status=sqlc.arg(expected_status) AND current_stage=sqlc.arg(current_stage) AND retry_revision=sqlc.arg(retry_revision);

-- name: ContinuationCopySteps :execrows
-- The copies' ids are minted by the caller, one per source row in id order.
WITH src AS (SELECT s.stage,s.name,s.type,s.config_json,row_number() OVER (ORDER BY s.id) AS n FROM steps s WHERE s.run_id=sqlc.arg(source_id) AND s.stage=sqlc.arg(stage))
INSERT INTO steps(id,run_id,stage,name,type,config_json) SELECT (sqlc.arg(new_ids)::uuid[])[src.n],sqlc.arg(run_id),src.stage,src.name,src.type,src.config_json-'retryMode'-'sendback'-'continueSession' FROM src ORDER BY src.n;

-- name: ContinuationFeedback :execrows
UPDATE steps SET config_json=config_json || jsonb_build_object('retryMode','continue','sendback',sqlc.arg(note)::text,'continueSession',sqlc.arg(session)::text) WHERE id=sqlc.arg(id);

-- name: CreateWorkflowGate :one
INSERT INTO steps(id, run_id,stage,name,type,status,evidence_json,finished_at)
VALUES (sqlc.arg(id), sqlc.arg(run_id),sqlc.arg(stage),sqlc.arg(name),sqlc.arg(type),sqlc.arg(status),sqlc.arg(evidence_json),sqlc.arg(at)::timestamptz) RETURNING *;

-- name: DeleteProjectSteps :exec
DELETE FROM steps WHERE run_id IN (SELECT id FROM runs WHERE project_id=$1);

-- name: DispatchResume :execrows
UPDATE steps SET status='pending', started_at=NULL, finished_at=NULL, evidence_json='{}',
config_json=CASE WHEN type='agent/pi' THEN jsonb_set(config_json, '{retryMode}', '"continue"'::jsonb, true) ELSE config_json END
WHERE steps.id=sqlc.arg(id) AND steps.run_id=sqlc.arg(run_id) AND status='error' AND evidence_json->>'reason'='orphaned by apptownd restart'
AND EXISTS (SELECT 1 FROM runs r WHERE r.id=steps.run_id AND r.status='running' AND r.current_stage=steps.stage);

-- name: DispatchSkip :execrows
UPDATE steps SET status='skipped', evidence_json=sqlc.arg(evidence_json), finished_at=sqlc.arg(at)::timestamptz
WHERE steps.id=sqlc.arg(id) AND steps.run_id=sqlc.arg(run_id) AND (status='pending' OR (status='error' AND evidence_json->>'reason'='orphaned by apptownd restart'))
AND EXISTS (SELECT 1 FROM runs r WHERE r.id=steps.run_id AND r.status='running' AND r.current_stage=steps.stage);

-- name: FinishAttempt :execrows
UPDATE steps SET status=sqlc.arg(status),evidence_json=sqlc.arg(evidence_json),finished_at=sqlc.arg(at)::timestamptz,
  config_json=CASE WHEN type='agent/pi' AND sqlc.arg(status)::text='pass'
    THEN config_json-'sendback' ELSE config_json END
WHERE id=sqlc.arg(id) AND status='running' AND attempt=sqlc.arg(attempt) AND override_by='';

-- name: FinishChildCall :execrows
UPDATE steps SET status='pass',evidence_json=sqlc.arg(evidence_json),finished_at=sqlc.arg(at)::timestamptz
WHERE id=sqlc.arg(id) AND status='running' AND attempt=sqlc.arg(attempt);

-- name: InsertStageStep :one
INSERT INTO steps(id, run_id,stage,name,type,config_json,status)
VALUES(sqlc.arg(id), sqlc.arg(run_id),sqlc.arg(stage),sqlc.arg(name),sqlc.arg(type),sqlc.arg(config_json),sqlc.arg(status)) RETURNING *;

-- name: InterruptStartupSteps :one
WITH candidates AS MATERIALIZED (
 SELECT s.id FROM steps s JOIN runs parent ON parent.id=s.run_id
 WHERE s.status=sqlc.arg(running_status) AND NOT (s.id=ANY(sqlc.arg(preserved_steps)::uuid[])) AND NOT (s.type='workflow/call' AND EXISTS (
  SELECT 1 FROM runs child WHERE child.parent_step_id=s.id
   AND child.parent_run_id=s.run_id AND child.parent_step_attempt=s.attempt
   AND child.project_id=parent.project_id
   AND child.id::text=s.evidence_json->>'childRunId' AND child.status=sqlc.arg(child_status)))
 ORDER BY s.id FOR UPDATE OF s
), changed AS (
 UPDATE steps SET status=sqlc.arg(next_status),finished_at=sqlc.arg(at)::timestamptz,
  evidence_json=CASE WHEN steps.type = ANY(sqlc.arg(ended_types)::text[]) THEN sqlc.arg(ended_evidence)::jsonb ELSE '{"reason":"orphaned by apptownd restart"}'::jsonb END
 FROM candidates WHERE steps.id=candidates.id AND steps.status=sqlc.arg(running_status) RETURNING steps.id
) SELECT (SELECT count(*) FROM candidates) AS expected,(SELECT count(*) FROM changed) AS interrupted;

-- name: InvalidateStep :execrows
UPDATE steps SET status='pending',started_at=NULL,finished_at=NULL,evidence_json='{}',override_by='',override_reason=''
WHERE id=$1 AND attempt=$2;

-- name: OverrideStep :execrows
UPDATE steps SET status='pass',override_by=sqlc.arg(override_by),override_by_principal=sqlc.arg(override_by_principal),override_by_via=sqlc.arg(override_by_via),override_reason=sqlc.arg(override_reason),finished_at=sqlc.arg(at)::timestamptz
WHERE id=sqlc.arg(id) AND attempt=sqlc.arg(attempt) AND status=sqlc.arg(expected_status) AND override_by='';

-- name: RecheckStep :execrows
UPDATE steps SET status='pending',started_at=NULL,finished_at=NULL,evidence_json='{}'
WHERE id=$1 AND attempt=$2 AND override_by='';

-- name: RecordApproval :execrows
UPDATE steps SET status=sqlc.arg(status),evidence_json=sqlc.arg(evidence_json),
  finished_at=CASE WHEN sqlc.arg(status)::text IN ('pass','fail') THEN sqlc.arg(at)::timestamptz ELSE finished_at END
WHERE id=sqlc.arg(id) AND attempt=sqlc.arg(attempt) AND status=sqlc.arg(expected_status) AND override_by='';

-- name: RecordStepEvaluation :execrows
UPDATE steps SET status=sqlc.arg(status),evidence_json=sqlc.arg(evidence_json),finished_at=sqlc.arg(at)::timestamptz
WHERE id=sqlc.arg(id) AND attempt=sqlc.arg(attempt) AND status=sqlc.arg(expected_status) AND override_by='';

-- name: ReopenApproval :execrows
UPDATE steps SET status='pending',evidence_json=$1,finished_at=NULL
WHERE id=$2 AND attempt=$3;

-- name: RetryStep :execrows
UPDATE steps SET status='pending',started_at=NULL,finished_at=NULL,evidence_json='{}',
  override_by='',override_reason='',config_json=$1
WHERE id=$2 AND attempt=$3;

-- name: ReworkAgent :execrows
UPDATE steps SET status='pending',started_at=NULL,finished_at=NULL,override_by='',override_reason='',
  config_json=jsonb_set(config_json, '{retryMode}', '"continue"'::jsonb, true)
WHERE id=$1 AND attempt=$2;

-- name: StartAttempt :execrows
UPDATE steps SET status='running',attempt=attempt+1,started_at=sqlc.arg(at)::timestamptz,finished_at=NULL
WHERE id=sqlc.arg(id) AND status='pending' AND attempt=sqlc.arg(attempt) AND override_by='';

-- name: StartParentCall :execrows
UPDATE steps SET status=sqlc.arg(status),attempt=attempt+1,started_at=sqlc.arg(started_at),finished_at=NULL,evidence_json=sqlc.arg(evidence_json)
WHERE id=sqlc.arg(id) AND status IS NOT DISTINCT FROM sqlc.arg(old_status) AND attempt=sqlc.arg(attempt);

-- name: ContinuationLink :execrows
INSERT INTO workflow_continuations(source_run_id,run_id) VALUES ($1,$2);

-- name: AttachAcceptedLanding :execrows
UPDATE workflow_landings SET landing_run_id=sqlc.arg(landing_run_id) WHERE work_run_id=sqlc.arg(work_run_id);

-- name: EnsureLanding :one
WITH inserted AS (
  INSERT INTO workflow_landings (work_run_id) VALUES ($1) ON CONFLICT DO NOTHING RETURNING work_run_id
)
SELECT work_run_id FROM inserted
UNION ALL SELECT work_run_id FROM workflow_landings WHERE work_run_id=$1 LIMIT 1;

-- name: LandingAcceptanceConsidered :many
SELECT (payload_json->>'stepId')::uuid AS step_id FROM run_events WHERE run_id=$1 AND kind IN ('landing.acceptance.carried','landing.acceptance.withheld') AND payload_json->>'stepId' IS NOT NULL;

-- name: LandingAcceptedWork :one
SELECT work_run_id FROM workflow_landings WHERE landing_run_id IS NOT DISTINCT FROM $1 AND acceptance_applied;

-- name: LandingMarkApplied :execrows
UPDATE workflow_landings SET acceptance_applied=true WHERE work_run_id=sqlc.arg(work_run_id) AND landing_run_id IS NOT DISTINCT FROM sqlc.arg(landing_run_id) AND NOT acceptance_applied;

-- name: ReserveLanding :exec
INSERT INTO workflow_landings(work_run_id) VALUES ($1) ON CONFLICT DO NOTHING;

-- name: ArmWorkflowSchedule :exec
INSERT INTO workflow_schedules (project_id,workflow_name,last_fired_at,last_outcome,last_change_id)
VALUES (sqlc.arg(project_id),sqlc.arg(workflow_name),sqlc.arg(last_fired_at),'armed',NULL)
ON CONFLICT (project_id,workflow_name) DO UPDATE SET last_fired_at=excluded.last_fired_at, last_outcome='armed', last_change_id=NULL;

-- name: RecordWorkflowScheduleFiring :exec
UPDATE workflow_schedules SET last_fired_at=sqlc.narg(last_fired_at), last_outcome=sqlc.arg(last_outcome), last_change_id=sqlc.narg(last_change_id)
WHERE project_id=sqlc.arg(project_id) AND workflow_name=sqlc.arg(workflow_name);

-- name: WorkflowSchedulesOfProject :many
SELECT s.workflow_name,s.last_fired_at,s.last_outcome,s.last_change_id,COALESCE(c.number,0)::bigint AS last_change_number
FROM workflow_schedules s LEFT JOIN changes c ON c.id=s.last_change_id AND c.project_id=s.project_id
WHERE s.project_id=$1 ORDER BY s.workflow_name;

-- Scoped metadata and reports

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
WHERE r.project_id=sqlc.arg(project_id) AND a.kind=ANY(sqlc.arg(kinds)::text[]);

-- Artifact identity only; sbom bodies can be large.
-- name: LatestSBOMIDBeforeMeta :many
SELECT a.id FROM artifacts a JOIN runs r ON r.id=a.run_id
WHERE r.project_id=$1 AND a.kind='sbom' AND a.created_at<=$2 ORDER BY a.id DESC LIMIT 1;

-- name: NarrativeArtifacts :many
SELECT a.kind,a.body FROM artifacts a JOIN runs r ON a.run_id=r.id WHERE r.change_id IS NOT DISTINCT FROM $1 AND r.project_id=$2 AND (a.kind LIKE '%report' OR a.kind='plan') AND a.kind NOT LIKE '%scan%' ORDER BY a.id DESC LIMIT 2;

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

-- Text evidence excludes potentially large body_bytes; binary downloads use the
-- separate artifact body door. Keep the text projection shared by both reports.
-- name: LatestClaim :one
-- The implementer's report: the newest artifact, on the change's runs (on
-- the run alone for work with no change), of a kind a review step there
-- declares it audits (agent/review's "of").
WITH scoped AS (
  SELECT r.id FROM runs r WHERE r.project_id=sqlc.arg(project_id)
  AND ((sqlc.narg(change_id)::uuid IS NOT NULL AND r.change_id=sqlc.narg(change_id)) OR (sqlc.narg(change_id)::uuid IS NULL AND r.id=sqlc.arg(run_id)))
)
SELECT a.id,a.run_id,a.kind,a.name,a.ref,a.body,a.created_at FROM artifacts a
WHERE a.run_id IN (SELECT id FROM scoped)
AND a.kind IN (SELECT s.config_json->>'of' FROM steps s WHERE s.run_id IN (SELECT id FROM scoped) AND s.type='agent/review' AND COALESCE(s.config_json->>'of','')<>'')
ORDER BY a.id DESC LIMIT 1;

-- name: SelectedTextArtifacts :many
SELECT a.id,a.run_id,a.kind,a.name,a.ref,a.body,a.created_at
FROM artifacts a JOIN runs r ON r.id=a.run_id
WHERE r.project_id=sqlc.arg(project_id) AND (sqlc.narg(run_id)::uuid IS NULL OR r.id=sqlc.narg(run_id))
AND (sqlc.narg(change_id)::uuid IS NULL OR (r.change_id=sqlc.narg(change_id) AND EXISTS(SELECT 1 FROM changes c WHERE c.id=sqlc.narg(change_id) AND c.project_id=sqlc.arg(project_id))))
AND (cardinality(sqlc.arg(kinds)::text[])=0 OR a.kind=ANY(sqlc.arg(kinds)::text[])) AND (sqlc.arg(name)::text='' OR a.name=sqlc.arg(name))
ORDER BY CASE WHEN sqlc.arg(latest)::boolean THEN a.id END DESC,CASE WHEN NOT sqlc.arg(latest)::boolean THEN a.id END ASC LIMIT NULLIF(sqlc.arg(limit_count)::integer,0);

-- Transitional policy observation: audit_log is audit-owned. Keep the existing
-- single-statement creator lookup until the policy is moved behind an audit door;
-- the independent/root CASE and project+Change scope must remain together.
-- name: ApproverEligibility :one
SELECT p.default_approvers,p.require_independent_approval,
 CASE WHEN p.require_independent_approval AND NOT sqlc.arg(root)::boolean THEN COALESCE((SELECT c.opened_by FROM changes c WHERE c.id=sqlc.narg(change_id) AND c.project_id=p.id),'') ELSE '' END::text AS author,
 (SELECT c.opened_by_principal FROM changes c WHERE c.id=sqlc.narg(change_id) AND c.project_id=p.id) AS author_principal
FROM projects p WHERE p.id=sqlc.arg(project_id) AND (sqlc.narg(change_id)::uuid IS NULL OR EXISTS(SELECT 1 FROM changes c WHERE c.id=sqlc.narg(change_id) AND c.project_id=p.id));

-- name: ProjectHasChange :one
SELECT EXISTS(SELECT 1 FROM changes WHERE project_id=$1);

-- name: ChangeProjectMeta :one
SELECT project_id FROM changes WHERE id=$1;

-- Summary requests omit potentially large bodies at the SQL boundary. Preserve
-- the established list projection rather than returning unseen Change fields.
-- name: ChangeSummaries :many
SELECT wi.id,wi.number,wi.project_id,wi.type,wi.title,CASE WHEN sqlc.arg(summary)::boolean THEN '' ELSE wi.body END::text AS body,wi.run_id,wi.created_at,
wi.state,wi.branch,wi.base_sha,wi.external_ref,wi.summary,wi.closed_at,wi.closed_as,
COALESCE(r.current_stage,'')::text AS stage,COALESCE(r.status,'')::text AS run_status,COALESCE(r.workflow_name,'')::text AS workflow_name,
GREATEST(wi.created_at,wi.closed_at,r.updated_at)::timestamptz AS updated_at
FROM changes wi LEFT JOIN runs r ON wi.run_id=r.id AND r.project_id=wi.project_id
WHERE wi.project_id=sqlc.arg(project_id) AND (sqlc.arg(state)::text='' OR wi.state=sqlc.arg(state)) ORDER BY wi.number DESC LIMIT NULLIF(sqlc.arg(limit_count)::integer,0);

-- name: ChangeTargetAdoptionMeta :one
SELECT target_adoption_id FROM changes WHERE id=$1 AND project_id=$2;

-- Workspace source needs only repository/Change identity and state; Change body
-- and acceptance can be large and are not part of the snapshot comparison.
-- name: ChangeWorkspaceSourceMeta :one
SELECT p.slug,p.default_branch,c.branch,c.base_sha,c.landed_sha,c.state,c.run_id
FROM changes c JOIN projects p ON p.id=c.project_id WHERE c.id=$1 AND c.project_id=$2;

-- name: HasOpenUplift :one
SELECT EXISTS(SELECT 1 FROM changes WHERE project_id=$1 AND type='uplift' AND state=$2);

-- Change metadata list omits potentially large body/acceptance/candidate JSON.
-- name: OpenChangesWithoutRunsMeta :many
SELECT c.id,c.number,c.title,c.state FROM changes c WHERE c.project_id=sqlc.arg(project_id) AND c.state IN (sqlc.arg(in_progress),sqlc.arg(landing))
AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.change_id=c.id AND r.project_id=c.project_id AND r.status=sqlc.arg(running))
ORDER BY c.number DESC LIMIT NULLIF(sqlc.arg(limit_count)::integer,0);

-- Startup needs only identities, not potentially large Change bodies/acceptance.
-- name: PendingUpliftAdoptions :many
SELECT id,project_id FROM changes WHERE type='uplift' AND state=$1 AND adopted_revision=0 ORDER BY id;

-- name: ProjectWorkBusy :one
SELECT (EXISTS(SELECT 1 FROM changes WHERE changes.project_id=sqlc.arg(project_id) AND changes.state=sqlc.arg(landing_state)) OR
 EXISTS(SELECT 1 FROM runs WHERE runs.project_id=sqlc.arg(project_id) AND runs.status=sqlc.arg(running_run_state) AND (runs.change_id IS DISTINCT FROM sqlc.arg(replacing_change_id) OR runs.change_id IS NULL)) OR
 EXISTS(SELECT 1 FROM steps s JOIN runs r ON r.id=s.run_id WHERE r.project_id=sqlc.arg(project_id) AND r.change_id IS NOT DISTINCT FROM sqlc.arg(replacing_change_id) AND s.status=sqlc.arg(running_step_state)))::boolean;

-- Branch metadata avoids large Change bodies in conformance attribution.
-- name: ShippedChangeBranchesMeta :many
SELECT id,branch FROM changes WHERE project_id=$1 AND state=$2 AND branch<>'' ORDER BY id;

-- Label metadata excludes potentially large Change bodies.
-- name: ShippedChangeLabelsMeta :many
SELECT id,title,type FROM changes WHERE project_id=sqlc.arg(project_id) AND state=sqlc.arg(shipped_state) AND closed_at>sqlc.arg(after)::timestamptz
ORDER BY closed_at DESC NULLS LAST,id DESC LIMIT NULLIF(sqlc.arg(row_limit)::integer,0);

-- name: ChangeStatesForScopes :many
SELECT c.id,c.project_id,c.state FROM generate_subscripts($1::uuid[],1) idx(n)
JOIN changes c ON c.id=($1::uuid[])[idx.n] AND c.project_id=($2::uuid[])[idx.n];

-- Presence-only prerequisite report; never load artifact bodies. Array indexes
-- preserve duplicates/order, as WITH ORDINALITY did before sqlc conversion.
-- name: MissingArtifactKinds :many
SELECT (sqlc.arg(kinds)::text[])[i]::text AS kind
FROM generate_subscripts(sqlc.arg(kinds)::text[],1) AS requested(i)
WHERE NOT EXISTS(SELECT 1 FROM artifacts a JOIN runs r ON r.id=a.run_id
 WHERE r.project_id=sqlc.arg(project_id) AND a.kind=(sqlc.arg(kinds)::text[])[i]) ORDER BY i;

-- name: PendingStepsForScopes :many
SELECT r.id AS run_id,r.project_id,s.id AS step_id,s.attempt,s.name,s.type,s.status
FROM generate_subscripts(sqlc.arg(run_ids)::uuid[],1) idx(n)
JOIN runs r ON r.id=(sqlc.arg(run_ids)::uuid[])[idx.n] AND r.project_id=(sqlc.arg(project_ids)::uuid[])[idx.n]
JOIN LATERAL (
 SELECT steps.id,steps.attempt,steps.name,steps.type,steps.status FROM steps WHERE steps.run_id=r.id AND steps.status IN (sqlc.arg(running_state),sqlc.arg(human_state),sqlc.arg(pending_state))
 ORDER BY CASE steps.status WHEN sqlc.arg(running_state) THEN 0 WHEN sqlc.arg(human_state) THEN 1 ELSE 2 END,steps.id LIMIT 1
) s ON true;

-- name: ProjectActivityReport :many
WITH change_counts AS (
 SELECT c.project_id,COUNT(*)::bigint AS changes,
 COUNT(*) FILTER(WHERE current.status=sqlc.arg(running_status)::text)::bigint AS active_items,
 COUNT(*) FILTER(WHERE c.state IN (sqlc.arg(proposed_state)::text,sqlc.arg(in_progress_state)::text,sqlc.arg(landing_state)::text))::bigint AS open_changes,
 COUNT(*) FILTER(WHERE c.type='uplift' AND c.state=sqlc.arg(in_progress_state)::text)::bigint AS open_uplifts
 FROM changes c LEFT JOIN runs current ON current.id=c.run_id AND current.project_id=c.project_id
 WHERE c.project_id=ANY(sqlc.arg(project_ids)::uuid[]) GROUP BY c.project_id
), run_counts AS (
 SELECT project_id,COUNT(*) FILTER(WHERE status=sqlc.arg(done_status)::text AND parent_run_id IS NULL)::bigint AS done_runs,
 COUNT(*) FILTER(WHERE status=sqlc.arg(running_status)::text)::bigint AS running_runs,MAX(updated_at) AS last_run_at
 FROM runs WHERE project_id=ANY(sqlc.arg(project_ids)::uuid[]) GROUP BY project_id
)
SELECT p.id,COALESCE(c.changes,0)::bigint AS changes,COALESCE(c.active_items,0)::bigint AS active_items,
 COALESCE(r.done_runs,0)::bigint AS done_runs,COALESCE(r.running_runs,0)::bigint AS running_runs,
 COALESCE(r.last_run_at,'epoch'::timestamptz)::timestamptz AS last_run_at,(r.last_run_at IS NOT NULL)::boolean AS has_last_run_at,
 COALESCE(c.open_changes,0)::bigint AS open_changes,COALESCE(c.open_uplifts,0)::bigint AS open_uplifts
FROM projects p LEFT JOIN change_counts c ON c.project_id=p.id LEFT JOIN run_counts r ON r.project_id=p.id
WHERE p.id=ANY(sqlc.arg(project_ids)::uuid[]);

-- name: ProjectIDBySlugMeta :one
SELECT id FROM projects WHERE slug=$1;

-- Scope enumeration does not need project documents or configuration payloads.
-- Transitional Delivery membership observation: releases and release_changes
-- are Delivery-owned. Preserve the one-statement project/release scope and
-- receipt ordering until a Delivery batch door replaces this report.
-- name: ReleaseChangeLabels :many
SELECT rel.id AS release_id,c.id AS change_id,c.title,c.type
FROM releases rel JOIN release_changes rc ON rc.release_id=rel.id
JOIN changes c ON c.id=rc.change_id AND c.project_id=rel.project_id
WHERE rel.project_id=sqlc.arg(project_id) AND rel.id=ANY(sqlc.arg(release_ids)::uuid[]) ORDER BY rel.id,c.id;

-- name: AgentStartReceiptCount :one
SELECT COUNT(*)::bigint FROM run_events WHERE run_id=$1 AND kind='step.started' AND payload_json->>'step'=sqlc.arg(step_name)::text;

-- name: ChangeHistory :many
SELECT c.id,c.title,c.type,c.state,CASE WHEN sqlc.narg(project_id)::uuid IS NULL THEN p.slug ELSE '' END::text AS project,
history.workflows,history.runs,COALESCE(history.last_activity,c.created_at)::timestamptz AS activity_at,history.took_s,history.completed,
EXISTS(SELECT 1 FROM run_events e JOIN runs r ON r.id=e.run_id WHERE r.change_id=c.id AND r.project_id=c.project_id AND e.kind='merge.completed') AS merged,
c.number,c.closed_as
FROM changes c JOIN projects p ON p.id=c.project_id CROSS JOIN LATERAL (
SELECT COALESCE(string_agg(r.workflow_name,' → ' ORDER BY r.number),'')::text AS workflows,
COUNT(*) AS runs,MAX(r.updated_at) AS last_activity,
COALESCE(SUM(EXTRACT(EPOCH FROM r.updated_at)-EXTRACT(EPOCH FROM r.created_at)),0)::bigint AS took_s,
COUNT(*) FILTER(WHERE r.status=sqlc.arg(done)) AS completed
FROM runs r WHERE r.change_id=c.id AND r.project_id=c.project_id AND r.parent_run_id IS NULL
) history
WHERE c.project_id=ANY(sqlc.arg(project_ids)::uuid[]) AND c.state IN (sqlc.arg(shipped),sqlc.arg(abandoned))
ORDER BY 8 DESC,c.number DESC LIMIT NULLIF(sqlc.arg(limit_count)::integer,0);

-- name: ChangeMergedReceipt :one
SELECT EXISTS(SELECT 1 FROM run_events e JOIN runs r ON r.id=e.run_id WHERE r.change_id IS NOT DISTINCT FROM $1 AND r.project_id=$2 AND e.kind='merge.completed');

-- name: ContinuationPinCount :one
SELECT count(*) FROM run_events WHERE run_id=$1 AND kind IN ('adoption.resolved','guidance.pinned','integration.resolved');

-- name: LandingAcceptanceReceipt :one
SELECT payload_json FROM run_events WHERE run_id=sqlc.arg(run_id) AND kind='landing.autostarted' AND payload_json->>'workRun'=sqlc.arg(work_run_id)::text ORDER BY id DESC LIMIT 1;

-- name: LatestAgentSessionReceipt :one
SELECT COALESCE(payload_json->>'sessionId','')::text FROM run_events
WHERE run_id=$1 AND kind='step.started' AND payload_json->>'step'=sqlc.arg(step_name)::text ORDER BY id DESC LIMIT 1;

-- name: LatestStageChecks :one
SELECT COALESCE(payload_json->'checks','null'::jsonb)::text AS checks FROM run_events
WHERE run_id=$1 AND kind='stage.entered' AND payload_json->>'stage'=sqlc.arg(stage)::text ORDER BY id DESC LIMIT 1;

-- name: ReadRunEvents :many
SELECT id,run_id,at,kind,payload_json FROM run_events WHERE run_id=sqlc.arg(run_id)
ORDER BY CASE WHEN sqlc.arg(newest_first)::boolean THEN id END DESC,
         CASE WHEN NOT sqlc.arg(newest_first)::boolean THEN id END ASC
LIMIT NULLIF(sqlc.arg(event_limit)::integer,0);

-- name: LatestChangeEvent :one
SELECT e.id,e.run_id,e.at,e.kind,e.payload_json FROM run_events e
JOIN runs r ON r.id=e.run_id
WHERE r.project_id=sqlc.arg(project_id) AND r.change_id IS NOT DISTINCT FROM sqlc.arg(change_id) AND e.kind=sqlc.arg(kind)
ORDER BY e.id DESC
LIMIT 1;

-- name: ReworkHistory :one
-- rounds counts the automatic reworks since a person last retried a step
-- (apptown retry, retry -m, a send-back): their act starts a fresh allowance.
WITH person AS (
  SELECT (SELECT e.id FROM run_events e WHERE e.run_id=sqlc.arg(run_id)::uuid AND e.kind='step.retried' ORDER BY e.id DESC LIMIT 1)::uuid AS since
), history AS (
  SELECT
    (SELECT e.id FROM run_events e WHERE e.run_id=sqlc.arg(run_id)::uuid AND e.kind=sqlc.arg(trigger_kind)::text AND e.payload_json->>'step'=sqlc.arg(source_name)::text ORDER BY e.id DESC LIMIT 1)::uuid AS trigger_event,
    (SELECT e.id FROM run_events e WHERE e.run_id=sqlc.arg(run_id)::uuid AND e.kind='step.started' AND e.payload_json->>'step'=sqlc.arg(source_name)::text ORDER BY e.id DESC LIMIT 1)::uuid AS check_start,
    (SELECT e.id FROM run_events e WHERE e.run_id=sqlc.arg(run_id)::uuid AND e.kind='step.started' AND e.payload_json->>'step'=sqlc.arg(agent_name)::text ORDER BY e.id DESC LIMIT 1)::uuid AS agent_start,
    (SELECT COUNT(*) FROM run_events e, person WHERE e.run_id=sqlc.arg(run_id)::uuid AND e.kind='step.rework' AND (person.since IS NULL OR e.id > person.since)) AS rounds
  FROM person
)
SELECT history.*, EXISTS (
  SELECT 1 FROM run_events e WHERE e.run_id=sqlc.arg(run_id)::uuid AND e.kind='step.rework'
    AND e.payload_json->>'triggerStep'=sqlc.arg(source_id)::uuid::text
    AND e.payload_json->>'triggerEvent'=history.trigger_event::text
) AS seen FROM history;

-- name: DispatchNeedsPreparation :one
SELECT EXISTS(SELECT 1 FROM run_starts WHERE run_id=$1 AND NOT ready);

-- name: ExecutionReadiness :one
SELECT EXISTS(SELECT 1 FROM run_starts rs WHERE rs.run_id=sqlc.arg(run_id) AND NOT rs.ready) AS unready,
(SELECT COUNT(*) FROM run_events re WHERE re.run_id=sqlc.arg(run_id) AND re.kind='step.rework') AS reworks
FROM changes c WHERE c.id=sqlc.arg(change_id) AND c.project_id=sqlc.arg(project_id);

-- name: RunPreparationPending :one
SELECT EXISTS(SELECT 1 FROM run_starts s JOIN runs r ON r.id=s.run_id WHERE s.run_id=$1 AND NOT s.ready AND r.status=$2);

-- name: ActiveChangeRun :one
SELECT (SELECT id FROM runs
  WHERE project_id=sqlc.arg(project_id) AND change_id IS NOT DISTINCT FROM sqlc.arg(change_id) AND status=sqlc.arg(status)
  ORDER BY number DESC LIMIT 1)::uuid AS run_id;

-- Active-run identity only: admission does not need the potentially large definition/inputs.
-- name: ActiveChangeRunMeta :one
SELECT (SELECT id FROM runs WHERE change_id IS NOT DISTINCT FROM $1 AND parent_run_id IS NULL AND status=$2 LIMIT 1)::uuid AS run_id;

-- name: CompletionHasProductionDeploy :one
WITH RECURSIVE tree(id) AS (SELECT sqlc.arg(run_id)::uuid UNION ALL SELECT r.id FROM runs r JOIN tree t ON r.parent_run_id=t.id WHERE r.project_id=sqlc.arg(project_id))
SELECT EXISTS(SELECT 1 FROM steps s WHERE s.run_id IN (SELECT id FROM tree) AND s.type='deploy' AND s.status=sqlc.arg(status) AND COALESCE(s.config_json->>'deployment','main') IN ('prod','main'));

-- name: DashboardActiveRuns :many
SELECT r.id AS run_id,r.number AS run_number,r.change_id,COALESCE(c.number,0)::bigint AS change_number,COALESCE(c.title,'')::text AS item_title,r.workflow_name,r.current_stage,p.slug AS project_slug
FROM runs r JOIN projects p ON p.id=r.project_id LEFT JOIN changes c ON c.id=r.change_id AND c.project_id=r.project_id
WHERE r.project_id=ANY(sqlc.arg(project_ids)::uuid[]) AND r.status=sqlc.arg(running)
AND NOT EXISTS(SELECT 1 FROM steps s WHERE s.run_id=r.id AND s.stage=r.current_stage AND s.type='approval' AND s.status IN (sqlc.arg(pending),sqlc.arg(needs_human)))
ORDER BY r.updated_at DESC,r.id DESC LIMIT 20;

-- name: DispatchRestartCandidates :many
SELECT r.id FROM runs r JOIN steps s ON s.run_id=r.id AND s.stage=r.current_stage
WHERE r.status='running' AND s.type=ANY($1::text[])
GROUP BY r.id HAVING bool_or(s.status='pending' OR (s.status='error' AND s.evidence_json->>'reason'='orphaned by apptownd restart'))
OR bool_and(s.status IN ('pass','skipped'))
UNION SELECT r.id FROM run_starts st JOIN runs r ON r.id=st.run_id
WHERE r.status='running' AND (NOT st.ready OR (r.parent_run_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM steps s WHERE s.run_id=r.id AND s.stage=r.current_stage AND s.type=ANY($1::text[]))))
ORDER BY id;

-- name: HasActiveLanding :one
SELECT EXISTS(SELECT 1 FROM runs WHERE project_id=$1 AND parent_run_id IS NULL AND status=$2 AND definition_json @> '{"role":"landing"}');

-- name: IsAcceptedWorkingRun :one
SELECT EXISTS(SELECT 1 FROM runs WHERE id=sqlc.arg(id) AND project_id=sqlc.arg(project_id) AND change_id IS NOT DISTINCT FROM sqlc.arg(change_id) AND parent_run_id IS NULL AND status=sqlc.arg(status) AND definition_json @> '{"role":"working"}');

-- name: IsChangeRootRun :one
SELECT EXISTS(SELECT 1 FROM runs WHERE id=sqlc.arg(id) AND project_id=sqlc.arg(project_id) AND change_id IS NOT DISTINCT FROM sqlc.arg(change_id) AND parent_run_id IS NULL);

-- Identity selection precedes canonical record validation; avoid definition/inputs.
-- name: LatestLandingRunMeta :one
SELECT id FROM runs WHERE project_id=$1 AND change_id IS NOT DISTINCT FROM $2 AND workflow_name='land' AND parent_run_id IS NULL ORDER BY number DESC LIMIT 1;

-- Narrative only needs identities/names/states, not large definitions/inputs.
-- name: NarrativeRunsMeta :many
SELECT id,workflow_name,status FROM runs WHERE change_id IS NOT DISTINCT FROM $1 AND project_id=$2 AND parent_run_id IS NULL ORDER BY number;

-- Parent pin checks need the identity and pin only, not its large definition.
-- name: ParentRunAdoptionMeta :one
SELECT adoption_id FROM runs WHERE id=$1 AND project_id=$2;

-- Scope checks need identity without parsing potentially invalid state or
-- pulling the pinned definition/inputs bodies before authorization.
-- name: RunProjectMeta :one
SELECT project_id FROM runs WHERE id=$1;

-- name: RunScopesMeta :many
SELECT id,project_id FROM runs WHERE id=ANY($1::uuid[]);

-- name: RunTimingReport :many
WITH selected AS (
 SELECT id,workflow_name,created_at FROM runs
 WHERE ((sqlc.narg(run_id)::uuid IS NOT NULL AND id=sqlc.narg(run_id)) OR
  (sqlc.narg(run_id)::uuid IS NULL AND (sqlc.arg(workflow_name)::text='' OR workflow_name=sqlc.arg(workflow_name)) AND created_at>sqlc.arg(since)))
 AND project_id=ANY(sqlc.arg(project_ids)::uuid[])
), reworks AS (
 SELECT e.run_id,e.payload_json->>'step' AS step,COUNT(*)::bigint AS n
 FROM run_events e JOIN selected r ON r.id=e.run_id
 WHERE e.kind='step.rework' AND jsonb_typeof(e.payload_json->'step')='string'
 GROUP BY e.run_id,e.payload_json->>'step'
)
SELECT r.id AS run_id,r.workflow_name,r.created_at,s.id AS step_id,
 COALESCE(s.stage,'')::text AS stage,COALESCE(s.name,'')::text AS name,COALESCE(s.type,'')::text AS type,COALESCE(s.status,'')::text AS status,
 s.started_at,s.finished_at,COALESCE(s.evidence_json,'{}'::jsonb) AS evidence_json,COALESCE(w.n,0)::bigint AS reworks
FROM selected r LEFT JOIN steps s ON s.run_id=r.id
LEFT JOIN reworks w ON w.run_id=r.id AND w.step=s.name
ORDER BY r.id,s.id;

-- Run workspace guard compares only state/stage/revision, not pinned definition.
-- name: RunWorkspaceGuardMeta :one
SELECT status,current_stage,retry_revision FROM runs WHERE id=$1 AND project_id=$2;

-- name: RunningWorkflowCount :many
SELECT COUNT(*) FROM runs WHERE project_id=sqlc.arg(project_id) AND workflow_name=sqlc.arg(workflow_name) AND status=sqlc.arg(status);

-- name: SendBackActiveRoots :many
SELECT id FROM runs WHERE change_id IS NOT DISTINCT FROM $1 AND parent_run_id IS NULL AND status='running' ORDER BY number;

-- Lock admission resolves identity only; it must not parse mutable run state
-- before the project lock, and it avoids large definition/inputs values.
-- name: StepRunIdentityMeta :one
SELECT r.project_id,r.id AS run_id FROM runs r JOIN steps s ON s.run_id=r.id WHERE s.id=$1;

-- Activity metadata omits large definition/config/evidence payloads except the
-- call evidence needed for ownership validation. UNION deliberately deduplicates.
-- name: WorkflowActivity :many
WITH RECURSIVE activity_tree(id,project_id,parent_run_id,parent_step_id,parent_step_attempt,status,current_stage) AS (
SELECT seed.id,seed.project_id,seed.parent_run_id,seed.parent_step_id,seed.parent_step_attempt,seed.status,seed.current_stage FROM runs seed WHERE seed.id=ANY(sqlc.arg(ids)::uuid[])
UNION
SELECT child.id,child.project_id,child.parent_run_id,child.parent_step_id,child.parent_step_attempt,child.status,child.current_stage
FROM activity_tree parent JOIN runs child ON child.parent_run_id=parent.id AND parent.status=sqlc.arg(run_running)::text AND child.project_id=parent.project_id
JOIN steps call ON call.id=child.parent_step_id AND call.run_id=parent.id AND call.stage=parent.current_stage AND call.type='workflow/call' AND call.status=sqlc.arg(step_running) AND call.attempt=child.parent_step_attempt
) SELECT r.id,r.project_id,r.parent_run_id,r.parent_step_id,r.parent_step_attempt,r.status,
COALESCE(CASE WHEN r.status='abandoned' THEN (SELECT e.payload_json->>'reason' FROM run_events e WHERE e.run_id=r.id AND e.kind='run.abandoned' ORDER BY e.id DESC LIMIT 1) END,'')::text AS abandonment_reason,
s.id AS step_id,COALESCE(s.attempt,0)::bigint AS step_attempt,COALESCE(s.name,'')::text AS step_name,COALESCE(s.type,'')::text AS step_type,COALESCE(s.status,'')::text AS step_status,
COALESCE(CASE WHEN s.type='workflow/call' THEN s.evidence_json ELSE jsonb_build_object('placement',s.evidence_json->'placement') END,'{}'::jsonb)::jsonb AS evidence,COALESCE(s.override_by,'')::text AS step_override_by,
COALESCE(g.stage_declaration,'null'::jsonb)::jsonb AS stage_declaration,COALESCE(g.stage_checks,'null'::jsonb)::jsonb AS stage_checks,
s.finished_at AS step_finished_at,
EXISTS(SELECT 1 FROM workflow_landings l WHERE l.landing_run_id=r.id AND NOT l.acceptance_applied) AS awaiting_acceptance
FROM activity_tree r
LEFT JOIN LATERAL (
  SELECT (SELECT st FROM runs rd, jsonb_array_elements(rd.definition_json->'stages') st WHERE rd.id=r.id AND st->>'name'=r.current_stage LIMIT 1) AS stage_declaration,
         (SELECT e.payload_json->'checks' FROM run_events e WHERE e.run_id=r.id AND e.kind='stage.entered' AND e.payload_json->>'stage'=r.current_stage ORDER BY e.id DESC LIMIT 1) AS stage_checks
  WHERE r.status=sqlc.arg(run_running)::text
) g ON true
LEFT JOIN steps s ON s.run_id=r.id AND s.stage=r.current_stage ORDER BY r.id,s.id;

-- name: WorkflowRunCounts :many
SELECT workflow_name,COUNT(*)::bigint AS runs FROM runs WHERE project_id=$1 GROUP BY workflow_name;

-- name: WorkflowSource :one
WITH RECURSIVE ancestry AS (
  SELECT r.id,r.parent_run_id,r.project_id,r.change_id,1 AS depth FROM runs r
  WHERE r.id=sqlc.arg(run_id) AND r.project_id=sqlc.arg(project_id)
  UNION ALL
  SELECT parent.id,parent.parent_run_id,parent.project_id,parent.change_id,child.depth+1
  FROM runs parent JOIN ancestry child ON parent.id=child.parent_run_id
  WHERE child.depth < sqlc.arg(max_depth)::integer AND parent.project_id=child.project_id
), root AS (
  SELECT * FROM ancestry ORDER BY depth DESC LIMIT 1
), lineage AS (
  SELECT root.id,root.project_id,root.change_id,true AS valid FROM root
  UNION ALL
  SELECT previous.id,previous.project_id,previous.change_id,
    previous.id<current.id AND previous.project_id=current.project_id AND previous.change_id IS NOT DISTINCT FROM current.change_id
  FROM lineage current JOIN workflow_continuations c ON c.run_id=current.id
  JOIN runs previous ON previous.id=c.source_run_id WHERE current.valid
)
SELECT root.id AS root_run_id,root.parent_run_id,root.change_id AS root_change_id,root.depth,
  owner.base_sha AS root_base_sha,owner.definition_json AS root_definition,
  current_run.base_sha AS run_base_sha,current_run.workflow_name,current_run.workflow_version,current_run.number AS run_number,
  (SELECT id FROM lineage ORDER BY id LIMIT 1)::uuid AS workspace_run_id,
  COALESCE((SELECT bool_and(valid) FROM lineage),false)::boolean AS valid_lineage,
  project.slug,project.default_branch,COALESCE(current_adoption.revision,0)::bigint AS adoption_revision,
  change.project_id AS change_project_id,COALESCE(change.number,0)::bigint AS change_number,
  COALESCE(change.branch,'')::text AS branch,COALESCE(change.type,'')::text AS change_type,
  COALESCE(change.base_sha,'')::text AS change_base_sha,
  COALESCE(change.expected_revision,0)::bigint AS expected_revision,
  (mr.id IS NOT NULL)::boolean AS has_mr,COALESCE(mr.name,'')::text AS mr_name,COALESCE(mr.body,'')::text AS mr_project,
  COALESCE((SELECT e.payload_json FROM run_events e WHERE e.run_id=sqlc.arg(run_id) AND e.kind='landing.candidate' ORDER BY e.id DESC LIMIT 1),'null'::jsonb)::jsonb AS landing_candidate,
  EXISTS(SELECT 1 FROM run_events e WHERE e.run_id=sqlc.arg(run_id) AND e.kind='landing.pin.required')::boolean AS landing_pin_required
FROM root JOIN projects project ON project.id=root.project_id
LEFT JOIN adoptions current_adoption ON current_adoption.project_id=project.id
JOIN runs owner ON owner.id=root.id
JOIN runs current_run ON current_run.id=sqlc.arg(run_id)
LEFT JOIN changes change ON change.id=root.change_id
LEFT JOIN LATERAL (
  SELECT a.id,a.name,a.body FROM artifacts a WHERE a.run_id=sqlc.arg(run_id) AND a.kind='mr' ORDER BY a.id DESC LIMIT 1
) mr ON true;

-- Summary projection excludes definitions/inputs and carries project identity.
-- name: WorkflowSummaries :many
SELECT r.id,r.number,r.status,r.current_stage,r.workflow_name,r.workflow_version,r.kind,r.change_id,
COALESCE(c.number,0)::bigint AS change_number,COALESCE(c.title,'')::text AS item_title,p.slug AS project_slug,r.created_at,r.updated_at
FROM runs r JOIN projects p ON p.id=r.project_id LEFT JOIN changes c ON c.id=r.change_id AND c.project_id=r.project_id
WHERE (sqlc.narg(project_id)::uuid IS NULL OR r.project_id=sqlc.narg(project_id)) AND (sqlc.arg(project_slug)::text='' OR p.slug=sqlc.arg(project_slug))
AND (sqlc.arg(status)::text='' OR r.status=sqlc.arg(status)) AND (NOT sqlc.arg(roots_only)::boolean OR r.parent_run_id IS NULL)
AND (sqlc.arg(project_ids)::uuid[] IS NULL OR r.project_id=ANY(sqlc.arg(project_ids)::uuid[]))
ORDER BY r.updated_at DESC,r.id DESC LIMIT NULLIF(sqlc.arg(limit_count)::integer,0);

-- name: WorkflowUsageReport :many
SELECT workflow_name,COUNT(*)::bigint AS runs,MAX(created_at)::timestamptz AS last_run_at
FROM runs WHERE project_id=$1 GROUP BY workflow_name;

-- name: AbandonFailedLandingEligible :one
SELECT NOT EXISTS(SELECT 1 FROM steps s WHERE s.run_id=$1 AND status IN ('running','pending') AND type<>'approval') AND EXISTS(SELECT 1 FROM steps s WHERE s.run_id=$1 AND status IN ('fail','error'));

-- Start admission needs run identity, role and counts; definition/inputs can be large.
-- name: ActiveChangeFlightsMeta :many
SELECT r.id,COALESCE(r.definition_json->>'role','')::text AS role,
 (SELECT COUNT(*) FROM steps s WHERE s.run_id=r.id AND s.status IN ('running','pending') AND s.type<>'approval')::bigint AS active,
 (SELECT COUNT(*) FROM steps s WHERE s.run_id=r.id AND s.status IN ('fail','error'))::bigint AS failed
FROM runs r WHERE r.project_id=$1 AND r.change_id IS NOT DISTINCT FROM $2 AND r.status='running' AND r.parent_run_id IS NULL ORDER BY r.number;

-- name: AgentSessionObservation :one
SELECT COUNT(e.id)::bigint AS dispatches,COALESCE((array_agg(e.payload_json->>'sessionId' ORDER BY e.id DESC))[1],'')::text AS session_id
FROM steps s JOIN runs r ON r.id=s.run_id
LEFT JOIN run_events e ON e.run_id=r.id AND e.kind='step.started' AND e.payload_json->>'step'=s.name
WHERE s.id=sqlc.arg(step_id) AND r.id=sqlc.arg(run_id) AND r.project_id=sqlc.arg(project_id) GROUP BY s.id;

-- name: ApprovalInbox :many
SELECT sqlc.embed(r),sqlc.embed(s),p.slug AS project_slug,p.name AS project_name,COALESCE(c.title,'')::text AS change_title
FROM steps s JOIN runs r ON r.id=s.run_id JOIN projects p ON p.id=r.project_id
LEFT JOIN changes c ON c.id=r.change_id AND c.project_id=r.project_id
WHERE r.project_id=ANY(sqlc.arg(projects)::uuid[]) AND r.status=sqlc.arg(running_state)
AND s.stage=r.current_stage AND s.type='approval' AND s.status IN (sqlc.arg(pending_state),sqlc.arg(human_state)) ORDER BY s.id;

-- name: CompletedParent :one
SELECT sqlc.embed(r), EXISTS (
  SELECT 1 FROM steps s JOIN runs child ON child.id=sqlc.arg(child_run_id)
    AND child.parent_run_id=r.id AND child.parent_step_id=s.id AND child.parent_step_attempt=s.attempt
    AND child.project_id=r.project_id AND child.status='done'
  WHERE s.id=sqlc.arg(parent_step_id) AND s.run_id=r.id AND s.stage=r.current_stage
    AND r.project_id=sqlc.arg(project_id) AND s.status='pass' AND s.attempt=sqlc.arg(parent_step_attempt)
    AND s.evidence_json->>'childRunId'=sqlc.arg(child_run_id)::uuid::text AND s.evidence_json->>'outcome'='done'
) AS current_call FROM runs r WHERE r.id=sqlc.arg(parent_run_id);

-- name: ContinuationStepCount :one
SELECT count(*) FROM steps WHERE run_id=$1 AND stage=$2;

-- name: DashboardPendingApprovals :many
SELECT s.id AS step_id,s.name AS step_name,s.stage,r.id AS run_id,r.change_id,COALESCE(c.number,0)::bigint AS change_number,COALESCE(c.title,'')::text AS item_title,p.slug AS project_slug,p.name AS project_name
FROM steps s JOIN runs r ON r.id=s.run_id JOIN projects p ON p.id=r.project_id
LEFT JOIN changes c ON c.id=r.change_id AND c.project_id=r.project_id
WHERE r.project_id=ANY(sqlc.arg(project_ids)::uuid[]) AND s.type='approval' AND s.status IN (sqlc.arg(pending),sqlc.arg(needs_human))
AND r.status=sqlc.arg(running) AND s.stage=r.current_stage ORDER BY s.id;

-- name: DispatchStepState :one
SELECT s.status, (s.status='error' AND COALESCE(s.evidence_json->>'reason','')='orphaned by apptownd restart') AS orphaned,
 (r.status='running' AND r.current_stage=s.stage) AS active
FROM steps s JOIN runs r ON r.id=s.run_id WHERE s.run_id=$1 AND s.id=$2;

-- A step attempt's runtime credential names only the step and attempt. Its
-- run, project, liveness and billing owner are read in one snapshot.
-- name: StepAttemptObservation :one
SELECT r.id AS run_id, r.project_id, r.status,
 (s.attempt=sqlc.arg(attempt) AND s.status=sqlc.arg(running_step_state))::boolean AS live_attempt,
 COALESCE(o.actor,'run/' || r.id::text)::text AS owner,
 o.actor_principal AS owner_principal
FROM steps s JOIN runs r ON r.id=s.run_id LEFT JOIN run_inference_owners o ON o.run_id=r.id
WHERE s.id=sqlc.arg(step_id);

-- name: InterruptPreservedCalls :one
SELECT COUNT(*) FROM steps WHERE status=$1;

-- name: LandingApprover :many
SELECT s.evidence_json
FROM steps s JOIN runs r ON r.id=s.run_id JOIN changes c ON c.id=r.change_id AND c.project_id=r.project_id
WHERE r.project_id=sqlc.arg(project_id) AND c.id=sqlc.arg(change_id) AND r.workflow_name='land' AND s.type='approval' AND s.status=sqlc.arg(pass_state)
ORDER BY s.finished_at DESC NULLS LAST,s.id DESC LIMIT 1;

-- name: LatestFinishedAgentWork :one
SELECT COALESCE(MAX(finished_at) FILTER (WHERE status='pass'), 'epoch'::timestamptz)::timestamptz AS latest_work,
 (COUNT(*) FILTER (WHERE status='pass' AND finished_at IS NOT NULL)>0)::boolean AS has_latest_work,
 COALESCE(bool_or(status IN ('pending','running')),false)::boolean AS busy
FROM steps WHERE run_id=$1 AND stage=$2 AND type='agent/pi';

-- name: LatestRetryFeedback :one
SELECT evidence_json::text FROM steps WHERE run_id=$1 AND stage=$2 AND status='fail'
AND type IN ('approval','agent/review','preview','guidance/conformance','tests')
ORDER BY finished_at DESC NULLS LAST,id DESC LIMIT 1;

-- Preview identity only; step config/evidence can be large.
-- name: PreviewStepIDsMeta :many
SELECT id FROM steps WHERE run_id=$1 AND type='preview';

-- name: ProjectRemovalCounts :one
SELECT
 (SELECT COUNT(*) FROM steps s JOIN runs r ON r.id=s.run_id WHERE r.project_id=$1)::bigint AS steps,
 (SELECT COUNT(*) FROM run_events e JOIN runs r ON r.id=e.run_id WHERE r.project_id=$1)::bigint AS events,
 (SELECT COUNT(*) FROM artifacts a JOIN runs r ON r.id=a.run_id WHERE r.project_id=$1)::bigint AS artifacts;

-- name: ProjectRemovalRemainder :one
SELECT EXISTS(SELECT 1 FROM agent_transcripts t JOIN runs r ON r.id=t.run_id WHERE r.project_id=$1) AS transcripts,
 EXISTS(SELECT 1 FROM steps s JOIN runs r ON r.id=s.run_id WHERE r.project_id=$1) AS steps,
 EXISTS(SELECT 1 FROM run_events e JOIN runs r ON r.id=e.run_id WHERE r.project_id=$1) AS events,
 EXISTS(SELECT 1 FROM artifacts a JOIN runs r ON r.id=a.run_id WHERE r.project_id=$1) AS artifacts,
 EXISTS(SELECT 1 FROM runs WHERE project_id=$1) AS runs,
 EXISTS(SELECT 1 FROM changes WHERE project_id=$1) AS changes;

-- name: RevisingAgentExists :one
SELECT EXISTS(SELECT 1 FROM steps WHERE run_id=$1 AND stage=$2 AND type='agent/pi' AND status IN ('pending','running'));

-- name: RunningSteps :many
SELECT p.slug AS project,r.id AS run_id,r.change_id,COALESCE(c.title,'')::text AS title,s.id AS step_id,s.name,s.type,s.started_at
FROM steps s JOIN runs r ON r.id=s.run_id JOIN projects p ON p.id=r.project_id
LEFT JOIN changes c ON c.id=r.change_id AND c.project_id=r.project_id
WHERE r.project_id=ANY(sqlc.arg(project_ids)::uuid[]) AND s.status=sqlc.arg(running) ORDER BY s.started_at NULLS LAST,s.id;

-- Every project, no visibility fence: the instance-wide answer to "what would
-- a restart interrupt", admitted only to a caller who reads every run. A
-- running workflow/call step only waits on its child, whose own steps are
-- listed when they execute.
-- name: ExecutingSteps :many
SELECT p.slug AS project,r.id AS run_id,r.change_id,COALESCE(c.title,'')::text AS title,s.id AS step_id,s.name,s.type,s.started_at
FROM steps s JOIN runs r ON r.id=s.run_id JOIN projects p ON p.id=r.project_id
LEFT JOIN changes c ON c.id=r.change_id AND c.project_id=r.project_id
WHERE s.status=sqlc.arg(running) AND s.type<>'workflow/call' ORDER BY s.started_at NULLS LAST,s.id;

-- Identity only, every project: the startup precondition of -no-local-docker.
-- name: RunningStepsMeta :many
SELECT id,run_id,type FROM steps WHERE status=sqlc.arg(running) ORDER BY id;

-- name: SendBackBusy :one
SELECT EXISTS(SELECT 1 FROM steps s JOIN runs r ON r.id=s.run_id WHERE r.change_id IS NOT DISTINCT FROM $1 AND r.status='running' AND s.status='running');

-- name: SlotSpendRows :many
SELECT COALESCE(evidence_json #>> '{modelSlot}', '')::text AS model_slot,
 COALESCE((evidence_json #>> '{usage,input}')::bigint, 0)::bigint AS input_tokens,
 COALESCE((evidence_json #>> '{usage,output}')::bigint, 0)::bigint AS output_tokens
FROM steps WHERE evidence_json ? 'modelSlot' AND finished_at > sqlc.arg(since)::timestamptz;

-- Decision snapshots read only identity, attempt, revision and step config;
-- large run definition/inputs and unrelated evidence are excluded.
-- name: StepDecisionSnapshotMeta :one
SELECT s.id AS step_id,r.id AS run_id,r.project_id,r.change_id,s.attempt,r.retry_revision,r.current_stage,s.type,s.config_json
FROM steps s JOIN runs r ON r.id=s.run_id WHERE s.id=$1;

-- name: StepRunIDMeta :one
SELECT run_id FROM steps WHERE id=$1;

-- name: LandingPending :one
SELECT l.landing_run_id AS landing_id,EXISTS(SELECT 1 FROM workflow_continuations WHERE source_run_id=r.id) AS superseded
FROM runs r LEFT JOIN workflow_landings l ON l.work_run_id=r.id WHERE r.id=$1;

-- A landing started from accepted work that has not yet taken the acceptance.
-- name: LandingAwaitingAcceptance :one
SELECT EXISTS(SELECT 1 FROM workflow_landings WHERE landing_run_id IS NOT DISTINCT FROM $1 AND NOT acceptance_applied);

-- Accepted work whose landing is reserved but not yet started, while its change
-- still waits to land.
-- name: LandingQueued :one
SELECT EXISTS(SELECT 1 FROM workflow_landings l JOIN runs r ON r.id=l.work_run_id JOIN changes c ON c.id=r.change_id
WHERE l.work_run_id=sqlc.arg(work_run_id) AND l.landing_run_id IS NULL AND c.state=sqlc.arg(change_state)::text
AND NOT EXISTS(SELECT 1 FROM workflow_continuations WHERE source_run_id=l.work_run_id));

-- name: LandingSuperseded :one
SELECT EXISTS(SELECT 1 FROM workflow_continuations WHERE source_run_id=$1);

-- name: WasRunSentBack :one
SELECT EXISTS(SELECT 1 FROM workflow_continuations WHERE source_run_id=$1);

-- name: RecentProjectIDs :many
SELECT p.id FROM projects p
LEFT JOIN runs r ON r.project_id=p.id AND r.project_id=ANY(sqlc.arg(run_project_ids)::uuid[])
WHERE p.id=ANY(sqlc.arg(project_ids)::uuid[])
GROUP BY p.id,p.namespace,p.name
ORDER BY MAX(r.updated_at) DESC NULLS LAST,p.namespace,p.name,p.id
LIMIT sqlc.arg(row_limit)::bigint;

-- name: LatestRunEventOfKind :one
SELECT id, payload_json FROM run_events WHERE run_id=sqlc.arg(run_id) AND kind=sqlc.arg(kind) ORDER BY id DESC LIMIT 1;

-- name: EarliestRunEventOfKind :one
SELECT id, payload_json FROM run_events WHERE run_id=sqlc.arg(run_id) AND kind=sqlc.arg(kind) ORDER BY id ASC LIMIT 1;
-- name: StepStartReceipt :one
SELECT payload_json FROM run_events WHERE run_id=sqlc.arg(run_id) AND kind='step.started'
AND payload_json->>'step'=sqlc.arg(step)::text AND payload_json->>'stage'=sqlc.arg(stage)::text AND (payload_json->>'attempt')::bigint=sqlc.arg(attempt)::bigint
ORDER BY id DESC LIMIT 1;

-- name: RunTreeActivity :one
WITH RECURSIVE tree(id) AS (SELECT root.id FROM runs root WHERE root.id=sqlc.arg(root_id) AND root.project_id=sqlc.arg(project_id) UNION SELECT r.id FROM runs r JOIN tree t ON r.parent_run_id=t.id WHERE r.project_id=sqlc.arg(project_id))
SELECT
  EXISTS(SELECT 1 FROM runs r WHERE r.id IN (SELECT id FROM tree) AND r.status=sqlc.arg(run_running)) AS running,
  EXISTS(SELECT 1 FROM steps s WHERE s.run_id IN (SELECT id FROM tree) AND s.status=sqlc.arg(step_running)) AS stepping,
  COALESCE((SELECT array_agg(id ORDER BY id) FROM tree), '{}')::uuid[] AS run_ids;

-- name: RunRoot :one
WITH RECURSIVE ancestry AS (
  SELECT r.id,r.parent_run_id,1 AS depth FROM runs r WHERE r.id=sqlc.arg(run_id)
  UNION ALL
  SELECT parent.id,parent.parent_run_id,child.depth+1
  FROM runs parent JOIN ancestry child ON parent.id=child.parent_run_id
  WHERE child.depth < sqlc.arg(max_depth)::integer
)
SELECT id FROM ancestry ORDER BY depth DESC LIMIT 1;

-- A change's id from its number within its project: the friendly form
-- people type ("egg-catch#12") resolves through it.
-- name: ChangeIDByNumber :one
SELECT id FROM changes WHERE project_id=sqlc.arg(project_id) AND number=sqlc.arg(number);

-- A run's id from its number within its project ("egg-catch#40").
-- name: RunIDByNumber :one
SELECT id FROM runs WHERE project_id=sqlc.arg(project_id) AND number=sqlc.arg(number);

-- name: ChangeIDForBranch :one
SELECT id FROM changes WHERE project_id=sqlc.arg(project_id) AND branch=sqlc.arg(branch) ORDER BY number DESC LIMIT 1;

-- An attempt's transcript is recorded only against its own start receipt,
-- which names the session the attempt ran.
-- name: AttemptSessionStarted :one
SELECT EXISTS(SELECT 1 FROM steps s JOIN runs r ON r.id=s.run_id
JOIN run_events e ON e.run_id=s.run_id AND e.kind='step.started' AND e.payload_json->>'step'=s.name
WHERE s.id=sqlc.arg(step_id) AND r.id=sqlc.arg(run_id) AND r.project_id=sqlc.arg(project_id)
AND e.payload_json->>'attempt'=sqlc.arg(attempt)::bigint::text AND e.payload_json->>'sessionId'=sqlc.arg(session_id)::text);

-- Each invocation of an attempt records its own row, once: a replay changes
-- nothing, and no stored file loses its reference.
-- name: RecordAgentTranscript :execrows
INSERT INTO agent_transcripts(run_id,step_id,attempt,session_id,invocation,digest,unavailable,recorded_at)
VALUES (sqlc.arg(run_id),sqlc.arg(step_id),sqlc.arg(attempt),sqlc.arg(session_id),sqlc.arg(invocation),sqlc.arg(digest),sqlc.arg(unavailable),sqlc.arg(recorded_at)::timestamptz)
ON CONFLICT (step_id,attempt,invocation) DO NOTHING;

-- The history a continuing attempt resumes: the latest other attempt in the
-- project whose start receipt names the session, and what it kept.
-- name: EarlierSessionTranscript :one
SELECT s.id AS step_id,COALESCE((e.payload_json->>'attempt')::bigint,0)::bigint AS attempt,
(t.step_id IS NOT NULL)::boolean AS recorded,COALESCE(t.digest,'')::text AS digest,COALESCE(t.unavailable,'')::text AS unavailable
FROM run_events e JOIN runs r ON r.id=e.run_id JOIN steps s ON s.run_id=e.run_id AND s.name=e.payload_json->>'step'
LEFT JOIN LATERAL (SELECT x.step_id,x.digest,x.unavailable FROM agent_transcripts x WHERE x.step_id=s.id AND x.attempt=COALESCE((e.payload_json->>'attempt')::bigint,0) ORDER BY x.invocation DESC LIMIT 1) t ON true
WHERE r.project_id=sqlc.arg(project_id) AND e.kind='step.started' AND e.payload_json->>'sessionId'=sqlc.arg(session_id)::text
AND NOT (s.id=sqlc.arg(step_id) AND COALESCE((e.payload_json->>'attempt')::bigint,0)=sqlc.arg(attempt)::bigint)
ORDER BY e.id DESC LIMIT 1;

-- The history an attempt of a step kept; attempt 0 is its latest attempt.
-- name: StepTranscript :one
SELECT COALESCE((e.payload_json->>'attempt')::bigint,0)::bigint AS attempt,COALESCE(e.payload_json->>'sessionId','')::text AS session_id,
(t.step_id IS NOT NULL)::boolean AS recorded,COALESCE(t.digest,'')::text AS digest,COALESCE(t.unavailable,'')::text AS unavailable
FROM steps s JOIN runs r ON r.id=s.run_id JOIN run_events e ON e.run_id=s.run_id AND e.kind='step.started' AND e.payload_json->>'step'=s.name
LEFT JOIN LATERAL (SELECT x.step_id,x.digest,x.unavailable FROM agent_transcripts x WHERE x.step_id=s.id AND x.attempt=COALESCE((e.payload_json->>'attempt')::bigint,0) ORDER BY x.invocation DESC LIMIT 1) t ON true
WHERE s.id=sqlc.arg(step_id) AND r.id=sqlc.arg(run_id) AND r.project_id=sqlc.arg(project_id)
AND (sqlc.arg(attempt)::bigint=0 OR e.payload_json->>'attempt'=sqlc.arg(attempt)::bigint::text)
ORDER BY e.id DESC LIMIT 1;

-- name: ProjectTranscriptDigests :many
SELECT DISTINCT t.digest FROM agent_transcripts t JOIN runs r ON r.id=t.run_id WHERE r.project_id=$1 AND t.digest<>'' ORDER BY t.digest;

-- name: DeleteProjectTranscripts :exec
DELETE FROM agent_transcripts WHERE run_id IN (SELECT id FROM runs WHERE project_id=$1);

-- name: TranscriptReferenced :one
SELECT EXISTS(SELECT 1 FROM agent_transcripts WHERE digest=$1);

-- Agent attempts whose start receipt names a session but that recorded no
-- history: runs from before transcripts were kept, and attempts the forge
-- lost mid-invocation. An attempt still running is not one: it records its
-- own. superseded: a later attempt in the project continued the session.
-- name: UnrecordedSessionStarts :many
SELECT r.id AS run_id,r.project_id,s.id AS step_id,COALESCE((e.payload_json->>'attempt')::bigint,0)::bigint AS attempt,
(e.payload_json->>'sessionId')::text AS session_id,
EXISTS(SELECT 1 FROM run_events l JOIN runs lr ON lr.id=l.run_id WHERE lr.project_id=r.project_id AND l.kind='step.started'
  AND l.payload_json->>'sessionId'=e.payload_json->>'sessionId' AND l.id>e.id)::boolean AS superseded
FROM run_events e JOIN runs r ON r.id=e.run_id JOIN steps s ON s.run_id=e.run_id AND s.name=e.payload_json->>'step'
WHERE e.kind='step.started' AND COALESCE(e.payload_json->>'sessionId','')<>'' AND COALESCE((e.payload_json->>'attempt')::bigint,0)>0
AND NOT EXISTS(SELECT 1 FROM agent_transcripts t WHERE t.step_id=s.id AND t.attempt=COALESCE((e.payload_json->>'attempt')::bigint,0))
AND NOT (s.status='running' AND s.attempt=COALESCE((e.payload_json->>'attempt')::bigint,0))
ORDER BY e.id LIMIT sqlc.arg(max_rows);

-- name: MarkRunLandingAuthority :execrows
UPDATE runs SET landing_authority = sqlc.arg(by), landing_authority_principal = sqlc.arg(by_principal), landing_authority_via = sqlc.arg(by_via) WHERE id=sqlc.arg(id) AND landing_authority = '';

-- name: RunLandingAuthority :one
SELECT parent_run_id, landing_authority FROM runs WHERE id = $1;

-- The evals owner's copy record, read where an eval's authority is judged:
-- which eval a project is a copy of, if any. A read of another owner's
-- table, never a write (scripts/schema-ownership.py).
-- name: EvalCopyOf :one
SELECT eval_id FROM eval_copies WHERE project_id = $1;

-- A project's Work, row by row, for moving it to another forge: each row
-- whole, as JSON, so the archive holds what the source held. Only landed
-- history moves: shipped changes, and the runs that are not
-- running and are on a shipped change or on none. Read in the export's
-- repeatable-read snapshot.
-- name: ExportChanges :many
SELECT to_jsonb(c)::text FROM changes c WHERE c.project_id = $1 AND c.state = 'shipped' ORDER BY c.id;

-- name: ExportRuns :many
SELECT to_jsonb(r)::text FROM runs r WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped')) ORDER BY r.id;

-- name: ExportContinuations :many
SELECT to_jsonb(w)::text FROM workflow_continuations w JOIN runs r ON r.id = w.source_run_id
WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped')) AND w.run_id IN (SELECT r.id FROM runs r WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped'))) ORDER BY w.source_run_id;

-- name: ExportLandings :many
SELECT to_jsonb(w)::text FROM workflow_landings w JOIN runs r ON r.id = w.work_run_id
WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped')) AND (w.landing_run_id IS NULL OR w.landing_run_id IN (SELECT r.id FROM runs r WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped')))) ORDER BY w.work_run_id;

-- name: ExportSteps :many
SELECT to_jsonb(s)::text FROM steps s JOIN runs r ON r.id = s.run_id WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped')) ORDER BY s.id;

-- name: ExportRunEvents :many
SELECT to_jsonb(e)::text FROM run_events e JOIN runs r ON r.id = e.run_id WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped')) ORDER BY e.id;

-- name: ExportArtifacts :many
SELECT to_jsonb(a)::text FROM artifacts a JOIN runs r ON r.id = a.run_id WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped')) ORDER BY a.id;

-- name: ExportRunInferenceOwners :many
SELECT to_jsonb(o)::text FROM run_inference_owners o JOIN runs r ON r.id = o.run_id WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped')) ORDER BY o.run_id;

-- name: ExportAgentTranscripts :many
SELECT to_jsonb(t)::text FROM agent_transcripts t JOIN runs r ON r.id = t.run_id
WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped')) ORDER BY t.step_id, t.attempt, t.invocation;

-- The commits the moving rows name, which the bundle must carry.
-- name: ExportNamedCommits :many
SELECT sha FROM (
  SELECT c.base_sha AS sha FROM changes c WHERE c.project_id = $1 AND c.state = 'shipped'
  UNION SELECT c.landed_sha FROM changes c WHERE c.project_id = $1 AND c.state = 'shipped'
  UNION SELECT r.base_sha FROM runs r WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped'))
) named WHERE sha <> '' ORDER BY sha;

-- The commits moving landings merged into the default branch: a read of
-- SCM's merge record, never a write. Each must be on the bundled default
-- branch.
-- name: ExportLandedCommits :many
SELECT m.result FROM run_merges m JOIN runs r ON r.id = m.run_id
WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped')) AND m.state = 'merged' AND m.result <> '' ORDER BY m.result;

-- The runs that stay: running, or on a change not landed. Their ledger
-- lines stay with them.
-- name: ExportStayingRuns :many
SELECT r.id FROM runs r WHERE r.project_id = $1 AND NOT (r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped'))) ORDER BY r.id;

-- What stays behind: each change not landed, and each run still running.
-- name: ExportSkipped :many
SELECT 'change'::text AS kind, c.id, c.title, c.state FROM changes c WHERE c.project_id = $1 AND c.state <> 'shipped'
UNION ALL
SELECT 'run'::text, r.id, r.workflow_name, r.status FROM runs r WHERE r.project_id = $1 AND r.status = 'running'
ORDER BY 1, 2;

-- name: NextChangeNumber :one
-- A change's number within its project, allocated in its creating transaction.
INSERT INTO project_numbers (project_id, changes) VALUES (sqlc.arg(project_id), 1)
ON CONFLICT (project_id) DO UPDATE SET changes = project_numbers.changes + 1
RETURNING changes;

-- name: NextRunNumber :one
-- A run's number within its project, allocated in its creating transaction.
INSERT INTO project_numbers (project_id, runs) VALUES (sqlc.arg(project_id), 1)
ON CONFLICT (project_id) DO UPDATE SET runs = project_numbers.runs + 1
RETURNING runs;

-- Every run still open anywhere, for an operation that must wait for none
-- (the AppTown rename's upgrade).
-- name: OpenRunsEverywhere :many
SELECT r.id, r.number, p.slug AS project FROM runs r JOIN projects p ON p.id = r.project_id
WHERE r.status = sqlc.arg(running) ORDER BY p.slug, r.number;

-- The recorded branches of a project's changes that are not finished.
-- name: OpenChangeBranches :many
SELECT c.number, c.branch FROM changes c
WHERE c.project_id = sqlc.arg(project_id) AND c.branch <> '' AND c.state IN (sqlc.arg(proposed), sqlc.arg(in_progress), sqlc.arg(landing))
ORDER BY c.number;
