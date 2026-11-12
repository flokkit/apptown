-- Changes: their rows, states and the project rows they hang from.

-- name: ChangeByID :one
SELECT * FROM changes WHERE id=sqlc.arg(id) AND (sqlc.narg(project_id)::uuid IS NULL OR project_id=sqlc.narg(project_id));

-- name: VisibleChanges :many
SELECT * FROM changes WHERE project_id=ANY(sqlc.arg(project_ids)::uuid[]) AND id=ANY(sqlc.arg(ids)::uuid[]) ORDER BY id;

-- name: ProjectByID :one
SELECT * FROM projects WHERE id=$1;

-- name: ChangeForUpdate :one
SELECT * FROM changes WHERE id=sqlc.arg(id) AND (sqlc.narg(project_id)::uuid IS NULL OR project_id=sqlc.narg(project_id)) FOR UPDATE;

-- name: ProjectChangesForRemovalMeta :many
SELECT id FROM changes WHERE project_id=$1 ORDER BY id FOR UPDATE;

-- Project identity/configuration is the existing project-first admission lock.
-- name: ProjectForUpdate :one
SELECT * FROM projects WHERE id=$1 FOR UPDATE;

-- name: SupersessionChangeStateForUpdate :one
WITH project AS MATERIALIZED (SELECT projects.id FROM projects WHERE projects.id=sqlc.arg(project_id) FOR UPDATE)
SELECT c.state FROM changes c JOIN project p ON p.id=c.project_id WHERE c.id=sqlc.arg(change_id) FOR UPDATE OF c;

-- name: AttachUpliftTarget :execrows
UPDATE changes SET target_adoption_id=sqlc.arg(target_adoption_id),target_candidate_json=sqlc.arg(target_candidate_json)
WHERE id=sqlc.arg(id) AND project_id=sqlc.arg(project_id) AND target_adoption_id IS NULL;

-- name: OpenUpliftTargets :many
SELECT id,target_candidate_json FROM changes
WHERE project_id=$1 AND type='uplift' AND state=$2 ORDER BY id;

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
INSERT INTO changes(id,number,project_id,type,title,body,external_ref,acceptance_json,state,title_ownership,chat_request,branch,base_sha,provenance,expected_revision,opened_by,opened_by_principal,opened_by_via,created_at)
VALUES(sqlc.arg(id),sqlc.arg(number),sqlc.arg(project_id),sqlc.arg(type),sqlc.arg(title),sqlc.arg(body),sqlc.arg(external_ref),sqlc.arg(acceptance_json),sqlc.arg(state),sqlc.arg(title_ownership),sqlc.arg(chat_request),sqlc.arg(branch),sqlc.arg(base_sha),sqlc.arg(provenance),sqlc.arg(expected_revision),sqlc.arg(opened_by),sqlc.arg(opened_by_principal),sqlc.arg(opened_by_via),sqlc.arg(created_at)::timestamptz) RETURNING *;

-- name: NameChangeWorkspaceBranch :execrows
UPDATE changes SET branch=sqlc.arg(branch)
WHERE id=sqlc.arg(change_id) AND project_id=sqlc.arg(project_id) AND branch='';

-- name: PublishChangeNarrative :execrows
UPDATE changes SET summary=sqlc.arg(summary),request_met=sqlc.arg(request_met),request_gap=sqlc.arg(request_gap),request_gap_quote=sqlc.arg(request_gap_quote) WHERE id=sqlc.arg(id) AND project_id=sqlc.arg(project_id) AND body=sqlc.arg(body) AND state=sqlc.arg(state) AND run_id IS NOT DISTINCT FROM sqlc.arg(run_id) AND branch=sqlc.arg(branch) AND base_sha=sqlc.arg(base_sha) AND summary IS NOT DISTINCT FROM sqlc.arg(old_summary);

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
WHERE wi.project_id=sqlc.arg(project_id) AND NOT wi.purge_pending AND NOT (wi.automated AND EXISTS(SELECT 1 FROM automated_changes a WHERE a.id=wi.id AND a.queue<>'')) AND (sqlc.arg(state)::text='' OR wi.state=sqlc.arg(state)) ORDER BY wi.number DESC LIMIT NULLIF(sqlc.arg(limit_count)::integer,0);

-- name: ChangeTargetAdoptionMeta :one
SELECT target_adoption_id FROM changes WHERE changes.id=$1 AND changes.project_id=$2;

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

