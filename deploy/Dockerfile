# AppTown's own images: AppTown daemon (target "server", published as
# apptown) and the worker (target "worker"), laid out as the release archive
# is (scripts/release.sh). Build from the repository root;
# Dockerfile.dockerignore keeps the context to what the builds read:
#
#   docker build -f deploy/Dockerfile --target server --build-arg VERSION=v1.0.0 -t apptown .
#   docker build -f deploy/Dockerfile --target worker --build-arg VERSION=v1.0.0 -t apptown-worker .
#
# The agent image is deploy/apptown-agent/Dockerfile, built from that folder.
# GitHub's images.yml builds all three on a release tag
# (design/operations/github.md).
#
# How each runs:
#
# - server, as the desktop trial (the default; README, Desktop trial): its
#   entrypoint, deploy/apptown-start.sh, starts a Docker daemon inside the
#   container, so it needs --privileged, and runs apptownd on it with the
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
# - server, as one server: APPTOWN_ONE_SERVER=true, on the host's network
#   with the host's Docker socket and its data folder at the same path inside
#   and out; deploy/one-server/compose.yaml starts it that way, with Caddy in
#   front (Running AppTown, One server).
#
# - server, as a server: APPTOWN_NO_LOCAL_DOCKER=true in its environment. A
#   AppTown with no local Docker: it runs everything on a worker and needs an
#   external database: APPTOWN_DB_URL, APPTOWN_WORKER_URL and the worker's
#   certificates (APPTOWN_WORKER_CERT, _KEY, _CA), APPTOWN_AUTH and
#   APPTOWN_PUBLIC_HOST; apptownd refuses to start without them (Running an
#   AppTown, Workers and running without local Docker). Data is under
#   /var/lib/apptown. It cannot run work on a Docker it shares through the
#   socket: it bind-mounts its own folders and bin/apptown-linux into the
#   containers it starts, and from inside a container those are the
#   container's paths, not the host's.
#
#     docker run -d -p 7777:7777 -v apptown-data:/var/lib/apptown \
#       -v /etc/apptown/worker:/etc/apptown/worker:ro --env-file apptownd.env ghcr.io/flokkit/apptown:<version>
#
#   with APPTOWN_NO_LOCAL_DOCKER=true in apptownd.env.
#
# - worker: runs work on the host's Docker through its socket. It mounts its
#   state folder into the task containers it starts, so the state folder has
#   the same path inside and out; and it publishes previews on the host's
#   loopback and dials 127.0.0.1 to reach them, so it uses the host's
#   network (Linux):
#
#     docker run -d --network host -v /var/run/docker.sock:/var/run/docker.sock \
#       -v /var/lib/apptown-worker:/var/lib/apptown-worker -v /etc/apptown-worker:/etc/apptown-worker:ro \
#       ghcr.io/flokkit/apptown-worker:<version> -installation forge.example.com \
#       -state /var/lib/apptown-worker -listen 10.0.0.5:7790 \
#       -cert /etc/apptown-worker/worker.pem -key /etc/apptown-worker/worker-key.pem \
#       -client-ca /etc/apptown-worker/ca.pem
#
# Both run as root, as the systemd units do (Running AppTown, Prepare the host).
# Base images are pinned by digest; the Go and Node stages run on the build
# machine's platform and cross-compile, so a multi-platform build emulates
# nothing.

