# The recipe for this app's files (the forge builds it for previews and
# releases). The node stage installs exactly the lockfile, checks the types
# and builds; the last stage holds only the built files in /dist, which the
# forge serves with its own file server (static: in .forge/app.yaml).
# Nothing from node ships.
FROM node:24-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM scratch AS site
COPY --from=build /app/dist /dist
