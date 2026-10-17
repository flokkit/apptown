-- Conformance (design/subsystems/conformance.md): reviews, the verdicts they
-- record, the adjudications and exception snapshots that interpret them, the
-- fixes that closed them, and a project's dispositions. Record-store style
-- (design/architecture/record-store-style.md).

-- ===== reviews ========================================================

-- The selection is sent as text: the driver sends a JSON null as SQL NULL,
-- and JSON null (the whole estate) is a value here.
-- name: NextReviewNumber :one
-- The next number, in the creating transaction: the row's lock orders
-- concurrent creations.
INSERT INTO conformance_review_numbers (id, last) VALUES (true, 1)
ON CONFLICT (id) DO UPDATE SET last = conformance_review_numbers.last + 1
RETURNING last;

-- name: InsertReview :one
INSERT INTO conformance_reviews (id, number, trigger, actor, actor_principal, actor_via, started_at, status, selected_projects, build, build_budget)
VALUES (sqlc.arg(id), sqlc.arg(number), sqlc.arg(trigger), sqlc.arg(actor), sqlc.arg(actor_principal), sqlc.arg(actor_via), sqlc.arg(started_at), sqlc.arg(status), sqlc.arg(selected_projects)::text::jsonb, sqlc.arg(build), sqlc.arg(build_budget)) RETURNING *;

-- One claimant wins: a review leaves queued once, for running.
-- name: ClaimQueuedReview :one
UPDATE conformance_reviews SET status = sqlc.arg(running) WHERE id=sqlc.arg(id) AND status = sqlc.arg(queued) RETURNING *;

-- name: ReviewByID :one
SELECT * FROM conformance_reviews WHERE id = $1;

-- name: ReviewByIDForUpdate :one
SELECT * FROM conformance_reviews WHERE id = $1 FOR UPDATE;

-- name: UpdateReviewProgress :execrows
UPDATE conformance_reviews SET projects_total = $2, projects_done = $3, current_project = $4
WHERE id=$1 AND status = $5;

-- name: FinishReview :execrows
UPDATE conformance_reviews SET finished_at = $2, projects = $3, verdicts = $4, errors = $5, cost = $6,
  status = $7, projects_done = $8, current_project = ''
WHERE id=$1 AND status = $9;

-- name: AbandonUnfinishedReviews :execrows
UPDATE conformance_reviews SET status = sqlc.arg(status), finished_at = sqlc.arg(at)::timestamptz, current_project = ''
WHERE status IN (sqlc.arg(queued), sqlc.arg(running));

-- name: UnfinishedReviewExists :one
SELECT EXISTS(SELECT 1 FROM conformance_reviews WHERE status IN (sqlc.arg(queued), sqlc.arg(running)));

-- name: PriorReviewBefore :one
SELECT * FROM conformance_reviews WHERE id < $1 AND status = $2 ORDER BY id DESC LIMIT 1;

-- The page a person sees, plus the newest finished review before it so a
-- comparison always has something to compare against. A zero limit is every
-- review: NULLIF turns it into LIMIT NULL, which is what "no bound" was.

-- name: ReviewSummariesPage :many
WITH page AS (
  SELECT id FROM conformance_reviews ORDER BY id DESC LIMIT NULLIF($1::bigint, 0)
), needed AS (
  SELECT id FROM page
  UNION (SELECT r.id FROM conformance_reviews r WHERE r.status = $2 AND r.id < (SELECT page.id FROM page ORDER BY page.id LIMIT 1) ORDER BY r.id DESC LIMIT 1)
)
SELECT * FROM conformance_reviews WHERE id IN (SELECT id FROM needed) ORDER BY id DESC;

-- name: ReviewsTouchingProject :many
SELECT * FROM conformance_reviews
WHERE id IN (SELECT DISTINCT review_id FROM conformance_verdicts WHERE project_id = $1 ORDER BY review_id DESC LIMIT $2)
ORDER BY id;

-- name: ReviewRecordCounts :one
SELECT
 (SELECT count(*) FROM conformance_reviews)::bigint AS reviews,
 (SELECT count(*) FROM conformance_verdicts)::bigint AS verdicts,
 (SELECT count(*) FROM conformance_adjudications)::bigint AS adjudications,
 (SELECT count(*) FROM conformance_review_exceptions)::bigint AS exceptions,
 (SELECT count(*) FROM conformance_fixes)::bigint AS fixes;

