# Application runtime contract

This page describes the native runtime declaration. Workflow evidence and deployment administration have their
own guides; they are not additional fields in the application's contract.

## Declare how the selected version runs

Commit **`.forge/app.yaml`** with the first runnable application revision. Forge
reads it with the selected source, including historical previews. Updating today's
contract cannot fix a missing contract in an older revision. The retired
`.forge/app.json` and `.forge/compose.yaml` forms are not runtime fallbacks.

`version: 1` is supported; omitting the version means 1. Explicit zero, unsupported
versions and unknown fields are errors. Use the instance's schema and validator:

```sh
forge config schema
forge config validate .forge/app.yaml
```

These commands contact your configured Forge API; they are not offline validators.
The parser lives in `internal/deploy/appcontract` in the Forge source tree.

## Server and static parts

```yaml
version: 1
app:
  web:
    build: .
    ports: ["8080"]
    public: true
    replicas: 1
    dataDir: /data
  worker:
    build: .
    command: ["node", "worker.js"]
needs:
  postgres: { version: "16" }
```

Each app part declares exactly one of `build`, `image` or `static`. `build`
selects a Docker build context; `image` selects an image; `static` selects files
for the file-server image. A static part cannot read environment variables, so
the file server also answers `/_forge/env.js` with the values an attached
integration gives the page, the ones safe for any visitor to read (never
secrets), as `window.__FORGE_ENV__`, and `/_forge/release` with the part and the revision
of the release it serves, which the forge checks after a deploy. The image
carries its file server's configuration, so it runs the same way on every
target. `/_forge/` is the forge's path: a file the app ships there is not
served. Exactly one part is public. Other fields include
`command`, `environment`, `dataDir` and structured `health`. `seed`
is accepted but reserved: nothing runs it, and the validator says so. Every
part runs wherever the app runs, in preview and in each deployment, built parts
included.
Consult the schema for supported health fields and target-specific behaviour.

`replicas` declares an app part's instance count. Omitted or `null` means `1`;
an explicit count must be a positive integer. **Only `1` is currently supported.**
For example, `replicas: 2` is refused as not supported during contract validation,
before build, provisioning or deployment; it never silently runs one instance.
This applies to public, static and worker parts. Replica counts are not profile
overrides and are not fields on init containers or needs. Load balancing,
multiple replicas and rolling deployments are not implemented by this field.

A restore rehearsal boots a deployment's release on a copy of its data, on a
host kept for that, and asks it its readiness, its front page and the data
checks the contract declares:

```yaml
rehearsal:
  checks: ["GET /healthz/data", "GET /api/orders?limit=1 expect 200"]
```

Each check is a path the app answers without its own sign-in and that reads
its database; it passes on 200 or on the status it names, answered by that
path itself: a redirect is not followed, so a path that sends a visitor to
sign in fails. The forge records only the status. Readiness should not touch the database, so without a data
check a rehearsal reads "passed: booted and ready; no data check declared".
The public part is told the deployment's own address in `FORGE_ORIGINS`;
other parts do not start.

A restore test, and a restored copy people can open, ask the same checks of
the release running on its own platform, through the forge's gate. There the
public part is told the copy's own address in `FORGE_ORIGINS` (the gate's),
and its readiness path is its platform health check on its private port.

Ports are container ports, not fixed host ports. An omitted public build/image
port defaults to 8080; static defaults to 80. A worker without a port stays a
worker. Declare the port the process actually listens on: Forge does not rewrite
the repository contract from Dockerfile `EXPOSE` at startup.

A client-only app can serve committed assets:

```yaml
version: 1
app:
  game:
    static: .
    public: true
```

This form serves the committed folder as it is and runs no build. A site
that is built first (Vite, a bundler, a game export) declares its build:

```yaml
version: 1
app:
  web:
    static:
      build: .        # the folder whose Dockerfile builds the site
      target: site    # optional: the stage holding the site (default: the last)
      output: dist    # the folder in that stage's files to serve
    public: true
```

