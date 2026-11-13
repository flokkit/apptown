-- Record reads, grouped by table.

-- name: DeploymentByID :one
SELECT sqlc.embed(d), a.finished_at AS last_applied_at FROM deployments d LEFT JOIN deployment_attempts a ON a.id=d.serving_attempt_id WHERE d.id=$1;

-- name: DeploymentByName :one
SELECT sqlc.embed(d), a.finished_at AS last_applied_at FROM deployments d LEFT JOIN deployment_attempts a ON a.id=d.serving_attempt_id WHERE d.project_id=$1 AND d.name=$2;

-- A restored copy is not one of the project's deployments: it is shown on
-- its source's card only (RestoredCopiesOf), never in listings, health,
-- conformance or the sweeps that read these.
-- name: DeploymentsForProject :many
SELECT sqlc.embed(d), a.finished_at AS last_applied_at FROM deployments d LEFT JOIN deployment_attempts a ON a.id=d.serving_attempt_id WHERE d.project_id=$1 AND d.kind<>'restored-copy' ORDER BY d.kind DESC,d.id;

-- name: PreviewDeployments :many
SELECT * FROM deployments WHERE kind=$1 ORDER BY id;

-- name: TargetByID :one
SELECT * FROM targets WHERE id=$1;

-- name: TargetByName :one
SELECT * FROM targets WHERE name=$1;

-- name: Targets :many
SELECT * FROM targets WHERE (sqlc.narg(ids)::uuid[] IS NULL OR id=ANY(sqlc.narg(ids)::uuid[])) ORDER BY id;

-- name: OffersForTarget :many
SELECT * FROM target_offers WHERE target_id=$1 ORDER BY group_name;

-- name: BundleByID :one
SELECT * FROM release_bundles WHERE id=$1;

-- name: LatestBundle :one
SELECT * FROM release_bundles WHERE project_id=$1 ORDER BY id DESC LIMIT 1;

-- name: BundleOfRelease :one
SELECT b.* FROM release_bundles b JOIN releases r ON r.bundle_id=b.id WHERE r.id=$1;

-- name: BundleOfRun :one
SELECT * FROM release_bundles WHERE project_id=sqlc.arg(project_id) AND built_from_json->>'runId'=sqlc.arg(run_id)::uuid::text ORDER BY id DESC LIMIT 1;

-- name: ProjectDigestBundles :many
SELECT * FROM release_bundles WHERE project_id=$1 AND registry<>'' ORDER BY id;

-- name: SetStartFrom :execrows
UPDATE deployments SET start_from_backup=sqlc.arg(backup_id) WHERE id=sqlc.arg(id) AND observed_generation=0 AND started_from_at IS NULL;

-- name: MarkStartedFrom :execrows
UPDATE deployments SET started_from_at=sqlc.arg(at)::timestamptz WHERE id=sqlc.arg(id) AND start_from_backup IS NOT DISTINCT FROM sqlc.arg(backup_id) AND started_from_at IS NULL;

-- name: ProductionOnTargetKind :many
SELECT d.* FROM deployments d JOIN targets t ON t.id=d.target_id
  WHERE d.kind=sqlc.arg(standing_kind) AND t.kind=sqlc.arg(target_kind) AND d.data_class='production' ORDER BY d.id;

-- name: OpenRetainedForInstance :many
SELECT * FROM retained_resources WHERE disposition=sqlc.arg(open_state) AND handle_json->>'id'=sqlc.arg(instance)::text ORDER BY id;

-- name: NeedInstances :many
SELECT * FROM need_instances WHERE deployment_id=ANY(sqlc.arg(deployment_ids)::uuid[]) ORDER BY deployment_id,need;

-- name: ReleaseByNumber :one
SELECT * FROM releases WHERE project_id=$1 AND number=$2;

-- name: ReleasesForProject :many
SELECT * FROM releases WHERE project_id=sqlc.arg(project_id) ORDER BY number DESC LIMIT NULLIF(sqlc.arg(row_limit)::bigint,0);

-- The newest releases of the listed projects; the selection comes before the
-- limit, so other projects' releases never crowd these out.
-- name: RecentReleases :many
SELECT * FROM releases WHERE project_id=ANY(sqlc.arg(project_ids)::uuid[]) ORDER BY id DESC LIMIT sqlc.arg(row_limit)::bigint;

-- name: LastRelease :one
SELECT * FROM releases WHERE project_id=$1 AND deployment=$2 ORDER BY number DESC LIMIT 1;

-- name: LatestReleasedOn :many
SELECT r.* FROM releases r WHERE r.deployment=$1 AND r.state=$2 AND r.number=(SELECT MAX(number) FROM releases WHERE project_id=r.project_id AND deployment=r.deployment AND state=$2) ORDER BY r.project_id;

-- The newest release on a deployment before number, in state.
-- name: ReleasedBefore :one
SELECT * FROM releases WHERE project_id=sqlc.arg(project_id) AND deployment=sqlc.arg(deployment) AND state=sqlc.arg(state) AND number<sqlc.arg(number) ORDER BY number DESC LIMIT 1;

-- name: ReleasesForChange :many
SELECT r.* FROM releases r JOIN release_changes rc ON rc.release_id=r.id WHERE rc.change_id IS NOT DISTINCT FROM $1 AND r.project_id=$2 ORDER BY r.number DESC;

-- name: ReleaseEvents :many
SELECT * FROM release_events WHERE release_id=$1 ORDER BY id;

-- name: AttemptByKey :one
SELECT * FROM deployment_attempts WHERE deployment_id=$1 AND idempotency_key=$2;

-- name: AttemptByID :one
SELECT * FROM deployment_attempts WHERE id=$1;

-- name: InterruptedAttempts :many
SELECT * FROM deployment_attempts WHERE status IN ($1,$2) ORDER BY id;

-- name: AttemptInputs :one
SELECT i.* FROM deployment_attempt_inputs i JOIN deployment_attempts a ON a.id=i.attempt_id WHERE a.id=sqlc.arg(id) AND a.deployment_id=sqlc.arg(deployment_id) AND a.generation=sqlc.arg(generation);

-- Locks: the callers retain the transaction and lock order.

-- name: DeploymentForUpdate :one
SELECT * FROM deployments WHERE id=$1 FOR UPDATE;

-- name: TargetByNameForUpdate :one
SELECT * FROM targets WHERE name=$1 FOR UPDATE;

-- name: TargetForShare :one
SELECT * FROM targets WHERE id=$1 FOR SHARE;

-- name: AttemptForUpdate :one
SELECT * FROM deployment_attempts WHERE id=sqlc.arg(id) AND deployment_id=sqlc.arg(deployment_id) AND generation=sqlc.arg(generation) FOR UPDATE;

-- name: AttemptsForUpdate :many
SELECT * FROM deployment_attempts WHERE deployment_id=$1 AND status=$2 ORDER BY id FOR UPDATE;

-- name: LockTargets :exec
LOCK TABLE targets IN SHARE ROW EXCLUSIVE MODE;

-- Writes: named operations preserve their guards and affected-row receipts.

