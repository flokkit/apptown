-- Record reads, grouped by table.

-- name: DeploymentByID :one
SELECT * FROM deployments WHERE id=$1;

-- name: DeploymentByName :one
SELECT * FROM deployments WHERE project_id=$1 AND name=$2;

-- A restored copy is not one of the project's deployments: it is shown on
-- its source's card only (RestoredCopiesOf), never in listings, health,
-- conformance or the sweeps that read these.
-- name: DeploymentsForProject :many
SELECT * FROM deployments WHERE project_id=$1 AND kind<>'restored-copy' ORDER BY kind DESC,id;

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
SELECT * FROM releases WHERE project_id=sqlc.arg(project_id) ORDER BY number DESC LIMIT sqlc.arg(row_limit)::bigint;

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

-- name: CompletedAttempt :one
SELECT * FROM deployment_attempts WHERE deployment_id=sqlc.arg(deployment_id) AND generation=sqlc.arg(generation) AND status=sqlc.arg(status);

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
INSERT INTO deployments (id, project_id, name, kind, target_id, feed, audience, data_class, promote_after, host_port, updated_at)
  VALUES (sqlc.arg(id), sqlc.arg(project_id),sqlc.arg(name),sqlc.arg(kind),sqlc.arg(target_id),sqlc.arg(feed),sqlc.arg(audience),sqlc.arg(data_class),sqlc.arg(promote_after),0,sqlc.arg(updated_at))
  ON CONFLICT (project_id, name) DO UPDATE SET target_id=excluded.target_id, feed=excluded.feed, audience=excluded.audience,
  data_class=excluded.data_class, promote_after=excluded.promote_after, updated_at=excluded.updated_at
  WHERE (deployments.desired_generation=0 AND deployments.observed_generation=0)
  OR (deployments.target_id=excluded.target_id AND
  (deployments.audience IN ('partners','public') OR deployments.data_class='production'
  OR (excluded.audience NOT IN ('partners','public') AND excluded.data_class!='production'))) RETURNING *;

-- name: RecordPreview :one
INSERT INTO deployments (id, project_id, name, kind, change_id, target_id, handle_json, host_port, container, url, feed, audience, data_class, observed_generation, desired_generation, updated_at)
  VALUES (sqlc.arg(id), sqlc.arg(project_id),sqlc.arg(name),sqlc.arg(kind),sqlc.arg(change_id),sqlc.arg(target_id),sqlc.arg(handle_json),sqlc.arg(host_port),sqlc.arg(container),sqlc.arg(url),'landing','team','synthetic',1,1,sqlc.arg(updated_at))
  ON CONFLICT (project_id, name) DO UPDATE SET handle_json=excluded.handle_json, host_port=excluded.host_port, container=excluded.container,
  url=excluded.url, desired_generation=deployments.desired_generation+1, observed_generation=deployments.desired_generation+1, updated_at=excluded.updated_at RETURNING *;

-- name: PutTarget :one
INSERT INTO targets (id, name, kind, provider, address_json, origin, capabilities_json, ingress_json, needs_mode, secrets_delivery, status, created_at, updated_at)
  VALUES (sqlc.arg(id), sqlc.arg(name),sqlc.arg(kind),sqlc.arg(provider),sqlc.arg(address_json),sqlc.arg(origin),sqlc.arg(capabilities_json),sqlc.arg(ingress_json),sqlc.arg(needs_mode),sqlc.arg(secrets_delivery),sqlc.arg(status),sqlc.arg(created_at),sqlc.arg(updated_at))
  ON CONFLICT (name) DO UPDATE SET kind=excluded.kind, provider=excluded.provider, address_json=excluded.address_json,
  capabilities_json=excluded.capabilities_json, ingress_json=excluded.ingress_json, needs_mode=excluded.needs_mode,
  secrets_delivery=excluded.secrets_delivery,
  status=CASE WHEN targets.status=sqlc.arg(retired_state) THEN targets.status ELSE excluded.status END, updated_at=excluded.updated_at,
  settings_revision=targets.settings_revision + CASE WHEN (targets.capabilities_json, targets.ingress_json, targets.needs_mode)
    IS DISTINCT FROM (excluded.capabilities_json, excluded.ingress_json, excluded.needs_mode) THEN 1 ELSE 0 END
  RETURNING *;

