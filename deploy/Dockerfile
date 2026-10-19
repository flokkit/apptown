# The forge's own images: the forge daemon (target "server", published as
# apptown) and the worker (target "worker"), laid out as the release archive
# is (scripts/release.sh). Build from the repository root;
# Dockerfile.dockerignore keeps the context to what the builds read:
#
#   docker build -f deploy/Dockerfile --target server --build-arg VERSION=v1.0.0 -t forge .
#   docker build -f deploy/Dockerfile --target worker --build-arg VERSION=v1.0.0 -t forge-worker .
#
# The agent image is deploy/forge-agent/Dockerfile, built from that folder.
# GitHub's images.yml builds all three on a release tag
# (design/operations/github.md).
#
# How each runs:
#
# - server, as the desktop trial (the default; README, Desktop trial): its
#   entrypoint, deploy/apptown-start.sh, starts a Docker daemon inside the
#   container, so it needs --privileged, and runs forged on it with the
#   built-in database, internal sign-in, http://localhost:7777, previews at
#   *.preview.localhost:7777 and the agent image
#   ghcr.io/flokkit/apptown-agent of the same version. Its data, the inner
#   Docker's included, is under /var/lib/apptown (a volume). It prints a
#   link carrying the one-time setup token, so that token is in the
#   container's log until the administrator exists: a trial-only exception.
#
#     docker run --name apptown --privileged -p 127.0.0.1:7777:7777 \
#       -v apptown:/var/lib/apptown ghcr.io/flokkit/apptown:<version>
#
# - server, as a server: FORGE_NO_LOCAL_DOCKER=true in its environment. A
#   forge with no local Docker: it runs everything on a worker and needs an
#   external database: FORGE_DB_URL, FORGE_WORKER_URL and the worker's
#   certificates (FORGE_WORKER_CERT, _KEY, _CA), FORGE_AUTH and
#   FORGE_PUBLIC_HOST; forged refuses to start without them (Running a
#   forge, Workers and running without local Docker). Data is under
#   /var/lib/forge. It cannot run work on a Docker it shares through the
#   socket: it bind-mounts its own folders and bin/forge-linux into the
#   containers it starts, and from inside a container those are the
#   container's paths, not the host's.
#
#     docker run -d -p 7777:7777 -v forge-data:/var/lib/forge \
#       -v /etc/forge/worker:/etc/forge/worker:ro --env-file forged.env ghcr.io/flokkit/apptown:<version>
#
#   with FORGE_NO_LOCAL_DOCKER=true in forged.env.
#
# - worker: runs work on the host's Docker through its socket. It mounts its
#   state folder into the task containers it starts, so the state folder has
#   the same path inside and out; and it publishes previews on the host's
#   loopback and dials 127.0.0.1 to reach them, so it uses the host's
#   network (Linux):
#
#     docker run -d --network host -v /var/run/docker.sock:/var/run/docker.sock \
#       -v /var/lib/forge-worker:/var/lib/forge-worker -v /etc/forge-worker:/etc/forge-worker:ro \
#       ghcr.io/flokkit/apptown-worker:<version> -installation forge.example.com \
#       -state /var/lib/forge-worker -listen 10.0.0.5:7790 \
#       -cert /etc/forge-worker/worker.pem -key /etc/forge-worker/worker-key.pem \
#       -client-ca /etc/forge-worker/ca.pem
#
# Both run as root, as the systemd units do (Running a forge, Prepare the host).
# Base images are pinned by digest; the Go and Node stages run on the build
# machine's platform and cross-compile, so a multi-platform build emulates
# nothing.