-- ===== verdicts =======================================================

-- name: InsertVerdict :one
INSERT INTO conformance_verdicts (id, review_id, project_id, project_slug, sha, block, version, check_name,
  evaluator, evaluator_ver, mode, state, severity, detail, created_at, excepted_by, evidence_hash)
VALUES ($17, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16) RETURNING *;

-- name: VerdictsForReview :many
SELECT * FROM conformance_verdicts WHERE review_id = $1 ORDER BY id;

-- name: VerdictsForReviews :many
SELECT * FROM conformance_verdicts
WHERE review_id=ANY($1::uuid[]) AND ($2::uuid IS NULL OR project_id = $2) ORDER BY id;

-- name: VerdictCountForReview :one
SELECT count(*)::bigint FROM conformance_verdicts WHERE review_id = $1;

-- name: VerdictByID :one
SELECT * FROM conformance_verdicts WHERE id = $1;

-- name: VerdictInProjectForUpdate :one
SELECT * FROM conformance_verdicts WHERE id = $1 AND project_id = $2 FOR UPDATE;

-- name: LatestVerdictForCheck :one
SELECT * FROM conformance_verdicts
WHERE project_id = $1 AND block = $2 AND version = $3 AND check_name = $4
ORDER BY id DESC LIMIT 1;

-- name: AllReviews :many
SELECT * FROM conformance_reviews ORDER BY id DESC;

-- name: VerdictsByIDsForShare :many
SELECT * FROM conformance_verdicts WHERE id=ANY($1::uuid[]) ORDER BY id FOR SHARE;

-- name: LatestVerdictsForProject :many
SELECT v.* FROM conformance_verdicts v
WHERE v.project_id = $1 AND v.review_id = (SELECT latest.review_id FROM conformance_verdicts latest WHERE latest.project_id = $1 ORDER BY latest.review_id DESC LIMIT 1)
ORDER BY v.id;

-- Current conformance is a projection, never a cell: the newest verdict for
-- each expectation the caller asked about, chosen inside the database.

-- name: CurrentVerdicts :many
SELECT * FROM (
  SELECT DISTINCT ON (v.project_id, v.block, v.version, v.check_name) v.*
  FROM conformance_verdicts v
  JOIN (
    SELECT ($1::uuid[])[i] AS project_id, ($2::text[])[i] AS block, ($3::bigint[])[i] AS version, ($4::text[])[i] AS check_name
    FROM generate_subscripts($1::uuid[], 1) AS i
  ) requested
    ON (v.project_id, v.block, v.version, v.check_name) = (requested.project_id, requested.block, requested.version, requested.check_name)
  ORDER BY v.project_id, v.block, v.version, v.check_name, v.id DESC
) current ORDER BY current.id;

-- The adjudication that stood on the previous verdict of the same check, so a
-- reader can say what changed since it was confirmed.

-- name: PriorAdjudicatedVerdicts :many
SELECT current.id, prior.sha, prior.by, prior.created_at
FROM conformance_verdicts current
JOIN LATERAL (
  SELECT v.sha, a.by, a.created_at FROM conformance_adjudications a
  JOIN conformance_verdicts v ON v.id = a.verdict_id
  WHERE v.project_id = current.project_id AND v.block = current.block AND v.check_name = current.check_name
    AND v.id < current.id
  ORDER BY a.id DESC LIMIT 1
) prior ON true
WHERE current.id=ANY($1::uuid[]);

-- ===== adjudications ==================================================

-- name: InsertAdjudication :one
INSERT INTO conformance_adjudications (id, verdict_id, project_id, kind, by, by_principal, by_via, note, created_at)
VALUES (sqlc.arg(id), sqlc.arg(verdict_id), sqlc.arg(project_id), sqlc.arg(kind), sqlc.arg(by), sqlc.arg(by_principal), sqlc.arg(by_via), sqlc.arg(note), sqlc.arg(created_at)) RETURNING *;

-- name: AdjudicationsForVerdicts :many
SELECT * FROM conformance_adjudications WHERE verdict_id=ANY($1::uuid[]) ORDER BY id;

