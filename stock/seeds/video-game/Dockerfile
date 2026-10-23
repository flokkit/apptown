# The recipe for the game's files (AppTown builds it for previews and
# releases). The node stage installs exactly the lockfile and builds; the
# last stage holds only the built game in /dist, which AppTown serves
# with its own file server (static: in .apptown/app.yaml). Players get static
# files; the toolchain never leaves the build stage.
# Each RUN that downloads mounts the extra certificates AppTown gives a build
# (the secret apptown-trust) and runs them first; without them it runs as is.
FROM node:24-slim AS build
WORKDIR /app
COPY package*.json ./
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; npm ci
COPY . .
RUN npm run build

FROM scratch AS site
COPY --from=build /app/dist /dist
