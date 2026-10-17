# The change catalog

Status: **built**, noted 2026-09-06 — the terminology settled here is live:
catalog changes are canned, versioned, parameterised definitions
(`catalog_changes`, `catalog_change_versions`), drafted, evaluated,
promoted and applied to any project (`forge catalog`, `forge apply`;
`../requirements/requirements.md` C3). The line below saying nothing is
built is the record as it was written.

Settled 2026-08-29. Status: DESIGN — terminology settled,
nothing built. Prior art in-tree: dependency-uplift, adopt-integration,
retire-integration are hand-built catalog changes that predate the word.

## The seam

The forge cans the PROCESS of a change but not the CHANGE itself. A lane
(workflow, role: working) fixes the how — agent, boot gate, review,
approval — and the what arrives as freetext in every work item. Running
`forge fix "..."` twice with the same words is two unrelated events: the
recipe lives in someone's head or shell history, unversioned, unaudited,
drifting.

One lane in the fleet quietly disproves the pattern: dependency-uplift
bakes the what in ("bring dependencies current, verify, report") and
needs no prompt at all. adopt/retire take a capability parameter instead
of prose. Those are catalog changes — each built as a bespoke lane
because we had no cheaper container for the idea.

## Terminology

- **Workflow** — canned process. How a change gets made and verified.
  ("Lane" is RETIRED (2026-08-29): it was a metaphor synonym for
  "workflow with role: working", and one concept gets one word. Where
  the distinction matters, say "working workflow".)
- **Catalog change** — a named, versioned definition of a specific
  change: what to do, how to know it was done right. Lives in the
  **change catalog**; its `workflow` field names the workflow that
  runs it.
- **Change** — one instance: a catalog change (or freetext task) applied
  to one repo, with its ordinary branch, gates, evidence, and landing.

Naming principle: the name must not promise what only evidence can
— "routine" promised low risk, "proven/validated" promised a verdict.
"Catalog" claims exactly what is true by construction: it is written
down, versioned, and findable. The receipts do the rest. No metaphor
vocabulary (recipes, jigs, harbors): six months later nobody should have
to ask what a metaphor means here.

ITIL anchor, said once: this is what ITIL calls a standard change —
pre-authorized, repeatable, lighter per-instance ceremony. Governance
rooms recognize the shape instantly; we use our own word because
ServiceNow wore that one out.

## What a catalog change is (v1 shape)

A versioned library entity, same store/pin pattern as guidance blocks
and stock workflows (stock + custom, origin-pinned, drift visible):

- **name / description** — `uplift/go-version`, `hygiene/license-headers`.
- **params** — typed, few: `{goVersion: "1.27"}`. Params fill the
  template; no freetext body.
- **prompt template** — the canned work-item body the agent receives.
- **lane binding** — which lane runs it (default quick-change; a catalog
  change may demand a heavier lane).
- **skills** — skill/* blocks the agent gets, as lanes do today.
- **paired verification (optional but the point)** — guidance checks
  (design/subsystems/guidance-checks.md) shipped WITH the change definition: the
  change and the proof-it-holds travel together, versioned together. A
  catalog change that introduces a convention ships the check that
  enforces it from then on.

## Deterministic core, agent shell

Where a deterministic engine exists for the transform, the catalog
change should USE it — OpenRewrite for Java/dependency rewrites,
codemods, go-fix-style rewriters — with the agent as glue and verifier
around it, not as the editor. An agent freehanding a mechanical rewrite
across 17 repos is 17 chances to be creative; OpenRewrite is zero. The
definition can therefore specify tool-first execution: run the engine,
then the agent reconciles, builds, and reports. Agents should use such
engines for most uplifts.

## Lifecycle: draft → catalogued

A definition enters the catalog only after being exercised for real —
run against at least one repo, gates passed, receipts kept. Before that
it is a draft change: invocable by hand, not listed, not fleet-eligible.
Promotion into the catalog is an admin act that records the evaluation
run(s) as evidence. The catalog listing IS the claim "this has been run
and watched"; the name stays quiet and the record speaks.

## Invocation

`forge apply <catalog-change> [-p proj] [--param k=v]` creates an
ordinary work item from the template and starts the bound lane. From the
engine's view nothing is new — a catalog change is a work-item factory.
Deliberate: the catalog adds a vocabulary layer, not a second execution
path. Friendly verbs remain as aliases (`forge uplift` =
`forge apply uplift/deps`).

## Why it matters: catalog × projects is the fleet story

One catalog change applied across N repos, each producing an ordinary
change with ordinary gates and a receipt per repo — that is the
institutional-software maintenance pitch in one sentence ("the
compliance change of the month, run across every repo, with evidence").
A fleet apply is just N invocations; per-project serialization and
landing queues already handle the traffic. A fleet VIEW (one catalog
change, N changes, who's landed / blocked / failed — Sourcegraph Batch
Changes got this screen right) is the only genuinely new surface, plus
migration-style bookkeeping: which repos has this been applied to.

## Retcon plan (when built)

dependency-uplift, adopt-integration, retire-integration become stock
catalog entries bound to their current lanes; the lanes stay. Existing
freetext verbs (fix, quick, build, plan) are untouched — the catalog is
for changes worth repeating, not a gate on ad-hoc work.

## Evaluation log

- **uplift/deps v1 — CATALOGUED** (promoted 2026-08-29): pure
  retcon of the dependency-uplift lane; its fleet run history is the
  receipt.
- **uplift/go-version v1 — evaluated on sloccount-ng** (change 78, run
  113, landed): the model pin verifiably won (evidence records the pinned
  model regardless of project slots); the toolchain bump was correct and
  build+tests+vet passed. Two lessons, both now in v2's prompt: (1)
  round 1 scope-crept — the agent added an uninvited --serve mode, likely
  nudged by the runtime-contract steering; v2 says a CLI stays a CLI.
  (2) the new toolchain's gofmt output is part of an uplift, not scope
  creep; v2 says so. Landing also surfaced a fleet wart: pre-migration
  repos tracked .forge runtime state (sloccount-ng wedged its own
  deploy on a stale CLI app.json; sourdough-app-node had 1691 files of
  npm cache in git). Migrated by hand to the modern pattern
  (.forge/* + !.forge/app.json); the shape of that fix is itself a
  future catalog entry (hygiene/untrack-forge-runtime).
- **uplift/go-version v2 — draft**, evaluation on webalizer-ng in
  flight; promotion is an admin's call on its receipts.

## Rulings (2026-08-29)

- **Scope of first build: single-project.** `forge apply <change> -p
  proj` and the catalog itself first; fleet verb, dashboard, and
  applied-state bookkeeping come once a few catalog changes exist and
  have been exercised.
- **Model pinning: the catalog change's pin WINS over project slots.**
  The definition's author knows the work; a mechanical change declares
  a cheap model and gets it fleet-wide, uniformly. (Consequence to
  honor: the pinned model is part of the versioned definition, so the
  evaluation receipts that admitted the change to the catalog were
  earned ON that model — changing the pin is a new version needing
  re-evaluation.)
- **Params: prompt + a whitelisted set of lane knobs** (timeoutMinutes,
  thinking level). Gates are not on the whitelist — an invocation can
  tune its effort, never reshape its own verification.
- **Promotion draft → catalog: admin-gated**, same trust boundary as
  guidance edits. an agent may prepare drafts and run evaluations;
  an admin promotes, with the evaluation run(s) recorded as evidence.
