# apptown-agent: the containerized step executor image.
# pi, Node (the base), Python/uv, and mise, which installs at step time the
# toolchains a project declares in its mise.toml (Go, Ruby, Java, ...); no Go
# toolchain is baked in. The versions are listed in deploy/tool-pins.txt.
# Build:  docker build -t apptown-agent deploy/apptown-agent
#
# The base is pinned by digest, and every archive or binary fetched below is
# pinned by version and checked against its SHA-256 for each architecture (the
# hash the project publishes where it publishes one; Docker's static CLI and
# hivemind publish none, so theirs were taken from the first download,
# 12 October 2026). Changing a version means changing its hashes beside it;
# TestTheAgentImageChecksEveryDownload keeps it so. Apt packages and package
# managers' dependencies resolve when built, so the image is not reproducible:
# one context names one recipe, not identical bytes.
# gopls, pi-lsp's Go server, is built here and only its binary is copied
# into the image: neither the Go toolchain nor the module and build caches
# `go install` leaves behind ship. At run time gopls uses the project's own
# go, from its mise.toml. CGO off, so the binary depends on no libc.
# Each RUN that downloads mounts the extra certificates AppTown gives a build
# (the secret apptown-trust; internal/trust) and runs them first, so a
# build behind a TLS-inspecting proxy can fetch; without them it runs as is.
FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452 AS gopls
ARG GOPLS_VERSION=v0.23.0
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; CGO_ENABLED=0 GOBIN=/out go install golang.org/x/tools/gopls@${GOPLS_VERSION} && /out/gopls version

FROM node:24-slim@sha256:55fb5f5a527ea2c5c4a95f963e48ef90aa38c7b346054fdc8ee9957a2b55d205

