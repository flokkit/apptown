# The technical interface between an application and the Forge

Status: code review — 2026-09-04. Reviewed against the tree of 4 September 2026,
including the work then in progress. This is an inventory of the interfaces the code
actually implements, not a proposal that every current behaviour is desirable.
Related: `../decisions/forge-native-contract.md`,
`../subsystems/migration-strategy.md`, `../subsystems/healthchecks.md`, `../subsystems/ingress.md`, `../subsystems/registries.md`, and
`../decisions/needs-and-fulfillment.md`.

## Purpose

An application is not coupled to the Forge through one file. It meets the
Forge at several technical boundaries:

```
repository tree
  -> contract and toolchain declarations
  -> build context / OCI image
  -> process, filesystem and environment ABI
  -> dependency and integration bindings
  -> network ingress and health probes
  -> conformance and evidence
  -> release identity and historical materialization
```

These boundaries are the application's real platform API. A Forge upgrade can
break an unchanged application by changing any of them, even if
`.forge/app.yaml` still parses. They should therefore be named, versioned or
tested deliberately rather than treated as implementation details.

## Summary

| Surface | Application supplies | Forge supplies | Current source of truth |
|---|---|---|---|
| Source tree | committed files and supported repository layout | isolated branch/workspace and landing | `gitrepo.go` |
| Runtime declaration | `.forge/app.yaml` | strict parser, validation and rendering | `internal/appcontract`, `internal/forge/deploy.go` and provider renderers |
| Toolchain | root `mise.toml` or supported mise alternative | mise runtime, mirrors, caches and PATH | `registries.go` |
| Packaging | `build`, `image`, or `static` per part | Docker build/pull or pinned static server | `deploy.go` |
| Process | command/image entrypoint; listen on declared port | container, `PORT`, restart and lifecycle | `deploy.go`, `executor.go` |
| Persistence | `dataDir` and migration/init intent | named volumes and boot ordering | `boot.go`, `deploy.go` |
| Dependencies | `needs` capability names and parameters | fulfillment, DNS and connection environment | `dependencies.go` |
| Integrations | code that reads contributed environment | provisioned/entered resource values and steering | `services.go`, integration adapters |
| Ingress | framework wiring that interprets the ABI | origins, proxy, secure signal and host routing | `vhost.go`, `ingress.go` |
| Health | per-part readiness/liveness intent | target-specific probe and restart controller | `health.go`, `health_controller.go` |
| Local development | the same contract and app commands | generated Compose, `.env` values and Procfile | `compose_gen.go`, `config_api.go` |
| Agent workspace | ordinary files, optional repo skills, requested output files | `/work`, steering, CLI, tokens and caches | `agent.go`, `pihome.go` |
| Evidence | testable tree, reports and scanner-compatible output | checks, scanners, artifacts and immutable verdicts | `teststep.go`, `scan.go`, conformance code |
| History | Git commits and contract at those commits | contract snapshots, release SHA and image digest | `contract_snapshots.go`, `deploy.go` |

## 1. Repository and Git interface

### The application-owned tree

The repository is the durable application unit. Repo-backed projects use a
Forge-owned hub repository and self-contained workspace clones. A Change owns
its branch until landing; the hub owns the default branch. Agent containers see
the clone at `/work` and normally have no ordinary Git remote.

The landing machinery stages the whole tree except `.forge`, then explicitly
force-adds:

- `.forge/app.yaml` — the live runtime contract;
- deletions of retired `.forge/compose.yaml` and `.forge/app.json` during
  migration.

All other `.forge` content is operational scratch or evidence and is excluded
regardless of the project's `.gitignore`. This is a security boundary: session
transcripts, generated credentials, steering, caches and attachments must not
enter source history accidentally.

### Repository paths with platform meaning

- `.forge/app.yaml` — committed runtime declaration.
- `mise.toml`, `.mise.toml`, or `.config/mise/config.toml` — opt in to the
  Forge-provided toolchain environment. Guidance currently says root
  `mise.toml`; the runtime recognizes all three.
- `.agents/skills/` — project-carried skill content that the generated agent
  home is allowed to load. Other project `.pi`/agent configuration is inert.
