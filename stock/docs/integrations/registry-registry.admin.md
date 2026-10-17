# Package registries & mirrors — admin guide

## What it's for

Builds pull thousands of packages, and the public
registries are slow, rate-limited and occasionally down or compromised.
Mirrors fetch each package once for the whole instance and serve it
from next door: faster builds, fewer mystery failures, and one place to
see (and if needed, block) what enters the building.

Forge routes all dependency and toolchain traffic through instance-local
mirrors. You stand up the mirrors; forge wires every container to them.

## 1. Stand up the proxy repositories

Any product that can proxy the upstream works (Sonatype Nexus, JFrog
Artifactory). Per ecosystem you want covered, create a proxy repo of the
upstream (immutable artifacts → never re-validate). The formats forge
understands, with typical upstreams:

| format | upstream | serves |
|---|---|---|
| go-proxy | proxy.golang.org | Go modules |
| npm | registry.npmjs.org | npm packages |
| pypi | pypi.org | uv/pip wheels + index |
| cargo | index.crates.io | crates (sparse index) |
| maven | repo.maven.apache.org | Maven/Gradle deps |
| github-releases | github.com | toolchain release assets (raw proxy) |
| node-dist / golang-dist | nodejs.org / dl.google.com | interpreter tarballs |
| apache-dist | archive.apache.org | maven tool etc. |
| rust-dist | static.rust-lang.org | rustup toolchains |
| docker-hub | registry-1.docker.io | image pulls for the private docker daemons in implementation steps |

Anonymous read is expected; if your proxy path strips auth headers
(recommended — some clients forward foreign credentials), do it at the
proxy edge.

## 2. Bind the formats in forge

Integrations → Package registries → New integration. One binding per
format: pick the format, paste the repo URL, Test (fetches a known-small
artifact through the mirror), Save. Presets exist for Nexus URL shapes.
Bind only what you have — unbound ecosystems simply go direct.

## 3. What forge does with the bindings

Per step/app container: injects the env vars (GOPROXY etc.), generates
client config at the SYSTEM layer (/etc/mise/config.toml, a forge-owned
CARGO_HOME config, a generated Maven settings.xml), records the bindings
used in step evidence, and renders the live inventory into agent
steering. Bindings point tools at the mirror; they do not block the
public upstreams — a build that can't reach a mirror falls back to the
real registry rather than failing. (Enforced egress containment is a
separate concern, not a per-binding flag.) The private docker
daemon each implementation step gets pulls Docker Hub images through
the `docker-hub` binding when one exists. You never configure a repo or
a workflow for any of this.

## 4. When the mirror itself is the problem

A proxy can serve its index (200) and refuse the downloads (403) — the
symptom of a product-side cap, not a forge bug. Sonatype Nexus
Community Edition stops caching new components once its usage ceiling
is hit; agents then report "the mirror 403s nearly every package" while
a handful of already-cached ones still work (complaint #37 on this
forge). Check the mirror's own status page before hunting in forge;
builds recover the moment the mirror serves again.

## 5. Container registries for releases

Package mirrors are what builds read. A container registry is where a
forge with a worker publishes each landing's release images: the worker
builds them, the forge pushes them to the project's container registry and
records each by digest, and every deployment target pulls those digests.
Nothing is built on a target. This needs the forge started with
`-private-docker`.

Offer one container-registry connector under Integrations: a project with
no registry is given it at its first landing, recorded as an attachment.
With several connectors, attach each project to one yourself; with none,
the landing is refused before anything happens, naming what to do. The
connector's credential (the name of a forge secret, or for a GitLab
connector a deploy token it makes for each project) must be able to push
and pull every project's repository. Every target must be able to
reach the registry and trust its TLS certificate. The forge does not delete
release images from the project's container registry; the registry's own
retention decides how long a release can be
deployed again. The repository's `design/operations/workers.md` ("Releases
built on the worker") has the full procedure.
