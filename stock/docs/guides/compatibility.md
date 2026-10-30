# What 1.0 promises

This page says what an AppTown installation can rely on through the 1.x
releases, and what it cannot. "Stable" means a 1.x release keeps it working: a change that
would break it waits for 2.0, or the old form keeps working beside the new one.
Anything this page does not list is not promised.

## Stable through 1.x

**Your app's contract.** Everything [the app contract](contract-reference.md)
promises: `.apptown/app.yaml` with `version: 2`, and version 1 (or no
version) read as version 1 always has been, what a build sees, the
environment, the startup order, the front door and health, target by target
as it states them, except the limits listed below. A higher version is
refused by name, never guessed at. The names of the variables AppTown gives
an app, and what they mean, are promised; their exact values (addresses,
credentials) are not. AppTown's contract is its own: a future importer or
exporter for another format, such as [Score](https://score.dev), would not
change it.

These limits of 1.0 may be lifted or corrected in a 1.x release, and nothing
else in the contract will change that way:

- **Fields refused today** may be accepted: health `period`, `timeout`,
  `failureThreshold`, `successThreshold` and `checksDeps`; a `tcp` or `exec`
  readiness; `seed`; `dataDir` on a part that is not public. A release that accepts one says what it does.
- **Health settings accepted but not used everywhere** may start being used:
  App Platform ignores `expect`, `requestContext` and `startupGrace`; a
  `tcp` liveness on a Docker host connects to the public part's port, not a
  port it names; liveness is used only on a Docker host, `health` not at all
  on Portainer, and a non-public part's `health` only on App Platform; a
  non-public part's `healthcheck` may be replaced by its `health` block, with
  notice.
- **Commands on App Platform** keeping a list's arguments as written.
- **Startup order on Portainer:** init steps run one after another, and the
  other parts wait for them, as on a Docker host.
- **Where values arrive:** an attached integration's values given to parts
  other than the public part, and on targets other than a Docker host and
  a bucket website; `PORT` given to the public part on Portainer and App
  Platform; App Platform's order of precedence made the same as the other
  targets'.
- **`APPTOWN_ORIGINS` gaining entries** (a custom domain, for example), in
  the syntax the contract states.

**What each need gives your app.** The variables each catalogue need gives an
app, what they mean, and what the app may rely on, as
[What each need gives the app](services-and-data.md) lists them. A
target may learn to fulfil a need it refuses today; a need's variables and
their meaning do not change. A need declared without a `version` gets the
default its catalogue entry has in 1.0 (PostgreSQL 16, MariaDB 11.4.13,
Redis 7) in every 1.x release: a newer default arrives only with a new major
version of AppTown, so a deployment's data never meets a newer server it did
not ask for. Write `version:` to choose another.

**Your ways of working.** Workflows (`apiVersion: apptown.dev/v1`), packs,
policies, guidance blocks and their checks, in the shapes the
[formats guide](ways-of-working-formats.md) gives. A version you saved is read
as it was saved: a pack version keeps the blocks, workflows and policy
definitions it pinned; a run keeps the workflow version it was admitted under.
A check's `appliesTo` words, and what not applicable means (never a pass,
never an error, listed on the change), are promised with the shapes.

**The command line's core loop.** The command names and their arguments for
`new`, `fix`, `quick`, `plan`, `build`, `land`, `show`, `changes`, `wait`,
`approve`, `release`, `complain`, `executing` and `version`, and their exit codes:

| Command | Exit codes |
| --- | --- |
| `apptown wait` (a run or change) | 0 finished, 1 failed, 2 needs a person |
| `apptown wait operation` | 0 succeeded, 1 failed or cancelled, 4 unknown: look before asking again |
| `apptown executing` | 0 nothing would be interrupted, 3 something would (a step or a running operation), any other code: AppTown cannot say |
| any command that sends a change | 4 the change was sent but the answer was lost: look before trying again |
| any command | 64 the command line was wrong (an unknown command or flag, a missing argument, an argument of the wrong shape), and no change was sent; 69 AppTown could not be reached; 1 any other error |