-- name: EnsureStanding :one
INSERT INTO deployments (display_label, id, project_id, name, kind, target_id, feed, audience, data_class, host_port, updated_at)
  VALUES (sqlc.arg(name), sqlc.arg(id), sqlc.arg(project_id),sqlc.arg(name),sqlc.arg(kind),sqlc.arg(target_id),sqlc.arg(feed),sqlc.arg(audience),sqlc.arg(data_class),0,sqlc.arg(updated_at))
  ON CONFLICT (project_id, name) DO UPDATE SET target_id=excluded.target_id, feed=excluded.feed,
 feed_revision=deployments.feed_revision+CASE WHEN deployments.feed IS DISTINCT FROM excluded.feed THEN 1 ELSE 0 END,
 feed_authority_json=CASE WHEN deployments.feed IS DISTINCT FROM excluded.feed THEN NULL ELSE deployments.feed_authority_json END,
  updated_at=excluded.updated_at
  WHERE (deployments.desired_generation=0 AND deployments.observed_generation=0)
  OR deployments.target_id=excluded.target_id RETURNING *;

-- name: SetDeploymentClassification :execrows
UPDATE deployments SET audience=sqlc.arg(audience), data_class=sqlc.arg(data_class), updated_at=sqlc.arg(updated_at)
WHERE id=sqlc.arg(id) AND audience=sqlc.arg(was_audience) AND data_class=sqlc.arg(was_data_class);

-- name: RecordPreview :one
INSERT INTO deployments (display_label, id, project_id, name, kind, change_id, target_id, handle_json, host_port, container, url, feed, audience, data_class, observed_generation, desired_generation, updated_at)
  VALUES (sqlc.arg(name), sqlc.arg(id), sqlc.arg(project_id),sqlc.arg(name),sqlc.arg(kind),sqlc.arg(change_id),sqlc.arg(target_id),sqlc.arg(handle_json),sqlc.arg(host_port),sqlc.arg(container),sqlc.arg(url),'manual','team','synthetic',1,1,sqlc.arg(updated_at))
  ON CONFLICT (project_id, name) DO UPDATE SET handle_json=excluded.handle_json, host_port=excluded.host_port, container=excluded.container,
  url=excluded.url, desired_generation=deployments.desired_generation+1, observed_generation=deployments.desired_generation+1, updated_at=excluded.updated_at RETURNING *;

-- name: InsertTarget :one
INSERT INTO targets (display_label, id, name, kind, provider, address_json, origin, capabilities_json, ingress_json, needs_mode, status, created_at, updated_at)
  VALUES (sqlc.arg(name), sqlc.arg(id), sqlc.arg(name),sqlc.arg(kind),sqlc.arg(provider),sqlc.arg(address_json),sqlc.arg(origin),sqlc.arg(capabilities_json),sqlc.arg(ingress_json),sqlc.arg(needs_mode),sqlc.arg(status),sqlc.arg(created_at),sqlc.arg(updated_at))
  ON CONFLICT (name) DO NOTHING
  RETURNING *;

-- name: PutTarget :one
INSERT INTO targets (display_label, id, name, kind, provider, address_json, origin, capabilities_json, ingress_json, needs_mode, status, created_at, updated_at)
  VALUES (sqlc.arg(name), sqlc.arg(id), sqlc.arg(name),sqlc.arg(kind),sqlc.arg(provider),sqlc.arg(address_json),sqlc.arg(origin),sqlc.arg(capabilities_json),sqlc.arg(ingress_json),sqlc.arg(needs_mode),sqlc.arg(status),sqlc.arg(created_at),sqlc.arg(updated_at))
  ON CONFLICT (name) DO UPDATE SET kind=excluded.kind, provider=excluded.provider, address_json=excluded.address_json,
  capabilities_json=excluded.capabilities_json, ingress_json=excluded.ingress_json, needs_mode=excluded.needs_mode,
  status=CASE WHEN targets.status=sqlc.arg(retired_state) THEN targets.status ELSE excluded.status END, updated_at=excluded.updated_at,
  settings_revision=targets.settings_revision + CASE WHEN (targets.capabilities_json, targets.ingress_json, targets.needs_mode)
    IS DISTINCT FROM (excluded.capabilities_json, excluded.ingress_json, excluded.needs_mode) THEN 1 ELSE 0 END
  RETURNING *;

-- name: RefreshTarget :one
UPDATE targets SET kind=sqlc.arg(kind), provider=sqlc.arg(provider), address_json=sqlc.arg(address_json), capabilities_json=sqlc.arg(capabilities_json),
  ingress_json=sqlc.arg(ingress_json), needs_mode=sqlc.arg(needs_mode),
  settings_revision=settings_revision+1, updated_at=sqlc.arg(updated_at)
WHERE id=sqlc.arg(id) RETURNING *;

-- name: EditTargetSettings :one
UPDATE targets SET capabilities_json=sqlc.arg(capabilities_json), ingress_json=sqlc.arg(ingress_json), needs_mode=sqlc.arg(needs_mode),
  address_json=sqlc.arg(address_json), settings_revision=settings_revision+1, updated_at=sqlc.arg(updated_at)
WHERE id=sqlc.arg(id) RETURNING *;

-- name: PutOffer :one
INSERT INTO target_offers (id, target_id, group_name, caps_json, created_by, created_by_principal, created_by_via, created_at) VALUES (sqlc.arg(id), sqlc.arg(target_id),sqlc.arg(group_name),sqlc.arg(caps_json),sqlc.arg(created_by),sqlc.arg(created_by_principal),sqlc.arg(created_by_via),sqlc.arg(created_at))
  ON CONFLICT (target_id, group_name) DO UPDATE SET caps_json=excluded.caps_json, created_by=excluded.created_by, created_by_principal=excluded.created_by_principal, created_by_via=excluded.created_by_via, created_at=excluded.created_at RETURNING *;

-- name: RevokeOffer :one
DELETE FROM target_offers WHERE id=sqlc.arg(id) AND target_id=sqlc.arg(target_id) RETURNING *;

-- name: CheckTarget :one
UPDATE targets SET status=sqlc.arg(status), updated_at=sqlc.arg(updated_at) WHERE id=sqlc.arg(id) AND status<>sqlc.arg(retired_state) AND settings_revision=sqlc.arg(settings_revision) RETURNING *;

-- name: RetireTarget :one
UPDATE targets SET status=sqlc.arg(status), updated_at=sqlc.arg(updated_at) WHERE id=sqlc.arg(id) RETURNING *;

-- name: ReserveHostPort :one
UPDATE deployments SET host_port=CASE WHEN host_port=0 THEN sqlc.arg(candidate) ELSE host_port END,
  container=sqlc.arg(container), updated_at=sqlc.arg(updated_at) WHERE project_id=sqlc.arg(project_id) AND name=sqlc.arg(name) AND target_id IS NOT NULL RETURNING *;

