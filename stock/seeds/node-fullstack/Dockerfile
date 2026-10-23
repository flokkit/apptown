# Packaging recipe for this way of working (built by AppTown at release).
# Adapt the build and entry to the app; keep the node base.
# npm builds; it does not ship. The runtime stage removes the package
# managers the node image carries (npm, corepack, yarn): the app never runs
# them, and their own dependencies' known vulnerabilities would fail the
# image scan on every landing. `node --run start` runs the root package's
# start script without npm (pre/post scripts do not run).
# Each RUN that downloads mounts the extra certificates AppTown gives a build
# (the secret apptown-trust) and runs them first; without them it runs as is.
FROM node:24-slim AS build
WORKDIR /app
# The whole tree before `npm ci`: with npm workspaces (server/, web/), npm
# installs only the dependencies of workspaces whose package.json it can see.
COPY . .
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; npm ci
RUN npm run build --if-present
# Only production dependencies ship: dev tools are build-time only, and some
# carry native binaries (TypeScript 7's compiler is a Go program) whose known
# vulnerabilities would fail the image scan though the app never runs them.
RUN npm prune --omit=dev

# The runtime stage takes the distribution's security updates at build
# time, and runs as the image's own unprivileged user, `node`.
FROM node:24-slim
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; apt-get update && apt-get upgrade -y && rm -rf /var/lib/apt/lists/* \
    && rm -rf /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/corepack \
      /usr/local/bin/npm /usr/local/bin/npx /usr/local/bin/corepack \
      /usr/local/bin/yarn /usr/local/bin/yarnpkg /opt/yarn-v*
WORKDIR /app
COPY --from=build /app /app
USER node
EXPOSE 8080
# Node's heap does not follow a container's limit on every version: about
# three quarters of the part's declared memory (128Mi in .apptown/app.yaml).
# Change both together.
ENV NODE_OPTIONS=--max-old-space-size=96
# The command first trusts the extra certificates AppTown gives the
# running app (APPTOWN_TRUST; nothing without it), then runs the app.
CMD ["sh", "-c", "eval \"${APPTOWN_TRUST:-}\"; exec \"$@\"", "sh", "node", "--run", "start"]