-- ===== exception snapshots ============================================

-- name: InsertReviewException :one
INSERT INTO conformance_review_exceptions (id, review_id, project_id, disposition_id, fingerprint, identifier,
  package, status, justification, by, by_principal, by_via, created_at, expires_at)
VALUES (sqlc.arg(id), sqlc.arg(review_id), sqlc.arg(project_id), sqlc.arg(disposition_id), sqlc.arg(fingerprint), sqlc.arg(identifier),
  sqlc.arg(package), sqlc.arg(status), sqlc.arg(justification), sqlc.arg(by), sqlc.arg(by_principal), sqlc.arg(by_via), sqlc.arg(created_at), sqlc.arg(expires_at)) RETURNING *;

-- name: ReviewExceptionsForReview :many
SELECT * FROM conformance_review_exceptions WHERE review_id = $1 ORDER BY id;

-- ===== fixes ==========================================================

-- name: InsertFix :one
INSERT INTO conformance_fixes (id, project_id, check_key, before_verdict_id, after_verdict_id, change_ids_json,
  commits, remediation_item_id, accepted_by, accepted_by_principal, accepted_by_via, created_at)
VALUES (sqlc.arg(id), sqlc.arg(project_id), sqlc.arg(check_key), sqlc.arg(before_verdict_id), sqlc.arg(after_verdict_id), sqlc.arg(change_ids_json),
  sqlc.arg(commits), sqlc.arg(remediation_item_id), sqlc.arg(accepted_by), sqlc.arg(accepted_by_principal), sqlc.arg(accepted_by_via), sqlc.arg(created_at)) RETURNING *;

-- A fix is only legible beside the verdicts it moved between, so the report
-- carries both.

-- name: FixesForProject :many
SELECT sqlc.embed(f), b.sha AS before_sha, b.state AS before_state, b.detail AS before_detail,
       a.sha AS after_sha, a.review_id AS after_review_id, a.check_name AS after_check_name,
       rv.number AS after_review_number,
       -- The attributed changes' numbers, in the same order as their ids;
       -- 0 for one that no longer exists. A read of Work's table, for naming.
       -- A malformed attribution reads no numbers here and is refused where
       -- its ids are decoded, not by failing the whole list.
       ARRAY(SELECT COALESCE(ch.number, 0)
             FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(f.change_ids_json) = 'array' THEN f.change_ids_json ELSE '[]'::jsonb END) WITH ORDINALITY AS x(id, n)
             LEFT JOIN changes ch ON ch.id = CASE WHEN x.id ~* '^[0-9a-f]{8}-([0-9a-f]{4}-){3}[0-9a-f]{12}$' THEN x.id::uuid END
             ORDER BY x.n)::bigint[] AS change_numbers
FROM conformance_fixes f
JOIN conformance_verdicts b ON b.id = f.before_verdict_id
JOIN conformance_verdicts a ON a.id = f.after_verdict_id
JOIN conformance_reviews rv ON rv.id = a.review_id
WHERE f.project_id = $1 ORDER BY f.id DESC LIMIT $2;

-- ===== dispositions ===================================================

-- name: InsertDisposition :one
INSERT INTO dispositions (id, project_id, fingerprint, identifier, package, status, justification, by, by_principal, by_via, created_at, expires_at)
VALUES (sqlc.arg(id), sqlc.arg(project_id), sqlc.arg(fingerprint), sqlc.arg(identifier), sqlc.arg(package), sqlc.arg(status), sqlc.arg(justification), sqlc.arg(by), sqlc.arg(by_principal), sqlc.arg(by_via), sqlc.arg(created_at), sqlc.arg(expires_at)) RETURNING *;

-- name: DispositionByID :one
SELECT * FROM dispositions WHERE id = $1;

-- name: DispositionsForProjects :many
SELECT * FROM dispositions
WHERE project_id=ANY(sqlc.arg(projects)::uuid[]) AND (NOT sqlc.arg(active)::boolean OR expires_at IS NULL OR expires_at > sqlc.arg(at)::timestamptz)
ORDER BY id;

-- name: DeleteDisposition :one
DELETE FROM dispositions WHERE id=$1 AND project_id=$2 RETURNING *;