-- name: InsertBundle :one
INSERT INTO release_bundles (id, project_id, contract_revision, contract_hash, parts_json, init_json, built_from_json, created_at, target_id,
    registry, platform, built_on_json, run_id, step_id, attempt)
  VALUES (sqlc.arg(id), sqlc.arg(project_id),sqlc.arg(contract_revision),sqlc.arg(contract_hash),sqlc.arg(parts_json),sqlc.arg(init_json),sqlc.arg(built_from_json),sqlc.arg(created_at),sqlc.arg(target_id),
    sqlc.arg(registry),sqlc.arg(platform),sqlc.arg(built_on_json),sqlc.arg(run_id),sqlc.arg(step_id),sqlc.arg(attempt)) RETURNING *;

-- name: RecordNeedIntent :one
INSERT INTO need_instances (id, deployment_id, need, mode, binding_json, handle_json, status, updated_at) VALUES (sqlc.arg(id), sqlc.arg(deployment_id),sqlc.arg(need),'managed','{}',sqlc.arg(handle_json),sqlc.arg(status),sqlc.arg(updated_at))
  ON CONFLICT (deployment_id, need) DO UPDATE SET handle_json=excluded.handle_json, status=CASE WHEN need_instances.status=sqlc.arg(ready_state) THEN need_instances.status ELSE excluded.status END, updated_at=excluded.updated_at RETURNING *;

-- name: RecordNeedFulfilled :one
INSERT INTO need_instances (id, deployment_id, need, mode, binding_json, handle_json, status, updated_at) VALUES (sqlc.arg(id), sqlc.arg(deployment_id),sqlc.arg(need),'managed',sqlc.arg(binding_json),sqlc.arg(handle_json),sqlc.arg(status),sqlc.arg(updated_at))
  ON CONFLICT (deployment_id, need) DO UPDATE SET binding_json=excluded.binding_json, handle_json=excluded.handle_json, status=excluded.status, updated_at=excluded.updated_at RETURNING *;

-- name: RecordOwned :execrows
INSERT INTO deployment_owned (id, deployment_id, kind, ident, name, holds_data, need, recorded_at, note)
  VALUES (sqlc.arg(id), sqlc.arg(deployment_id),sqlc.arg(kind),sqlc.arg(ident),sqlc.arg(name),sqlc.arg(holds_data),sqlc.arg(need),sqlc.arg(recorded_at),sqlc.arg(note))
  ON CONFLICT (deployment_id, kind, ident) DO NOTHING;

-- name: ForgetOwned :execrows
DELETE FROM deployment_owned WHERE deployment_id=sqlc.arg(deployment_id) AND kind=sqlc.arg(kind) AND ident=sqlc.arg(ident);

-- name: RecordRepository :execrows
INSERT INTO owned_repositories (id, project_id, connection_id, connector, registry, repository, recorded_at, note)
  VALUES (sqlc.arg(id), sqlc.arg(project_id),sqlc.arg(connection_id),sqlc.arg(connector),sqlc.arg(registry),sqlc.arg(repository),sqlc.arg(recorded_at),sqlc.arg(note))
  ON CONFLICT (connector, registry, repository) DO NOTHING;

-- name: RepositoryOwned :one
SELECT EXISTS(SELECT 1 FROM owned_repositories WHERE project_id=sqlc.arg(project_id) AND connection_id=sqlc.arg(connection_id) AND registry=sqlc.arg(registry) AND repository=sqlc.arg(repository));

-- name: OtherDeploymentsOnConnector :one
SELECT count(*) FROM deployments d JOIN targets t ON t.id=d.target_id
  WHERE d.project_id=sqlc.arg(project_id) AND d.id<>sqlc.arg(excluding) AND t.kind=sqlc.arg(kind) AND t.provider=sqlc.arg(provider);

-- name: OwnedByDeployment :many
SELECT * FROM deployment_owned WHERE deployment_id=sqlc.arg(deployment_id) ORDER BY id;

-- name: OpenRetainedDataForName :many
SELECT * FROM retained_resources WHERE project_id=sqlc.arg(project_id) AND deployment=sqlc.arg(deployment) AND target_id=sqlc.arg(target_id) AND holds_data AND disposition=sqlc.arg(open_state) ORDER BY id;

-- name: InsertRetainedResource :one
INSERT INTO retained_resources (id, project_id, deployment, target_id, need, handle_json, retained_by, retained_by_principal, retained_by_via, retained_at, disposition, holds_data)
  VALUES (sqlc.arg(id), sqlc.arg(project_id),sqlc.arg(deployment),sqlc.arg(target_id),sqlc.arg(need),sqlc.arg(handle_json),sqlc.arg(retained_by),sqlc.arg(retained_by_principal),sqlc.arg(retained_by_via),sqlc.arg(retained_at),sqlc.arg(disposition),sqlc.arg(holds_data)) RETURNING *;

-- name: InsertAttempt :one
INSERT INTO deployment_attempts (id, deployment_id, generation, idempotency_key, status, started_at) VALUES (sqlc.arg(id), sqlc.arg(deployment_id),sqlc.arg(generation),sqlc.arg(idempotency_key),sqlc.arg(status),sqlc.arg(started_at)) RETURNING *;

-- name: ForgetPreview :execrows
DELETE FROM deployments WHERE kind=sqlc.arg(kind) AND change_id IS NOT DISTINCT FROM sqlc.arg(change_id);

-- name: DeleteDeployment :execrows
DELETE FROM deployments WHERE id=sqlc.arg(id);

-- name: RecordAddress :execrows
UPDATE deployments SET url=sqlc.arg(url), updated_at=sqlc.arg(updated_at) WHERE id=sqlc.arg(id);

-- name: RecordOwnedResource :execrows
UPDATE deployments SET handle_json = handle_json || jsonb_build_object(sqlc.arg(handle_key)::text, sqlc.arg(handle_value)::text), updated_at=sqlc.arg(updated_at)
  WHERE id=sqlc.arg(id) AND (handle_json->>sqlc.arg(handle_key)::text IS NULL OR handle_json->>sqlc.arg(handle_key)::text = sqlc.arg(handle_value)::text);

-- name: ForgetPreviewContainer :execrows
DELETE FROM deployments WHERE kind=sqlc.arg(kind) AND container=sqlc.arg(container);

-- name: ResetHostPort :execrows
UPDATE deployments SET host_port=sqlc.arg(host_port), updated_at=sqlc.arg(updated_at) WHERE project_id=sqlc.arg(project_id) AND name=sqlc.arg(name);

-- name: RecordNeedIdentity :execrows
UPDATE need_instances SET handle_json=sqlc.arg(handle_json) WHERE deployment_id=sqlc.arg(deployment_id) AND need=sqlc.arg(need);

-- name: SettleRetainedResource :execrows
UPDATE retained_resources SET disposition=sqlc.arg(disposition), note=sqlc.arg(note), settled_by=sqlc.arg(settled_by), settled_by_principal=sqlc.arg(settled_by_principal), settled_by_via=sqlc.arg(settled_by_via), settled_at=sqlc.arg(settled_at) WHERE id=sqlc.arg(id) AND project_id=sqlc.arg(project_id) AND disposition=sqlc.arg(open_state);

