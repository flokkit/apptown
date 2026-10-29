-- Runs: their rows, starts, inference owners, children and numbers.

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

-- name: ExecutionReworks :one
SELECT (SELECT COUNT(*) FROM run_events re WHERE re.run_id=sqlc.arg(run_id) AND re.kind='step.rework') AS reworks
FROM changes c WHERE c.id=sqlc.arg(change_id) AND c.project_id=sqlc.arg(project_id);

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
OR bool_and(s.status IN ('pass','skipped','not_applicable'))
UNION SELECT r.id FROM runs r LEFT JOIN run_starts st ON r.id=st.run_id
WHERE r.status='running' AND (st.run_id IS NULL OR NOT st.ready OR (r.parent_run_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM steps s WHERE s.run_id=r.id AND s.stage=r.current_stage AND s.type=ANY($1::text[]))))
ORDER BY id;

-- A landing whose merge SCM has recorded can no longer move main: what is
-- left of it is deploying a commit main already contains, so it no longer
-- holds the project's one landing slot (a fix-forward may land beside it).
-- name: HasActiveLanding :one
SELECT EXISTS(SELECT 1 FROM runs WHERE project_id=$1 AND parent_run_id IS NULL AND status=$2 AND definition_json @> '{"role":"landing"}'
  AND NOT EXISTS(SELECT 1 FROM run_merges m WHERE m.run_id=runs.id AND m.state='merged'));

-- Running landings whose merge is recorded: a new landing is admitted beside
-- them and names them in its landing.passed receipt.
-- name: MergedActiveLandings :many
SELECT r.id, r.change_id, m.result FROM runs r JOIN run_merges m ON m.run_id=r.id AND m.state='merged'
WHERE r.project_id=$1 AND r.parent_run_id IS NULL AND r.status=$2 AND r.definition_json @> '{"role":"landing"}'
ORDER BY r.id;

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
 s.started_at,s.finished_at,COALESCE(w.n,0)::bigint AS reworks
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
  COALESCE(mr_identity.identity_json,'null'::jsonb)::jsonb AS mr_identity,
  COALESCE((SELECT e.payload_json FROM run_events e WHERE e.run_id=sqlc.arg(run_id) AND e.kind='landing.candidate' ORDER BY e.id DESC LIMIT 1),'null'::jsonb)::jsonb AS landing_candidate,
  EXISTS(SELECT 1 FROM run_events e WHERE e.run_id=sqlc.arg(run_id) AND e.kind='landing.pin.required')::boolean AS landing_pin_required
FROM root JOIN projects project ON project.id=root.project_id
LEFT JOIN adoptions current_adoption ON current_adoption.project_id=project.id
JOIN runs owner ON owner.id=root.id
JOIN runs current_run ON current_run.id=sqlc.arg(run_id)
LEFT JOIN changes change ON change.id=root.change_id
LEFT JOIN LATERAL (
  SELECT a.id,a.name,a.body FROM artifacts a WHERE a.run_id=sqlc.arg(run_id) AND a.kind='mr' ORDER BY a.id DESC LIMIT 1
) mr ON true
LEFT JOIN repository_mr_identities mr_identity ON mr_identity.artifact_id=mr.id;

-- Summary projection excludes definitions/inputs and carries project identity.
-- name: WorkflowSummaries :many
SELECT r.id,r.number,r.status,r.current_stage,r.workflow_name,r.workflow_version,r.kind,r.change_id,
COALESCE(c.number,0)::bigint AS change_number,COALESCE(c.title,'')::text AS item_title,p.slug AS project_slug,r.created_at,r.updated_at
FROM runs r JOIN projects p ON p.id=r.project_id LEFT JOIN changes c ON c.id=r.change_id AND c.project_id=r.project_id
WHERE (sqlc.narg(project_id)::uuid IS NULL OR r.project_id=sqlc.narg(project_id))
AND (sqlc.arg(status)::text='' OR r.status=sqlc.arg(status)) AND (NOT sqlc.arg(roots_only)::boolean OR r.parent_run_id IS NULL)
AND (sqlc.arg(project_ids)::uuid[] IS NULL OR r.project_id=ANY(sqlc.arg(project_ids)::uuid[]))
ORDER BY r.updated_at DESC,r.id DESC LIMIT NULLIF(sqlc.arg(limit_count)::integer,0);

-- name: WorkflowUsageReport :many
SELECT workflow_name,COUNT(*)::bigint AS runs,MAX(created_at)::timestamptz AS last_run_at
FROM runs WHERE project_id=$1 GROUP BY workflow_name;

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

-- A run's id from its number within its project ("egg-catch#40").
-- name: RunIDByNumber :one
SELECT id FROM runs WHERE project_id=sqlc.arg(project_id) AND number=sqlc.arg(number);

-- name: MarkRunLandingAuthority :execrows
UPDATE runs SET landing_authority = sqlc.arg(by), landing_authority_principal = sqlc.arg(by_principal), landing_authority_via = sqlc.arg(by_via) WHERE id=sqlc.arg(id) AND landing_authority = '';

-- name: RunLandingAuthority :one
SELECT parent_run_id, landing_authority FROM runs WHERE id = $1;

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

-- name: LayoutWorkspaceOwners :many
SELECT id,project_id,'change'::text AS kind FROM changes
UNION ALL
SELECT id,project_id,'run'::text AS kind FROM runs
ORDER BY kind,id;
