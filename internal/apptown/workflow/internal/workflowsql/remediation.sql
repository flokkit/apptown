
-- name: OpenRemediationChecks :many
SELECT c.id, c.project_id, c.number,
       COALESCE(checks.value->>'block', '')::text AS block,
       COALESCE((checks.value->>'version')::bigint, 0)::bigint AS version,
       COALESCE(checks.value->>'check', '')::text AS check_name
FROM changes c
CROSS JOIN LATERAL jsonb_array_elements(COALESCE(c.acceptance_json->'checks', '[]'::jsonb)) checks(value)
WHERE c.project_id = ANY(sqlc.arg(projects)::uuid[])
  AND c.type = 'remediation' AND c.state NOT IN ('shipped', 'abandoned')
ORDER BY c.number, c.id;
