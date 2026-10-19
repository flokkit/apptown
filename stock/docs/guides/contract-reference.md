# The app contract

What an app must do to run on AppTown, and what AppTown does in return. It
applies on every deployment target unless a sentence names one. This page is
what 1.x keeps stable ([What 1.0 promises](/docs/guides/compatibility)).
Workflows, checks and approvals are not part of it; they have their own
guides.

## The files AppTown reads

| File | What it is for |
| --- | --- |
| `.forge/app.yaml` | how the app runs: its parts, its needs, its startup steps |
| `secretspec.toml` | the secrets each part needs ([Secrets](/docs/guides/secrets)) |
| `mise.toml`, `.mise.toml` or `.config/mise/config.toml` | the development tools a chat or step installs ([Project toolchains](/docs/guides/toolchains)) |
| a `Dockerfile` in each build context | how a `build` part's image is made |
| `.agents/skills/` | skills the project gives AppTown's agents |

Everything else under `.forge/` is AppTown's working space for a change
(reports, plans, chat sessions, attachments). It never lands on `main`, even
when it is committed: only `.forge/app.yaml` travels.

AppTown reads `.forge/app.yaml` from the commit it boots, so a preview of an
old commit uses that commit's contract. Fixing today's file does not fix an
older commit that has none.

## `.forge/app.yaml`

```yaml
version: 1
app:
  web:
    build: .
    ports: ["8080"]
    public: true
    dataDir: /data
    environment: { LOG_LEVEL: info }
    health:
      readiness: { http: /healthz }
  worker:
    build: .
    command: ["node", "worker.js"]
init:
  - name: migrate
    build: .
    command: ["npm", "run", "migrate"]
needs:
  postgres: { version: "16" }
```

- **`version`** is `1`, or left out. Another version, an explicit `0`, and
  any field the format does not have are errors that name it.
- **`app`** names the parts. Each part declares exactly one of `build` (a
  Docker build context), `image` (an image to pull) or `static` (files to
  serve). Exactly one part is `public: true`: the one people reach.
- A part may also declare `ports`, `command` (a list, or a string), `environment`,
  `dataDir`, `health`, `resources` and `replicas`. `replicas` is `1` or left
  out; a larger number is refused before anything is built.
- **`init`** is an ordered list of steps that run to completion before the
  app starts (see "Startup order" below). Each declares `build` or
  `image`, and may declare `name`, `command` and `environment`.
- **`needs`** names services from the catalogue (`postgres`, `mariadb`,
  `redis`, `s3`, `llm`) with their parameters. Each parameter is one value.
  What each need gives the app is in
  [What each need gives the app](/docs/guides/services-and-data).
- **`secrets`** chooses the `secretspec.toml` declaration and how values are
  generated and delivered. Delivery is by environment variable; `file`
  delivery is refused.
- **`profiles`** and **`resources`**: see "Resources and profiles" below.
- **`rehearsal`**: see "Restore checks" below.
- **`seed`** on a part is accepted and runs nowhere; the validator says so.
  Put a command that must run before the app starts in `init`.

The schema and validator are on every AppTown:

```sh
forge config schema
forge config validate .forge/app.yaml
```

Both ask your AppTown's API; they do not work offline.

## What a build sees

A `build` part's image is built from its context folder with the
`Dockerfile` there. The context holds the committed files of the commit
being built; a preview of a change's folder also includes tracked edits not
yet committed. Untracked and ignored files are never in it. An app that
works in a chat because of a file nobody committed fails its preview.

Each declared context is built once per boot, and the parts that name the
same context share the image. An `image` part runs its image as it is, even
when a `Dockerfile` sits beside it. AppTown installs no `mise` tools inside
a built or pulled image: the `Dockerfile` or the image carries its own
runtime.

## One architecture

Pick one CPU architecture for an AppTown, `linux/amd64` or `linux/arm64`,
and run its workers and its deployment targets on it. App Platform runs
`linux/amd64` only. AppTown builds no multi-architecture image.

