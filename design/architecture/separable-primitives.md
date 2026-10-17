# Separable primitives: useful machines inside Forge

Status: **proposed direction, 2026-09-06**. These are candidate
abstractions grounded in a code review, not implemented library boundaries,
a public API commitment, or a backend-wide refactoring programme. Interface
sketches below express responsibilities; they are not existing APIs.

Companion to [code-structure.md](code-structure.md) and
[execution-primitives.md](execution-primitives.md). Related:
[ways-of-working-versions.md](../subsystems/ways-of-working-versions.md),
[conformance.md](../subsystems/conformance.md), [deployment.md](../subsystems/deployment.md),
[authorization-capabilities.md](../subsystems/authorization-capabilities.md).

## 1. Position

Forge can compose independently meaningful mechanisms into its institutional
workflow. Those mechanisms do not all need to understand the institution.

The test for a useful abstraction is: **does it let its caller forget an
entire category of problems?** A second test is whether another application
could use it without inventing fake Forge projects, changes, or runs.

Imagine a third-party library that does exactly the required job through a
small interface. What would its inputs, outputs, and guarantees be? Familiar
shapes help: package manager, compiler, assessment engine, durable work queue,
isolated change workbench, asset register. The point is their recognisable
responsibility, not adopting an external dependency or imitating every feature
of an existing product.

These are semantic boundaries, not just directories. Moving functions out of
`App` while passing `App` back into them does not establish independence.
Conversely, a component can be useful and well bounded before it lives in a
separate module or repository.

Authorization remains cross-cutting, and domain models remain rich. Shared
mechanics must not flatten changes, releases, exceptions, and adoptions into
one generic business object or universal state machine.

## 2. A package manager for ways of working

Forge already has versioned components, packs, adoption receipts, upgrade
proposals, and compatibility assessments.
[The adoption resolver](../../internal/forge/adoption/resolve.go) (`resolver.Union`)
is recognisably doing dependency-resolution and lockfile work; an
[adoption candidate](../../internal/forge/adoption/candidate.go), a prepared
manifest and how it differs from what the project has now, describes a change
to what a project has adopted.

Make that shape explicit:

```text
Resolve(catalogSnapshot, selections, overrides) -> LockedManifest + Conflicts
Diff(oldManifest, proposedManifest) -> UpgradePlan
```

The resolver owns exact versions, composition order, provenance, conflicts,
and explaining why a component is present. Resolution uses an explicit
catalog snapshot: the library changing midway through an operation must not
silently change its answer. Publishing and adopting remain separate acts.

Guidance, workflow definitions, and checks retain their own rich schemas.
Package machinery handles their identities and composition; it does not
execute workflows or decide whether an institution should adopt an upgrade.
Checking whether application code meets a proposed way of working belongs
to assessment, not dependency resolution.

**Simplification:** consumers receive one resolved, immutable way of working
instead of independently discovering which versions, tiers, project edits,
and pack contributions apply.

**Independent use:** distribute and maintain engineering standards across
existing repositories and CI systems without using Forge's development
workflow.

## 3. An application/environment compiler

This is a stronger abstraction than a cloud-driver interface. Forge takes an
application declaration, combines it with environmental choices, and
translates it into runnable arrangements: a compiler with target backends.

```text
Compile(contract, environmentProfile, catalogSnapshot, bindingReferences)
    -> Plan + Diagnostics
```

The plan describes parts, initialization order, service requirements, wiring,
health checks, storage, and required preparation. Target renderers translate
that into their particular representation. Existing pieces include the
[contract parser and prepared inputs](../../internal/deploy/appcontract/),
[Compose generation](../../internal/deploy/intranet.go), dependency fulfillment,
and release rendering through each target driver's `Render`
([the driver contract](../../internal/deploy/driver.go)).

The boundary should guarantee:

- Explicit inputs, without secretly reading the current project checkout or
  discovering mutable configuration through `App`.
- Creating databases, publishing images, and deploying are explicit effects
  outside compilation. A plan may describe required preparation without
  performing it.
- Durable plans contain secret references, not secret values. Controlled
  delivery resolves values at the execution boundary.
- Target differences remain legitimate. This is not a lowest-common-
  denominator cloud model. Correct health-check adaptation belongs to Forge;
  stronger verification than runtime monitoring is acceptable.

**Simplification:** local development, preview, release, and recovery share
the interpretation of the application instead of each reconstructing it.

**Independent use:** an application launcher/deployer that understands Forge
contracts but knows nothing about coding agents or approval workflows.

## 4. An assessment engine with a proper evidence model

The recognisable shape is a test or inspection engine:

