-- name: JanitorProjectLabels :many
SELECT p.id,p.slug,p.name FROM projects p
WHERE p.id=ANY(sqlc.arg(project_ids)::uuid[]) AND p.purpose='ordinary'
AND NOT EXISTS(SELECT 1 FROM project_lifecycle l WHERE l.project_id=p.id)
ORDER BY p.name,p.id;

-- name: JanitorLandedChanges :many
SELECT a.*,c.number,c.title,c.target_candidate_json,c.closed_at,p.slug,p.name
FROM automated_changes a JOIN changes c ON c.id=a.active_change_id AND c.project_id=a.project_id
JOIN projects p ON p.id=a.project_id
WHERE a.project_id=ANY(sqlc.arg(project_ids)::uuid[]) AND a.outcome='shipped' AND a.queue<>''
AND (c.landed_sha<>'' OR (a.queue='packs' AND c.adopted_revision>0))
AND c.closed_at>=sqlc.arg(since)::timestamptz AND c.closed_at<sqlc.arg(until)::timestamptz
ORDER BY c.closed_at DESC,c.id DESC LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: JanitorProjectChoices :many
SELECT DISTINCT ON (project_id) project_id,record_json FROM standing_project_choices
WHERE project_id=ANY(sqlc.arg(project_ids)::uuid[]) ORDER BY project_id,at DESC,id DESC;
