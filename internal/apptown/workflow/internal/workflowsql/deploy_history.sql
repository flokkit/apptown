-- name: LandingDeployAttempts :many
SELECT s.id AS step_id,s.run_id,s.name,s.stage,s.config_json,s.attempt AS current_attempt,
 s.status AS current_status,s.evidence_json AS current_evidence,s.started_at,s.finished_at,
 r.created_at AS run_started_at,c.id AS change_id,c.number AS change_number,c.title AS change_title,
 e.payload_json AS started,e.at AS attempt_started_at,
 COALESCE(f.payload_json,'{}'::jsonb) AS finished,COALESCE(f.at,e.at) AS attempt_finished_at
FROM steps s JOIN runs r ON r.id=s.run_id
JOIN changes c ON c.id=r.change_id AND c.project_id=r.project_id
JOIN run_events e ON e.run_id=r.id AND e.kind='step.started' AND e.payload_json->>'step'=s.name
LEFT JOIN LATERAL (
 SELECT payload_json,at FROM run_events
 WHERE run_id=r.id AND kind='step.finished' AND payload_json->>'step'=s.name
 AND payload_json->>'attempt'=e.payload_json->>'attempt' ORDER BY id DESC LIMIT 1
) f ON true
WHERE r.project_id=$1 AND r.definition_json @> '{"role":"landing"}' AND s.type='deploy'
ORDER BY e.at DESC,e.id DESC;
