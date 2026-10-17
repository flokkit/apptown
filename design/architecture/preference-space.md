# Forge — Preference Space

Status: **as built**, refreshed 2026-09-06. The v2 draft (2026-08-24) mapped
the space of software preferences as a browsable index of named stances.
The model was built: stances are guidance blocks, rivalry is declared
metadata, adoption is a pinned import through a pack or an addition, and
the "profile" became the pack. The index in §2 is kept as the catalogue of
stances worth writing; the marks now say which exist as stock blocks and
which the live instance holds through its packs.

## 1. The model

| Concept | What it is | As built |
|---|---|---|
| **Stance** | A named, opinionated preference — broad (`arch/boring-technology`) or narrow (`lang/go/errors-are-values`) — written to be imported verbatim | a guidance block: markdown with front matter (`name`, `tags`, `conflicts`, `checks`), versioned append-only |
| **Domain** | Browsing category; the first path segment, plus an optional subdomain | the block's name prefix; the library page groups by it |
| **Rivalry** | Two stances that cannot be held together | `conflicts:` in the front matter. Prepare refuses a revision holding both; a project already holding both shows a condition only a person resolves |
| **Adoption** | A project opting in | a pinned import in the project's adoption revision, arriving through a pack or as an addition (`design/subsystems/ways-of-working-versions.md`) |
| **Profile** | A curated bundle of stances, an entry point | a **pack**: blocks at pinned versions, plus workflows, policies, a toolchain, starter files |

Naming grammar: `domain[/subdomain]/stance-name` — the name must stand on
its own when read aloud (`postgres-maximalism`, `fifty-line-budget`).
Rivals coexist in the store, each written as the best version of its own
argument. Stances are steering-sized: the stance, the compressed
rationale, how to deviate. **ADRs record adoption, not preference** (§4).

Scope: everything here is a **software preference** — it steers how
software is written in a project, including the prose around it. A block
may also carry **checks**, which is how a stance gets teeth: the
deterministic subset as shell (`no-file-database` greps the manifests for
SQLite drivers), the judgment subset as a model-judged rubric over named
files (`state-behind-needs`).

## 2. The index

★ = shipped as a stock block. ⭐ = held by the live instance's default pack
(`go-small`) or the platform pack. ⚔ = notable rivalry. Unmarked entries
are candidates: named here so the next block lands in a slot, not written.

### arch/ — system shape

| Stance | Gloss |
|---|---|
| `boring-technology` ★⭐ | proven tools by default; one innovation token per project |
| `modular-monolith` ★⭐ | one deployable; module boundaries, not network boundaries ⚔ `microservices` |
| `unix-philosophy` | small tools, text streams, composition over integration |
| `worse-is-better` | implementation simplicity beats interface completeness |
| `yagni` | no speculative generality |
| `hexagonal` | ports and adapters; the domain never imports the infrastructure |
| `functional-core-imperative-shell` | pure logic inside, effects at the edges |
| `event-sourcing` | state is a fold over an event log ⚔ `crud-is-fine` |
| `microservices` | independently deployable services per capability |
| `twelve-factor` | config in env, stateless processes, logs as streams |
| `local-first` | user data lives on-device; sync is a feature |
| `single-binary` | the deliverable is one static artifact |
| `explicit-over-implicit` | no magic; you can follow the call graph ⚔ `convention-over-configuration` |
| `gate-all-effects` | irreversible actions pass a gate (the forge thesis) |

### data/ — persistence

| Stance | Gloss |
|---|---|
| `networked-database` ★⭐ | state lives in a declared need (postgres, redis, s3), never a file on disk ⚔ `sqlite-everywhere` |
| `postgres-maximalism` ★⭐ | Postgres is the queue, the cache, the search index, the document store |
| `sqlite-everywhere` ★ | single-node software gets an embedded database and a file ⚔ `networked-database` |
| `boring-relational` | normalised schemas, foreign keys on, constraints in the database |
| `hand-written-sql` | SQL is a language you write, not one an ORM emits ⚔ `orm-embraced` |
| `migrations-forward-only` | versioned SQL applied forward; no down-migration theatre |
| `content-addressed` | identity is the hash |
| `backups-are-restores` | a backup exists when its restore is tested |

### build/ · deploy/ · repo/ · dev/ — the forge's own coupling

| Stance | Gloss |
|---|---|
| `build/container-image` ★⭐ | the app builds to an image from its own Dockerfile |
| `build/registries` ★⭐ | pulls go through the instance's mirrors |
| `build/toolchain-mise` ★⭐ | the toolchain is declared and installed by mise |
| `deploy/health-declared` ★⭐ | health is declared once in the contract and rendered per environment |
| `repo/no-build-artifacts` ★⭐ | build output is never committed |
| `dev/process-and-container-mode` ★⭐ | the app runs as a process for the loop and as a container for the gate |

### lang/selection/ — which language, per niche

| Stance | Gloss |
|---|---|
| `go-for-services` ★⭐ | anything that serves or daemonises is Go |
| `typescript-for-web` ★⭐ | web UIs are TypeScript |
| `django-fullstack` ★ · `rails` ★ · `node-fullstack` ★ · `java-spring-boot` ★ · `dotnet-aspnet` ★ · `rust` ★ | one block per stack the packs offer; each names the stack's conventions |
| `python-for-data` | ML, analysis, notebooks |
| `polyglot-per-niche` | best boring tool per niche, few niches ⚔ `one-language-everywhere` |

### lang/<language>/ — habits

