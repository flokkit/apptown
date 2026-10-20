---
name: lang/selection/node-fullstack
tags: lang,typescript,node
checks: [{"name": "gitignore-names-build-output", "expects": "git would ignore what Node writes (node_modules/, dist/), so build output can never be committed by accident. If the project commits that output deliberately, this check does not fit it: say so and stop rather than add the line, since an ignore rule over tracked files makes the repository contradict itself", "severity": "medium", "evaluator": "sh", "sh": "t=$(mktemp -d) && git -c init.defaultBranch=main init -q \"$t\" || exit 2; bad=0; for p in node_modules/probe.js dist/probe.js; do git --git-dir=\"$t/.git\" --work-tree=. -c core.excludesFile=/dev/null check-ignore -q \"$p\" || { echo \".gitignore does not cover ${p%/*}/\"; bad=1; }; done; exit $bad", "title": "Declare generated files in the repository’s ignore rules"}, {"name": "lockfile-in-step", "expects": "package.json and package-lock.json are in step in every package directory (npm ci --dry-run succeeds), so an install resolves exactly what is committed", "severity": "medium", "evaluator": "tool", "tool": "npm-lock-check", "title": "Keep the dependency lockfile in sync"}]
---
The whole application is TypeScript on Node — one language, one runtime,
one package graph. The server is Fastify (plain node:http for a tiny one).
A browser UI beyond server-rendered pages is React built with Vite, in an
npm workspace beside the server, with types shared end to end; an app
with no such UI does not grow one to fit this note.

Rationale: zero context-switching; types flow from database to DOM; the enormous
ecosystem is a feature when you accept lockfile discipline.

The pack seeds the packaging recipe, the ignore rules and the runtime
contract; the app comes from the ecosystem's own generators, never from a
hand-kept skeleton.

**The first act on an empty repo** (the "build the first version" change):

    npm init -y
    npm pkg set workspaces='["server","web"]'
    mkdir server && (cd server && npm init -y && npm i fastify pg && npm i -D typescript tsx @types/node @types/pg)
    npm create vite@latest web -- --template react-ts   # only when the app has a browser UI

The seeded Dockerfile runs `npm ci` and `npm run build` at the root, then
`npm prune --omit=dev`, and starts the app with `node --run start` in an
image that carries no npm and no dev dependencies. Both stages are
`node:24-slim`; the final one copies the built app and removes npm,
corepack and yarn. Never use the full `node` image (Debian's
build image, with compilers, about 1 GB) for either stage. Give the
root package `build` and `start` scripts (build the web app, then
serve it and the API from one Fastify process on port 8080). The start
script must not call npm or npx, and a `prestart` script does not run. It
runs the built JavaScript with `node` (never `tsx` or `ts-node`), and
anything the app imports at run time is a dependency, not a
devDependency: dev dependencies are not in the image. The
seeded runtime contract at `.apptown/app.yaml` declares the web part on
port 8080 with `GET /healthz` as its readiness path, so serve that, and
the Postgres the app gets at deploy (add an `init:` step for migrations
once you have a migration tool):

    app:
      web:
        build: .
        ports: ["8080"]
        public: true
        health:
          readiness: { http: /healthz }
    needs:
      postgres:
        version: "16"

Commit `package-lock.json`; add one test that hits the API, and give the
root package a `test` script that runs every workspace's tests: the
landing runs `npm test` at the root.

Build output stays out of git: `.gitignore` names `node_modules/` and
`dist/`, so an install or a build can never be committed by accident.

Dependencies are what the files say: `package-lock.json` is committed
beside every `package.json` and agrees with it, so `npm ci` installs
exactly what is committed, everywhere.

**Fits a container limit.** Node's heap does not follow a container's limit
on every version, so the seeded Dockerfile sets
`NODE_OPTIONS=--max-old-space-size=96` for the declared
`resources: { memory: 128M }`: about three quarters of it. Change both
together, and raise both when the deployment's card shows the app near its
limit; deploy/memory-declared says how to choose and check it.
