# Package and container registries

Integration stores package-registry bindings and the instance hub registry. Agentenv translates supported package formats into helper environment
settings and configuration files. Docker preparation supplies its daemon/build
configuration separately.

A project's `mise.toml` is installed before an agent session or script step
whether or not any registry is bound: Agentenv always generates mise's system
layer (precompiled interpreters only, `ubi` off), a per-workspace install folder
and its shims on `PATH`. Bindings only add routing to it. Every download is
tried up to three more times before the install fails (`http_retries`). Maven's
own tarball, which mise fetches from archive.apache.org (it throttles and refuses
connections), comes from Maven Central instead, or from the bound Maven
repository; a bound Apache dist mirror keeps the route.

mise's download cache (`MISE_CACHE_DIR`, at `execution.ToolCacheMount`) is
shared, so containers reuse the release listings and archives an earlier one
fetched instead of asking for them per workspace; each workspace still
installs from it into its own folder. On a worker it is one folder mounted
into every task, check and chat container
([tool_cache.go](../../internal/worker/tool_cache.go)). On an AppTown's own Docker
the same path lies inside a cache volume: the project's
(`apptown-project-cache-<project UUID>`) for its steps and checks, and each chat's own
(`apptown-cache-console-<session>`) for chats.

Accepted limit for now: an AppTown's own Docker downloads again for each chat
what an earlier chat fetched, and with no mirror bound mise asks GitHub's API
for a release list (and checks the download's attestation) whenever its cache
does not answer. Unauthenticated, that API allows 60 requests an hour per
address, so a busy machine can run out and an install fail with a rate-limit
error. A token (`MISE_GITHUB_TOKEN` or `GITHUB_TOKEN`) raises the limit, but it
is a credential and none is configured; turning attestation checks off would
remove one call but not the release listing, and weakens what an install
proves. A bound GitHub releases mirror removes the dependence.

Package mirrors route downloads. They are not an outbound network allowlist.
Nested builds and containers do not automatically inherit the helper's routing;
required settings must be passed explicitly. A failed download does not by itself
prove that a security policy denied it.

One instance hub connector supplies release credentials. Its `url`, `group`
and project slug derive the repository. Its username and `tokenFrom` resolve
through the secrets store when used, and a missing named secret is an error.
Eval copies receive no general registry credential. Publication and deployment
of their own bundles use only their own derived repository. GitLab per-project
provisioning and project registry attachments are unavailable.

Implementation steps, application deployment and chats have different grants.
Private Docker in a change chat does not automatically confer registry push access.
See [runtime environments](../../stock/docs/guides/runtime-environments.md) for
those differences and [integrations](integration-runtime.md) for record ownership.

Generic mirroring of every ecosystem and enforced egress restrictions are outside
the implemented registry contract.

## Release-build registries

On an AppTown with a worker, a landing's release images are built once on the
worker and published to the project's container registry; the bundle records
each image as `<repository>@sha256:<digest>`. The worker build carries no
registry credential. AppTown pushes each image with the hub's
credential and reads it back by digest, and only the registry's answer is
recorded. Targets pull those digests and build nothing: Docker hosts with a
per-operation login, Portainer by naming each digest in the stack, and App
Platform from DigitalOcean's registry (DOCR), into which AppTown copies each
digest. That DOCR repository, one per project and connector, is recorded as the
deployment's own and emptied when the project's last App Platform deployment
on that connector is taken down. A bucket website reads the site's files out
of the release image. Deployment applies the hub's retention policy daily;
its operator manages blob collection. Without a worker, a landing builds images
on its build host without booting them and publishes to the hub when configured.
With no hub it records image IDs on that host. Portainer and bucket websites
require digest bundles. A confirmed missing original image is rebuilt from its
recorded commit through a new build run; a registry refusal or outage is an
error. An agent
step's private Docker is logged in to no registry, with or without a worker. See
[deployment targets](deploy-targets.md#where-a-release-comes-from) and
[workers](../operations/workers.md#releases-built-on-the-worker).

## Implementation references

- [internal/apptown/integration/registry_bindings.go](../../internal/apptown/integration/registry_bindings.go)
- [internal/apptown/integration/registry_integration.go](../../internal/apptown/integration/registry_integration.go)
- [internal/apptown/agentenv/registries.go](../../internal/apptown/agentenv/registries.go)
- [internal/apptown/agentenv/workshop.go](../../internal/apptown/agentenv/workshop.go)
- [internal/apptown/deployment/release_publication.go](../../internal/apptown/deployment/release_publication.go)

Project package caches belong to the immutable project UUID. Completed project
cleanup retires that exact local volume; a failed removal keeps cleanup pending.
New work never adopts the old `apptown-cache-<slug>` or shared `apptown-cache`
volumes. These disposable legacy caches can be removed explicitly by an operator
after old work has settled and no containers use them. Exclude the separately
owned `apptown-cache-console-*` volumes. Neither a reused slug nor
its deletion proves ownership of their contents. Console and worker tool caches
keep their separate lifecycles.

Already-admitted tasks retain their saved request and cache mount. An older task
without a kept request can be refused if reconstruction changes its admitted
digest; cache upgrades do not rewrite requests or silently execute changed work.

## Release image retention

Container images use one instance hub connector (`container-registry`), with
repositories derived from its host, prefix and project slug. Project registry
attachments and GitLab per-project provisioning are unavailable. Migration 223
refuses multiple or GitLab connectors before deleting attachment rows.

Deployment owns the daily `image-retention` pass. It reads its release, bundle
and request records and Data protection's kept-backup release IDs before deleting
anything. The hub defaults to 90 days and the newest 10 bundles per project; it
also retains every spoke keep set and in-flight selection. Driver stores combine
keep sets by physical store identity; only recorded expired references and owned
landing build tags can be removed. The pass writes one `images.retained` audit
record, including unknown reads and refused deletions. The hub operator manages
blob collection. The [image guide](../../stock/docs/guides/images.md) states the
policy and target-specific limits.