# The headers native gems and wheels build against when no precompiled build
# fits (a lockfile on the ruby platform, psycopg2, an older pg): libpq for
# Postgres clients (pg, psycopg), libyaml for psych, libffi, OpenSSL, zlib,
# and pkg-config to find them. A Rails app's bundle stopped at pg without
# them (sweep, 2026-10-02). Toolchains themselves come precompiled through
# mise, so no interpreter is compiled here.
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; apt-get update && apt-get install -y --no-install-recommends \
      git curl ca-certificates ripgrep fd-find jq file \
      python3 python3-venv build-essential pkg-config \
      libpq-dev libyaml-dev libffi-dev libssl-dev zlib1g-dev \
      tmux \
      chromium-headless-shell fonts-liberation \
      socat libnss3-tools \
    && rm -rf /var/lib/apt/lists/* \
    && ln -s /usr/bin/fdfind /usr/local/bin/fd
# fd as pi's tools manager looks for it, so a chat never downloads it.

# sha <amd64-sum> <arm64-sum> <file>: checks a download for this architecture.
RUN printf '#!/bin/sh\nset -e\ncase "$(dpkg --print-architecture)" in amd64) s=$1;; arm64) s=$2;; *) echo "unsupported architecture" >&2; exit 1;; esac\necho "$s  $3" | sha256sum -c -\n' > /usr/local/bin/sha \
    && chmod +x /usr/local/bin/sha

# uv for python projects, from its release archive
ARG UV_VERSION=0.13.0
ARG UV_SHA256_AMD64=1468ebd5a5541121837c5a2817b9972ba6090fa6caa3d142620850a47fb75154
ARG UV_SHA256_ARM64=3ccfb6af6e242433eb552f7d9676abd5412c6595c497e990d9c8cb7b5bd4d2c3
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; case "$(dpkg --print-architecture)" in amd64) t=x86_64;; arm64) t=aarch64;; esac \
    && curl -fsSL "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/uv-${t}-unknown-linux-gnu.tar.gz" -o /tmp/uv.tgz \
    && sha "$UV_SHA256_AMD64" "$UV_SHA256_ARM64" /tmp/uv.tgz \
    && tar -xzf /tmp/uv.tgz -C /usr/local/bin --strip-components=1 && rm /tmp/uv.tgz && uv --version

# mise: the toolchain layer of the ambient build environment
# (design/subsystems/registries.md §5). Tools install at step time from the repo's
# mise.toml, cached under MISE_DATA_DIR in AppTown-cache volume; registry
# routing comes from AppTown-generated /etc/mise/config.toml at run time.
# MISE_MIRROR is overridable to pull mise itself through a local proxy.
ARG MISE_VERSION=v2026.8.12
ARG MISE_MIRROR=https://github.com
ARG MISE_SHA256_AMD64=f2092b1e67f0abc8803d3be120dd2bc5b656dd99680ba3159f710e149da10d05
ARG MISE_SHA256_ARM64=071e2d16905360fa04762422a2a889692bb3a4d30f27650de50bc1ac0564840b
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; arch=$(dpkg --print-architecture) && case "$arch" in amd64) m=x64;; arm64) m=arm64;; *) echo "unsupported arch $arch" && exit 1;; esac \
    && curl -fsSL "${MISE_MIRROR}/jdx/mise/releases/download/${MISE_VERSION}/mise-${MISE_VERSION}-linux-${m}" -o /usr/local/bin/mise \
    && sha "$MISE_SHA256_AMD64" "$MISE_SHA256_ARM64" /usr/local/bin/mise \
    && chmod +x /usr/local/bin/mise && mise --version

# hivemind: a single-binary Procfile runner for process-mode multi-process apps
# (design/decisions/forge-native-contract.md, the dev/process-and-container-mode stance).
# Starts a Procfile's processes together, aggregates logs, stops them cleanly.
ARG HIVEMIND_VERSION=1.1.0
ARG HIVEMIND_SHA256_AMD64=809ca1c5fedbbfca8fdfb3a4b9bb9c41868b046f35f85a3e893ccf2583a3921a
ARG HIVEMIND_SHA256_ARM64=e4e0b7809bc5a24a61500e11695aa754fc681f19d5d7ba4aeb5f5f1556d69360
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; arch=$(dpkg --print-architecture) \
    && curl -fsSL "https://github.com/DarthSim/hivemind/releases/download/v${HIVEMIND_VERSION}/hivemind-v${HIVEMIND_VERSION}-linux-${arch}.gz" -o /tmp/hivemind.gz \
    && sha "$HIVEMIND_SHA256_AMD64" "$HIVEMIND_SHA256_ARM64" /tmp/hivemind.gz \
    && gunzip -c /tmp/hivemind.gz > /usr/local/bin/hivemind && rm /tmp/hivemind.gz \
    && chmod +x /usr/local/bin/hivemind && hivemind --version
LABEL apptown.hivemind.version=${HIVEMIND_VERSION}

# glab: GitLab CLI for steps granted scoped SCM access (with: {scm: true}).
# Auth arrives purely via env (GITLAB_HOST/GITLAB_TOKEN) at step time.
ARG GLAB_VERSION=1.55.0
ARG GITLAB_MIRROR=https://gitlab.com
ARG GLAB_SHA256_AMD64=ae3feac2aa1034a585e4a24787970dacd30f07906cdc75af8911c32d158b44e8
ARG GLAB_SHA256_ARM64=36c7368617111148dd34e5cd86ffa11665f76101e4ba396db943f8d17ceaa989
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; arch=$(dpkg --print-architecture) \
    && curl -fsSL "${GITLAB_MIRROR}/gitlab-org/cli/-/releases/v${GLAB_VERSION}/downloads/glab_${GLAB_VERSION}_linux_${arch}.deb" -o /tmp/glab.deb \
    && sha "$GLAB_SHA256_AMD64" "$GLAB_SHA256_ARM64" /tmp/glab.deb \
    && dpkg -i /tmp/glab.deb && rm /tmp/glab.deb && glab --version
LABEL apptown.glab.version=${GLAB_VERSION}

# docker client + buildx + compose: implementation steps and writable change
# consoles get a PRIVATE daemon sidecar (DOCKER_HOST points at it).
# No socket from the host enters these containers. compose is what
# `apptown dev up` rehearses the contract with (complaint #54: the plugin was
# missing, so the rehearsal could not run).
ARG DOCKER_VERSION=27.5.1
ARG BUILDX_VERSION=0.19.3
ARG COMPOSE_VERSION=5.5.0
ARG DOCKER_SHA256_AMD64=4f798b3ee1e0140eab5bf30b0edc4e84f4cdb53255a429dc3bbae9524845d640
ARG DOCKER_SHA256_ARM64=e6b53725a73763ab3f988c73f8772eaed429754c1a579db5ff11f21990fd1817
ARG BUILDX_SHA256_AMD64=32ed111e941e385c2fb8261eba06a4056915718fd606f8278834ac1931d261a2
ARG BUILDX_SHA256_ARM64=138b587399b27bb61945a36d67866177b85dea1155101a2be63c7ab715f18a2e
ARG COMPOSE_SHA256_AMD64=c57ab918abd5b05ca7e7d0f275875dd1330a695074f309dc9eab1b49efafcd4b
ARG COMPOSE_SHA256_ARM64=ff42489f5a9b879d5d117c5ffea6defc27390b3286da8ad52cbc9c6ab5df590e
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; arch=$(dpkg --print-architecture) && case "$arch" in amd64) d=x86_64;; arm64) d=aarch64;; *) echo "unsupported arch $arch" && exit 1;; esac \
    && curl -fsSL "https://download.docker.com/linux/static/stable/${d}/docker-${DOCKER_VERSION}.tgz" -o /tmp/docker.tgz \
    && sha "$DOCKER_SHA256_AMD64" "$DOCKER_SHA256_ARM64" /tmp/docker.tgz \
    && tar -xzf /tmp/docker.tgz -C /tmp && rm /tmp/docker.tgz \
    && mv /tmp/docker/docker /usr/local/bin/docker && rm -rf /tmp/docker \
    && mkdir -p /usr/local/libexec/docker/cli-plugins \
    && curl -fsSL "https://github.com/docker/buildx/releases/download/v${BUILDX_VERSION}/buildx-v${BUILDX_VERSION}.linux-${arch}" \
         -o /usr/local/libexec/docker/cli-plugins/docker-buildx \
    && sha "$BUILDX_SHA256_AMD64" "$BUILDX_SHA256_ARM64" /usr/local/libexec/docker/cli-plugins/docker-buildx \
    && chmod +x /usr/local/libexec/docker/cli-plugins/docker-buildx \
    && curl -fsSL "https://github.com/docker/compose/releases/download/v${COMPOSE_VERSION}/docker-compose-linux-${d}" \
         -o /usr/local/libexec/docker/cli-plugins/docker-compose \
    && sha "$COMPOSE_SHA256_AMD64" "$COMPOSE_SHA256_ARM64" /usr/local/libexec/docker/cli-plugins/docker-compose \
    && chmod +x /usr/local/libexec/docker/cli-plugins/docker-compose \
    && docker --version && docker compose version
LABEL apptown.docker.version=${DOCKER_VERSION}
LABEL apptown.compose.version=${COMPOSE_VERSION}

# Language servers for pi-lsp (the opt-in real-time-diagnostics extension baked
# below). pi-lsp expects the servers on PATH and skips any that are missing, so
# we bake the ones for our committed stances: biome (TS/JS/JSON/CSS), ruff
# (Python lint/fix), gopls (Go, built in the stage above). Rust/Java servers
# can be added later. The npm and uv download caches are not used at run time.
ARG BIOME_VERSION=2.3.14
ARG RUFF_VERSION=0.17.0
COPY --from=gopls /out/gopls /usr/local/bin/gopls
# No npm package's install script runs while this image is built
# (--ignore-scripts here, npm_config_ignore_scripts for pi install, which
# runs npm); none of these tools needs one. A project's own npm installs in
# the running container are not affected.
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; npm install -g --ignore-scripts @biomejs/biome@${BIOME_VERSION} \
    && UV_TOOL_BIN_DIR=/usr/local/bin uv tool install ruff==${RUFF_VERSION} \
    && npm cache clean --force && rm -rf /root/.cache/uv \
    && biome --version && ruff --version && gopls version
LABEL apptown.lsp.servers=biome,ruff,gopls

# pi itself
ARG PI_VERSION=0.84.3
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; npm install -g --ignore-scripts @earendil-works/pi-coding-agent@${PI_VERSION} && npm cache clean --force
# Runtime version surfaces in step evidence via this label (receipts).
LABEL apptown.pi.version=${PI_VERSION}

# pi-subagents: the one extension AppTown blesses (delegation to cheaper
# model slots). Installed into a baked HOME at build time; each session's
# generated pi-home symlinks this npm dir and lists the package in its
# settings, so session start never touches the network. The label is how
# apptownd learns the pinned version (and receipts record it).
ARG PI_SUBAGENTS_VERSION=0.61.0
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; npm_config_ignore_scripts=true HOME=/opt/pi-baked pi install npm:pi-subagents@${PI_SUBAGENTS_VERSION}
# UPSTREAM PATCH (pi-subagents 0.61.0, complaint #7): child model verification
# compares the alias we pin (claude-haiku-4-5) against the dated id the
# provider reports (claude-haiku-4-5-20251001) and fails the run. The package
# ships stripTrailingDateStamp for exactly this but doesn't apply it on the
# verification path. The trailing grep asserts the patch landed — a version
# bump that moves this code must fail the build, not silently drop the fix.
# Drop when a release handles dated ids.
RUN f=/opt/pi-baked/.pi/agent/npm/node_modules/pi-subagents/src/runs/shared/model-fallback.ts && \
    sed -i 's|if (expectedBase === observedBase) return undefined;|if (expectedBase === observedBase) return undefined;\n\t/* forge-patch-dated-ids */ if (stripTrailingDateStamp(expectedBase.slice(expectedBase.lastIndexOf("/") + 1)) === stripTrailingDateStamp(observedBase.slice(observedBase.lastIndexOf("/") + 1))) return undefined;|' "$f" && \
    grep -q "forge-patch-dated-ids" "$f"
