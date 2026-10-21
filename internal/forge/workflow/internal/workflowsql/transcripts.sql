-- Agent transcripts, workflow schedules and eval copies: Workflow's records written on their own.

-- name: WorkflowScheduleByIdentity :many
SELECT * FROM workflow_schedules WHERE project_id=$1 AND workflow_name=$2;

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

-- name: ProjectRemovalRemainder :one
SELECT EXISTS(SELECT 1 FROM agent_transcripts t JOIN runs r ON r.id=t.run_id WHERE r.project_id=$1) AS transcripts,
 EXISTS(SELECT 1 FROM steps s JOIN runs r ON r.id=s.run_id WHERE r.project_id=$1) AS steps,
 EXISTS(SELECT 1 FROM run_events e JOIN runs r ON r.id=e.run_id WHERE r.project_id=$1) AS events,
 EXISTS(SELECT 1 FROM artifacts a JOIN runs r ON r.id=a.run_id WHERE r.project_id=$1) AS artifacts,
 EXISTS(SELECT 1 FROM runs WHERE project_id=$1) AS runs,
 EXISTS(SELECT 1 FROM changes WHERE project_id=$1) AS changes;

-- Each invocation of an attempt records its own row, once: a replay changes
-- nothing, and no stored file loses its reference.
-- name: RecordAgentTranscript :execrows
INSERT INTO agent_transcripts(run_id,step_id,attempt,session_id,invocation,digest,unavailable,recorded_at)
VALUES (sqlc.arg(run_id),sqlc.arg(step_id),sqlc.arg(attempt),sqlc.arg(session_id),sqlc.arg(invocation),sqlc.arg(digest),sqlc.arg(unavailable),sqlc.arg(recorded_at)::timestamptz)
ON CONFLICT (step_id,attempt,invocation) DO NOTHING;

-- name: ProjectTranscriptDigests :many
SELECT DISTINCT t.digest FROM agent_transcripts t JOIN runs r ON r.id=t.run_id WHERE r.project_id=$1 AND t.digest<>'' ORDER BY t.digest;

-- name: DeleteProjectTranscripts :exec
DELETE FROM agent_transcripts WHERE run_id IN (SELECT id FROM runs WHERE project_id=$1);

-- name: TranscriptReferenced :one
SELECT EXISTS(SELECT 1 FROM agent_transcripts WHERE digest=$1);

-- The evals owner's copy record, read where an eval's authority is judged:
-- which eval a project is a copy of, if any. A read of another owner's
-- table, never a write (scripts/schema-ownership.py).
-- name: EvalCopyOf :one
SELECT eval_id FROM eval_copies WHERE project_id = $1;

-- name: ExportAgentTranscripts :many
SELECT to_jsonb(t)::text FROM agent_transcripts t JOIN runs r ON r.id = t.run_id
WHERE r.project_id = $1 AND r.status <> 'running' AND (r.change_id IS NULL OR r.change_id IN (SELECT m.id FROM changes m WHERE m.project_id = $1 AND m.state = 'shipped')) ORDER BY t.step_id, t.attempt, t.invocation;