-- name: DeleteProjectDispositions :exec
DELETE FROM dispositions WHERE project_id=$1;

-- ===== other domains' rows, read never written ========================

-- Eval copies are never assessed: they appear under their eval.
-- name: ConformanceProjects :many
SELECT id, slug, name, default_branch FROM projects
WHERE ($1::uuid[] IS NULL OR id=ANY($1::uuid[])) AND (NOT $2::boolean OR default_branch <> '')
AND purpose = 'ordinary'
ORDER BY slug;

-- name: LockProjectForReceipt :one
SELECT id FROM projects WHERE id = $1 FOR UPDATE;

-- name: LockProjectsForOrigins :many
SELECT id FROM projects WHERE id=ANY($1::uuid[]) ORDER BY id FOR KEY SHARE;

-- ===== history maintenance ============================================

-- NOWAIT refuses concurrent readers and writers rather than queueing; the
-- locks are held until the caller's transaction ends.

-- name: LockConformanceHistory :exec
LOCK TABLE conformance_reviews, conformance_verdicts, conformance_adjudications,
  conformance_review_exceptions, conformance_fixes IN ACCESS EXCLUSIVE MODE NOWAIT;

-- name: DeleteAllFixes :execrows
DELETE FROM conformance_fixes;

-- name: DeleteAllReviewExceptions :execrows
DELETE FROM conformance_review_exceptions;

-- name: DeleteAllAdjudications :execrows
DELETE FROM conformance_adjudications;

-- name: DeleteAllVerdicts :execrows
DELETE FROM conformance_verdicts;

-- name: DeleteAllReviews :execrows
DELETE FROM conformance_reviews;

-- The selection is sent as text, as InsertReview's is.
-- name: InsertWhatIf :one
INSERT INTO what_if_reviews(id, pack,pack_version,requested_by,requested_by_principal,requested_by_via,rubrics,spend_limit_micros,created_at,projects,state)
VALUES(sqlc.arg(id), sqlc.arg(pack),sqlc.arg(pack_version),sqlc.arg(requested_by),sqlc.arg(requested_by_principal),sqlc.arg(requested_by_via),sqlc.arg(rubrics),sqlc.arg(spend_limit_micros),sqlc.arg(created_at),sqlc.arg(projects)::text::jsonb,'queued') RETURNING *;

-- One claimant wins: a what-if leaves queued once, for running.
-- name: ClaimQueuedWhatIf :one
UPDATE what_if_reviews SET state='running' WHERE id=$1 AND state='queued' RETURNING *;

-- name: WhatIfStarted :execrows
UPDATE what_if_reviews SET projects_total=$2 WHERE id=$1 AND state='running';

-- name: WhatIfProgress :execrows
UPDATE what_if_reviews SET projects_done=projects_done+1,cost_micros=cost_micros+$2,incomplete=incomplete+$3 WHERE id=$1 AND state='running';

-- name: InterruptUnfinishedWhatIfs :execrows
UPDATE what_if_reviews SET state='interrupted',detail=sqlc.arg(detail),finished_at=sqlc.arg(at)::timestamptz WHERE state IN ('queued','running');

-- name: FinishWhatIf :execrows
UPDATE what_if_reviews SET state=sqlc.arg(state),detail=sqlc.arg(detail),finished_at=sqlc.arg(at)::timestamptz WHERE id=sqlc.arg(id) AND state='running';

-- name: InsertWhatIfRow :exec
INSERT INTO what_if_rows(id, what_if_id,project_id,project_slug,sha,adoption_revision,candidate_digest,block,check_name,
 current_version,proposed_version,current_state,proposed_state,result,detail,brief,history_json,reason,joins)
VALUES($19, $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18);

-- name: WhatIfByID :one
SELECT * FROM what_if_reviews WHERE id=$1;

-- name: LatestWhatIf :one
SELECT * FROM what_if_reviews WHERE pack=$1 AND pack_version=$2 ORDER BY id DESC LIMIT 1;

-- name: WhatIfRows :many
SELECT * FROM what_if_rows WHERE what_if_id=$1 ORDER BY project_slug,block,check_name,id;

-- The record people name by its number ("conformance review 9").
-- name: ReviewIDByNumber :one
SELECT id FROM conformance_reviews WHERE number = $1;
