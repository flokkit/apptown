# The workflow map, and how to test through it

Status: **as built**, refreshed 2026-09-06. Written 2026-08-31;
the journey tier it describes is built and has grown; the era snapshot it
relies on predates the Postgres move and has not been re-cut (§5). Extends
`testing.md` with the tier between integration tests and live fire:
**journeys** — whole operational flows driven through the public API and
CLI, the way a person or agent actually uses the forge.

## Why this is tractable at all

The observation that is the whole strategy: the system vocabulary is
**closed**. There are a few doors work can enter through, one exit, and a
short list of governance nouns. The map is not "everything anyone might
do" — it is a small matrix, and both axes are enumerable from code, which
means the map can be a **test** (a completeness check that fails when the
vocabulary grows) instead of a document that rots.

## The map

### Axis 1 — the work verbs (the doors, one exit)

Every piece of work enters one way and leaves one way:

```
D1  simple change      (one sentence; the first-time user's flow)
D2  chat change        (a chat session on a change — the workshop)
D3  detailed change    (title, lane and body by hand)
D4  catalog change     (prepared, parameterised, promoted)
D5  import branch      (adopt existing work)
D6  coding agent       (a connected agent over MCP opens and drives a change)
    → change lifecycle: preview → gates → approval → LAND
    → main environment → RELEASE → intranet, or a cloud environment
```

D6 was added with MCP (2026-09-05); it is the same change and the same
landing, driven by an agent acting as the person who connected it.
Cross-cutting on every door: previews and the boot gate, approvals (means,
quorum, groups), overrides, rework loops, abandon, complaints, the
current-rules pause, and packaging (one rail: releasable = an image
contract or a Dockerfile in the repo; the forge builds Dockerfiles, it
never writes them).

### Axis 2 — the governance nouns and their lifecycle

Each noun has the same five-beat lifecycle:

```
create → adopt (pack or direct) → amend (propagates?) → deprecate → remove
```

Applied to: guidance blocks · library workflows · policies · packs ·
catalog changes · integrations and targets · accounts, groups, roles and
grants · model slots · project rosters · adoption revisions (the noun added
2026-09-06: amend never propagates on its own; a project moves only by
adopting).

### The matrix

The core suite is: for each governance beat, prove the work verbs still
function and **reflect** the change. Not every cell is distinct — many
collapse (amending guidance looks the same from D1 and D3) — but the
completeness check (`TestJourneyLedgerComplete`) enumerates the cells and
demands each is either covered by a journey or explicitly marked as
collapsed into another. A new step type, door or noun fails the check
until mapped.

### The weird scenarios (named, because they find what the matrix cannot)

```
W1  Stack migration: project on pack A, running env → pull pack B
    (adds and removes) → porting change lands → env serves throughout.
W2  Law changes mid-flight: a policy amended while a landing is parked
    (the current-rules pause, driven end to end).
W3  Forge upgrades under load: the aged-instance corpus (§5).
W4  Multiplayer: internal auth, rosters, quorum approval with a
    rejection, override, permission grants — as one story.
W5  Failure weather: reviewer rejects, rework rounds, merge conflict,
    wedged landing superseded, boot check fails and the helper is told.
W6  Uplift: a project prepares a new way of working, the move needs code,
    the uplift change carries it, landing flips the revision (built as
    unit tests in wow_test.go; not yet a journey).
```

## The strategy

### 1. The fake agent is the unlock

Journeys must be deterministic, fast and free. Real model calls are none
of those. The harness ships a **fake pi** (`testdata/fake-pi`): a small
program the executor runs instead of pi, which reads its prompt for a
directive (`[test: apply fixture-3]`, `[test: write report saying X]`,
`[test: fail]`) and performs exactly that — real file edits in the real
clone, real commits through the real engine, zero tokens. Agent steps
become scripted actors; everything around them (gates, evidence, merges,
deploys, containers) stays fully real. The same switch gives failure
weather on demand.

### 2. Drive only the public surface

