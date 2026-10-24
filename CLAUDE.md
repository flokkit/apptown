# CLAUDE.md

The shared project guide for agents working in this repository. Keep it focused
on durable product intent, design boundaries and working rules. Current feature
inventories, versions, configuration, commands and rollout status belong with
their owning code or guide.

## What AppTown is for

AppTown (Flokkit AppTown) is the accountable home for a team's internal
software and the changes made to it. Coding agents make producing code cheap;
owning software remains expensive. AppTown addresses four problems: unauditable
work, inconsistent applications, organisational knowledge that does not reliably
steer changes, and more code than humans can understand by reading diffs.

The product makes agent-assisted development a shared organisational practice.
It records who asked for a change, which rules governed it, what was checked,
who accepted it and what is running. Shared ways of working, evidence, history
and fleet maintenance make a portfolio manageable after its original makers
have moved on. A feature should serve one of those problems.

The intended applications are ordinary, load-bearing institutional software.
Consistency and maintainability matter more than novelty. Work on AppTown itself
may explore new designs; that does not waive its authority, evidence or quality
requirements.

The output remains ordinary, portable software. AppTown supplies the institution
around it. It is not a general agent-orchestration framework, a replacement SCM
or ticket tracker, a low-code platform, or a pursuit of maximum autonomy.

The [product model](design/subsystems/product-model.md) explains the main boundaries.

## Ways of working are data

AppTown provides a design language of reusable affordances: guidance, workflows,
policies, checks, approvals, execution, artifacts, configuration delivery and
deployment. A way of working composes those affordances as versioned data.
Projects adopt that data explicitly; the record identifies what governed the work.

A team can prescribe a framework, toolchain, architecture or review practice
through packs and their declarations. That knowledge belongs in the adopted
content, including executable checks and build instructions where supported.
Adding or revising a way of working should normally require no change to AppTown's
implementation. Stock content uses the same supported language as other content;
avoid hidden privileges or engine branches keyed to a particular stock pack.

Before adding engine behaviour, ask whether the existing affordances can express
it. If not, identify the missing reusable capability and define its contract.
“Run a declared build and publish its static output” is an affordance. “If this
project uses Vite, apply these special deployment rules” encodes a particular
way of working in the engine. Deployment mechanisms consume declared artifacts
and runtime requirements; framework conventions belong in data.

This is deliberate, bounded extensibility, not arbitrary hooks into AppTown.
Core code owns schema validation, authority, isolation, lifecycle, evidence
admissibility and provider mechanisms. Data cannot grant itself privileges,
bypass a gate or turn an assertion into trusted evidence. Prefer a small coherent
language over a new configuration switch or special case for every application.

## Principles that decide design questions

- **Witness, do not assert.** Bind checks, approvals and deployment facts to the
  exact work they support. Gates read evidence, not intentions. Errors, missing
  evidence and inability to assess are not success. A passing check, an exception
  and an approval remain different facts; never fake a receipt for a demonstration.
- **Preserve attribution.** An institution or role may answer for a decision, but
  the forensic record retains the exact actor, authority, meaning and evidence.
  Make authority and consequential acts legible through authorized views without
  assuming every caller can read every record.
- **Check authority before effects.** An actor name is not permission. Resolve
  current grants, scope, credential limits and operation-specific eligibility.
  Missing authority does not grant access. Approval, landing, release and
  administration are distinct acts; agent access must not silently inherit all
  of a person's powers. Agents can request governed work without supplying human
  assent. Read the current
  [authorization contract](design/subsystems/authorization-capabilities.md)
  instead of inferring permissions from a role's display name.
- **Procedure snapshots; law does not.** A run keeps the workflow definition it
  was admitted under. Current policy binds at consequential acts, including
  landing. Preserve both the historical procedure and the current decision.
- **Acceptance has a stated meaning.** An approval declares what assent means,
  presents that meaning when a person decides, and preserves it in the evidence.
  Reuse of acceptance must preserve its scope and current eligibility.
- **AppTown environment is the environment of record.** External results may
  be useful self-reported evidence; they must not masquerade as witnessed gate
  execution. Keep declared requirements, observed facts and unknown state distinct.
  Integrations need an explicit contract for absence and unavailability.
- **Small changes, independent evidence.** Unattended work should have a bounded
  intention and acceptance criterion. Batch computation where useful without
  collapsing distinct decisions or losing their receipts.
- **Keep the language deliberate.** Core schemas and a bounded policy vocabulary
  make shared conventions dependable. Use standards at external interchange
  boundaries where they fit; retain AppTown's own domain meaning inside them.
- **Apply shared rules to AppTown too.** Validate shipped ways of working through
  the supported contracts. Recurring application problems should improve shared
  guidance, checks or affordances rather than accumulate unrelated local patches.

Use the [concepts](design/requirements/concepts.md) and
[shipped glossary](stock/docs/guides/glossary.md) for vocabulary. A Change is the
unit of governed work, a Run executes a workflow, and landing publishes accepted
work to the canonical repository. A deployment's desired state, observed state
and release evidence answer different questions. Preserve these distinctions;
do not invent synonyms or copy lifecycle enumerations into this guide.

## Ownership and implementation boundaries