`release` is promised in the form `release <slug> <deployment>`. Bare
`release <slug>` and its `-domain` release to the intranet, which is
Portainer, an experimental target: they are experimental too.

Each of these commands except `executing` takes `--json`: it prints one JSON
document whose field names [The command line's JSON](cli-json.md)
lists, never follows or asks, and keeps the exit codes above. Those fields are stable; a field may be
added. A field whose values the guide lists (a state, an outcome) may gain
values: a script treats one it does not know as "look at the change". What a
command prints for a person may change, so a script reads `--json` and exit
codes, not printed text.

**apptownd's settings.** The flags below, what `apptownd -h` says each
does, and each one's environment variable (`APPTOWN_` and the flag's name in
capitals, `-` as `_`: `-db-url` is `APPTOWN_DB_URL`); a flag on the command
line wins over its variable. These are what AppTown needs to start and what
only the machine's owner decides. What an administrator changes while
AppTown runs is set in AppTown itself, not here.

`-addr`, `-data`, `-db-url`, `-auth`, `-auth-header`, `-auth-proxy-secret-env`,
`-open-on-network`, `-trusted-proxies`, `-local-tls`, `-admins`,
`-public-host`, `-tier-domain`, `-console-addr`, `-console-url`,
`-console-cli`, `-inference-addr`, `-inference-url`, `-inference-networks`,
`-worker-url`, `-worker-cert`, `-worker-key`, `-worker-ca`,
`-no-local-docker`, `-private-docker`,
`-git-import-allow-private`, `-agent-image`,
`-restore-gate-image`, `-task-cpus`, `-task-memory-mib`, `-task-pids`,
`-chat-cpus`, `-chat-memory-mib`, `-chat-pids`, `-import-bundle-max-bytes`,
`-otlp-endpoint`, `-otlp-protocol`, `-disable-reap`, `-verify-only` and
`-reset-password`. `-version` is promised too, on the command line only:
`APPTOWN_VERSION` names the release the install scripts and the one-server
compose file use, and does not make apptownd print its version.

In the one-server compose file, `APPTOWN_DOMAIN` and
`APPTOWN_VERSION` in `.env` are promised too.

**Record identities and links.** Every record (a change, a run, a chat, a
release, an operation) is identified by a UUID, and a link to a record uses
it. A project's changes and runs are also numbered within the project and
shown as `slug#12`; you can type that form where a change or a run is expected.
Where a command takes either (`apptown wait`), the form names a change; give a
run by its UUID. A project's address can change; the old address then names
nothing, and once free another project may take it, so a `slug#12` written down
names whichever project has that address now. A project's first address stays
with it until it is purged. Names AppTown has already given things outside itself (branches,
backup paths, containers) keep the names they were given. How AppTown names
and labels what it makes from now on (containers, volumes, networks,
Portainer stacks, Docker labels, App Platform apps, buckets) is not promised:
find them through AppTown, not by their names.

**Which versions work together.** Two protocol numbers decide it, as
the "Versions" section of [Upgrading](upgrading.md)
explains: AppTown refuses a CLI, and a worker refuses an AppTown, outside the
supported range, with a sentence naming both sides and what to install. A
worker keeps serving the previous protocol for one release, so a worker can be
upgraded first.

**Your data across upgrades.** Every 1.x release upgrades the database and data
directory of the previous 1.x release in place, through the upgrade that
[Upgrading](upgrading.md) describes. The way back is the database dump and
the data-directory copy taken before the upgrade, restored with the previous
release; work done after the new release's first write is not in them.

## Experimental in 1.0

- **The HTTP API beyond the above.** `/api/v1` is versioned, and the web UI and
  the CLI are its clients, but its routes and response shapes may change in a
  1.x release. The release notes name each change (a release is not built
  without them). Every route is described,
  marked experimental, in the OpenAPI document AppTown serves at
  `/api/v1/openapi.json`. Build on the CLI where you can.
  Some routes still use older words: a chat's routes are under `/console/` and
  `/transcript/`, and a deployment's secrets under `/secrets/{deployment}`
  rather than `/deployments/{name}`.