# The web bundle apptownd embeds.
FROM --platform=$BUILDPLATFORM node:24-trixie-slim@sha256:43b9ba1142b8dcf33fa8f922ca1be85714adc444e0586c7cf7726daaeabc1bea AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json web/.npmrc ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27.3-trixie@sha256:c65ecfe613b539ac74c62a4c3b2c27e8f8a6076384c8f27e8a3c0f3be17cc385 AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY stock/ stock/
COPY scripts/stock-files.go scripts/
COPY web/*.go web/resource-policy.json web/
COPY --from=web /src/web/dist web/dist
ARG TARGETOS TARGETARCH
# VERSION is the release name (v1.0.0); REVISION the commit, for a build
# without one (Go stamps no revision without .git in the context).
ARG VERSION=
ARG REVISION=
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOFLAGS=-buildvcs=false
RUN --mount=type=cache,target=/root/.cache/go-build \
    ld="-X github.com/flokkit/apptown/internal/version.Release=$VERSION -X github.com/flokkit/apptown/internal/version.Revision=$REVISION" \
    && for c in apptownd apptown apptown-worker; do go build -trimpath -ldflags "$ld" -o /out/bin/$c ./cmd/$c || exit; done \
    && cp /out/bin/apptown /out/bin/apptown-linux \
    && GOOS= GOARCH= go run -ldflags "$ld" scripts/stock-files.go -o /out/share/apptown/updates

# Every third-party licence and notice the binaries and the web bundle carry
# (scripts/third-party-notices.py, as a release archive has).
FROM go AS notices
RUN apt-get update && apt-get install -y --no-install-recommends python3 && rm -rf /var/lib/apt/lists/*
COPY scripts/third-party-notices.py scripts/
COPY --from=web /src/web/node_modules web/node_modules
COPY web/package.json web/
RUN GOOS='' GOARCH='' python3 scripts/third-party-notices.py > /out/THIRD_PARTY_NOTICES

# The Docker CLI with Compose and Buildx, for the target platform.
FROM docker:29.9.2-cli@sha256:5e6baf67325a79f64ed3bed6863954d5e05d0f3d4e57cbdb14809346814bbfdb AS docker

# The Docker daemon and Docker's dind wrapper, for the desktop trial.
FROM docker:29.9.2-dind@sha256:9e79b879b177fbd72c34376a3dd3d91747da5db49e1f560f76aea59561471977 AS dockerd

FROM debian:trixie-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f AS base
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates git openssh-client curl tar \
    && rm -rf /var/lib/apt/lists/*
COPY --from=docker /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker /usr/local/libexec/docker/cli-plugins/docker-compose /usr/local/libexec/docker/cli-plugins/docker-buildx /usr/local/libexec/docker/cli-plugins/

FROM base AS server
RUN apt-get update && apt-get install -y --no-install-recommends iptables \
    && rm -rf /var/lib/apt/lists/*
# The pinned daemon embeds containerd; nested containers still need its shim.
COPY --from=dockerd /usr/local/bin/dockerd /usr/local/bin/containerd-shim-runc-v2 \
     /usr/local/bin/runc /usr/local/bin/docker-init /usr/local/bin/docker-proxy /usr/local/bin/dind /usr/local/bin/
WORKDIR /opt/apptown
COPY --from=go /out/bin/apptownd /out/bin/apptown /out/bin/apptown-linux bin/
# The stock and tool versions the binaries read at start.
COPY --from=go /out/share/ share/
COPY deploy/apptown-agent/ deploy/apptown-agent/
COPY deploy/apptown-start.sh bin/
# The checked database dump a one-server compose install runs before an
# upgrade (Upgrading), where the release archive has it too.
COPY scripts/apptown-db-backup.sh scripts/
COPY LICENSE NOTICE ./
COPY --from=notices /out/THIRD_PARTY_NOTICES ./
# The trial's agent image is ghcr.io/flokkit/apptown-agent of this version,
# tagged as images.yml tags it (v1.0.0 -> 1.0.0); a build with no VERSION
# leaves the file empty and the trial builds apptown-agent instead.
ARG VERSION=
RUN v="$VERSION"; echo "${v#v}" > /opt/apptown/agent-tag
# No APPTOWN_ADDR, APPTOWN_DATA or other setting here: apptown-start.sh sets
# each mode's defaults, so one mode inherits none of another's (one server
# listens on loopback only; the trial on every address of its container).
ENV PATH=/opt/apptown/bin:$PATH
VOLUME /var/lib/apptown
EXPOSE 7777
ENTRYPOINT ["/opt/apptown/bin/apptown-start.sh"]

FROM base AS worker
WORKDIR /opt/apptown-worker
COPY --from=go /out/bin/apptown-worker bin/
COPY --from=go /out/share/ share/
COPY deploy/apptown-agent/ deploy/apptown-agent/
COPY LICENSE NOTICE ./
COPY --from=notices /out/THIRD_PARTY_NOTICES ./
ENTRYPOINT ["/opt/apptown-worker/bin/apptown-worker", "-agent-context", "/opt/apptown-worker/deploy/apptown-agent"]