Journeys speak API and CLI, never SQL — the rule set for the customer
feedback goal ("if you have to go into the database, make the CLI give you
the information"). A journey that needs a back door has found either a
missing verb (file it, add it) or a wrong journey. Every journey doubles
as a test **of** the vocabulary.

### 3. Harness shape

In-process `forged` (the App and the real handler on a loopback port), a
cloned test database, real Docker, a temp data directory, fake pi on
`PATH`, `-tags=journey`. Same conventions as the integration tier:
pid-salted names, `t.Cleanup`, a missing daemon fails loudly. Each journey
is a Go test that reads as a story; shared story steps (create a project,
land a change, release it) live in one file so a new journey is mostly
composition. Target: the full set under ten minutes, single journeys in
seconds.

### 4. Invariant probes: "keep working" is assertable

The stateful worry is not that an operation fails — it is that an
operation breaks **something else** that was fine. So journeys run with
standing probes, asserted continuously and at the end:

- environments that were serving stay serving (the W1 test polls the
  environment **through** the migration; measured downtime about 300 ms,
  the deploy's own container swap);
- the audit log only appends; done runs keep non-empty evidence;
- no 5xx from any read endpoint at any point;
- pinned copies are byte-identical before and after every propagation
  event.

### 5. The aged-instance corpus (the upgrade answer)

"Don't break people's stuff while changing the forge" means: current
binary, **yesterday's data**. After a release, the journey suite's final
data directory is archived as an **era snapshot**; the upgrade test boots
the current binary against each era and runs a smoke journey — read every
page-shaped endpoint, land a change, release it, pull a pack. Migrations
and the travel lessons get exercised against data that really is old.

**State today.** There is no era snapshot yet, and the era test is
**not running**. The first era was cut in the SQLite format, which the
forge no longer opens since its move to Postgres, so it was retired. The
test returns when a Postgres-era format exists: a data directory plus a
`pg_dump`, restored into a clone database before boot.
The stopped-service upgrade procedure in
`design/subsystems/postgres-migration.md` §9 is the manual version of the
same idea and is what protected the live instance meanwhile.

The capture and upgrade journeys were removed with the SQLite format
(fba9fe5b); re-establishing them means writing them again for the
Postgres-era format.

### 6. CI shape

```
unit + race         every push       minutes      (docker: the test database)
integration         every push       ~8 min       (docker-host runner; nightly on a Mac, every package: ~13 min)
journeys            every push       ~1 min       (docker-host runner, fake pi; nightly on a Mac)
browser, pi,        nightly on a Mac seconds      (Playwright, the pinned Pi, the forge-agent image)
 inference_docker
era upgrades        not run — removed with the SQLite format (§5)
```

The nightly run is `make nightly` (testing.md, "The gates the repository
holds itself to").

## Status

```
go test -tags=journey ./internal/forge/ -run TestJourney -v
```

Journeys: **KidStory** (D1, sentence → shipped → serving) · **PackEvolution**
(D4 and a second pack, greyed → pull → runnable) · **LawMidFlight** (W2:
park → pause → restart → override → shipped) · **Multiplayer** (W4:
internal auth, roster, quorum, three signed-in people) · **StackMigration**
(W1, re-pack under a standing probe) · **LedgerComplete** (the matrix,
every cell dispositioned) · **PackDeliversItsPromises** and
**ServerPacksDeliver** (a project made from each stock pack previews,
lands and serves). The era journeys (EraCapture, EraUpgrade) were removed
with the SQLite format in fba9fe5b and are not run (§5).

First-day yield, still the argument for the tier: the journeys found
deploy-before-merge (main environments served the *previous* landing's
content) within an hour of existing. Honest gaps: release to the intranet
and `scm/mr` need backing services (a throwaway Portainer, a GitLab); the
chat door (D2) has no containerised harness, so a real pi loading the
generated home and a real delegation round-trip are verified live only;
D6 is covered by unit tests of the MCP tools, not a journey; guidance and
pack deprecation remain vocabulary holes.
