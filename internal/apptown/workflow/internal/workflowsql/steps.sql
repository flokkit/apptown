-- Steps: their rows, states, attempts and decisions.

-- name: ContinuationAgentStep :one
SELECT * FROM steps WHERE run_id=sqlc.arg(run_id) AND stage=sqlc.arg(stage) AND name=sqlc.arg(name) AND type='agent/pi';

-- name: LatestFeedbackStep :one
SELECT s.* FROM steps s JOIN runs r ON r.id=s.run_id
WHERE r.id=sqlc.arg(run_id) AND r.project_id=sqlc.arg(project_id)
AND ((s.type IN ('approval','agent/review','preview','guidance/conformance','tests') AND s.status=sqlc.arg(failed_state))
 OR (s.type='approval' AND s.evidence_json #>> '{lastRejection,at}' IS NOT NULL))
ORDER BY COALESCE(s.finished_at,NULLIF(s.evidence_json #>> '{lastRejection,at}','')::timestamptz) DESC,s.id DESC LIMIT 1;

-- name: RunSteps :many
SELECT * FROM steps WHERE run_id = sqlc.arg(run_id) AND (NOT sqlc.arg(stage_only)::boolean OR stage = sqlc.arg(stage))
AND (sqlc.narg(project_id)::uuid IS NULL OR EXISTS(SELECT 1 FROM runs WHERE runs.id=steps.run_id AND runs.project_id=sqlc.narg(project_id))) ORDER BY id;

-- name: StepByID :one
SELECT * FROM steps WHERE id = $1;

-- Project removal inventories IDs without loading potentially large Change bodies.
-- Run-step previews name their stacks by step id (pv<step>); a deletion
-- records them before the rows go.
-- name: ProjectPreviewStepsForRemovalMeta :many
SELECT s.id FROM steps s JOIN runs r ON r.id=s.run_id WHERE r.project_id=$1 AND s.type='preview' ORDER BY s.id;

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

-- name: PendingStepsForScopes :many
SELECT r.id AS run_id,r.project_id,s.id AS step_id,s.attempt,s.name,s.type,s.status
FROM generate_subscripts(sqlc.arg(run_ids)::uuid[],1) idx(n)
JOIN runs r ON r.id=(sqlc.arg(run_ids)::uuid[])[idx.n] AND r.project_id=(sqlc.arg(project_ids)::uuid[])[idx.n]
JOIN LATERAL (
 SELECT steps.id,steps.attempt,steps.name,steps.type,steps.status FROM steps WHERE steps.run_id=r.id AND steps.status IN (sqlc.arg(running_state),sqlc.arg(human_state),sqlc.arg(pending_state))
 ORDER BY CASE steps.status WHEN sqlc.arg(running_state) THEN 0 WHEN sqlc.arg(human_state) THEN 1 ELSE 2 END,steps.id LIMIT 1
) s ON true;

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

-- An attempt's transcript is recorded only against its own start receipt,
-- which names the session the attempt ran.
-- name: AttemptSessionStarted :one
SELECT EXISTS(SELECT 1 FROM steps s JOIN runs r ON r.id=s.run_id
JOIN run_events e ON e.run_id=s.run_id AND e.kind='step.started' AND e.payload_json->>'step'=s.name
WHERE s.id=sqlc.arg(step_id) AND r.id=sqlc.arg(run_id) AND r.project_id=sqlc.arg(project_id)
AND e.payload_json->>'attempt'=sqlc.arg(attempt)::bigint::text AND e.payload_json->>'sessionId'=sqlc.arg(session_id)::text);

-- The history an attempt of a step kept; attempt 0 is its latest attempt.
-- name: StepTranscript :one
SELECT COALESCE((e.payload_json->>'attempt')::bigint,0)::bigint AS attempt,COALESCE(e.payload_json->>'sessionId','')::text AS session_id,
(t.step_id IS NOT NULL)::boolean AS recorded,COALESCE(t.digest,'')::text AS digest,COALESCE(t.unavailable,'')::text AS unavailable
FROM steps s JOIN runs r ON r.id=s.run_id JOIN run_events e ON e.run_id=s.run_id AND e.kind='step.started' AND e.payload_json->>'step'=s.name
LEFT JOIN LATERAL (SELECT x.step_id,x.digest,x.unavailable FROM agent_transcripts x WHERE x.step_id=s.id AND x.attempt=COALESCE((e.payload_json->>'attempt')::bigint,0) ORDER BY x.invocation DESC LIMIT 1) t ON true
WHERE s.id=sqlc.arg(step_id) AND r.id=sqlc.arg(run_id) AND r.project_id=sqlc.arg(project_id)
AND (sqlc.arg(attempt)::bigint=0 OR e.payload_json->>'attempt'=sqlc.arg(attempt)::bigint::text)
ORDER BY e.id DESC LIMIT 1;

-- name: ExportSteps :many
SELECT to_jsonb(s)::text FROM steps s JOIN runs r ON r.id = s.run_id WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped')) ORDER BY s.id;