-- name: RefreshTarget :one
UPDATE targets SET kind=sqlc.arg(kind), provider=sqlc.arg(provider), address_json=sqlc.arg(address_json), capabilities_json=sqlc.arg(capabilities_json),
  ingress_json=sqlc.arg(ingress_json), needs_mode=sqlc.arg(needs_mode), secrets_delivery=sqlc.arg(secrets_delivery),
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
UPDATE targets SET status=sqlc.arg(status), updated_at=sqlc.arg(updated_at) WHERE id=sqlc.arg(id) AND status<>sqlc.arg(retired_state) RETURNING *;

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
INSERT INTO owned_repositories (id, project_id, connector, registry, repository, recorded_at, note)
  VALUES (sqlc.arg(id), sqlc.arg(project_id),sqlc.arg(connector),sqlc.arg(registry),sqlc.arg(repository),sqlc.arg(recorded_at),sqlc.arg(note))
  ON CONFLICT (connector, registry, repository) DO NOTHING;

-- name: RepositoryOwned :one
SELECT EXISTS(SELECT 1 FROM owned_repositories WHERE project_id=sqlc.arg(project_id) AND connector=sqlc.arg(connector) AND registry=sqlc.arg(registry) AND repository=sqlc.arg(repository));

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

-- name: InsertReconciliation :one
INSERT INTO reconciliations (id, deployment_id, kind, status, actor, actor_principal, actor_via, started_at) VALUES (sqlc.arg(id), sqlc.arg(deployment_id),'reconcile',sqlc.arg(status),sqlc.arg(actor),sqlc.arg(actor_principal),sqlc.arg(actor_via),sqlc.arg(started_at)) RETURNING *;

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

-- name: EndReconciliation :execrows
UPDATE reconciliations SET status=sqlc.arg(status), receipt_json=sqlc.arg(receipt_json), finished_at=sqlc.arg(finished_at) WHERE id=sqlc.arg(id) AND status=sqlc.arg(running_state);

-- name: SupersedeAttempts :execrows
UPDATE deployment_attempts SET status=sqlc.arg(status), terminal=true, superseded_by=sqlc.arg(superseded_by), finished_at=sqlc.arg(finished_at) WHERE deployment_id=sqlc.arg(deployment_id) AND status=sqlc.arg(running_state) AND id!=sqlc.arg(id);

-- name: AdmitGeneration :execrows
UPDATE deployments SET desired_generation=sqlc.arg(desired_generation), readiness=sqlc.arg(readiness), updated_at=sqlc.arg(updated_at) WHERE id=sqlc.arg(id);

-- name: FinishSupersededAttempt :execrows
UPDATE deployment_attempts SET status=sqlc.arg(status), terminal=sqlc.arg(terminal), finished_at=sqlc.arg(finished_at) WHERE id=sqlc.arg(id);

-- name: SetReadiness :execrows
UPDATE deployments SET readiness=sqlc.arg(readiness), updated_at=sqlc.arg(updated_at) WHERE id=sqlc.arg(id);

-- name: ObserveGeneration :execrows
UPDATE deployments SET observed_generation=sqlc.arg(observed_generation), handle_json=sqlc.arg(handle_json), url=sqlc.arg(url), container=sqlc.arg(container), host_port=sqlc.arg(host_port),
  release_run=sqlc.arg(release_run), release_sha=sqlc.arg(release_sha), image_digest=sqlc.arg(image_digest), readiness=sqlc.arg(readiness), updated_at=sqlc.arg(updated_at) WHERE id=sqlc.arg(id);

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
UPDATE releases r SET state=sqlc.arg(failed_state) WHERE state=sqlc.arg(open_state)
  AND NOT EXISTS(SELECT 1 FROM release_requests q WHERE q.release_id=r.id AND q.claimed_at IS NULL
    AND r.id=ANY(sqlc.arg(queued_release_ids)::uuid[]))
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
INSERT INTO release_requests(release_id,target_id,base_generation,attributes_json,start_from_backup)
VALUES ($1,$2,$3,$4,$5) RETURNING *;

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
SELECT sqlc.embed(d), p.slug FROM deployments d JOIN projects p ON p.id=d.project_id
WHERE d.kind<>'restored-copy' AND (sqlc.narg(project_ids)::uuid[] IS NULL OR d.project_id=ANY(sqlc.narg(project_ids)::uuid[]))
ORDER BY d.kind DESC,d.id;

-- name: InsertRestoredCopyDeployment :one
INSERT INTO deployments (id, project_id, name, kind, target_id, feed, audience, data_class, expires_at, start_from_backup, updated_at)
  VALUES (sqlc.arg(id), sqlc.arg(project_id), sqlc.arg(name), 'restored-copy', sqlc.arg(target_id), 'promotion', 'team', sqlc.arg(data_class), sqlc.arg(expires_at), sqlc.arg(backup_id), sqlc.arg(at))
  RETURNING *;

-- name: ExtendRestoredCopyDeployment :execrows
UPDATE deployments SET expires_at=sqlc.arg(expires_at) WHERE id=sqlc.arg(id) AND kind='restored-copy';

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