# The web bundle forged embeds.
FROM --platform=$BUILDPLATFORM node:24-trixie-slim@sha256:7f486cabe892bff8fef04c0f7da8dba84a560be3e4e2740b7a86afc57ebec315 AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26.9-trixie@sha256:f06b7122f888680048afb247c4e5f49c0069eb6c4c1ccae0f3f4834527a8d22c AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY stock/ stock/
COPY web/*.go web/
COPY --from=web /src/web/dist web/dist
ARG TARGETOS TARGETARCH
# VERSION is the release name (v1.0.0); REVISION the commit, for a build
# without one (Go stamps no revision without .git in the context).
ARG VERSION=
ARG REVISION=
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOFLAGS=-buildvcs=false
RUN --mount=type=cache,target=/root/.cache/go-build \
    ld="-X flokkit.io/forge/internal/version.Release=$VERSION -X flokkit.io/forge/internal/version.Revision=$REVISION" \
    && for c in forged forge forge-worker; do go build -trimpath -ldflags "$ld" -o /out/bin/$c ./cmd/$c; done \
    && cp /out/bin/forge /out/bin/forge-linux

# Every third-party licence and notice the binaries and the web bundle carry
# (scripts/third-party-notices.py, as a release archive has).
FROM go AS notices
RUN apt-get update && apt-get install -y --no-install-recommends python3 && rm -rf /var/lib/apt/lists/*
COPY scripts/third-party-notices.py scripts/
COPY --from=web /src/web/node_modules web/node_modules
COPY web/package.json web/
RUN GOOS='' GOARCH='' python3 scripts/third-party-notices.py > /out/THIRD_PARTY_NOTICES

# The Docker CLI with Compose and Buildx, for the target platform.
FROM docker:29.8.1-cli@sha256:018edbc908e08fcc9dbf029c812c34251e9b4719e6f71ca0e5eae2a987d014ca AS docker

# The Docker daemon and Docker's dind wrapper, for the desktop trial.
FROM docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0 AS dockerd

FROM debian:trixie-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f AS base
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates git openssh-client curl tar \
    && rm -rf /var/lib/apt/lists/*
COPY --from=docker /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker /usr/local/libexec/docker/cli-plugins/docker-compose /usr/local/libexec/docker/cli-plugins/docker-buildx /usr/local/libexec/docker/cli-plugins/

FROM base AS server
RUN apt-get update && apt-get install -y --no-install-recommends iptables \
    && rm -rf /var/lib/apt/lists/*
COPY --from=dockerd /usr/local/bin/dockerd /usr/local/bin/containerd /usr/local/bin/containerd-shim-runc-v2 \
     /usr/local/bin/runc /usr/local/bin/docker-init /usr/local/bin/docker-proxy /usr/local/bin/dind /usr/local/bin/
WORKDIR /opt/forge
COPY --from=go /out/bin/forged /out/bin/forge /out/bin/forge-linux bin/
COPY deploy/forge-agent/ deploy/forge-agent/
COPY deploy/apptown-start.sh bin/
COPY LICENSE NOTICE ./
COPY --from=notices /out/THIRD_PARTY_NOTICES ./
# The trial's agent image is ghcr.io/flokkit/apptown-agent of this version,
# tagged as images.yml tags it (v1.0.0 -> 1.0.0); a build with no VERSION
# leaves the file empty and the trial builds forge-agent instead.
ARG VERSION=
RUN v="$VERSION"; echo "${v#v}" > /opt/forge/agent-tag
# No FORGE_DATA or FORGE_NO_LOCAL_DOCKER here: apptown-start.sh sets each
# mode's defaults, so a server inherits none of the trial's.
ENV PATH=/opt/forge/bin:$PATH FORGE_ADDR=:7777
VOLUME /var/lib/apptown
EXPOSE 7777
ENTRYPOINT ["/opt/forge/bin/apptown-start.sh"]

FROM base AS worker
WORKDIR /opt/forge-worker
COPY --from=go /out/bin/forge-worker bin/
COPY deploy/forge-agent/ deploy/forge-agent/
COPY LICENSE NOTICE ./
COPY --from=notices /out/THIRD_PARTY_NOTICES ./
ENTRYPOINT ["/opt/forge-worker/bin/forge-worker", "-agent-context", "/opt/forge-worker/deploy/forge-agent"]
