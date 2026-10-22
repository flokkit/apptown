-- Run events: the receipts a run records as it moves.

-- name: RunAbandonmentReason :one
SELECT COALESCE((SELECT payload_json->>'reason' FROM run_events
WHERE run_id=$1 AND kind='run.abandoned' ORDER BY id DESC LIMIT 1),'')::text AS reason;

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

-- name: LandingAcceptanceConsidered :many
SELECT (payload_json->>'stepId')::uuid AS step_id FROM run_events WHERE run_id=$1 AND kind IN ('landing.acceptance.carried','landing.acceptance.withheld') AND payload_json->>'stepId' IS NOT NULL;

-- name: InAdvanceConsidered :many
SELECT (payload_json->>'stepId')::uuid AS step_id FROM run_events WHERE run_id=$1 AND kind IN ('acceptance.inadvance.applied','acceptance.inadvance.withheld') AND payload_json->>'stepId' IS NOT NULL;

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

-- name: LatestRunEventOfKind :one
SELECT id, payload_json FROM run_events WHERE run_id=sqlc.arg(run_id) AND kind=sqlc.arg(kind) ORDER BY id DESC LIMIT 1;

-- name: EarliestRunEventOfKind :one
SELECT id, payload_json FROM run_events WHERE run_id=sqlc.arg(run_id) AND kind=sqlc.arg(kind) ORDER BY id ASC LIMIT 1;

-- name: StepStartReceipt :one
SELECT payload_json FROM run_events WHERE run_id=sqlc.arg(run_id) AND kind='step.started'
AND payload_json->>'step'=sqlc.arg(step)::text AND payload_json->>'stage'=sqlc.arg(stage)::text AND (payload_json->>'attempt')::bigint=sqlc.arg(attempt)::bigint
ORDER BY id DESC LIMIT 1;

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

-- Agent attempts whose start receipt names a session but that recorded no
-- history: runs from before transcripts were kept, and attempts AppTown
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