- `Dockerfile` — effectively the repo-root packaging recipe for build parts.
- `REQUIREMENTS.md` and ordinary application documents — not runtime ABI, but
  consumed by workflows and agents as governed source.

`.forge/REPORT.md`, `.forge/CHANGE.md`, `.forge/PLAN.md`, `.forge/SPEC.md`,
`.forge/EXEC.md`, `.forge/attachments/`, `.forge/resources/`,
`.forge/steering.md`, and `.forge/sessions/` are workflow/workspace conventions,
not application runtime files. Workflows may require a named file and harvest
it as an artifact even though it is not committed.

### Build-tree semantics

Docker builds do not simply receive the live directory. For a Git workspace,
the Forge creates a temporary archive from `git stash create` (or `HEAD` when
clean). This includes tracked edits but excludes untracked and ignored files.
Consequently:

- an untracked source file is invisible to a preview image;
- ignored development state and secrets are deliberately absent;
- behaviour in the warm agent workspace is not evidence that a cold build
  contains the same files.

This selection rule is part of the build ABI and should have a regression test
for every future build backend.

## 2. The committed runtime contract

The only live declaration is `.forge/app.yaml`. The live reader does not fall
back to Compose or `app.json`. The parser is strict: unknown fields fail, an
absent version means version 1, explicit version 0 fails, and a version newer
than the binary refuses with a specific error.

Top-level fields currently parsed are:

- `version`;
- `app`, a map of named application parts;
- `init`, ordered run-to-completion containers;
- `needs`, abstract dependency capabilities and parameters.

Every app part must declare exactly one of `build`, `image`, or `static`.
Exactly one part must set `public: true`. Part fields are `command`, `ports`,
`environment`, `dataDir`, `seed`, legacy `healthcheck`, structured `health`,
and `public`.

The contract describes intent, but the implementation still projects the
public part into an older single-application internal shape. Extra parts and
init containers are layered around that projection. This compatibility seam is
migration-sensitive until the internal representation is natively per-part.

### Contract tooling

The Forge exposes three application-authoring operations through its API and
CLI:

- fetch the JSON Schema;
- validate supplied `app.yaml` text structurally and semantically;
- render local-development Compose/environment output.

The parser is authoritative when schema and parser disagree. That disagreement
must nevertheless be treated as a platform bug because agents and editors are
told to rely on the schema.

## 3. Packaging interface

### `build`

A build part asks the Forge to build a Docker image. The effective current
contract is narrower than the syntax suggests:

- the normal recipe is the repository-root `Dockerfile`;
- multi-part build and init entries share a single image built from the
  repository root;
- the public single-part path can build its declared context, while the
  multi-part helper builds the repository root.

Independent per-part contexts or Dockerfiles are therefore not a reliable
current capability. Until implemented, validation should require the supported
shape instead of accepting declarations whose meaning changes by execution
path.

### `image`

An image part is self-contained. The workspace and toolchain layer are not
mounted into it. The Forge pulls it using project-scoped registry credentials;
an optional `command` overrides its normal command through `sh -c` on the
public-part compatibility path. Images used this way therefore currently need
a shell if a string command override is supplied.

A deployed release records both source SHA and image identity. Registry digest
is preferred; a local image ID is used for locally built images. Source SHA
alone is not proof of artifact identity.

### `static`

A static part is served by the Forge's pinned `caddy:2-alpine` image, with its
declared directory mounted read-only at `/srv`. It receives no application
toolchain, general workspace mount, persistent data volume, or ambient
environment. The runtime assigns container port 80 when none is declared.

## 4. Process and filesystem ABI

Server processes must listen on the container port declared in `ports`; when no
port is declared, the public server default is 8080. The Forge injects `PORT`.
Only the public part gets a host mapping, bound to `127.0.0.1`; non-public parts
are reachable by their part name on the private Docker network.

From-source execution runs in the Forge agent image with:

- workspace mounted at `/work`, which is also the working directory;
- `forge-cache` at `/forge-cache`;
- npm, Go module and Go build caches directed into that volume;
- declared mise tools installed before the command;
- `host.docker.internal` mapped to the host gateway.

Image execution does not receive `/work`, the toolchain, or build caches.
Application code must not depend on them.