-- name: SupersedeAttempts :execrows
UPDATE deployment_attempts SET status=sqlc.arg(status), terminal=true, superseded_by=sqlc.arg(superseded_by), finished_at=sqlc.arg(finished_at) WHERE deployment_id=sqlc.arg(deployment_id) AND status=sqlc.arg(running_state) AND id!=sqlc.arg(id);

-- name: AdmitGeneration :execrows
UPDATE deployments SET desired_generation=GREATEST(desired_generation, sqlc.arg(desired_generation)), readiness=sqlc.arg(readiness), updated_at=sqlc.arg(updated_at) WHERE id=sqlc.arg(id);

-- name: FinishSupersededAttempt :execrows
UPDATE deployment_attempts SET status=sqlc.arg(status), terminal=sqlc.arg(terminal), finished_at=sqlc.arg(finished_at) WHERE id=sqlc.arg(id);

-- name: SetReadiness :execrows
UPDATE deployments SET readiness=sqlc.arg(readiness), updated_at=sqlc.arg(updated_at) WHERE id=sqlc.arg(id);

-- name: ObserveGeneration :execrows
UPDATE deployments SET observed_generation=sqlc.arg(observed_generation), handle_json=sqlc.arg(handle_json), url=sqlc.arg(url), container=sqlc.arg(container), host_port=sqlc.arg(host_port),
  release_run=sqlc.arg(release_run), release_sha=sqlc.arg(release_sha), image_digest=sqlc.arg(image_digest), readiness=sqlc.arg(readiness),
  serving_release_id=sqlc.arg(serving_release_id), serving_attempt_id=sqlc.arg(serving_attempt_id), updated_at=sqlc.arg(updated_at) WHERE id=sqlc.arg(id);

-- name: FinishAttempt :execrows
UPDATE deployment_attempts SET status=sqlc.arg(status), terminal=sqlc.arg(terminal), observed_json=sqlc.arg(observed_json), finished_at=sqlc.arg(finished_at) WHERE id=sqlc.arg(id);

-- name: CheckpointAttempt :execrows
UPDATE deployment_attempts SET observed_json=observed_json || sqlc.arg(observed_json)::jsonb WHERE id=sqlc.arg(id);

-- name: UncertainAttempt :execrows
UPDATE deployment_attempts SET status=sqlc.arg(status),terminal=sqlc.arg(terminal),finished_at=NULL,
  observed_json=observed_json || sqlc.arg(observed_json)::jsonb WHERE id=sqlc.arg(id);

-- name: RemoveProjectReleaseEvents :execrows
DELETE FROM release_events WHERE release_id IN (SELECT id FROM releases WHERE project_id=$1);

-- name: RemoveProjectReleaseChanges :execrows
DELETE FROM release_changes WHERE release_id IN (SELECT id FROM releases WHERE project_id=$1);

-- name: RemoveProjectReleases :execrows
DELETE FROM releases WHERE project_id=$1;

-- name: RemoveProjectReleaseBundles :execrows
DELETE FROM release_bundles WHERE project_id=$1;

-- name: RemoveProjectDeployments :execrows
DELETE FROM deployments WHERE project_id=$1;

-- name: RepairInterruptedReleases :many
-- The caller appends each repaired release's event (the database never
-- mints an id).
UPDATE releases r SET state=sqlc.arg(failed_state), failed_stage=stage, stage_finished_at=sqlc.arg(at)::timestamptz WHERE state=sqlc.arg(open_state)
  AND NOT EXISTS(SELECT 1 FROM release_requests q WHERE q.release_id=r.id AND q.claimed_at IS NULL
    AND r.id=ANY(sqlc.arg(queued_release_ids)::uuid[]))
  AND NOT EXISTS(SELECT 1 FROM release_requests q WHERE q.release_id=r.id AND q.rebuild_pending)
  AND NOT EXISTS(SELECT 1 FROM deployment_attempts a LEFT JOIN deployment_attempt_inputs i ON i.attempt_id=a.id
  WHERE i.release_id=r.id OR (i.attempt_id IS NULL AND a.idempotency_key='release-' || r.id::text))
  RETURNING id;

-- Reports over Delivery tables and project identity.

-- name: Placements :many
SELECT sqlc.embed(e),p.slug FROM deployments e JOIN projects p ON p.id=e.project_id WHERE e.kind<>'restored-copy' AND (NOT sqlc.arg(target_only)::boolean OR e.target_id IS NOT DISTINCT FROM sqlc.arg(target_id)) AND (NOT sqlc.arg(name_only)::boolean OR e.name=sqlc.arg(name)) AND (NOT sqlc.arg(idle_standing)::boolean OR (e.kind=sqlc.arg(standing_state) AND NOT EXISTS(SELECT 1 FROM deployment_attempts a WHERE a.deployment_id=e.id AND a.status IN (sqlc.arg(running_state),sqlc.arg(uncertain_state))))) ORDER BY p.slug,e.name;

-- name: RetainedResources :many
SELECT sqlc.embed(r),COALESCE(p.slug,'')::text AS project,COALESCE(t.name,'')::text AS target FROM retained_resources r LEFT JOIN projects p ON p.id=r.project_id LEFT JOIN targets t ON t.id=r.target_id WHERE (NOT sqlc.arg(project_only)::boolean OR r.project_id=sqlc.arg(project_id)) AND (NOT sqlc.arg(target_only)::boolean OR r.target_id IS NOT DISTINCT FROM sqlc.arg(target_id)) AND (sqlc.arg(disposition)::text='' OR r.disposition=sqlc.arg(disposition)) ORDER BY r.id DESC;

-- name: BoundTargetDeployments :one
SELECT COALESCE(string_agg(p.slug || '/' || e.name, ', ' ORDER BY p.slug, e.name), '')
  FROM deployments e JOIN projects p ON p.id=e.project_id WHERE e.target_id=sqlc.arg(target_id) AND e.kind=sqlc.arg(kind);

-- name: RemoveTargetOffers :one
WITH selected AS MATERIALIZED (
  SELECT target_offers.id FROM target_offers WHERE target_offers.target_id=sqlc.arg(target_id)
  ), removed AS (
  DELETE FROM target_offers USING selected WHERE target_offers.id=selected.id RETURNING target_offers.id
  ) SELECT (SELECT COUNT(*) FROM selected) AS selected_count,(SELECT COUNT(*) FROM removed) AS removed_count;

-- name: LatestReleasedNumber :one
SELECT COALESCE(MAX(number),0)::bigint FROM releases WHERE project_id=sqlc.arg(project_id) AND deployment=sqlc.arg(deployment) AND state=sqlc.arg(state);

-- name: ReleasingCount :one
SELECT COUNT(*) FROM releases WHERE state=sqlc.arg(state);

-- name: UncertainAttemptID :one
SELECT (SELECT id FROM deployment_attempts WHERE deployment_id=sqlc.arg(deployment_id) AND status=sqlc.arg(status) ORDER BY id DESC LIMIT 1)::uuid AS id;

