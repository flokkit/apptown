# ci-heavy: the image the heavy tiers' Go tests run in (.github/workflows/heavy.yml,
# design/architecture/testing.md). The tests run in it as root, beside the
# host's daemon, because the containers AppTown starts run as root and write
# into the folders it bind-mounts, and only root can then remove them (as on
# every AppTown that runs containers on Linux). scripts/ci-heavy.sh builds it
# with no context. Every pin is an ARG default here, the one place it is set;
# the bases and the npm registry are the public upstreams.
ARG GO_IMAGE=golang:1.26.10-trixie@sha256:9ba7dc6d09ebf6e9317263973174bbf44e15bdf58cb0103747d7376f7cfa2d2f
ARG NODE_IMAGE=node:24@sha256:6449e19d8184d84062525b6a7524def03146c2cb04a7f08b076680d0e8e4240a
FROM ${GO_IMAGE} AS go

# node: a test reads the static server's values back in JavaScript. The
# node image is the one CI's web job uses.
FROM ${NODE_IMAGE}
COPY --from=go /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:/root/go/bin:$PATH

# The Docker CLI with its buildx and compose plugins, pinned by version and
# digest (x86_64 static builds); the daemon is the host's, through its socket.
ARG DOCKER_CLI_VERSION=29.9.1
ARG DOCKER_CLI_SHA256=c158ecc55adb3afb01bbd3539ed3b997d20c26dbdc418080506cc1bf5243ff57
ARG DOCKER_BUILDX_VERSION=v0.37.1
ARG DOCKER_BUILDX_SHA256=9447199cdb435f25880548343c128a4b6650e8891ee598905d8d29d39a8e359b
ARG DOCKER_COMPOSE_VERSION=v5.5.1
ARG DOCKER_COMPOSE_SHA256=db1889184726840f75c4f9c001048430d4f25b3be3cb084d3ddd762bc0aed576
RUN set -e; plugins=/usr/local/lib/docker/cli-plugins; mkdir -p "$plugins" \
    && curl -fsSL -o /tmp/docker.tgz "https://download.docker.com/linux/static/stable/x86_64/docker-${DOCKER_CLI_VERSION}.tgz" \
    && echo "${DOCKER_CLI_SHA256}  /tmp/docker.tgz" | sha256sum -c - \
    && tar -xzf /tmp/docker.tgz -C /usr/local/bin --strip-components=1 docker/docker && rm /tmp/docker.tgz \
    && curl -fsSL -o "$plugins/docker-buildx" "https://github.com/docker/buildx/releases/download/${DOCKER_BUILDX_VERSION}/buildx-${DOCKER_BUILDX_VERSION}.linux-amd64" \
    && echo "${DOCKER_BUILDX_SHA256}  $plugins/docker-buildx" | sha256sum -c - \
    && curl -fsSL -o "$plugins/docker-compose" "https://github.com/docker/compose/releases/download/${DOCKER_COMPOSE_VERSION}/docker-compose-linux-x86_64" \
    && echo "${DOCKER_COMPOSE_SHA256}  $plugins/docker-compose" | sha256sum -c - \
    && chmod 755 "$plugins/docker-buildx" "$plugins/docker-compose" \
    && docker --version && docker buildx version && docker compose version && node --version && go version

# oxipng: make tour compresses the tour's screenshots with it when it is on
# PATH (the release's tour job runs make tour in this image).
ARG OXIPNG_VERSION=10.2.1
ARG OXIPNG_SHA256=1813750ef592c5350ca79c88f98b2c0876d05c826dc7156245256d4255c1ad17
RUN set -e; dir="oxipng-${OXIPNG_VERSION}-x86_64-unknown-linux-musl" \
    && curl -fsSL -o /tmp/oxipng.tgz "https://github.com/shssoichiro/oxipng/releases/download/v${OXIPNG_VERSION}/${dir}.tar.gz" \
    && echo "${OXIPNG_SHA256}  /tmp/oxipng.tgz" | sha256sum -c - \
    && tar -xzf /tmp/oxipng.tgz -C /usr/local/bin --strip-components=1 "${dir}/oxipng" && rm /tmp/oxipng.tgz \
    && oxipng --version

# The opt-in tiers' prerequisites (the opt-in-tiers job): Playwright with its
# Chromium and the libraries Chromium needs, and the Pi the agent image pins
# (ci-heavy.sh passes PI_VERSION from deploy/apptown-agent/Dockerfile). The
# nightly's prepare installs the same Playwright version, read from here.
# No package's install script runs (--ignore-scripts): Chromium comes from
# the explicit playwright install, and Pi needs none.
ARG PLAYWRIGHT_VERSION=1.60.0
ARG PI_VERSION
ARG NPM_REGISTRY=https://registry.npmjs.org/
RUN set -e; test -n "${PI_VERSION}" \
    && mkdir -p /opt/apptown-tiers/playwright /opt/apptown-tiers/pi \
    && cd /opt/apptown-tiers/playwright \
    && npm install --ignore-scripts --no-audit --no-fund --registry "${NPM_REGISTRY}" "playwright@${PLAYWRIGHT_VERSION}" \
    && npx --no-install playwright install --with-deps chromium \
    && cd /opt/apptown-tiers/pi \
    && npm install --ignore-scripts --no-audit --no-fund --registry "${NPM_REGISTRY}" "@earendil-works/pi-coding-agent@${PI_VERSION}"
ENV APPTOWN_PLAYWRIGHT_MODULE=/opt/apptown-tiers/playwright/node_modules/playwright/index.mjs \
    APPTOWN_PI_MODULE=/opt/apptown-tiers/pi/node_modules/@earendil-works/pi-coding-agent