- **A release built on a worker** is built natively, for the worker's
  architecture. A release to a target of another architecture is refused
  before anything is built, naming both: "the worker builds for
  linux/arm64 and droplet-1 runs linux/amd64". AppTown asks a Docker host
  or Portainer for its platform at each release, and a release published
  by digest is refused on a target of another platform.
- **A Docker host without a worker** builds the app's images on the host
  itself, so they match it.
- **Portainer without a worker** builds on AppTown's own Docker, for the
  platform the Portainer machines report. A build that runs programs of
  that other architecture needs AppTown's Docker to emulate it, and is
  slow; one that only copies files, such as a static site, does not.
- **An `image` part** must already be built for the target's architecture.
  A release published from a worker checks every image's platform and
  refuses a mismatch. Portainer without a worker checks the public part's
  image when its manifest can be read. An image no check caught fails when
  it starts.
- **A bucket website is files**, so it goes to a bucket whatever the
  architecture.
- **The app must run on that architecture.** A dependency shipped only for
  another architecture fails the build, or fails when the app starts.
- **Moving a project to an AppTown of another architecture** means building
  its releases again there: an earlier release's images cannot run on the
  other architecture (a bucket website's release still can). Its data moves
  through a restore. Database backups are dumps (`pg_dump`, `mariadb-dump`),
  and object storage and `dataDir` copies are files; none of them depend on
  the architecture, though files an app writes in its own format are the
  app's to keep portable.

## The process

- **Listen on the declared port**, the container's own port. A public
  `build` or `image` part with no port uses 8080; a `static` part uses 80. A
  part with no port and not public is a worker. AppTown does not read
  `EXPOSE` from the `Dockerfile`.
- **Other parts and needs are reached by name** on the app's private
  network: a part called `api` at `api`, Postgres at the host its variables
  give. Never hard-code `localhost` for them.
- **A standing deployment restarts a part that exits.** A preview is
  thrown away when it stops.
- **Only `dataDir` and needs keep data.** Anything else a container writes
  is lost at the next deploy. On a Docker host only the public part's
  `dataDir` is kept; on Portainer every part's `dataDir` is kept. App
  Platform has no volumes and refuses an app that declares a `dataDir`.

## The environment an app gets

Each part, and each init step, gets its own `environment`, with the
connection variables of its needs set over it, and then the secrets its
scope names. AppTown adds a few values of its own, which differ by target:

| Value | Docker host | Portainer | App Platform |
| --- | --- | --- | --- |
| `PORT` | every part that has a port | other parts that declare a port; not the public part | not set by AppTown |
| `FORGE_ORIGINS` | public part: the address patterns browsers use, as wildcards (`https://*.apps.example.com`) | public part, the same | public part: the app's own address |
| `DATA_DIR` | public part, when it has a `dataDir`, set last | public part, the same | — (no `dataDir`) |
| attached integrations (error reporting's DSN and the like) | public part | not delivered; the release records it | not delivered; the release records it |
| order of precedence | secrets over connections over the part's own `environment` | the same | the same, then the needs' credentials over the part's secrets |

So that an app runs the same everywhere:

- read `PORT` if it is set, and fall back to the port you declared;
- do not set `PORT`, `FORGE_ORIGINS` or `DATA_DIR` in `environment`;
- read every connection value from the need's variables;
- keep durable files in the public part's `dataDir` or in a need.

Values reach the app as written. App Platform cannot promise that for
`${…}`: it replaces `${NAME}` with its own variable of that name, in
secrets too, so a deploy there refuses an environment value or secret
containing `${…}` before anything is made.

## Startup order

On a Docker host, every deploy and every preview boot goes in this order:

1. the needs start, and AppTown waits until each is healthy;
2. the init steps run, one after another, each to completion; a step that
   fails stops the boot, with its output;
3. the other parts start;
4. the public part starts, and AppTown waits until it is ready.

Init steps run at every deploy, so they must be safe to run again: a
migration that has already run does nothing. A container that restarts on
its own does not run them.

On Portainer the init steps and the other parts wait only for the needs,
not for each other: two init steps can run at the same time, and a worker
can start before a migration has finished. The public part waits for all
of them. On App Platform the init steps run as pre-deploy jobs.

## Behind the front door

AppTown's own front door (the built-in Docker host, and every preview)
passes a request on with:

- the visitor's `Host`, unchanged;
- `X-Forwarded-Proto`, `X-Forwarded-Host` and `X-Forwarded-For` set by the
  front door. Whatever the visitor sent in those headers is removed first,
  so the app can trust them;
- `Cache-Control: no-cache` on every preview answer, and on a deployment's
  answer when the app set no caching of its own.

WebSockets pass through. The front door does not sign visitors in: an
app's pages are as open as the app makes them.

An attached Docker host routes through its own Traefik, and App Platform
through DigitalOcean's; each sets the forwarding headers its own way.

Use `FORGE_ORIGINS` for the trusted origins a framework checks forms and
CSRF tokens against, not a typed host name. On a Docker host and Portainer
it is a set of patterns that can cover more than this one deployment.

## Health

```yaml
health:
  readiness: { http: /healthz, expect: 200, startupGrace: 60s }
  liveness:  { http: /livez }
```

A probe names one target, `http` (a path), `tcp` (a port) or `exec` (a
command run in the container), and may give `startupGrace`, `period`,
`timeout`, `failureThreshold` and `successThreshold`. An `http` probe may
give `expect` (a status). Readiness may also give `requestContext`
(`frontDoor`, the default, or `direct`) and `checksDeps`. Liveness may not check other services: a
liveness probe restarts the part, and restarting it for a database's
fault makes things worse.

What each target uses:

- **Docker host.** Readiness, checked at every boot, is an HTTP GET of the
  declared path, `/` when none is declared, sent as the front door would
  send it unless `requestContext: direct`. A `tcp` or `exec` readiness is
  checked as that GET. Liveness is watched on the public part of a
  standing deployment, which is restarted when it fails: an `http`
  liveness is a GET of its path, an `exec` liveness runs its command in the
  container (exit 0 is alive), and a `tcp` liveness, or none declared,
  connects to the part's port. For any HTTP probe, with `expect` the
  answer must be exactly that status, without it any HTTP answer counts,
  and redirects are not followed. `startupGrace` is used; `period`,
  `timeout` and the thresholds are not.
- **Portainer.** No `health` block is used. A part's `healthcheck:` shell
  command (exit 0 is healthy) is run on the other parts.
- **App Platform.** The readiness path only, with the platform's fixed
  timings, and a port check when no path is declared. `expect` is not used.

The readiness answer should not touch the database: use `rehearsal`
checks for that.

## Static sites

A `static` part serves files with AppTown's file server. It runs no code of
yours and reads no environment.

```yaml
app:
  game:
    static: .
    public: true
```

This serves the committed folder as it is. A site built first (Vite, a
bundler, a game export) declares its build:

```yaml
app:
  web:
    static:
      build: .        # the folder whose Dockerfile builds the site
      target: site    # optional: the stage holding the site (default: the last)
      output: dist    # the folder in that stage's files to serve
    public: true
```

AppTown builds that stage, takes its `output` folder and serves it. End the
`Dockerfile` with a stage of files only (`FROM scratch AS site`,
`COPY --from=build /app/dist /dist`). The folder may hold only folders and
files; a symbolic link in it, or on the way to it, is refused. Such a site
must be the public part, and `forge dev up` does not run it: use the app's
own dev server.

The file server serves files that exist and nothing else; there is no
fallback to `index.html` for other paths. An app that needs path routes
keeps its own server with `build:`. The file server also answers:

- `/_forge/env.js`: the values attached integrations give a page, the ones
  safe for any visitor to read (never secrets), as
  `window.__FORGE_ENV__`. They are delivered on a Docker host and a bucket
  website; Portainer and App Platform refuse a static site that has them.
- `/_forge/release`: the part and revision it serves, which AppTown checks
  after a deploy.

`/_forge/` is AppTown's path: AppTown supplies those two files in place of
any the app ships, so keep the app's own files out of it.

An app whose only part is one public `static` part, with no `init` and no
`needs`, can also be deployed to a bucket website
([Previews and deployments](/docs/guides/container-lifecycle)).

## Resources and profiles

A part may declare the most it may use:

```yaml
app:
  web:
    build: .
    public: true
    resources: { memory: 512M, cpu: 1 }
profiles:
  preview:
    web: { resources: { memory: 256M } }
  production:
    web:
      resources: { memory: 2G, cpu: 2 }
      environment: { LOG_LEVEL: warn }
```

- `memory` is a whole number of `M` or `G` (MiB, GiB), at least `32M`, swap
  included. `cpu` is a number of CPUs.
- A **profile** changes parts' `resources` (field by field) and
  `environment` (key by key), or a need's resources or catalogue size, for
  the deployments that use it. `default` is the parts as declared.
- Each deployment uses **one profile**, for this file and `secretspec.toml`
  alike: the one chosen for it (Settings › Secrets, or the deployment's
  card), else `preview` for a preview when either file declares it, else
  `default`. A file that does not declare the chosen profile uses its own
  default.
- **Targets apply it differently.** A Docker host and Portainer hold each
  part to exactly what it declares. App Platform runs each part on the
  cheapest instance size that covers it (a `768M` part runs on a 1G size)
  and refuses a part no size covers before anything is made. A bucket
  website has no runtime.
- **A part that declares no memory still gets a cap** on a Docker host,
  Portainer and in a preview: the memory default of the project's adopted
  pack, else 512M. A static part's file server gets a cap too. A cap is not
  a declaration: the `deploy/memory-declared` check still asks each part
  built or pulled to declare its memory, and that each part of the `main`
  deployment uses under 90% of its limit.
- **A generation keeps what it applied.** A rebuild or recovery of it runs
  with the same resources; a profile chosen since waits for the next
  deploy.
- **An operator may override a part's resources** on one deployment, with a
  reason, for example when production runs out of memory before a change
  can declare more. It needs the authority to deploy there, redeploys, and
  holds until it is cleared or a deploy's profile declares the same.
  `memory: none` lifts the limit on a Docker host or Portainer.
- Init steps take no resources.

A **need** is sized the same way, on the need or in a profile:

```yaml
needs:
  postgres:
    version: "16"
    resources: { memory: 512M }
profiles:
  production:
    postgres: { resources: { memory: 2G, cpu: 2 } }
```

- A need without its own declaration takes its catalogue kind's memory
  default.
- A need may instead select a catalogue **size**, which matches the limit
  to the service's own settings: `needs.mariadb.size: small` (128M) is the
  one shipped. `GET /api/v1/dependencies` lists each kind's sizes. A size
  and explicit `resources` on one need are refused together.
- A size for a need that runs no service (`llm`) is refused.
- On a Docker host a changed cap is applied to the running service, keeping
  its data; a limit taken away remakes it on the same volume. A changed
  startup command on real or unknown data is refused before anything
  changes.
- App Platform refuses a need's size: its managed Postgres is made at the
  integration's database size.
- One name is a part or a need, never both.
- A Docker host refuses a part or need larger than itself before anything
  is built or removed. The sum of the limits on a host may exceed it,
  because limits are caps, not reservations; the target's page then says
  it is overcommitted.

## Restore checks

A restore rehearsal boots a deployment's release on a copy of its data and
asks it its readiness, its front page and the data checks the contract
declares:

```yaml
rehearsal:
  checks: ["GET /healthz/data", "GET /api/orders?limit=1 expect 200"]
```

Each check is a path the app answers without its own sign-in and that reads
its database. It passes on 200, or on the status it names after `expect`.
A redirect is not followed: a path that sends a visitor to sign in fails a
check expecting 200, and passes only one that names the redirect's status
(which then proves little). AppTown records only the status. Without a data
check, a rehearsal reads "passed: booted and ready; no data check
declared". In a rehearsal only the public part starts, on a network of its
own that nobody outside reaches, and `FORGE_ORIGINS` holds the source
deployment's address, so the app accepts requests made as if to it. Which secrets a rehearsal gets is
in [Secrets](/docs/guides/secrets).

## On your own computer

`forge dev` runs the same contract on your machine: the needs, with their
variables written to `.forge/dev.env`, or the whole app in containers. See
[Running your app while you work](/docs/guides/how-apps-run). It is useful
evidence; the preview and the deploy still boot the app from its contract
on their own.