| Stance | Gloss |
|---|---|
| `go/errors-are-values` ★⭐ | wrap with `%w`, no panics across boundaries |
| `go/formatting` ★⭐ | gofmt is not a discussion |
| `go/prefer-stdlib` | net/http is enough; frameworks are a smell |
| `java/spring-conventions` ★ | the Spring Boot way, not a bespoke one |
| `shell/fifty-line-budget` ★⭐ | past fifty lines or real logic, rewrite in Go or Python |
| `shell/shellcheck-always` | `set -euo pipefail`, linted |
| `python/uv-pinned` · `python/typed-always` · `typescript/strict-always` · `typescript/types-from-schema` | candidates |

### api/ · frontend/ · observability/

| Stance | Gloss |
|---|---|
| `api/rest-pragmatist` ★⭐ | JSON over HTTP verbs, versioned paths, no HATEOAS ceremony ⚔ `hypermedia-purist` |
| `api/sse-over-websockets` ★⭐ | server push is plain HTTP until traffic is truly bidirectional |
| `frontend/hypermedia-htmx` ★ | server renders HTML, sprinkle interactivity ⚔ `spa-react` |
| `frontend/spa-react` | client-rendered app, API-driven (what the forge's own UI is; not yet a block) |
| `observability/error-reporting` ★ | errors report to the bound provider; the boom test hook proves it |
| `structured-logging` · `metrics-when-needed` · `observability-wide-events` | candidates |

### game/ — the project classes for games

| Stance | Gloss |
|---|---|
| `client-only` ★ | the game is static files; no server ⚔ server-backed games |
| `no-toolchain` ★ | plain HTML and JavaScript, no build ⚔ `open-toolchain` |
| `open-toolchain` ★ | an open engine with a web export |
| `godot-web` ★ | Godot exported to the browser (`design/subsystems/godot-3d.md`) |
| `playable-everywhere` ★ | keyboard, touch and gamepad; any screen |
| `kid-readable` ★ | code a beginner can follow |
| `player-first` ★ | fun before architecture |

### deps/ · security/ · test/ · perf/

| Stance | Gloss |
|---|---|
| `deps/minimal-deps` ★⭐ | stdlib first; every dependency is a liability |
| `deps/lockfile-discipline` | every build is pinned and reproducible |
| `security/least-privilege` · `localhost-by-default` · `secrets-in-files` · `supply-chain-paranoia` | candidates; the forge enforces the first three on itself |
| `test/drive-the-real-app` · `classical-no-mocks` · `fast-suite-or-bust` | candidates; the forge's own `testing.md` holds all three |
| `perf/measure-first` · `cache-last-resort` | candidates |

### process/ · spec/ · docs/ · prose/ · cli/ · ai/

| Stance | Gloss |
|---|---|
| `prose/no-claude-speak` ★⭐ | banned LLM house style; plain verbs |
| `prose/terse-style` ★⭐ | lead with the outcome; short sentences |
| `prose/readme-plain` ★⭐ | the README is the front door, in plain words |
| `process/gated-releases` | promotion through tiers with gates (the forge way; enforced by the landing, not a block) |
| `process/adr-for-deviations` | ADRs when leaving the paved road (§4) |
| `spec/design-doc-first` · `spec/executable-specs` | candidates; `skill/spec-development` is the procedure |
| `docs/docs-as-code` · `docs/runbooks` | candidates |
| `cli/unix-conventions` · `cli/json-flag` · `cli/helpful-next-step` | candidates; the forge CLI follows them |
| `ai/agents-behind-gates` · `reviewed-as-human` · `transcripts-retained` · `steering-pinned` | the forge's own law, enforced by the engine; not blocks |

### skill/ — procedures

Not preferences, but the same store mechanics: `skill/*` blocks are
procedures an agent follows, assigned per `agent/pi` step and injected into
its system prompt at the version the project's revision pins. Shipped:
`skill/implementation`, `skill/spec-development`, `skill/excavation`.
Multi-file skills live in the repository at `.agents/skills`, pinned by the
same snapshot as the code. The `resource/*` kind was not built.

## 3. Browsing the store

How a person finds things, in the order the UI offers:

1. **Packs** (Ways of working › Packs) — curated entry points: `go-small`
   (the house style), `conventions-only` (the forge's own conventions and
   nothing else), the stack packs (`django-htmx`, `rails`, `node-fullstack`,
   `java-spring`, `dotnet`, `rust-service`), `frontend-only` (browser apps
   with no server), the game packs (`html-game`, `godot-3d`,
   `video-game`). The wizard offers them at project creation;
   adopting one later goes through prepare and adopt.
2. **The library** (Ways of working › Guidance) — every block by domain,
   with its size, its checks and which projects hold it.
3. **Tags** — front-matter tags cut across domains (`data`, `platform`,
   `game`).
4. **Rivalries** — a block's `conflicts:` line; the argument is clearest at
   the fault lines, and prepare shows the conflict before a project holds
   both.

## 4. Relationship to ADRs

Unchanged: **stances record preference; ADRs record adoption.** Generic
preferences never live as a project's numbered decisions. A project writes
a decision when it adopts against the grain, deviates from an imported
stance, or spends its innovation token — and `adr/require` keeps gating
those local decisions. A deviation with teeth is an **exclusion** in the
project's adoption revision, recorded and visible in the fleet view.

## 5. Store mechanics

The guidance library is the store. Adopting is a pinned import in an
adoption revision; compiles are deterministic and hash-recorded per run;
stock blocks sync at boot and a changed stock block becomes a new version
without moving any project. The v1 axis-named blocks were wiped during
ideation under the checkpoint rule (nothing reachable pinned them); since
adoption receipts exist, a rename is a new block plus a conflict or an
exclusion, never an edit of a name a receipt holds.