Standing environment containers use `restart: unless-stopped`. Preview/run
containers are ephemeral and reaped. App code must not use container-local
writable storage for durable state.

## 5. Environment-variable ABI and precedence

The public app may receive values from five sources:

1. Forge topology (`PORT`, `FORGE_ORIGINS`);
2. generated toolchain/registry ambient environment for from-source execution;
3. attached project integrations;
4. fulfilled `needs` connection environment;
5. the part's declared `environment`.

`DATA_DIR` is injected last when `dataDir` is present so application-declared
environment cannot redirect the persistent mount. Need fulfillment is merged
over the contract environment before the public container is launched.

This precedence is not uniformly expressed across paths. The public container,
non-public parts, init containers, agent process-mode, and generated local
Compose assemble environment in different functions. Each target should use
one named precedence rule:

```
platform-owned immutable values
  > fulfilled capability values
  > attached integration values
  > app-declared defaults
```

If an application is meant to override a value, it is not platform-owned and
should be named accordingly. In particular, `PORT` and `FORGE_ORIGINS` should
not be overrideable accidentally by `environment` on one path while protected
on another.

## 6. Persistence, initialization and boot order

`dataDir` means the declared container directory is backed by a Forge-named
volume. Preview volumes are throwaway; standing-environment volumes survive
redeploys and host/daemon restarts. The exact volume name and host location are
provider implementation, while the mounted path and persistence class are app
ABI.

The implemented boot sequence is:

```
resolve needs and connection environment
  -> start/reuse needs and wait for declared health
  -> run init containers in order, each to successful completion
  -> start non-public app parts
  -> start the public part
  -> evaluate readiness
```

Init containers inherit fulfilled dependency environment and may add their own
environment. A non-zero exit stops boot. This is the migration/schema-change
interface applications should use.

The part-level `seed` field is parsed and advertised as a one-shot data seed,
but no runtime code consumes `AppPart.Seed`. It currently makes a promise the
system does not keep and should be implemented or removed from the public
schema immediately.

## 7. Dependency capability interface

Applications declare what they need, not a concrete container or cloud
resource. Each `needs` key resolves through the instance dependency catalog.
Parameters are scalar values, with a permissive `properties` escape hatch;
application values override catalog defaults during template interpolation.
Unknown capabilities refuse boot.

For the Docker provider, each provisioned need contributes:

- a private-network service with DNS alias equal to the capability name;
- optional readiness command and persistent emulator data;
- injected connection environment.

Stock vocabulary currently includes:

- PostgreSQL: `DATABASE_URL`, `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`,
  `PGPASSWORD`;
- Redis: `REDIS_URL`;
- S3: `AWS_ENDPOINT_URL_S3`, `AWS_ACCESS_KEY_ID`,
  `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`;
- telemetry cloud fulfillment: `OTEL_EXPORTER_OTLP_ENDPOINT`.

Each stock kind states these under `provides:`, with what each means; the
current contract is the services-and-data guide's "What each need gives the
app". These environment names and their meaning are application-facing ABI even
though their values vary by provider. Catalog changes that rename them require
compatibility planning just as contract changes do.

Register-only needs are accepted by the catalog but currently skipped by local
fulfillment. A declaration can therefore validate while contributing no local
mock or environment. That gap must remain visible to the author.

## 8. Attached integration interface

Project integrations contribute runtime environment and generated steering.
Built-in error reporting currently injects `SENTRY_DSN`, `GLITCHTIP_DSN`, and
`SENTRY_EXCEPTION_RESOLVER_ORDER`. Feedback and custom service kinds contribute
their adapter-defined variables. The container-registry integration is
engine-only and contributes no app environment.

Attaching a service does not itself change application source. A governed
Change wires the application to read the contributed environment and initialize
the relevant client. Detaching stops contributions but intentionally does not
delete the provider-side resource.

Adapter-defined environment keys form a runtime ABI and need stable ownership,
secret classification, collision rules, and versioning. Today unresolved
custom templates are omitted rather than injected as garbage; this is safe but
can make a required integration silently incomplete unless conformance detects
the missing contribution.

## 9. Network and ingress interface