-- name: ContradictorySettlements :many
SELECT a.id AS attempt_id,a.deployment_id,a.generation,a.status,r.id AS release_id FROM deployment_attempts a
  JOIN deployment_attempt_inputs i ON i.attempt_id=a.id
  JOIN releases r ON r.id=i.release_id
  WHERE r.state=sqlc.arg(release_state) AND a.status IN (sqlc.arg(done_state),sqlc.arg(failed_state),sqlc.arg(superseded_state)) ORDER BY a.id;

-- name: HasOpenAttempt :one
SELECT EXISTS(SELECT 1 FROM deployment_attempts WHERE deployment_id=sqlc.arg(deployment_id) AND status IN (sqlc.arg(running_state),sqlc.arg(uncertain_state)));

-- name: StandingCount :one
SELECT COUNT(*) FROM deployments WHERE project_id=sqlc.arg(project_id) AND target_id=sqlc.arg(target_id) AND kind=sqlc.arg(kind) AND name<>sqlc.arg(excluding);

-- name: RetainedOpenCount :one
SELECT COUNT(*) FROM retained_resources WHERE target_id=sqlc.arg(target_id) AND disposition=sqlc.arg(disposition);

-- name: InsertReleaseRequest :one
INSERT INTO release_requests(release_id,target_id,base_generation,attributes_json,start_from_backup,provider_binding_json,ingress_json,authority_json,require_promotion)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING *;

-- name: ReleaseStartFrom :one
SELECT start_from_backup FROM release_requests WHERE release_id=sqlc.arg(release_id);

-- name: RequestedReleaseForUpdate :one
SELECT sqlc.embed(r),sqlc.embed(q) FROM releases r JOIN release_requests q ON q.release_id=r.id
WHERE r.id=$1 FOR UPDATE OF q;

-- name: ClaimReleaseRequest :execrows
UPDATE release_requests SET claimed_at=$2 WHERE release_id=$1 AND claimed_at IS NULL;

-- name: ProviderHasLiveTargets :one
SELECT EXISTS(SELECT 1 FROM targets WHERE provider=$1 AND status<>$2);

-- Runtime display/observation facts. NULL scope is private to the background sweep.
-- name: RuntimeDeployments :many
SELECT sqlc.embed(d), a.finished_at AS last_applied_at, p.slug FROM deployments d LEFT JOIN deployment_attempts a ON a.id=d.serving_attempt_id JOIN projects p ON p.id=d.project_id
WHERE d.kind<>'restored-copy' AND (sqlc.narg(project_ids)::uuid[] IS NULL OR d.project_id=ANY(sqlc.narg(project_ids)::uuid[]))
ORDER BY d.kind DESC,d.id;

-- name: InsertRestoredCopyDeployment :one
INSERT INTO deployments (display_label, id, project_id, name, kind, target_id, feed, audience, data_class, start_from_backup, updated_at)
  VALUES (sqlc.arg(name), sqlc.arg(id), sqlc.arg(project_id), sqlc.arg(name), 'restored-copy', sqlc.arg(target_id), 'promotion', 'team', sqlc.arg(data_class), sqlc.arg(backup_id), sqlc.arg(at))
  RETURNING *;

-- name: DeploymentOverrides :many
SELECT * FROM deployment_overrides WHERE deployment_id = $1 ORDER BY part;

-- name: PutDeploymentOverride :exec
INSERT INTO deployment_overrides(deployment_id, part, memory_bytes, memory_unlimited, cpus, reason, set_by, set_by_principal, set_by_via, set_at)
VALUES ($1, $2, $3, sqlc.arg(memory_unlimited), $4, $5, $6, $7, $8, $9)
ON CONFLICT (deployment_id, part) DO UPDATE SET memory_bytes = EXCLUDED.memory_bytes, memory_unlimited = EXCLUDED.memory_unlimited, cpus = EXCLUDED.cpus,
  reason = EXCLUDED.reason, set_by = EXCLUDED.set_by, set_by_principal = EXCLUDED.set_by_principal,
  set_by_via = EXCLUDED.set_by_via, set_at = EXCLUDED.set_at;

-- name: DeleteDeploymentOverride :execrows
DELETE FROM deployment_overrides WHERE deployment_id = $1 AND part = $2;

-- name: RecordLimitEvent :execrows
INSERT INTO deployment_limit_events(id, deployment_id, part, kind, container, at, exit_code, generation)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (deployment_id, container, kind, at) DO NOTHING;

-- name: LimitEventsSince :many
SELECT * FROM deployment_limit_events WHERE deployment_id = $1 AND at >= $2 ORDER BY at, part, kind;

-- name: LatestLimitEventOnTarget :one
SELECT COALESCE(MAX(e.at), '0001-01-01T00:00:00Z'::timestamptz)::timestamptz AS at
FROM deployment_limit_events e JOIN deployments d ON d.id = e.deployment_id
WHERE d.target_id = $1;

-- name: PruneLimitEvents :execrows
DELETE FROM deployment_limit_events WHERE at < $1;

-- name: ReleasedGeneration :one
SELECT id, number FROM releases WHERE project_id = $1 AND deployment_id = $2 AND generation = $3 AND state = $4;

-- After a whole-AppTown restore (restore_check.go).
-- name: HoldStandingAfterRestore :many
UPDATE deployments d SET restore_check=sqlc.arg(held), updated_at=sqlc.arg(updated_at)
FROM targets t WHERE t.id=d.target_id AND d.kind=sqlc.arg(standing) AND t.kind=ANY(sqlc.arg(kinds)::text[])
RETURNING d.id, d.project_id, d.name;

-- name: DeploymentsHeldAfterRestore :many
SELECT id FROM deployments WHERE restore_check=sqlc.arg(held) ORDER BY id;

-- name: SettleRestoreCheck :execrows
UPDATE deployments SET restore_check=sqlc.arg(next), updated_at=sqlc.arg(updated_at)
WHERE id=sqlc.arg(id) AND restore_check=sqlc.arg(current);

-- What the provider runs becomes the record (the driver's change to the
-- handle, deploy.RunningRecord) and its observed release serves: what
-- AppTown applied there, and its revision, are not known.
-- name: AdoptAfterRestore :execrows
UPDATE deployments SET handle_json=handle_json || sqlc.arg(handle_patch)::jsonb,
  release_sha='', serving_release_id=sqlc.arg(serving_release_id), serving_attempt_id=NULL, restore_check=sqlc.arg(next), updated_at=sqlc.arg(updated_at)
WHERE id=sqlc.arg(id) AND restore_check=sqlc.arg(current);

-- name: StorageBinding :one
SELECT * FROM deployment_storage_bindings WHERE deployment_id=sqlc.arg(deployment_id) AND need=sqlc.arg(need) AND volume=sqlc.arg(volume);

-- name: StorageBindings :many
SELECT * FROM deployment_storage_bindings WHERE deployment_id=sqlc.arg(deployment_id) ORDER BY need, volume;

-- name: EstablishStorageBinding :exec
INSERT INTO deployment_storage_bindings (id, deployment_id, need, volume, data_path, declared_image, image_id, repo_digest, volume_created, volume_driver, origin, recorded_at)
VALUES (sqlc.arg(id), sqlc.arg(deployment_id), sqlc.arg(need), sqlc.arg(volume), sqlc.arg(data_path), sqlc.arg(declared_image), sqlc.arg(image_id), sqlc.arg(repo_digest), sqlc.arg(volume_created), sqlc.arg(volume_driver), sqlc.arg(origin), sqlc.arg(recorded_at));

