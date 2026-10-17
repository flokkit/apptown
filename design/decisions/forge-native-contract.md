# ADR: A forge-native contract; compose is a generated local-dev target

Status: **implemented** (2026-09-01). Reverses the "the contract IS a
constrained compose.yaml" decision taken earlier this session. Companion to
design/architecture/contracts.md (supersedes its on-disk-format section) and
design/decisions/needs-and-fulfillment.md (the needs model this builds on).

**As built:** `app:` is a map of named parts from the start (multi-container is
uniform, not retrofitted); each part uses compose conventions (build/image/
static, command, ports, environment) plus forge-native `dataDir`/`seed`/`public`,
with exactly one `public: true`. `needs:` entries take typed params plus a
`properties:` escape hatch (arbitrary KV) for esoteric provisioning params.
`forge config schema` / `forge config validate` let models self-serve the schema
and check a file; `forge dev up` generates and runs the local compose. Ingress/
front-door design beyond a single `public: true` is deliberately deferred.
Preview/deploy run the public part today; non-public build parts run under
`forge dev` and are a flagged follow-up (`forge config validate` warns).

## Context

We moved the runtime contract to `.forge/compose.yaml` — a constrained compose
file with `x-forge-*` extensions — on the theory that a compose file is a
familiar, runnable artifact. On use, the resemblance turns out to be shallow:

- **Compose only naturally describes the app's own parts** — the containers the
  app *is* (a web service, maybe a worker). That half is genuine.