- **The MCP tools** a coding agent reaches at `/mcp/user`: their names,
  arguments, descriptions and answers may change in a 1.x release. The
  release notes name each change.
- **The backup-set manifest.** The file a backup set carries to describe
  itself (`flokkit.backup-set`) is AppTown's own record, not a promised
  format: restore a set with AppTown, which reads the sets it made.
- **Approvals given in advance** (an approval's `inAdvance`), as the
  [formats guide](ways-of-working-formats.md) says.
- **Anything a guide marks experimental**, and the stock content listed under
  "Stock content in 1.0" below as Experimental.
- **Commands outside the core loop.** `apptown help` lists them under "Not
  promised in 1.x".

## Stock content in 1.0

What AppTown ships, and how far it has been proven. An Experimental item
says so at the start of its description, wherever you choose it.

**Supported**: shown to work on an AppTown like yours.

- Packs: `server-base`, `go`, `django`, `rails`, `postgres`, `vite-react`,
  `server-rendered`, `frontend-only`, `html-game`, `conventions-only`, and the
  `apptown` platform pack.
- Needs: `postgres`, `mariadb` and `s3`, fulfilled on a Docker host.
- Targets: the Docker host, built in or attached over SSH.
- Workflows: `land`, `ship`, `quick-change`, `implement`, `fix-bug`,
  `plan-execute`, `dependency-uplift`, `adopt-integration` and
  `retire-integration`.
- Integrations: error reporting (GlitchTip), GitLab, the pi agent harness and its model providers, the
  built-in secret store (its key is kept in the same database as the values,
  so it is not a vault: see [Secrets](secrets.md)) and the Infisical secret store, package-registry mirrors, container
  registries, and the automatic scanners (osv, gitleaks, sbom, trivy,
  trivy-license).

**Experimental**: useful, not yet proven. A *soak* below means two weeks of
use in production by the maintainers.

| Item | What it lacks |
| --- | --- |
| Packs `java`, `node` | a new app shipped since their landing fixes |
| Pack `wordpress` | an app shipped from the current starter |
| Pack `dotnet` | a soak, and a mirrored SDK download |
| Pack `rust` | a soak with a clean image scan |
| Packs `godot-3d`, `video-game` | an app shipped from the current starter |
| Need `redis` | an app shipped with it |
| Need `llm` | a deployed app that used its own key |
| Targets Portainer, App Platform, bucket website | a qualification that released a real app to them |
| Workflows `research`, `excavate`, `product-spec` | more use |
| Customer feedback integration | more than one project, and a service that signs in |

**No longer shipped.** A release can stop shipping a stock workflow, guidance
block, policy or pack. On upgrade it is no longer offered: it leaves the
libraries, and a pack is turned off. A project that already uses it keeps it,
its checks keep running, and its page says "No longer shipped". 1.0 does not
ship the `build`, `change-request` and `patch-dependencies` workflows, the
`mr-requires-secrets-scan` policy, the `data/sqlite-everywhere` guidance, or the
`sso`, `telemetry` and `dynamodb` needs. A stock need is removed outright: an
app that declares one is refused as asking for an unknown need.

## Not in 1.0

High availability and more than one control plane; running untrusted
projects side by side on one worker; signing in through an identity provider
(OIDC) and syncing its groups; signed evidence export; importing a project
from another AppTown; more than one replica of a part; deployment targets
beyond those above; agent harnesses other than pi.

## How things change in 1.x

- A deprecation is announced in the release notes at least one minor release
  before the thing goes, with what to use instead.
- A format version is never redefined. A new shape gets a new version; the old
  one keeps being read, or is refused by name.
- A refusal names what was refused and what to do instead.

## Licence

Apache License 2.0 (`LICENSE`): use, change and run AppTown, including
commercially, under its terms. The name "Flokkit" is not licensed with the code
(`NOTICE`): a fork may use the code but not present itself as Flokkit.
