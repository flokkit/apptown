# Package and container registries

Integration stores package-registry bindings and project container-registry
attachments. Agentenv translates supported package formats into helper environment
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
(`apptown-cache-<slug>`) for its steps and checks, and each chat's own
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

A container-registry attachment can provide scoped credentials for image operations.
The linked GitLab adapter can provision a project deploy token. Any other
attachment either holds its own `username` and `token`, or holds only its
`repo` and is written with its connector's credential: the connector's
`username` and the secret its `tokenFrom` names, read from the secrets store
when used and never copied into the attachment. A named secret the store does
not hold is an error, not an anonymous registry. On an AppTown with a worker, a
landing whose project has no container registry attaches the one
container-registry connector AppTown has (a GitLab one by provisioning,
any other as `<url>/<group>/<slug>`); with none or several it is refused,
naming the choice. Detaching stops
Forge contributions but does not itself revoke that provider-side token. Treat
staged client authentication as a credential available to its runtime, even when
it is not an environment variable.

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
registry credential. AppTown pushes each image with the attachment's
credential and reads it back by digest, and only the registry's answer is
recorded. Targets pull those digests and build nothing: Docker hosts with a
per-operation login, Portainer by naming each digest in the stack, and App
Platform from DigitalOcean's registry (DOCR), into which AppTown copies each
digest. That DOCR repository, one per project and connector, is recorded as the
deployment's own and emptied when the project's last App Platform deployment
on that connector is taken down. A bucket website reads the site's files out
of the release image. Forge never deletes from the project's own registry; its
retention decides how long a release can be redeployed. Without a worker, a
landing's deploy builds on the Docker host and records image IDs, with no
registry. An intranet release (`apptown release`) of a landing without published
digests, with or without a worker as long as AppTown has local Docker, builds
a build or static contract on AppTown's Docker and pushes to the project's container registry, attaching
one first if needed (`deployment/intranet_release.go`). See
[deployment targets](deploy-targets.md#where-a-release-comes-from) and
[workers](../operations/workers.md#releases-built-on-the-worker).

## Implementation references

- [internal/forge/integration/registry_bindings.go](../../internal/forge/integration/registry_bindings.go)
- [internal/forge/integration/registry_integration.go](../../internal/forge/integration/registry_integration.go)
- [internal/forge/agentenv/registries.go](../../internal/forge/agentenv/registries.go)
- [internal/forge/agentenv/workshop.go](../../internal/forge/agentenv/workshop.go)
- [internal/forge/deployment/release_publication.go](../../internal/forge/deployment/release_publication.go)