The raw public-part host port is loopback-only. Legitimate browser traffic
reaches the app through the Forge virtual-host proxy (and, for released
intranet apps, the configured outer proxy). App routes are not protected by
Forge authentication merely because the Forge proxies them.

The current Forge proxy:

- selects an environment from the request `Host`;
- forwards the original Host;
- overwrites `X-Forwarded-Proto` with the public scheme;
- overwrites `X-Forwarded-Host` with the public host;
- applies `Cache-Control: no-cache` when the app is silent, and always for
  previews;
- supports WebSocket upgrade through Go's reverse proxy.

The Forge injects `FORGE_ORIGINS`, a comma-separated set of wildcard origins
for the known tiers, plus the ingress domain of an attached Docker host whose
own Traefik routes the app (`https://*.<domain>`). Framework-specific application code is expected to use
this for CSRF/trusted-origin configuration and to interpret the secure-request
signal according to its way of working.

The stable concept is the ingress ABI, not the present header spelling. The
loopback bind and header overwrite are part of its trust boundary: a client
must not be able to reach the backend directly and spoof the secure signal.

## 10. Health interface

Health is declared per part under `health.readiness` and `health.liveness`.
A probe selects exactly one of HTTP path, TCP port, or exec command and may
declare startup grace, period, timeout, failure threshold, and success
threshold. Readiness additionally carries `requestContext`, expected HTTP
status, and the `checksDeps` assertion. Liveness may not check dependencies.

For the current cold-boot/public-part path:

- no declaration defaults to `GET /` with the ingress front-door headers;
- `requestContext: direct` suppresses those headers;
- redirects are not followed;
- a declared status must match exactly;
- without `expect`, any HTTP response proves that a server answered;
- two successful answers separated by a settle period are required.

The standing Docker health controller evaluates only the public part's
liveness. It handles running-but-unhealthy containers because Docker restart
policy handles process exit, not failed health. It currently uses global
period/threshold/backoff constants rather than the declaration's `period`,
`timeout`, `failureThreshold`, and `successThreshold`; only startup grace and
probe target materially reach the live controller.

TCP and exec readiness are accepted by the schema but cold boot falls back to
the HTTP front-door probe. That is an explicit implementation gap: accepted
intent is not faithfully rendered.

## 11. Local-development interface

The dev API takes the working copy's `app.yaml`, rather than requiring a
commit. It can render:

- full container mode: app parts, init containers, needs, volumes, dependency
  ordering and health checks as generated Docker Compose;
- needs-only process mode: emulator Compose plus localhost-resolved connection
  environment and, for multiple command-bearing parts, a Procfile suggestion.

The generated Compose file is disposable and must not be edited. Local full
mode currently relaxes the platform-executed health rule by emitting
in-container shell commands; images therefore need the generated probe's tools.
It also represents front-door behaviour as direct local access, so ingress
behaviour is not fully reproduced by `docker compose up` alone.

## 12. Agent and workspace interface

This is not application runtime ABI, but it is a technical interface through
which application code is produced.

Docker agent steps receive:

- the repository at `/work`;
- a generated, isolated `HOME` under `.forge/pi-home`;
- the Forge CLI first on `PATH`;
- `FORGE_URL`, `FORGE_RUN_ID`, `FORGE_STEP`, `FORGE_SESSION_TOKEN`, and when
  applicable `FORGE_CHANGE` and `FORGE_PROJECT`;
- scoped SCM variables only when the workflow grants them;
- generated registry/mise configuration and caches;
- optional dependency emulators, sidecars and private Docker daemon;
- attached app-integration environment for implementation/Docker work;
- generated steering, pinned guidance, resources and project skills.

Console workshops additionally receive `FORGE_USER`, `FORGE_SESSION_ID`, and a
read-only `FORGE_MAIN_REMOTE` capable only of fetching that project's main.
Front-desk consoles mount the project read-only; Change workshops mount their
Change workspace writable.

The session token is scoped and revoked when the step ends. Application code
must never depend on these agent-only variables or on the presence of the Forge
CLI.

## 13. Conformance and evidence interface

Application trees are inspected by shell, tool, runtime and LLM checks supplied
by adopted, pinned ways of working. Tools conventionally see source at `/work`
and may write reports under `.forge`; SARIF paths are normalized back to
repository-relative paths. A check result is about an exact tree/check/evaluator
identity, not an ambient workspace.