-- name: RetireStorageBinding :exec
DELETE FROM deployment_storage_bindings WHERE deployment_id=sqlc.arg(deployment_id) AND need=sqlc.arg(need) AND volume=sqlc.arg(volume);

-- A release belongs to a placement identity; reused display names cannot
-- establish current attribution or select another placement's history.
-- name: LatestReleasedForDeployment :one
SELECT * FROM releases WHERE deployment_id=sqlc.arg(deployment_id) AND state=sqlc.arg(state) ORDER BY number DESC LIMIT 1;

-- name: AssociateTargetProvider :one
UPDATE targets SET connection_id=sqlc.arg(connection_id), provider=sqlc.arg(provider), provider_scope_json=sqlc.arg(provider_scope_json),
  settings_revision=settings_revision+1, updated_at=sqlc.arg(at)
WHERE id=sqlc.arg(id) AND settings_revision=sqlc.arg(expected_revision) AND provider_scope_json IS NULL
RETURNING *;

-- name: ProviderDeploymentsOnTarget :many
SELECT * FROM deployments WHERE target_id=$1 ORDER BY id;

-- name: SetProviderAssociationHandle :execrows
UPDATE deployments SET handle_json=handle_json || jsonb_build_object('portainerAssociations',sqlc.arg(associations)::jsonb)
WHERE id=sqlc.arg(id) AND target_id=sqlc.arg(target_id) AND desired_generation=sqlc.arg(generation)
 AND handle_json=sqlc.arg(expected_handle)::jsonb;

-- name: InsertProviderTarget :one
INSERT INTO targets(id,name,display_label,kind,provider,connection_id,provider_scope_json,address_json,origin,capabilities_json,ingress_json,needs_mode,status,created_at,updated_at)
VALUES(sqlc.arg(id),sqlc.arg(name),sqlc.arg(name),sqlc.arg(kind),sqlc.arg(provider),sqlc.arg(connection_id),sqlc.arg(provider_scope_json),sqlc.arg(address_json),sqlc.arg(origin),sqlc.arg(capabilities_json),sqlc.arg(ingress_json),sqlc.arg(needs_mode),sqlc.arg(status),sqlc.arg(at),sqlc.arg(at))
ON CONFLICT(name) DO NOTHING RETURNING *;

-- name: ConnectorHasRetainedIdentity :one
SELECT (EXISTS(SELECT 1 FROM targets WHERE provider=sqlc.arg(provider) AND connection_id IS NOT NULL)
 OR EXISTS(SELECT 1 FROM owned_repositories WHERE connector=sqlc.arg(provider) AND connection_id IS NOT NULL))::boolean AS referenced;

-- name: EditTargetDisplayLabel :one
UPDATE targets SET display_label=sqlc.arg(display_label)
WHERE id=sqlc.arg(id) AND display_label=sqlc.arg(expected_label) RETURNING *;

-- name: EditDeploymentDisplayLabel :one
UPDATE deployments SET display_label=sqlc.arg(display_label)
WHERE project_id=sqlc.arg(project_id) AND id=sqlc.arg(id) AND display_label=sqlc.arg(expected_label) RETURNING *;

-- name: LockProjectReleaseSettings :one
SELECT * FROM project_release_settings WHERE project_id=$1 FOR UPDATE;

-- name: ProjectReleaseSettings :one
SELECT * FROM project_release_settings WHERE project_id=$1;

-- name: ChooseDefaultDeployment :one
INSERT INTO project_release_settings(project_id, default_deployment_id, revision)
SELECT sqlc.arg(project_id), sqlc.narg(default_deployment_id)::uuid, 1
WHERE sqlc.arg(expected_revision)::bigint=0
ON CONFLICT(project_id) DO UPDATE SET default_deployment_id=excluded.default_deployment_id,
 revision=project_release_settings.revision+1
WHERE project_release_settings.revision=sqlc.arg(expected_revision)
RETURNING *;

-- name: UpdateDefaultDeployment :one
UPDATE project_release_settings SET default_deployment_id=sqlc.narg(default_deployment_id)::uuid, revision=revision+1
WHERE project_id=sqlc.arg(project_id) AND revision=sqlc.arg(expected_revision) RETURNING *;

-- name: ClearRemovedDefault :exec
UPDATE project_release_settings SET default_deployment_id=NULL, revision=revision+1 WHERE default_deployment_id=$1;

-- name: TargetHasReleaseDefault :one
SELECT EXISTS(SELECT 1 FROM project_release_settings s JOIN deployments d ON d.id=s.default_deployment_id WHERE d.target_id=$1);

-- name: DeploymentAddressSettings :one
SELECT * FROM deployment_address_settings WHERE deployment_id=$1;

-- name: DeploymentAddressSettingsForUpdate :one
SELECT * FROM deployment_address_settings WHERE deployment_id=$1 FOR UPDATE;

-- name: InsertDeploymentAddressSettings :one
INSERT INTO deployment_address_settings(deployment_id,generated_hostname,custom_hostname,certificate_domain)
VALUES(sqlc.arg(deployment_id),sqlc.arg(generated_hostname),sqlc.arg(custom_hostname),sqlc.arg(certificate_domain)) RETURNING *;

-- name: UpdateDeploymentAddressSettings :one
UPDATE deployment_address_settings SET generated_hostname=sqlc.arg(generated_hostname),custom_hostname=sqlc.arg(custom_hostname),certificate_domain=sqlc.arg(certificate_domain),revision=revision+1
WHERE deployment_id=sqlc.arg(deployment_id) AND revision=sqlc.arg(expected_revision) RETURNING *;

-- name: EnsureHostnameClaim :exec
INSERT INTO deployment_hostname_claims(hostname) VALUES($1) ON CONFLICT DO NOTHING;

-- name: LockHostnameClaim :one
SELECT hostname FROM deployment_hostname_claims WHERE hostname=$1 FOR UPDATE;

-- name: HostnameClaimants :many
SELECT deployment_id FROM deployment_hostname_claimants WHERE hostname=$1 ORDER BY deployment_id;

-- name: AddHostnameClaimant :exec
INSERT INTO deployment_hostname_claimants(hostname,deployment_id) VALUES($1,$2) ON CONFLICT DO NOTHING;

-- name: DeploymentHostnameClaims :many
SELECT hostname FROM deployment_hostname_claimants WHERE deployment_id=$1 ORDER BY hostname;

-- name: RemoveDeploymentHostnameClaims :exec
DELETE FROM deployment_hostname_claimants WHERE deployment_id=$1;

-- name: RemoveEmptyHostnameClaim :exec
DELETE FROM deployment_hostname_claims c WHERE c.hostname=$1 AND NOT EXISTS(SELECT 1 FROM deployment_hostname_claimants h WHERE h.hostname=c.hostname);

-- name: ReleaseNumbersForProject :many
SELECT number FROM releases WHERE project_id=sqlc.arg(project_id) ORDER BY number;