# Warm the TypeScript transpile caches (/tmp/jiti, /tmp/node-compile-cache):
# first extension load costs ~7s of transpilation; with the caches baked
# into the layer every container cold-starts in under a second. Node
# realpaths through the session npm symlink, so the keys match at runtime.
# The || true: this run fails at the model call, which is after everything
# we want warmed.
RUN cd /tmp && HOME=/opt/pi-baked PI_SKIP_VERSION_CHECK=1 \
      timeout 90 pi --session-dir /tmp/warm --session-id warm \
      --no-context-files --print warm-cache >/dev/null 2>&1 || true; \
    rm -rf /tmp/warm /tmp/pi-subagents-uid-0; test -d /tmp/jiti
LABEL apptown.pi-subagents.version=${PI_SUBAGENTS_VERSION}

# pi-lsp: an OPT-IN extension (design: AppTown blesses baked extensions and
# enables them per project). Baked into the same /opt/pi-baked HOME as
# pi-subagents at a pinned version; a project turns it on and its packages list
# gains npm:@narumitw/pi-lsp. Off by default — trust:never means a repo can't
# enable it, only a blessed pack/setting can.
ARG PI_LSP_VERSION=0.49.6
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; npm_config_ignore_scripts=true HOME=/opt/pi-baked pi install npm:@narumitw/pi-lsp@${PI_LSP_VERSION}
LABEL apptown.pi-lsp.version=${PI_LSP_VERSION}

WORKDIR /work
