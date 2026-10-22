# The app contract

What an app must do to run on AppTown, and what AppTown does in return. It
applies on every deployment target unless a sentence names one. This page is
what 1.x keeps stable ([What 1.0 promises](/docs/guides/compatibility)).
Workflows, checks and approvals are not part of it; they have their own
guides.

## The files AppTown reads

| File | What it is for |
| --- | --- |
| `.apptown/app.yaml` | how the app runs: its parts, its needs, its startup steps |
| `secretspec.toml` | the secrets each part needs ([Secrets](/docs/guides/secrets)) |
| `mise.toml`, `.mise.toml` or `.config/mise/config.toml` | the development tools a chat or step installs ([Project toolchains](/docs/guides/toolchains)) |
| a `Dockerfile` in each build context | how a `build` part's image is made |
| `.agents/skills/` | skills the project gives AppTown's agents |

Everything else under `.apptown/` is AppTown's working space for a change
(reports, plans, chat sessions, attachments). It never lands on `main`, even
when it is committed: only `.apptown/app.yaml` travels.

AppTown reads `.apptown/app.yaml` from the commit it boots, so a preview of an
old commit uses that commit's contract. Fixing today's file does not fix an
older commit that has none.

## `.apptown/app.yaml`

```yaml
version: 2
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

- **`version`** is `2`. A file with `version: 1`, or none, is still read as
  version 1, which differs in two ways: memory written as `M` or `G` means
  MiB or GiB, and environment values never read need references. Another version, an explicit `0`, and any field the format does not
  have are errors that name it.
- **`app`** names the parts. Each part declares exactly one of `build` (a
  Docker build context), `image` (an image to pull) or `static` (files to
  serve). Exactly one part is `public: true`: the one people reach.
- **Names.** A part's or init step's name is 2 to 32 lower-case letters,
  digits and hyphens, starting with a letter and not ending with a hyphen.
  Parts, init steps and needs share one set of names, because every target
  runs them side by side. `app`, and names starting `init-` or `apptown-`,
  are the ones targets give their own services, so they are refused.
- A part may also declare `ports`, `command`, `environment`, `health`,
  `resources` and `replicas`; the public part may declare `dataDir`, and
  another part `healthcheck` (below). `replicas` is `1` or left out; a
  larger number is refused before anything is built. `ports` lists the one
  port the part listens on (`["8080"]`); a second entry, or a host port
  (`"3000:8080"`), is refused, because AppTown chooses where a part is
  published.
- **`command`** is a list, run as written, or a string, run as
  `sh -c "<string>"`: an image with a string `command` needs `/bin/sh`. On a
  Docker host and on Portainer a list's arguments are kept as written; App
  Platform takes a command as one line, its arguments joined by spaces, so
  an argument holding a space is not kept there.
- **`environment`** may not set `PORT`, `APPTOWN_ORIGINS`, `DATA_DIR`,
  `APPTOWN_RELEASE` or `APPTOWN_BROWSER_ENV`: AppTown sets them (see "The
  environment an app gets"), and an app's own value would win on one target
  and lose on another. Names starting `APPTOWN_` are AppTown's, in parts,
  init steps, profiles and secrets: a variable AppTown adds later takes one.
  A name is letters, digits and `_`, not starting with a digit. A value
  reaches the app exactly as written, so it is text, a whole number or
  `true`/`false`; anything else is refused and should be quoted: `"3.10"`
  (unquoted it would be the number 3.1), `""` for an empty value, a date.
- **`init`** is an ordered list of steps that run to completion before the
  app starts (see "Startup order" below). Each declares `name` and either
  `build` or `image`, and may declare `command` and `environment`. A secret
  is scoped to an init step by its name, so every step has one.
- **`needs`** names services from the catalogue (`postgres`, `mariadb`,
  `redis`, `s3`, `llm`) with their parameters. Each parameter is one value,
  and a need takes only the parameters its catalogue entry lists (for
  `postgres`: `version`, `database` and `user`), plus `disposable`, `size`
  and `resources`; any other is refused. The words `kind`, `prefix` and `as`
  are kept for naming two needs of one kind later.
  What each need gives the app is in
  [What each need gives the app](/docs/guides/services-and-data).
- **`secrets`** chooses the `secretspec.toml` declaration (`spec`, a file
  inside the project, written relative) and how values are
  generated and delivered. Delivery is by environment variable; `file`
  delivery is refused.
- **`profiles`** and **`resources`**: see "Resources and profiles" below.
- **`rehearsal`**: see "Restore checks" below.
- **Refused until a target uses them:** `seed` on a part (put a command
  that must run before the app starts in `init`), `dataDir` on a part that
  is not public, `healthcheck` on the public part, and the health fields
  "Health" names below. Each is refused by name, so a later release can give
  it a meaning without changing an app that already wrote it.

The schema and validator are on every AppTown:

```sh
apptown config schema
apptown config validate .apptown/app.yaml
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
- **Only the public part's `dataDir` and needs keep data.** Anything else
  a container writes is lost at the next deploy. App Platform has no
  volumes and refuses an app that declares a `dataDir`.