-- name: ProjectWorkBlockers :many
WITH busy AS (
 SELECT c.id AS change_id, true AS landing FROM changes c
 WHERE c.project_id=sqlc.arg(project_id) AND c.state=sqlc.arg(landing_state)
 UNION ALL
 SELECT r.change_id, false AS landing FROM runs r
 WHERE r.project_id=sqlc.arg(project_id)
 AND EXISTS(SELECT 1 FROM steps s WHERE s.run_id=r.id AND s.status=sqlc.arg(running_step_state))
)
SELECT COALESCE(c.id, '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS id, COALESCE(c.number, 0)::bigint AS number,
 CASE WHEN bool_or(busy.landing) THEN 'landing' ELSE 'step' END::text AS kind
FROM busy LEFT JOIN changes c ON c.id=busy.change_id AND c.project_id=sqlc.arg(project_id)
GROUP BY c.id, c.number ORDER BY number, id;

-- Branch metadata avoids large Change bodies in conformance attribution.
-- name: ShippedChangeBranchesMeta :many
SELECT id,branch FROM changes WHERE project_id=$1 AND state=$2 AND branch<>'' ORDER BY id;

-- Label metadata excludes potentially large Change bodies.
-- name: ShippedChangeLabelsMeta :many
SELECT id,number,title,type FROM changes WHERE project_id=sqlc.arg(project_id) AND state=sqlc.arg(shipped_state) AND closed_at>sqlc.arg(after)::timestamptz
ORDER BY closed_at DESC NULLS LAST,id DESC LIMIT NULLIF(sqlc.arg(row_limit)::integer,0);

-- name: ChangeStatesForScopes :many
SELECT c.id,c.project_id,c.state FROM generate_subscripts($1::uuid[],1) idx(n)
JOIN changes c ON c.id=($1::uuid[])[idx.n] AND c.project_id=($2::uuid[])[idx.n];

-- name: ProjectActivityReport :many
WITH change_counts AS (
 SELECT c.project_id,COUNT(*)::bigint AS changes,
 COUNT(*) FILTER(WHERE current.status=sqlc.arg(running_status)::text)::bigint AS active_items,
 COUNT(*) FILTER(WHERE c.state IN (sqlc.arg(in_progress_state)::text,sqlc.arg(landing_state)::text))::bigint AS open_changes,
 COUNT(*) FILTER(WHERE c.type='uplift' AND c.state=sqlc.arg(in_progress_state)::text)::bigint AS open_uplifts
 FROM changes c LEFT JOIN runs current ON current.id=c.run_id AND current.project_id=c.project_id
 WHERE c.project_id=ANY(sqlc.arg(project_ids)::uuid[]) AND NOT c.purge_pending AND NOT (c.automated AND EXISTS(SELECT 1 FROM automated_changes a WHERE a.id=c.id AND a.queue<>'')) GROUP BY c.project_id
), run_counts AS (
 SELECT project_id,COUNT(*) FILTER(WHERE status=sqlc.arg(done_status)::text AND parent_run_id IS NULL)::bigint AS done_runs,
 COUNT(*) FILTER(WHERE status=sqlc.arg(running_status)::text)::bigint AS running_runs,MAX(updated_at) AS last_run_at
 FROM runs WHERE project_id=ANY(sqlc.arg(project_ids)::uuid[]) GROUP BY project_id
)
SELECT p.id,COALESCE(c.changes,0)::bigint AS changes,COALESCE(c.active_items,0)::bigint AS active_items,
 COALESCE(r.done_runs,0)::bigint AS done_runs,COALESCE(r.running_runs,0)::bigint AS running_runs,
 COALESCE(r.last_run_at,'epoch'::timestamptz)::timestamptz AS last_run_at,(r.last_run_at IS NOT NULL)::boolean AS has_last_run_at,
 COALESCE(c.open_changes,0)::bigint AS open_changes,COALESCE(c.open_uplifts,0)::bigint AS open_uplifts,
 COALESCE((SELECT latest.title FROM changes latest WHERE latest.project_id=p.id AND NOT latest.purge_pending AND NOT (latest.automated AND EXISTS(SELECT 1 FROM automated_changes a WHERE a.id=latest.id AND a.queue<>'')) ORDER BY latest.created_at DESC,latest.id DESC LIMIT 1),'')::text AS last_change_title
FROM projects p LEFT JOIN change_counts c ON c.project_id=p.id LEFT JOIN run_counts r ON r.project_id=p.id
WHERE p.id=ANY(sqlc.arg(project_ids)::uuid[]);

-- Scope enumeration does not need project documents or configuration payloads.
-- Transitional Delivery membership observation: releases and release_changes
-- are Delivery-owned. Preserve the one-statement project/release scope and
-- receipt ordering until a Delivery batch door replaces this report.
-- name: ReleaseChangeLabels :many
SELECT rel.id AS release_id,c.id AS change_id,c.number,c.title,c.type
FROM releases rel JOIN release_changes rc ON rc.release_id=rel.id
JOIN changes c ON c.id=rc.change_id AND c.project_id=rel.project_id
WHERE rel.project_id=sqlc.arg(project_id) AND rel.id=ANY(sqlc.arg(release_ids)::uuid[]) ORDER BY rel.id,c.id;

-- A change's id from its number within its project: the friendly form
-- people type ("egg-catch#12") resolves through it.
-- name: ChangeIDByNumber :one
SELECT id FROM changes WHERE project_id=sqlc.arg(project_id) AND number=sqlc.arg(number);

-- name: ChangeIDForBranch :one
SELECT id FROM changes WHERE project_id=sqlc.arg(project_id) AND branch=sqlc.arg(branch) ORDER BY number DESC LIMIT 1;

-- The recorded branches of a project's changes that are not finished.
-- name: OpenChangeBranches :many
SELECT c.number, c.branch FROM changes c
WHERE c.project_id = sqlc.arg(project_id) AND c.branch <> '' AND c.state IN (sqlc.arg(in_progress), sqlc.arg(landing))
ORDER BY c.number;

-- Whether any change closed or any run stopped running at or after a time:
-- the only events after which a workspace's state can be newly finished.
-- name: SettledSince :one
SELECT (EXISTS(SELECT 1 FROM changes WHERE closed_at>=sqlc.arg(since)::timestamptz) OR
 EXISTS(SELECT 1 FROM runs WHERE status<>sqlc.arg(running)::text AND updated_at>=sqlc.arg(since)::timestamptz))::boolean;

-- name: LastLandedTitles :many
SELECT c.title FROM changes c
JOIN runs r ON r.change_id=c.id AND r.project_id=c.project_id
JOIN run_events e ON e.run_id=r.id AND e.kind='merge.completed'
WHERE c.project_id=$1 AND c.landed_sha<>''
GROUP BY c.id ORDER BY max(e.at) DESC,c.id DESC LIMIT 10;

-- Background preview observation selections; private to the collector.
-- name: PreviewObservationChanges :many
SELECT id,project_id,state FROM changes WHERE state=sqlc.arg(open_state) OR id=ANY(sqlc.arg(listed)::uuid[]) ORDER BY id;

-- name: PreviewObservationChange :one
SELECT id,project_id,state FROM changes WHERE id=$1;

-- name: PublishRequestSummary :execrows
UPDATE changes SET request_summary=sqlc.arg(request_summary)
WHERE changes.id=sqlc.arg(id) AND changes.project_id=sqlc.arg(project_id) AND chat_request
AND state IN ('in_progress','landing') AND request_summary=sqlc.arg(old_request_summary)
AND NOT EXISTS (SELECT 1 FROM runs r JOIN steps s ON s.run_id=r.id WHERE r.change_id=changes.id AND s.type='approval' AND s.status='pass');

-- name: RequestSummaryOpen :one
SELECT chat_request AND state IN ('in_progress','landing')
AND NOT EXISTS (SELECT 1 FROM runs r JOIN steps s ON s.run_id=r.id WHERE r.change_id=changes.id AND s.type='approval' AND s.status='pass') AS open
FROM changes WHERE changes.id=$1 AND changes.project_id=$2;

-- name: MarkAutomatedChange :exec
UPDATE changes SET automated=true, automated_origin=true WHERE id=$1;

-- name: RecordAutomatedChange :exec
INSERT INTO automated_changes(id,project_id,active_change_id,queue,what,opened_at) VALUES($1,$2,$1,$3,$4,$5);

-- name: PromoteAutomatedChange :execrows
UPDATE changes SET automated=false WHERE id=$1 AND project_id=$2 AND NOT purge_pending;

-- name: RunChangeMeta :one
SELECT change_id FROM runs WHERE id=sqlc.arg(id) AND project_id=sqlc.arg(project_id);

-- name: AutomatedChangeRecords :many
SELECT * FROM automated_changes WHERE project_id=$1 AND ($2::text='' OR queue=$2) ORDER BY opened_at DESC LIMIT $3;

-- name: EndAutomatedChange :exec
UPDATE automated_changes SET outcome=$2, reason=$3, ended_at=$4 WHERE id=$1;

-- name: IsAutomatedQueueChange :one
SELECT EXISTS(SELECT 1 FROM changes c JOIN automated_changes a ON a.id=c.id WHERE c.id=$1 AND c.automated AND a.queue<>'');

-- name: AutomatedPurgeCandidates :many
SELECT * FROM changes WHERE automated AND state='abandoned' ORDER BY closed_at;

-- name: ClaimAutomatedPurge :execrows
UPDATE changes SET purge_pending=true WHERE id=$1 AND automated AND state='abandoned';

-- name: AutomatedPurgeBusy :one
SELECT EXISTS(SELECT 1 WHERE EXISTS(SELECT 1 FROM runs r WHERE r.change_id=$1 AND (r.status='running'
 OR EXISTS(SELECT 1 FROM steps s WHERE s.run_id=r.id AND s.status='running')
 OR EXISTS(SELECT 1 FROM runs child WHERE child.parent_run_id=r.id AND child.change_id IS DISTINCT FROM r.change_id)))
 OR EXISTS(SELECT 1 FROM workflow_schedules WHERE last_change_id=$1));

-- name: PurgeAutomatedRuns :exec
DELETE FROM runs WHERE change_id=$1;

-- name: PurgeAutomatedChange :execrows
DELETE FROM changes WHERE id=$1 AND automated AND purge_pending AND state='abandoned';

-- name: ChangeTranscriptDigests :many
SELECT DISTINCT t.digest FROM agent_transcripts t JOIN runs r ON r.id=t.run_id WHERE r.change_id=$1;
