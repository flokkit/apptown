CREATE TABLE IF NOT EXISTS releases (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  number       BIGINT NOT NULL,
  state        TEXT NOT NULL DEFAULT 'released',
  deployment  TEXT NOT NULL DEFAULT 'prod',
  external_ref TEXT NOT NULL DEFAULT '',
  approved_by  TEXT NOT NULL DEFAULT '',
  run_id UUID,
  created_at   TIMESTAMPTZ NOT NULL,
  released_at  TIMESTAMPTZ,
  notes        TEXT NOT NULL DEFAULT '',
  commit_sha   TEXT NOT NULL DEFAULT '',
  source_sha   TEXT NOT NULL DEFAULT '',
  bundle_id UUID, -- §2.8: what shipped
  deployment_id UUID, -- §2.8: where
  generation     BIGINT NOT NULL DEFAULT 0,   -- the deployment's generation this release is
  artifact_hash  TEXT NOT NULL DEFAULT '',    -- the rendered artifact's hash
  UNIQUE(project_id, number)
);
CREATE TABLE IF NOT EXISTS release_changes (
  release_id UUID NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  change_id UUID NOT NULL REFERENCES changes(id) ON DELETE CASCADE,
  PRIMARY KEY (release_id, change_id)
);
CREATE TABLE IF NOT EXISTS release_events (
  id UUID PRIMARY KEY,
  release_id UUID NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  at         TIMESTAMPTZ NOT NULL,
  text       TEXT NOT NULL
);
-- A deployment (design/subsystems/deployment.md §2.3): identity, a kind, a binding
-- to a target, the target-specific handle the driver owns, the desired and
-- observed deployment generations, and the four attributes that carry the
-- nuance (§2.3a; protected is derived in Go). A preview row is named
-- preview-<change> so (project, name) stays the one key. host_port and
-- container are the built-in Docker host's handle, kept as columns because
-- the boot path reads them; other kinds keep theirs in handle_json.
CREATE TABLE IF NOT EXISTS deployments (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  -- The name the deployment carries on its host (forge.environment labels,
  -- boot proofs): its old integer for a deployment made before the identity
  -- cutover, kept because its containers carry it; '' for one made after,
  -- which uses its UUID.
  host_label TEXT NOT NULL DEFAULT '',
  host_port  BIGINT NOT NULL DEFAULT 0,
  container  TEXT NOT NULL DEFAULT '',
  release_run UUID,
  updated_at TIMESTAMPTZ NOT NULL,
  release_sha  TEXT NOT NULL DEFAULT '', -- the commit the serving release was built from
  image_digest TEXT NOT NULL DEFAULT '', -- the image id it actually runs (artifact identity)
  kind         TEXT NOT NULL DEFAULT 'standing',   -- standing | preview | restored-copy
  change_id UUID, -- previews only
  target_id UUID,
  handle_json  JSONB NOT NULL DEFAULT '{}'::jsonb,
  desired_generation  BIGINT NOT NULL DEFAULT 0,
  observed_generation BIGINT NOT NULL DEFAULT 0,
  readiness    TEXT NOT NULL DEFAULT 'reconciled', -- reconciled | needs-reconcile | reconciling | failed
  feed         TEXT NOT NULL DEFAULT 'landing',    -- landing | promotion | schedule | manual
  audience     TEXT NOT NULL DEFAULT 'team',       -- team | organisation | partners | public
  data_class   TEXT NOT NULL DEFAULT 'synthetic',  -- synthetic | non-production | production
  promote_after TEXT NOT NULL DEFAULT '',
  url          TEXT NOT NULL DEFAULT '',
  expires_at   TIMESTAMPTZ,
  -- A deployment started from a copy: the set its first release fills its
  -- data from, and when that was done (null until then).
  start_from_backup UUID,
  started_from_at   TIMESTAMPTZ,
  -- What its names on a Docker host or Portainer environment begin with,
  -- fixed when it is made: 'forge-' for one made before 1.0, which keeps
  -- them for good, so nothing it made is looked up under another name.
  host_prefix TEXT NOT NULL DEFAULT 'apptown-',
  UNIQUE(project_id, name)
);
-- A target (design/subsystems/deployment.md §2.2): a reusable place AppTown can
-- deploy to, reached through one provider (an integration name, '' for
-- the built-in host). Absent by default except the built-in Docker host.
CREATE TABLE IF NOT EXISTS targets (
  id UUID PRIMARY KEY,
  name              TEXT NOT NULL UNIQUE,
  kind              TEXT NOT NULL,                 -- docker-host | portainer | do-app | ...
  provider          TEXT NOT NULL DEFAULT '',
  address_json      JSONB NOT NULL DEFAULT '{}'::jsonb,
  origin            TEXT NOT NULL DEFAULT 'attached', -- built-in | attached | provisioned
  capabilities_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  ingress_json      JSONB NOT NULL DEFAULT '{}'::jsonb,
  needs_mode        TEXT NOT NULL DEFAULT 'emulators', -- emulators | managed | register
  secrets_delivery  TEXT NOT NULL DEFAULT 'inject',
  status            TEXT NOT NULL DEFAULT 'ready',
  created_at        TIMESTAMPTZ NOT NULL,
  updated_at        TIMESTAMPTZ NOT NULL,
  -- Advances when the capabilities, ingress or needs mode change, never with
  -- a status check, so an edit started from an older reading is refused
  -- without routine checks causing conflicts.
  settings_revision BIGINT NOT NULL DEFAULT 0
);
-- A release bundle (§2.8): the immutable thing that ships, committed once
-- by a landing's deploy step attempt.
CREATE TABLE IF NOT EXISTS release_bundles (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  contract_revision TEXT NOT NULL,                  -- the tree sha
  contract_hash     TEXT NOT NULL DEFAULT '',       -- sha256 of the contract bytes
  parts_json        JSONB NOT NULL DEFAULT '{}'::jsonb, -- part -> image id, or <registry>@sha256:<hex>
  init_json         JSONB NOT NULL DEFAULT '{}'::jsonb, -- init step -> image id, or <registry>@sha256:<hex>
  built_from_json   JSONB NOT NULL DEFAULT '{}'::jsonb, -- change, run, tree
  created_at        TIMESTAMPTZ NOT NULL,
  -- the target whose daemon the image IDs were read from; 0 for a bundle
  -- recorded before releases named their target, and for a digest bundle
  target_id UUID,
  -- A digest bundle's repository, every image of it by digest there; ''
  -- for a bundle of daemon image IDs.
  registry          TEXT NOT NULL DEFAULT '',
  platform          TEXT NOT NULL DEFAULT '',             -- os/arch, a digest bundle's
  built_on_json     JSONB NOT NULL DEFAULT '{}'::jsonb,   -- where a digest bundle was built
  -- The deploy step attempt that committed the bundle; 0 for bundles
  -- committed before attempts were recorded.
  run_id UUID,
  step_id UUID,
  attempt           BIGINT NOT NULL DEFAULT 0,
  CONSTRAINT release_bundle_form CHECK (registry = '' OR (platform <> '' AND target_id IS NULL AND attempt > 0))
);
-- One bundle per deploy step attempt.
CREATE UNIQUE INDEX IF NOT EXISTS release_bundles_attempt ON release_bundles(step_id, attempt) WHERE attempt > 0;
-- One attempt to bring a deployment to a generation (§2.4): the
-- idempotency key makes a retry the same attempt.
CREATE TABLE IF NOT EXISTS deployment_attempts (
  id UUID PRIMARY KEY,
  deployment_id UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  generation      BIGINT NOT NULL,
  idempotency_key TEXT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'running',  -- running | uncertain | done | failed | superseded
  terminal        BOOLEAN NOT NULL DEFAULT false,
  observed_json   JSONB NOT NULL DEFAULT '{}'::jsonb,
  superseded_by UUID,
  started_at      TIMESTAMPTZ NOT NULL,
  finished_at     TIMESTAMPTZ,
  UNIQUE(deployment_id, idempotency_key)
);
-- Immutable, non-secret inputs required by deployment execution. Migration 62
-- converts old release-linked facts once; missing inputs remain unresolved.
CREATE TABLE IF NOT EXISTS deployment_attempt_inputs (
  attempt_id UUID PRIMARY KEY REFERENCES deployment_attempts(id) ON DELETE CASCADE,
  release_id UUID,
  release_run UUID,
  revision      TEXT NOT NULL DEFAULT '',
  image_digest  TEXT NOT NULL DEFAULT '',
  artifact_hash TEXT NOT NULL DEFAULT '',
  docker_source TEXT NOT NULL DEFAULT '',
  rebuild BOOLEAN NOT NULL DEFAULT false,
  bundle_id UUID,
  -- What the generation applied of the deployment's profile and
  -- override (deployment.Applied): its profile, each part's resources and
  -- the environment the profile changed, and what each part runs on. A
  -- rebuild reapplies it; '{}' is a generation from before it was kept.
  applied JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_deployment_inputs_release ON deployment_attempt_inputs(release_id) WHERE release_id IS NOT NULL;
-- One need of one project fulfilled in one deployment (§2.5).
CREATE TABLE IF NOT EXISTS need_instances (
  id UUID PRIMARY KEY,
  deployment_id UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  need             TEXT NOT NULL,
  mode             TEXT NOT NULL DEFAULT 'managed',  -- emulator | managed | register
  binding_json     JSONB NOT NULL DEFAULT '{}'::jsonb,
  secret_refs_json JSONB NOT NULL DEFAULT '[]'::jsonb,
  handle_json      JSONB NOT NULL DEFAULT '{}'::jsonb,
  status           TEXT NOT NULL DEFAULT 'pending',  -- pending | ready | failed | orphaned
  updated_at       TIMESTAMPTZ NOT NULL,
  UNIQUE(deployment_id, need)
);
-- A deployment reconcile (§4.1) and its receipt.
CREATE TABLE IF NOT EXISTS reconciliations (
  id UUID PRIMARY KEY,
  deployment_id UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  kind           TEXT NOT NULL DEFAULT 'reconcile',
  status         TEXT NOT NULL DEFAULT 'running',   -- running | done | failed
  receipt_json   JSONB NOT NULL DEFAULT '{}'::jsonb,
  actor          TEXT NOT NULL DEFAULT '',
  -- The starter's principal and way of acting (access.Actor); NULL for AppTown, or one not resolved to a person.
  actor_principal UUID,
  actor_via TEXT NOT NULL DEFAULT '',
  started_at     TIMESTAMPTZ NOT NULL,
  finished_at    TIMESTAMPTZ
);
-- What AppTown created for a deployment at its target, recorded when the
-- provider confirmed the creation (design/subsystems/deployment.md).
-- Take-down deletes exactly these; nothing found, reused or adopted is
-- listed. note is '' for a creation AppTown saw, and says who approved
-- an entry recorded later by the one-time inspection.
CREATE TABLE IF NOT EXISTS deployment_owned (
  id UUID PRIMARY KEY,
  deployment_id UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  kind          TEXT NOT NULL,
  ident         TEXT NOT NULL,
  name          TEXT NOT NULL DEFAULT '',
  holds_data    BOOLEAN NOT NULL DEFAULT false,
  need          TEXT NOT NULL DEFAULT '',
  recorded_at   TIMESTAMPTZ NOT NULL,
  note          TEXT NOT NULL DEFAULT '',
  UNIQUE(deployment_id, kind, ident)
);
-- An image repository AppTown created for a project on a provider's
-- account (its connector): App Platform's DOCR repository, named after the
-- project. AppTown uses only one it recorded creating, and empties it
-- when the project's last deployment on that account is taken down. note
-- says who approved an entry the one-time inspection recorded.
CREATE TABLE IF NOT EXISTS owned_repositories (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  connector   TEXT NOT NULL,
  registry    TEXT NOT NULL,
  repository  TEXT NOT NULL,
  recorded_at TIMESTAMPTZ NOT NULL,
  note        TEXT NOT NULL DEFAULT '',
  UNIQUE(connector, registry, repository)
);
-- An offer (§15.7): this target is available to this group, or to
-- everyone ('*'), within these caps. Built-in targets have no rows; they
-- are offered to everyone by construction.
CREATE TABLE IF NOT EXISTS target_offers (
  id UUID PRIMARY KEY,
  target_id UUID NOT NULL REFERENCES targets(id) ON DELETE CASCADE,
  group_name TEXT NOT NULL,
  caps_json  JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_by TEXT NOT NULL DEFAULT '',
  -- The offerer's principal and way of acting (access.Actor); NULL when not resolved to a person.
  created_by_principal UUID,
  created_by_via TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL,
  UNIQUE(target_id, group_name)
);
-- A data-bearing resource AppTown made for a deployment and kept when
-- the deployment went (§2.6: AppTown never deletes what holds data).
-- Ownership outlives the deployment row: who made it, for what, where.
CREATE TABLE IF NOT EXISTS retained_resources (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL,
  deployment  TEXT NOT NULL,
  target_id UUID,
  need         TEXT NOT NULL,
  handle_json  JSONB NOT NULL DEFAULT '{}'::jsonb,   -- the provider's identity and what was requested
  retained_by  TEXT NOT NULL DEFAULT '',
  -- The retainer's principal and way of acting (access.Actor); NULL for AppTown, or one not resolved to a person.
  retained_by_principal UUID,
  retained_by_via TEXT NOT NULL DEFAULT '',
  retained_at  TIMESTAMPTZ NOT NULL,
  disposition  TEXT NOT NULL DEFAULT 'retained',    -- retained | released | kept
  note         TEXT NOT NULL DEFAULT '',
  settled_by   TEXT NOT NULL DEFAULT '',
  -- The settler's principal and way of acting (access.Actor); NULL unless settled by a resolved person.
  settled_by_principal UUID,
  settled_by_via TEXT NOT NULL DEFAULT '',
  settled_at   TIMESTAMPTZ,
  holds_data   BOOLEAN NOT NULL DEFAULT false            -- data, which blocks the deployment's name until released
);

-- Accepted deployment placement and one preparation claim. The release owns
-- bundle/revision/actor; the queue owns scheduling, not this immutable selection.
CREATE TABLE IF NOT EXISTS release_requests (
  release_id UUID PRIMARY KEY REFERENCES releases(id) ON DELETE CASCADE,
  target_id UUID NOT NULL,
  base_generation BIGINT NOT NULL,
  attributes_json JSONB NOT NULL,
  claimed_at TIMESTAMPTZ,
  -- The copy the release's caller was authorised to fill a new deployment
  -- from (0: none); the release fills that copy or nothing.
  start_from_backup UUID
);
-- An operator's override of one part's resources on one deployment
-- (design/subsystems/deployment.md): set as a recorded act,
-- applied over the deployment's profile at each deploy until cleared by
-- an act or by a deploy whose committed profile declares the same values.
CREATE TABLE IF NOT EXISTS deployment_overrides (
  deployment_id UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  part TEXT NOT NULL,
  memory_bytes BIGINT NOT NULL DEFAULT 0,       -- 0: the override leaves memory as the profile says
  cpus DOUBLE PRECISION NOT NULL DEFAULT 0,      -- 0: the override leaves cpu as the profile says
  reason TEXT NOT NULL,
  set_by TEXT NOT NULL DEFAULT '',
  -- The setter's principal and way of acting (access.Actor); NULL when not resolved to a person.
  set_by_principal UUID,
  set_by_via TEXT NOT NULL DEFAULT '',
  set_at TIMESTAMPTZ NOT NULL,
  memory_unlimited BOOLEAN NOT NULL DEFAULT false, -- explicit operator no-limit choice
  PRIMARY KEY(deployment_id, part)
);

-- What a standing deployment's containers did, as its Docker daemon
-- reported it (docker events): killed out of memory, or exited without
-- anyone stopping them. Observations, not acts (a crash-loop hold is the
-- AppTown's act, with its receipt); read back per deployment and part, kept 30 days. One row per
-- container, kind and moment, so reading a window again records nothing
-- twice.
CREATE TABLE IF NOT EXISTS deployment_limit_events (
  id UUID PRIMARY KEY,
  deployment_id UUID NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  part TEXT NOT NULL,                       -- the part or need, by name
  kind TEXT NOT NULL CONSTRAINT deployment_limit_event_kind CHECK(kind IN ('out-of-memory','exited')),
  container TEXT NOT NULL,
  at TIMESTAMPTZ NOT NULL,                  -- the daemon's time for it
  exit_code INTEGER,                        -- an exit's code; NULL otherwise
  generation BIGINT,                        -- the generation it happened under; NULL when not known
  UNIQUE(deployment_id, container, kind, at)
);
CREATE INDEX IF NOT EXISTS deployment_limit_events_at ON deployment_limit_events(deployment_id, at);