```text
Assess(subjectSnapshot, checkSet, evaluationContext) -> Assessment
Compare(beforeAssessment, afterAssessment) -> FindingDelta
```

Forge already approaches this through immutable
[verdicts](../../internal/forge/conformance/verdict.go), baseline comparisons,
evaluator identities, and [historical views](../../internal/forge/conformance/history_store.go).
The important abstraction is the assessed subject, not whatever happens to
be in a run's workspace.

Depending on the check, the subject identifies an exact source revision, a
before-and-after revision pair, a built image, or a deployed generation with
observations taken at a particular time. These are meaningful typed subjects,
not interchangeable strings with one universal notion of freshness.

An assessment records the check version, evaluator identity, relevant inputs,
evidence references, and outcome. Keep three questions separate:

1. What did the evaluator observe?
2. What finding does that support?
3. Does that finding prevent this particular action?

A fleet review, landing gate, and remediation verification can share
assessment mechanics while applying different decision rules. An accepted
exception does not erase the underlying failure. Introduced and carried debt
remain distinguishable; an unavailable comparison does not pretend to prove
that a finding is old.

Underneath this, introduce a small binary-safe artifact store with stable
references and digests. Current [artifact insertion](../../internal/forge/workflow/forge_participants.go)
(`AddArtifactIn`) is explicitly run-owned. Independent assessments should not require a dummy
run to retain evidence. A workflow can link an assessment and its artifacts
into its own record without owning the only route to creating them.

Access control, retention, and durable recording still apply. A digest
identifies content; it is not permission to read it. This proposal does not
require a universal event store or event-sourcing the entire application.

**Simplification:** identity, provenance, freshness, and comparability stop
being reconstructed differently by every consumer. A matching source SHA
alone does not make a runtime observation fresh or an AI judgment
automatically reusable.

**Independent use:** conformance checking and debt tracking over repositories
hosted elsewhere, with no Forge change-authoring workflow.

## 5. A durable-operation substrate

This is not another workflow engine. It is a smaller mechanism beneath
workflows and other consequential background operations: a durable work queue
plus an operation journal.

```text
Submit(operationKey, typedRequest, authorityContext) -> OperationID
```

Claiming, checkpointing, completion, and recovery are explicit operations.
Existing versions of these mechanics appear in deployment attempts,
remediation leases, scheduled work, and
[Git landing recovery](../../internal/forge/gitrepo.go) (`RecoverLandings`). Landing recovery already
demonstrates the useful discipline: record intent, perform an external action,
and recover by discovering whether it happened.

The common substrate could own:

- Durable handoff from a committed request to background execution.
- Deduplication and concurrency keys.
- Worker ownership and protection against stale workers completing work.
- Error-returning checkpoints and attempt history.
- Restart recovery and cancellation requests.

The domain owns what to do next and whether retry is safe. A cloud timeout
can mean that a resource exists but the answer was lost. This mechanism must
not promise exactly-once external effects or blindly rerun everything.
Provider-supported idempotency, discovery, and an honest unknown outcome
remain necessary. Cancellation is likewise a request, not a promise to undo
an effect already performed.

The request carries its authority context; the worker is not an implicit
administrator. Domain operations enforce the applicable authorization rules.
Durable submission must preserve transaction boundaries rather than create
another gap between recording intent and scheduling the work.

Do not force every periodic health observation into a durable job. Derived
controllers and persistent interactive sessions keep their distinct
lifecycles. Workflow sequencing stays with the workflow engine.

**Simplification:** each feature stops rebuilding the machinery for what
happens if Forge dies partway through, while retaining its meaningful states
and recovery decisions.

**Independent use:** durable execution of deployment, assessment, or
maintenance operations without the full change workflow.

## 6. A Git change workbench

This is neither a generic Git wrapper nor the execution sandbox. Its job is
isolated editable work and exact publication candidates:

```text
Open(repository, baseRevision) -> Workspace
Snapshot(workspace) -> CandidateRevision
Publish(candidate, expectedMainRevision) -> Published | MainMoved | Conflict
```

It owns workspace isolation, branch synchronization, snapshots, conflict
representation, and safe publication mechanics. It does not decide whether
a person may land or whether a candidate passed its required checks.

A mutable workspace and an immutable candidate are different things. Checks
and approvals refer to the candidate; people and agents edit the workspace.
Publication must enforce the expected revisions at the actual write boundary,
not merely check them earlier and then merge whatever is current.

The existing workspace and merge code ([workspace_git.go](../../internal/forge/workflow/workspace_git.go),
[merge.go](../../internal/forge/workflow/merge.go)) contains
much of this, but speaks heavily in Forge runs and changes. Exact source
materialization is also useful to assessment and deployment without granting
access to an editable workspace.

