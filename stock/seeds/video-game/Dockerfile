# The recipe for the game's files (AppTown builds it for previews and
# releases). The node stage installs exactly the lockfile and builds; the
# last stage holds only the built game in /dist, which AppTown serves
# with its own file server (static: in .forge/app.yaml). Players get static
# files; the toolchain never leaves the build stage.
FROM node:24-slim AS build
WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM scratch AS site
COPY --from=build /app/dist /dist