## The environment an app gets

Each part, and each init step, gets its own `environment`, with the
connection variables of its needs set over it, and then the secrets its
scope names. AppTown adds a few values of its own, which differ by target:

| Value | Docker host | Portainer | App Platform |
| --- | --- | --- | --- |
| `PORT` | every part that has a port | other parts that declare a port; not the public part | not set by AppTown |
| `APPTOWN_ORIGINS` | public part: the address patterns browsers use, as wildcards (`https://*.apps.example.com`) | public part, the same | public part: the app's own address |
| `DATA_DIR` | public part, when it has a `dataDir`, set last | public part, the same | — (no `dataDir`) |
| attached integrations: `SENTRY_DSN` (error reporting); `FEEDBACK_URL` and `FEEDBACK_APP_KEY` (customer feedback, Experimental) | public part | not delivered; the release records it | not delivered; the release records it |
| order of precedence | secrets over connections over the part's own `environment` | the same | the same, then the needs' credentials over the part's secrets |

So that an app runs the same everywhere:

- read `PORT` if it is set, and fall back to the port you declared;
- leave `PORT`, `APPTOWN_ORIGINS` and `DATA_DIR` to AppTown (the contract
  refuses them in `environment`);
- read every connection value from the need's variables;
- keep durable files in the public part's `dataDir` or in a need.

Values reach the app as written, with one exception: a version-2
environment value may read a value a need gives, as
`${needs.<need>.<VARIABLE>}`. This is for an image that expects its own
names for a connection:

```yaml
app:
  web:
    image: wordpress:6
    public: true
    environment:
      WORDPRESS_DB_HOST: "${needs.mariadb.DB_HOST}:${needs.mariadb.DB_PORT}"
needs:
  mariadb: {}
```

- `<need>` is a need the app declares, and `<VARIABLE>` one of the
  variables that need gives ([Services and data](/docs/guides/services-and-data)).
  Anything else is refused: any other `${needs.`, or a need the app does
  not declare, when the contract is read; a variable the need does not give,
  before a deploy.
- The need's own variables are still given; a reference adds a name.
- The value is the one the target gives that need, so it is the same on
  every target. On App Platform a wired value is sent as a secret, since a
  need's values often carry its credential.
- `$${needs.` writes the literal text `${needs.`. Nothing else in a value is
  special.
- An `llm` need's values arrive by registration and cannot be referenced
  yet: read `LLM_API_KEY` and `LLM_BASE_URL` by their own names.
- In process mode (`apptown dev`) every process shares one environment, so
  two parts that set one name from needs to different values are refused.

App Platform cannot deliver other `${…}` text as written: it replaces
`${NAME}` with its own variable of that name, in secrets too, so a deploy
there refuses an environment value or secret containing `${…}` (a
`$${needs.` included) before anything is made.

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

Use `APPTOWN_ORIGINS` for the trusted origins a framework checks forms and
CSRF tokens against, not a typed host name. Its syntax is stable: a
comma-separated list of origins, each `scheme://host` or
`scheme://host:port`, where a host may begin with `*.` to cover every name
under it. On a Docker host and Portainer it holds patterns that can cover
more than this one deployment; on App Platform, the app's own address. A
later release may add entries (a custom domain, say), never change the
syntax: split on commas, and treat a leading `*.` as a wildcard.

## Health

```yaml
health:
  readiness: { http: /healthz, expect: 200, startupGrace: 60s }
  liveness:  { http: /livez }
```

Readiness is an `http` probe (a path). Liveness names one target: `http`
(a path), `tcp` (a port) or `exec` (a command run in the container). Either
may give `startupGrace`; an `http` probe may give `expect` (a status), and
readiness `requestContext` (`frontDoor`, the default, or `direct`). A
liveness probe restarts the part, so it should not check other services:
restarting it for a database's fault makes things worse.