**Simplification:** consoles, remote MCP workspaces, local Git pushes, and
remediation use one account of what is editable, what was examined, and what
can be published.

**Independent use:** supervised coding workspaces or a small review-and-landing
service, with any editor or agent supplying changes.

## 7. A resource-ownership registry

Develop this alongside the application-and-state roadmap rather than fully
generalising it immediately. Its shape is an asset register with explicit
ownership and lifecycle relationships:

```text
PlanRetirement(environment) -> StopThese + RetainThese + NeedsDecision
```

Forge already has [retained resources](../../internal/forge/deployment/retained.go),
[provider inventory](../../internal/forge/deployment/inventory.go), need instances,
and container ownership labels. Strengthen the shared model around:

- Stable resource identity.
- Ownership versus merely using a resource, including shared consumers.
- Authoritative, rebuildable, or disposable state.
- Retention obligations and supported recovery operations.

Depends-on does not mean owns, and owns does not automatically mean may
delete. An unreachable provider is not proof that a resource is gone.
Planning retirement and authorizing or executing destructive actions remain
separate operations.

**Simplification:** deployment teardown, orphan discovery, preview cleanup,
retirement, and eventually recovery planning stop inferring lifecycle rules
from names, resource types, and scattered tables.

**Independent use:** application operations and state stewardship, including
applications developed outside Forge. The bounded support and no-host-
administration positions in the deployment and ownership designs still apply.

## 8. A smaller primitive: revision-bound consent

Separate from authorization, consent records who agreed to a proposition.
[Workflow approvals](../../internal/forge/workflow/approval.go) implement this
today. The separate exception-assurance prototype was unreachable from production
and was removed on 2026-09-10; it is not evidence for a shared primitive. Defer an
extraction until there is another live caller with the same requirements.

Authorization asks whether a person may decide. Consent asks whether the
required people have agreed to this exact proposition.

A small shared primitive can handle distinct voters, quorum, dissent, and
binding votes to a subject revision. Changing the proposition must not
silently carry its earlier approval forward. Record what assent meant and
the authority under which it was given; historical consent is not a blanket
authorization for a later action.

Changes, exceptions, and adoptions retain their own decision semantics,
eligibility rules, expiry rules, and lifecycles. Share the mechanics without
inventing a universal approval-shaped business object.

## 9. Useful subsets

These are architectural tests, not commitments to ship separate products.

| Useful subset | Pieces it needs | Pieces it can omit |
|---|---|---|
| Ways-of-working distribution | Catalog, resolver, manifests | Agents, deployment, hosted Git |
| Repository assurance | Resolver, assessor, evidence, execution | Change authoring, release management |
| Application operations | Compiler, drivers, durable operations, ownership | Coding agents, Forge-hosted development |
| Supervised development | Workbench, workflow, assessments, consent | Cloud deployment |

Each exposed product still needs appropriate authorization and ownership.
Subsetting functionality must not remove its safety boundaries. A deployer
can accept a bundle without a Change; an assessor can accept a source
snapshot without a Run. Forge connects their records into the richer story.

Keep one process and one database where appropriate. These boundaries do not
require microservices, network calls between modules, separate binaries, or
a public plugin ABI.

## 10. Priorities and proof

Among these candidates, prioritise the application compiler and the
assessment/evidence boundary, alongside the execution extraction already
described in execution-primitives.md. This is consistent with deployment as
the first domain-sized extraction in code-structure.md: the compiler is a
useful boundary within that work, not a competing rewrite.

Make the ways-of-working resolver explicit as that area evolves. Extract
shared durability incrementally from real operations, preserving their
transaction and recovery guarantees. Develop the resource registry with the
next supported application/state lifecycle rather than anticipating every
possible resource.

Keep the components internal initially. Useful proof points are:

- Compile a fixture contract for supported targets without constructing
  `App`, accessing the network, or provisioning anything.
- Resolve and compare manifests from a fixed catalog without touching live
  project state.
- Assess a source snapshot and retain its evidence without creating a
  Change or Run; let a workflow consume that same result through an adapter.
- Exercise an operation across interruption points, including an effect
  succeeding before its receipt is saved and a stale worker attempting to
  finish it.
- Change a workspace after assessment and demonstrate that the recorded
  candidate and its consent cannot silently refer to the new contents.

Move structure and change behavior in separately reviewable increments.
Do not use extraction as a reason to relax the Postgres concurrency strategy,
change authorization, or unify chat and batch lifecycles.

The success criterion is not publishing several libraries. It is being able
to understand, test, and use one of these machines without constructing the
whole application—and having Forge gain clarity from composing them.