The forge builds that stage, takes its `output` folder and serves it with
the same file server, on previews, deployments and releases, so the page
gets `/_forge/env.js` and `/_forge/release` as a committed folder does.
End the Dockerfile with a stage of files only (`FROM scratch AS site`,
`COPY --from=build /app/dist /dist`) so the build hands over nothing
else. The output folder may hold only folders and files: a symbolic link
in it, or on the way to it, is refused. The file server serves files that
exist and nothing else (no fallback to `index.html` for other paths), so
an app that needs path routes keeps its own server with `build:`. Such a
site must be the public part, and `forge dev up` does not run it: use the
app's own dev server. Local warm state is not part of a cold preview or
deployment.

An app whose only part is one public `static` part, with no `init` and no
`needs`, can also be deployed to a bucket website; see
[Previews and deployments](/docs/guides/container-lifecycle).

## Initialization, needs and secrets

`init` is an ordered list of run-to-completion containers. Each entry chooses
exactly one of `build` or `image`, with optional name, command and environment.
The runtime prepares needs, runs initialization and then starts app parts.
Build contexts are explicit; do not assume every init shares the public app's
image. A failed initialization prevents successful boot.

`needs` names catalog capabilities such as Postgres, with typed parameters and
provider-specific properties where supported. Each parameter and property is a
single value; a list or map there, or `properties` that is not a map, makes the
contract invalid. Target support determines what can be provisioned. Consume the
supplied connection environment rather than hard-code credentials or assume a
cloud service has a local emulator's hostname. The catalog, and what each need
supplies, is in [Services and data](/docs/guides/services-and-data).

Logical secrets are declared through SecretSpec; the contract's `secrets` metadata
can select the declaration, generation domains and delivery. Environment delivery
is supported; `file` delivery is refused. A declared secret still needs a suitable
binding or generation rule. No provider-key grant follows from declaring an app
need. See [Secrets](/docs/guides/secrets).

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
  included. `cpu` is a number of CPUs. A part that declares nothing has **no
  limit**, and the deployment's page says so.
- A **profile** changes parts' `resources` (field by field) and `environment`
  (key by key), or selects a catalogue size for a need, for the deployments
  that use it. `default` is the parts themselves; it is not declared under
  `profiles:`.