`period`, `timeout`, `failureThreshold`, `successThreshold`, `checksDeps`,
and a `tcp` or `exec` readiness are refused: no target uses them yet.

A part that is not public may also declare `healthcheck:`, a shell command
run in its container (exit 0 is healthy): its container health check on a
Docker host and on Portainer. Its `health` block is used only on App
Platform, as the readiness path of a part with a port.

What each target uses:

- **Docker host.** Readiness, checked at every boot, is an HTTP GET of the
  declared path, `/` when none is declared, sent as the front door would
  send it unless `requestContext: direct`. Liveness is watched on the public part of a
  standing deployment, which is restarted when it fails: an `http`
  liveness is a GET of its path, an `exec` liveness runs its command in the
  container (exit 0 is alive), and a `tcp` liveness, or none declared,
  connects to the part's port. For any HTTP probe, with `expect` the
  answer must be exactly that status, without it any HTTP answer counts,
  and redirects are not followed.
- **Portainer.** No `health` block is used; another part's `healthcheck:`
  is.
- **App Platform.** The readiness path only, with the platform's fixed
  timings, and a port check when no path is declared. `expect` is not used.

The readiness answer should not touch the database: use `rehearsal`
checks for that.

## Static sites

A `static` part serves files with AppTown's file server. It runs no code of
yours and reads no environment, so `environment` (in the part or a
profile), `command`, `dataDir` and `healthcheck` are refused on it; its
readiness is `health`, and `resources` and `ports` apply as to any part.

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
must be the public part, and `apptown dev up` does not run it: use the app's
own dev server.

The file server serves files that exist and nothing else; there is no
fallback to `index.html` for other paths. An app that needs path routes
keeps its own server with `build:`. The file server also answers:

- `/_apptown/env.js`: the values attached integrations give a page, the ones
  safe for any visitor to read (never secrets), as
  `window.__APPTOWN_ENV__`. They are delivered on a Docker host and a bucket
  website; Portainer and App Platform refuse a static site that has them.
- `/_apptown/release`: the part and revision it serves, which AppTown checks
  after a deploy.

`/_apptown/` is AppTown's path: AppTown supplies those two files in place of
any the app ships, so keep the app's own files out of it.

The same holds for every app, not only a static site: AppTown may answer
paths under `/_apptown/` in front of any app (an access gate in front of a
deployment signs visitors in at `/_apptown/enter`), and may add paths there
in a 1.x release. The one path an
app serves under it is error reporting's test hook, `/_apptown/boom`.

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
    resources: { memory: 512Mi, cpu: 1 }
profiles:
  preview:
    web: { resources: { memory: 256Mi } }
  production:
    web:
      resources: { memory: 2Gi, cpu: 2 }
      environment: { LOG_LEVEL: warn }
```

- `memory` is in Kubernetes binary units: a whole number of `Mi` or `Gi`, at
  least `32Mi`, swap included. A bare `512M` is refused with the spelling to
  use, because Kubernetes and Score read `M` as decimal megabytes; a
  version-1 file still reads `M` and `G` as MiB and GiB. `cpu` is a number of
  CPUs (`0.5`, `2`) or millicores (`500m`).
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
  cheapest instance size that covers it (a `768Mi` part runs on a 1Gi size)
  and refuses a part no size covers before anything is made. A bucket
  website has no runtime.
- **A part that declares no memory still gets a cap** on a Docker host,
  Portainer and in a preview: the memory default of the project's adopted
  pack, else 512Mi. A static part's file server gets a cap too. A cap is not
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
    resources: { memory: 512Mi }
profiles:
  production:
    postgres: { resources: { memory: 2Gi, cpu: 2 } }
```

- A need without its own declaration takes its catalogue kind's memory
  default.
- A need may instead select a catalogue **size**, which matches the limit
  to the service's own settings: `needs.mariadb.size: small` (128Mi) is the
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
own, with no published port and no address. Software on the host it runs
on can still reach it: that is a host kept for rehearsals, or the app's own
host when none is kept (see [Deployment backups](/docs/guides/deployment-backups),
"Where restore rehearsals run"). `APPTOWN_ORIGINS` holds the source
deployment's address, so the app accepts requests made as if to it. Which secrets a rehearsal gets is
in [Secrets](/docs/guides/secrets).

## On your own computer

`apptown dev` runs the same contract on your machine: the needs, with their
variables written to `.apptown/dev.env`, or the whole app in containers. See
[Running your app while you work](/docs/guides/how-apps-run). It is useful
evidence; the preview and the deploy still boot the app from its contract
on their own.