Libraries under `internal/` are independent of AppTown's domain and tables.
Subsystems under `internal/apptown/` own product concepts and operations; not every
subsystem owns tables. The composition root assembles collaborators and owns
instance/protocol lifecycle. Wiring binds dependencies; domain behaviour belongs
with its owner. The [layout guide](design/architecture/layout.md),
[ownership map](design/architecture/domains.md) and
[code navigation](internal/apptown/README.md) contain the current package boundaries
and call sequences.

Transports translate requests into owner operations. They must not acquire the
whole application or bypass ownership through forwarding methods and broad
callback bundles. Shared behaviour belongs in the owner. Keep transport logic to
request-specific coordination and response shaping. Owners classify failures;
transports render those classifications instead of guessing their meaning.

An owner controls its records and legal transitions. Use its operations and
transaction participants rather than writing its tables from another package.
External Git, container and provider effects do not become atomic because nearby
SQL is in a transaction. Make admission, effects, settlement, retries and unknown
outcomes explicit. Use the repository's process/Git abstractions so cancellation,
timeouts and source selection retain their guarantees.

Keep project-controlled execution and executable configuration within the admitted
execution boundary. Do not treat a proposed backend or isolation design as an
implemented guarantee. Application artifacts, controller authority and provider
credentials have different owners and lifetimes.

Database schema and query changes follow the
[schema ownership rules](design/architecture/schema-ownership.md). Use named,
generated queries for production operations, regenerate affected output and
include it with the change. Do not silently discard database failures. Legitimate
best-effort operations still need an explicit failure contract.

For web work, follow the [shared data contract](design/architecture/web-data.md).
Reuse the data layer and generated response types; preserve loading, empty, error,
stale and identity-change semantics. Editors must not overwrite newer state
silently. Dependency versions and component layout belong in the source tree.

## Working and validating

Start with [design/README.md](design/README.md) and the affected owner's guide and
API comments. Current code and tests establish implementation; the
requirements (and the maintainers' position documents) establish intent. When they differ, identify the gap rather than
silently changing the promise.

Changes land on `main` as one pull request per change, squash-merged after
the required `ci-ok` and `heavy-ok` checks pass. The pull request's title and
description are the public record: they say what changed and why, not how the
work went.
The maintainers plan and independently review changes in a separate private
repository, checked out at `private/` and ignored here; when it is present,
its working rules apply as well: @private/CLAUDE.md
[CONTRIBUTING.md](CONTRIBUTING.md) says how to propose a change.

[Makefile](Makefile), [CI configuration](.github/workflows/ci.yml),
[the heavy tiers' CI](.github/workflows/heavy.yml) and
[web package scripts](web/package.json) own executable commands and tool versions.
`make build` builds the application; `make check` runs the local validation gate.
Check the affected owner's requirements and the
[testing guide](design/architecture/testing.md) before selecting additional tiers.
Do not assume local and CI coverage are identical. Runtime options belong in
command help and [operations documentation](design/operations/README.md).

Before adding a test, state the guarantee and search the owner's existing tests.
Extend coverage where possible. Names describe guarantees; enumerable contracts
benefit from completeness checks. Test behaviour and failure boundaries rather
than copying the implementation into assertions.

Tests live with the behaviour they protect. Root tests cover assembly and
cross-owner guarantees. Set up domain state through owner operations; use raw
storage writes only when no operation can produce the required state, with the
reason recorded at the site. Preserve independent persistence assertions when
storage representation is the guarantee. Journeys exercise public interfaces.

Shared fakes of external systems belong beside their adapters. Fakes of AppTown's
own collaborators remain private to the owning package; do not build a shared
application fixture to work around ownership. Isolate resources, clean them up,
poll with deadlines, and make concurrency choices visible at each test rather
than hiding them in fixture helpers.

Run checks appropriate to the change and the required repository gates. Report
what ran and what remains unverified. A skipped infrastructure test is not proof
of isolation, recovery or deployment. Keep local validation, deployment and live
observation separate. Preserve architecture and reachability ratchets: tighten
after removing debt and never disguise a bypass to keep a count flat. Exact
rules and exceptions belong in the gate implementations and owning guides.

## Writing and maintaining guidance

Text a person reads (screens, messages, errors, guides) follows
[How AppTown writes](design/writing.md).
Use plain, concrete language and literal names. Lead product surfaces with the
current state and useful next action; put detail behind them without hiding
required approvals, failures or uncertainty. Unknown and stale must remain
visibly different from empty, stopped or successful. Avoid jargon, promotional
claims and invented metaphors. Comments should explain why a rule exists.

Keep this file and AGENTS.md small enough to guide decisions. Do not add package,
framework, role or capability catalogues, dependency versions, port numbers,
timing measurements, migration status or copied command matrices. Link to the
owner of those facts. A durable rule belongs here when it changes how an agent
should reason or work across the repository.

Update the owning current guide when behaviour changes. Package documentation
owns concepts and API obligations; subsystem guides own cross-package behaviour;
operating guides own setup and rollout procedures.
Current guides state the current rule in the present tense, without dates,
status headers or history; git history records what changed when. A reason
that matters is written into the guide.

For live operation, read the [AppTown operating skill](.claude/skills/apptown/SKILL.md).
Satisfy gates rather than working around them. Projects move through
Changes: when AppTown's contract or a way of working changes, existing projects
get governed Changes that a person accepts, not edits to their repositories or
records behind the product's back. AppTown's own stored state moves through
numbered migrations. Local code or passing tests do not
authorize a production rollout; follow the operating procedure and the user's
actual authorization.