-- name: Reading :one
SELECT identity, generation, invalidated, payload, act_counter, complete_act_counter FROM deployment_readings WHERE subject=$1 AND kind=$2;

-- name: StoreReading :exec
INSERT INTO deployment_readings (id, subject, kind, identity, payload)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (subject,kind) DO UPDATE SET identity=EXCLUDED.identity, payload=EXCLUDED.payload
WHERE deployment_readings.identity=EXCLUDED.identity OR deployment_readings.generation=0;

-- name: StartInventoryReading :one
INSERT INTO deployment_readings (id, subject, kind, identity)
VALUES ($1,$2,$3,$4)
ON CONFLICT (subject,kind) DO UPDATE SET
    identity=EXCLUDED.identity,
    payload=CASE WHEN deployment_readings.identity=EXCLUDED.identity THEN deployment_readings.payload ELSE '{}'::jsonb END
RETURNING identity, generation, invalidated, payload;

-- name: FinishInventoryReading :execrows
UPDATE deployment_readings SET payload=$5, invalidated=$6
WHERE subject=$1 AND kind=$2 AND identity=$3 AND generation=$4;

-- name: InvalidateReading :exec
UPDATE deployment_readings SET generation=generation+1, invalidated=TRUE WHERE subject=$1 AND kind=$2;

-- name: StartHostSnapshot :one
INSERT INTO deployment_readings (id, subject, kind, identity)
VALUES ($1,$2,'host-snapshot',$3)
ON CONFLICT (subject,kind) DO UPDATE SET
    identity=EXCLUDED.identity,
    payload=CASE WHEN deployment_readings.identity=EXCLUDED.identity THEN deployment_readings.payload ELSE '{}'::jsonb END
RETURNING act_counter;

-- name: BeforeHostAct :exec
INSERT INTO deployment_readings (id, subject, kind, identity, act_counter)
SELECT sqlc.arg(id)::uuid,sqlc.arg(subject)::text,'host-snapshot',sqlc.arg(identity)::text,1
WHERE EXISTS (SELECT 1 FROM targets WHERE id::text=sqlc.arg(subject) AND kind='docker-host')
ON CONFLICT (subject,kind) DO UPDATE SET act_counter=deployment_readings.act_counter+1;

-- name: LockHostActCounter :one
-- Holds the host reading row so an act cannot start until the caller commits.
SELECT act_counter FROM deployment_readings WHERE subject=sqlc.arg(subject) AND kind='host-snapshot' FOR UPDATE;

-- name: MarkHostActForDeployment :exec
-- Recording an image copy is an act on the deployment's host reading, so a
-- reading that saw the image absent cannot retire the new record.
UPDATE deployment_readings SET act_counter=act_counter+1
WHERE kind='host-snapshot' AND subject=(SELECT d.target_id::text FROM deployments d WHERE d.id=sqlc.arg(deployment_id));

-- name: FinishHostSnapshot :execrows
UPDATE deployment_readings r SET payload=sqlc.arg(payload),
    complete_act_counter=CASE WHEN sqlc.arg(complete)::boolean AND r.act_counter=sqlc.arg(act_counter)
      THEN sqlc.arg(act_counter) ELSE r.complete_act_counter END
WHERE r.subject=sqlc.arg(subject) AND r.kind='host-snapshot' AND r.identity=sqlc.arg(identity)
 AND EXISTS (SELECT 1 FROM targets t WHERE t.id::text=r.subject AND t.settings_revision::text=r.identity AND t.status<>'retired');

-- name: ActiveDockerTargets :many
SELECT * FROM targets WHERE kind='docker-host' AND status<>'retired' ORDER BY name;

-- name: ReleasesBeforeBackups :many
SELECT backup_id, number FROM releases WHERE project_id=sqlc.arg(project_id) AND backup_id=ANY(sqlc.arg(backup_ids)::uuid[]);

-- name: ConfirmMainFeed :execrows
UPDATE deployments SET feed_authority_json=sqlc.arg(authority), feed_revision=feed_revision+1
WHERE id=sqlc.arg(id) AND feed='main' AND feed_revision=sqlc.arg(revision);

-- name: MainDeployments :many
SELECT * FROM deployments WHERE project_id=$1 AND feed='main' AND kind='standing' ORDER BY id;

-- name: LandingReleaseExists :one
SELECT EXISTS(SELECT 1 FROM releases WHERE run_id=$1 AND deployment_id=$2);

-- name: LinkLandingRequest :execrows
UPDATE release_requests SET feed_revision=$2, landing_approvers=$3 WHERE release_id=$1;

-- name: RecordReleaseBackup :execrows
UPDATE release_requests SET backup_evidence_json=$2 WHERE release_id=$1;

-- name: AttachReleaseBackup :execrows
UPDATE releases SET backup_id=$2 WHERE id=$1;

-- name: LandingRequestsForChange :many
SELECT r.* FROM releases r JOIN release_requests q ON q.release_id=r.id JOIN runs w ON w.id=r.run_id
WHERE r.project_id=$1 AND w.change_id=$2 AND q.feed_revision IS NOT NULL ORDER BY r.number DESC;

-- name: ReleaseRequestMetadata :one
SELECT routine,authority_json,feed_revision,landing_approvers,backup_evidence_json,uncopied_acceptance_json FROM release_requests WHERE release_id=$1;

-- name: RecordUncopiedAcceptance :execrows
UPDATE release_requests SET uncopied_acceptance_json=$2 WHERE release_id=$1;

-- name: LockFeedSettings :many
SELECT * FROM deployments WHERE project_id=$1 ORDER BY id FOR UPDATE;

-- name: SetDeploymentFeed :execrows
UPDATE deployments SET feed=sqlc.arg(feed), promoted_from_id=sqlc.narg(promoted_from_id),
 approval_role=sqlc.arg(approval_role), feed_authority_json=sqlc.narg(authority), feed_revision=feed_revision+1
WHERE id=sqlc.arg(id) AND feed_revision=sqlc.arg(revision);

-- name: RecordPromotionSource :execrows
UPDATE releases SET from_deployment_id=$2 WHERE id=$1;

-- name: SetReleaseApprovalRule :execrows
UPDATE releases SET approval_role=$2, state=$3 WHERE id=$1;

-- name: SupersedeWaitingReleases :many
UPDATE releases SET state='superseded', superseded_by=sqlc.arg(new_release)
WHERE deployment_id=sqlc.arg(deployment_id) AND state='waiting for approval' AND id<>sqlc.arg(new_release)
RETURNING id;

-- name: DecideWaitingRelease :execrows
UPDATE releases SET state=sqlc.arg(state), approval_json=sqlc.narg(approval),
 stage_entered_at=CASE WHEN sqlc.arg(state)::text='releasing' THEN sqlc.arg(at)::timestamptz ELSE stage_entered_at END
WHERE id=sqlc.arg(id) AND state='waiting for approval';

-- name: WaitingReleases :many
SELECT * FROM releases WHERE state='waiting for approval' ORDER BY created_at;