Workflow outputs are often files rather than chat text. `artifact/require`
turns the named file into durable evidence. Tests, boot results, scan reports,
steering hashes, model identity and approvals become Forge records around the
code without becoming part of the application tree.

This surface still affects application authors: filenames, scanner formats,
check scopes, and acceptance criteria determine what the system can prove.
They should remain workflow/check contracts rather than leak into runtime code.

## 14. Historical and release interface

At landing and release, the Forge snapshots the canonical contract for the
exact commit SHA. Historical legacy Compose/app.json contracts are converted
once into app.yaml form. Snapshot status distinguishes present, absent and
invalid; the size cap is 256 KiB.

This preserves the declaration, not necessarily the old runtime. Recreating a
historical application also depends on the compatible contract parser, runtime
ABI, renderer/provider version, referenced images, dependency fulfillment and
data state.

Standing deployments record source SHA and image digest/ID. Those two values
are the minimum join between source history and executed bytes; neither should
be inferred from the other.

## 15. Material gaps found in the code review

These are current mismatches at the application/Forge boundary:

1. ~~**The advertised JSON Schema is behind the parser.**~~ Resolved 27
   September 2026: the schema lives beside the parser as
   `internal/deploy/appcontract/schema.json`, now describes `version`, `health`
   and `secrets`, and a test fails when its keys and the parser's differ.
2. **`seed` is a false promise.** It is parsed, documented and carried into
   `AppPart`, but no boot path executes it.
3. **Multi-part build semantics are narrower than the contract.** Parts appear
   to own independent `build` values, but current multi-part execution builds
   one root image and shares it. Validation should refuse unsupported shapes.
4. **Health fields are only partially honoured.** Cold boot does not execute
   declared TCP/exec readiness, and the live controller ignores declared
   period, timeout and thresholds.
5. **Environment precedence is duplicated.** Public parts, other parts, init
   containers and local Compose can resolve collisions differently. Platform
   ownership needs one renderer and conformance tests.
6. **Contract validation contains stale capability messaging.** It warns that
   non-public build parts do not run in preview/deploy, while the current boot
   path now starts them. Interface diagnostics are part of the interface and
   must track behaviour.
7. **Live source comments still name retired Compose as the active contract in
   places.** This does not change execution but makes future maintenance and
   migration work error-prone.
8. **Register-only needs validate but have no local fulfillment.** The local
   result can omit the declared capability without a hard incompatibility.
9. **`FORGE_ORIGINS` is injected as a global tier wildcard set.** It is not
   currently a precise per-application/per-environment origin set; framework
   adapters must understand the security consequence.
10. **The internal contract remains a public-part projection.** This is the
    seam behind several differences between the public part and other parts and
    should be removed before adding materially different deployment targets.

## 16. What should be versioned

Not every implementation detail deserves an application-visible version. The
following do:

- app.yaml syntax, validation and defaults;
- runtime ABI: filesystem mounts, working directory, process invocation and
  platform-owned environment;
- ingress ABI and trust/sanitization rules;
- health intent semantics and renderer capability;
- dependency/integration capability names and injected environment meanings;
- boot ordering and persistence guarantees;
- artifact identity and build-context selection;
- local-dev parity guarantees.

Generated steering and conformance definitions already carry their own hashes
or versions. Provider implementations and renderer versions should be recorded
with releases even where they remain invisible to app source.

## 17. Recommended compatibility fixture

Keep a small executable application fixture for every supported contract and
runtime-ABI generation. Each fixture should prove, across every target:

- strict parsing and default expansion;
- build-context contents and image identity;
- `PORT`, origins, dependency and integration environment precedence;
- private DNS between parts and needs;
- persistent data and ordered init migration;
- front-door header sanitization and framework secure-request interpretation;
- readiness, liveness and restart behaviour;
- local-dev equivalence within its explicitly narrower ingress scope;
- historical materialization or a precise refusal.

That fixture suite is the executable definition of the application/Forge
interface. The prose tells maintainers what it means; the suite prevents a
routine Forge cleanup from changing it accidentally.