- Each deployment uses **one profile**, for this file and `secretspec.toml`
  alike: the one chosen for it (Settings → Secrets, or the deployment's card),
  else `preview` for a preview when either file declares it, else `default`.
  A file that does not declare the profile uses its own default.
- **Targets apply it differently:**
  - a Docker host and Portainer hold each part to exactly what it declares;
  - App Platform runs each part on the cheapest current instance size that
    covers it (a `768M` part runs on a 1G size), and a part no size covers is
    refused before anything is made;
  - a static site published to a bucket has no runtime.
- **A generation keeps what it applied.** A rebuild or rehearsal of it runs the
  same, and a profile chosen since waits for the next deploy.
- **An operator may override a part's resources** on one deployment, with a
  reason, for example when production runs out of memory before a change can
  commit more. It needs the authority to deploy that deployment. It redeploys
  what runs, holds over the profile until cleared, and is cleared by itself
  once a deploy's committed profile declares the same.
- Init steps take no resources.
- **Every project is held to it.** The platform block `deploy/memory-declared`
  checks that each part the forge runs from a build or an image declares its
  memory (judged on the contract, at every landing and in every review), and
  that each part of the standing main deployment uses under 90% of the limit
  applied to it (judged in reviews, where the target reports use). A static
  part is the forge's file server's and declares nothing. The block says how
  each runtime fits a limit (the JVM's `-XX:MaxRAMPercentage`, gunicorn and
  puma workers, Node's heap).

A **need** is sized the same way, on the need and in a profile:

```yaml
needs:
  postgres:
    version: "16"
    resources: { memory: 512M }
profiles:
  production:
    postgres: { resources: { memory: 2G, cpu: 2 } }
```

- `resources` on a need is not a parameter: the catalogue never sees it. A
  profile changes a need's resources or selects a catalogue size; its environment
  is the catalogue's.
- **A size for a need that runs no service** (a key the forge registers, such
  as `llm`) is refused, since nothing could hold it.
- **On a Docker host,** a changed resource cap is applied to the standing
  container, keeping its data. A limit taken away remakes the container on
  the same volume. A changed explicit startup command is checked first:
  real or unknown data refuses before resizing; synthetic data is replaced.
- **App Platform refuses a need's size:** its managed Postgres is made at the
  integration's database size.
- **Catalogue sizes match limits to startup settings.** Inspect
  `GET /api/v1/dependencies` for each kind's `sizes`. Only local-container
  MariaDB `small` (128M) ships initially. Select `needs.mariadb.size: small`,
  or `profiles.preview.mariadb: {size: small}`. It cannot combine with explicit
  resources on that need, including inherited profile values. Unsupported
  names and register-only/managed targets refuse before execution. The
  selector is not a free parameter, including under `properties`.
- **A generation records the selected name, command and cap.** Replay does
  not consult today's preset. The resources view shows the applied name;
  operator cap overrides remain separate and do not retune server settings.
  Existing real-data command changes require reviewed maintenance; selecting
  a size does not implement it. See [WordPress sizes](/docs/guides/wordpress)
  for the bounded measurements and limitations.
- **One name is a part or a need, never both:** they share the stack's
  network, each aliased by its name.
- **A Docker host refuses a part or need larger than itself** before anything
  is built, removed or started, previews included (a worker's own daemon is
  asked). Portainer is not asked this. The sum of every limit on the host may exceed
  it, because limits are caps, not reservations; the target's page then says
  it is overcommitted.

## Environment and toolchains

For the Docker public app, Forge supplies its port, configured public origins,
attached integration contributions, the prepared contract/need environment and
resolved consumer secrets. Secrets override committed placeholders; `DATA_DIR`,
when present, is applied last for the mounted volume. Do not treat this one
driver's details as a universal cloud ABI or assume undeclared host credentials
and tools are inherited.

`mise.toml` describes development tools. The Dockerfile/image supplies the build
and deployed runtime. Keep the intended versions aligned; Forge does not infer
or repair that alignment, and a pin alone does not prove reproducibility.
See [Project toolchains](/docs/guides/toolchains) and
[What chats and steps can use](/docs/guides/runtime-environments).

## Local development and historical versions

`forge dev` and its gears run the contract locally; see
[How apps run](/docs/guides/how-apps-run). Preview and deployment
independently build and boot the selected source; success in the local loop
is useful evidence, not shipping authority.

Historical versions need their own valid native declaration and available build
inputs. Forge's one-time history normalization is a dated maintenance operation,
not an automatic compatibility layer. Original application defects and vanished
external dependencies can still prevent a historical version running.

## Memory defaults on container targets

Fresh Docker-host and Portainer generations, and fresh local/worker previews,
receive a memory cap after profiles and operator overrides are applied. A positive
declaration wins; otherwise an app part takes its primary adopted pack's memory
default, a fulfilled service need takes its catalogue kind's default, and absent
defaults fall back to 512M. Independently supplied workflow sidecars keep explicit
positive memory or take 512M; their image or name is never guessed as a catalogue
capability. Key-only needs receive no service limit. CPU behavior is unchanged.

Static app parts remain exempt from application-authored memory declarations;
Forge's file-server container still receives a runtime cap. A default does not
satisfy `memory-declared`: it is a cap, not a declaration. Applied generation
records retain bytes and their default source, including adopted pack/version.
Rebuilds/recovery replay those recorded choices, including old unlimited records,
without substituting current defaults. Deploying Forge or publishing stock does
not resize running containers; a fresh generation takes the new rules.

App Platform keeps its selected finite instance sizes and managed database
configuration. Container defaults do not size its needs; explicit need sizes are
still refused. Bucket websites run no containers and take no synthetic cap.

Unlimited memory is available only through the existing authorized resource
override on Docker/Portainer targets: `memory: none`, with a reason. It is
recorded separately from an omitted memory override or DELETE/clear. Clearing
restores declaration/default on the next admitted redeploy. Omitting memory in
app.yaml cannot silently clear an explicit unlimited override. Provider-managed
sizes cannot be made unlimited by this setting.