-- name: CancelWaitingReleasesForRemoval :many
UPDATE releases SET state='cancelled'
WHERE deployment_id=$1 AND state='waiting for approval'
RETURNING id;

-- name: RecordRollback :execrows
UPDATE releases SET rollback_of=sqlc.arg(rollback_of), restore_backup_id=sqlc.arg(restore_backup_id) WHERE id=sqlc.arg(id);

-- name: ReleaseByID :one
SELECT * FROM releases WHERE id=sqlc.arg(id);

-- name: BundlesForProject :many
SELECT * FROM release_bundles WHERE project_id=sqlc.arg(project_id) ORDER BY id DESC;

-- name: EarlierReleaseForBundle :one
-- An earlier release of this bundle, when the deployment's latest released
-- release runs another bundle: deploying it goes back to that release.
-- Releasing the bundle already running again is not a return to anything.
SELECT r.* FROM releases r WHERE r.deployment_id=sqlc.arg(deployment_id) AND r.bundle_id=sqlc.arg(bundle_id) AND r.state='released'
AND (SELECT latest.bundle_id FROM releases latest WHERE latest.deployment_id=r.deployment_id AND latest.state='released' ORDER BY latest.number DESC LIMIT 1) IS DISTINCT FROM r.bundle_id
ORDER BY r.number DESC LIMIT 1;

-- name: SetRuntimeState :execrows
UPDATE deployments SET runtime_state=sqlc.arg(state), runtime_actor=sqlc.arg(actor), runtime_at=sqlc.narg(at), runtime_error=sqlc.arg(error) WHERE id=sqlc.arg(id);

-- name: MarkDeploymentAct :execrows
UPDATE deployments SET act_at=GREATEST(act_at, sqlc.arg(at)::timestamptz) WHERE id=sqlc.arg(id);

-- name: ReleaseRecoveryProgress :one
-- Recovery observes an existing effect; its old execution milestone is history.
SELECT EXISTS (
 SELECT 1 FROM deployment_attempts a
 JOIN deployment_attempt_inputs i ON i.attempt_id=a.id
 WHERE i.release_id=$1 AND (a.status <> 'running' OR jsonb_exists(a.observed_json, '_recovery'))
)::boolean AS uncertain, EXISTS (
 SELECT 1 FROM deployment_attempts a
 JOIN deployment_attempt_inputs i ON i.attempt_id=a.id
 WHERE i.release_id=$1 AND jsonb_exists(a.observed_json, '_restart')
)::boolean AS interrupted_by_restart;

-- name: LatestDeploymentRelease :one
SELECT r.number, r.stage, r.state, r.stage_finished_at,
 COALESCE((SELECT e.text FROM release_events e WHERE e.release_id=r.id ORDER BY e.at DESC, e.id DESC LIMIT 1), '')::text AS failure
FROM releases r WHERE r.deployment_id=$1 ORDER BY (r.state='releasing') DESC, r.number DESC LIMIT 1;

-- name: ClaimRestartRetry :execrows
UPDATE deployment_attempts
SET observed_json = observed_json || jsonb_build_object('_restartRetry', sqlc.arg(at)::timestamptz)
WHERE id=sqlc.arg(id) AND NOT jsonb_exists(observed_json, '_restartRetry');
-- name: RetentionBundles :many
SELECT * FROM release_bundles ORDER BY created_at DESC,id DESC;

-- name: RetentionReleases :many
SELECT * FROM releases ORDER BY number DESC;

-- name: RetentionRunningBundles :many
SELECT i.bundle_id FROM deployments d
JOIN deployment_attempt_inputs i ON i.attempt_id=d.serving_attempt_id
WHERE i.bundle_id IS NOT NULL;

-- name: AppPlatformReleaseInFlight :one
SELECT EXISTS(SELECT 1 FROM release_requests q JOIN releases r ON r.id=q.release_id JOIN targets t ON t.id=q.target_id WHERE t.kind='do-app' AND r.state='releasing');

-- name: ReplaceRebuiltReleaseBundle :execrows
UPDATE releases SET bundle_id=sqlc.arg(bundle_id),notes=sqlc.arg(notes),state=sqlc.arg(state),approval_json=NULL,approval_role=sqlc.arg(approval_role),rebuilt_from=sqlc.arg(rebuilt_from),rebuilt_commit=sqlc.arg(rebuilt_commit)
WHERE id=sqlc.arg(id) AND state='releasing' AND bundle_id=sqlc.arg(original_bundle_id);

-- name: ResetRebuiltReleaseClaim :execrows
UPDATE release_requests SET claimed_at=NULL,rebuild_pending=false WHERE release_id=sqlc.arg(release_id);

-- name: MarkReleaseRebuilding :execrows
UPDATE release_requests SET rebuild_pending=true WHERE release_id=sqlc.arg(release_id);

-- name: RetainsAutomatedWork :one
SELECT EXISTS(SELECT 1 WHERE EXISTS(SELECT 1 FROM releases retained0 WHERE retained0.run_id=ANY(sqlc.arg(run_ids)::uuid[])) OR EXISTS(SELECT 1 FROM release_changes retained1 WHERE retained1.change_id=sqlc.arg(change_id)) OR EXISTS(SELECT 1 FROM deployments retained2 WHERE retained2.change_id=sqlc.arg(change_id)) OR EXISTS(SELECT 1 FROM release_bundles retained3 WHERE retained3.run_id=ANY(sqlc.arg(run_ids)::uuid[])));

-- name: JanitorReleaseLedger :many
SELECT r.id, r.number, r.state, r.bundle_id, b.created_at AS bundle_created_at
FROM releases r LEFT JOIN release_bundles b ON b.id=r.bundle_id
WHERE r.deployment_id=$1 ORDER BY r.number DESC;

-- name: SetRoutineUpdates :execrows
UPDATE deployments SET routine_updates=sqlc.arg(routine_updates), routine_updates_revision=routine_updates_revision+1 WHERE id=sqlc.arg(id) AND routine_updates_revision=sqlc.arg(revision);

-- name: MarkRoutineRelease :execrows
UPDATE release_requests SET routine=true WHERE release_id=$1;

-- name: WaitRoutineRelease :execrows
UPDATE releases SET state='waiting for approval' WHERE id=$1 AND state='releasing';

-- name: RefreshRoutineGeneration :execrows
UPDATE release_requests SET base_generation=$2 WHERE release_id=$1 AND routine AND claimed_at IS NULL;

-- name: JanitorDeployments :many
SELECT * FROM deployments WHERE project_id=ANY(sqlc.arg(project_ids)::uuid[]) AND kind='standing' AND feed='main' ORDER BY project_id,id;

-- name: JanitorChangeReleases :many
SELECT rc.change_id,r.project_id,r.number,r.state,d.name AS deployment,d.display_label
FROM release_changes rc JOIN releases r ON r.id=rc.release_id
JOIN deployments d ON d.id=r.deployment_id
WHERE r.project_id=ANY(sqlc.arg(project_ids)::uuid[]) AND rc.change_id=ANY(sqlc.arg(change_ids)::uuid[])
ORDER BY r.number DESC;