- **Everything else is forge intent bolted onto compose's extension slot.**
  `x-forge-app`, `x-forge-static`, `x-forge-datadir`, and especially
  `x-forge-needs` are forge concepts wearing an `x-` prefix because we decided
  the file had to be compose. Dependencies already have a natural shape (the
  catalog's `capability` / `local` / `cloud` / `register`); flattening them into
  a header list is a downgrade.
- **Local dev is where it breaks.** `x-forge-needs: [postgres]` is not a compose
  service — the forge materializes the postgres/minio containers at preview time
  (`fulfillNeedsLocal`), not in the file. So `docker compose up .forge/compose.yaml`
  starts the app with **no database**. The file that markets itself as runnable
  compose is a lie the instant an app has a dependency.

The contract is really two different things fused into one syntax: how the app's
own containers run (compose-shaped) and what the app depends on + how it's wired
(forge-shaped). Forcing both into compose serves neither.

## Decision

**The canonical contract is a forge-native file. Compose becomes a generated
build target for local development.**

1. **`.forge/app.yaml` is the source of truth** — a small forge-native document,
   no `x-` prefixes:
   ```yaml
   app:
     build: .            # or image: <ref>, or static: ./dir
     port: 8080
     dataDir: /data
     env: { LOG_LEVEL: info }
   needs:
     postgres: { version: "16" }
     s3: {}
   ```
   A multi-part app (web + worker) expresses `app` as a map of named parts, one
   marked public. Dependencies live under `needs` as first-class entries with
   parameters — the same shape the catalog and design/decisions/needs-and-fulfillment.md
   already use.

2. **`docker-compose.yml` is generated, never authored.** `forge dev up`
   renders the contract to a real compose file at the repo root — the app
   part(s) plus each need's `local` fulfillment (a postgres container, minio, …)
   with the connection env wired in — then runs it. The file is **gitignored**
   and regenerated every time. But that is the *container-mode* gear; the
   default gear is lighter — see below.

2a. **Process-mode vs container-mode (design/subsystems/deploy-targets.md).** How the app
   runs is orthogonal to how its needs are fulfilled. Most of the time — the
   agent's inner loop, a human iterating — the app runs as *processes* and only
   its *needs* are containers. So `forge dev` has two gears: bare `forge dev`
   brings up the needs and writes `.env` (you run your processes against them),
   and `forge dev up` renders and runs the whole app in containers. Agent build/
   verify steps take `needs: true` — the forge fulfills the contract's needs onto
   the step network and injects the env, and the agent runs the app's processes.
   Containers are the medium of the *gates* (preview, deploy), not of iteration.

3. **The local machine is the fourth fulfillment target of the same needs.**
   The needs model already fulfills per environment: emulator containers in
   previews, terraform in cloud (planned), register-with-connector for SSO. "A
   compose file the dev's own docker can run" is one more renderer of that same
   binding — not a special case, just a new output format for the `local`
   fulfillment.

## Why generate-on-demand, not model-maintained

The alternative — commit `docker-compose.yml` and have the agent keep it in sync
with the contract — recreates exactly the failure this session just spent hours
killing: a second copy of the truth that silently drifts. A generated,
gitignored file that is rebuilt on every `forge dev up` **cannot** drift: there
is no sync discipline to forget, no conformance check to add, no rot. If a dev
wants it committed for CLI-less convenience, generate-and-commit with a
`# GENERATED by forge dev — do not edit` header and a freshness check — but the
default is gitignored and ephemeral.

## What this does NOT change

- The internal `appContract` struct is already format-agnostic — it is what
  deploy, preview, and the boot gate read. Only the **on-disk serialization**
  changes (compose+`x-` → `.forge/app.yaml`), plus a new compose **generator**.
- Preview and cloud fulfillment are unchanged: the forge fulfills needs directly
  (it does not go through the generated compose). Compose generation exists
  **only** for local-machine ergonomics.
- The dependency catalog is unchanged — and stops being a bolt-on. It becomes
  the centerpiece the generator consumes.

## Migration / compatibility

- Add a `.forge/app.yaml` parser producing the existing `appContract`. Keep
  reading `.forge/compose.yaml` and (legacy) `.forge/app.json` as fallback
  inputs during the transition — same "read old, write new" posture as the last
  migration.
- Boot migration converts each project's `.forge/compose.yaml` →
  `.forge/app.yaml` (app service → `app:`, `x-forge-needs` → `needs:`,
  `x-forge-static`/`x-forge-datadir` → native fields), and drops the compose
  file. Change worktrees migrate on their next contract edit.
- Re-steer, again: `skill/implementation`, `build.yaml`, `runtimeContractDoc`,
  the contract-reference / services-and-data guides, and the new-project flow
  move from teaching compose+`x-forge-needs` to teaching `.forge/app.yaml` +
  `forge dev`. (Pre-release tax; cheaper now than after.)
- `forge dev up` / `forge dev down` land in the CLI; the generated
  `docker-compose.yml` is added to the seed `.gitignore`.

## Consequences

- Dependencies get their natural home; the awkward `x-` headers are gone.
- Local dev genuinely works — `forge dev up` gives you the app **and** its
  databases/caches, wired, from one command.
- The contract is smaller and reads as intent, not as a constrained compose
  dialect a reader has to know the honored-key list for.
- One more re-steer of the guidance corpus (now cheap to do, thanks to stock
  guidance being repo-versioned).
- A dev without the forge CLI can't regenerate compose — acceptable given the
  CLI is the entry point; the generate-and-commit escape hatch covers it if
  needed.

## Open questions

- **Multi-part apps.** Exact shape of `app:` as a map of parts (which is public,
  inter-part `depends_on`, per-part env). Single-part is the common case; design
  the map so the single case stays a flat `app:`.
- **Custom app-part topology.** An app whose *own* parts need compose features
  the native `app:` doesn't express — is there an escape hatch (an
  `app.composeFragment`) or is that a signal to keep it simple? Lean: keep it
  simple, revisit if a real app needs it.
- **Where the generated file lives** — repo root `docker-compose.yml` (so bare
  `docker compose up` finds it) vs `.forge/`. Lean root, gitignored.
- **Name.** `.forge/app.yaml` vs `.forge/forge.yaml` vs `.forge/contract.yaml`.
- **Does `forge dev` also run register-style needs** (a mock OIDC locally) or
  only provision-style — ties into the register-executor slice.
