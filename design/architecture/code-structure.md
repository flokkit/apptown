# Code structure: explicit boundaries, straightforward internals

> Historical refactoring proposal and progress notes, preserved in place.
> The intermediate types and ownership claims below are not current instructions.
> Start with the [current ownership map](domains.md) and [layout](layout.md).

Status: **proposed direction, 2026-09-06**, reviewed independently. This is an
incremental refactoring proposal, not a description of packages already
implemented or a prerequisite for all further feature work. It develops
the implementation-structure guidance in [architecture.md](architecture.md);
it does not replace the product designs or change external contracts.

Implementation update (2026-09-12): the split this page proposed has
largely happened. `domains.md` is the map as built: which package owns
which tables and rules, the doors forge keeps, the measures over the
week, and how to add a domain. Read that first; this page remains as the
reasoning behind the boundaries.

Implementation update (2026-09-09): `App` and its construction now live in
`internal/forge/app.go`, separate from schema history in `store.go`.
`deployment_wiring.go` assembles Docker, Portainer, and DigitalOcean drivers
with explicit operations. Drivers no longer hold `*App` or issue SQL:
managed-resource intent and identity writes go through `managedNeedRecorder`,
implemented by `deploymentNeedStore`. A failed identity write stops provisioning
and is returned to the caller. The independent `internal/progress` package owns
live buffers, locks, offsets, and detached snapshots; Forge owns HTTP responses
and persisting completed agent logs. Alongside `internal/execution`, this is an
incremental boundary, not a completed package split. Driver types still share
the Forge package; Docker still boots the current project checkout, and
deployment orchestration, authorization, and remaining runtime state still
belong to App. The proposed destination below remains a guide for later slices.

The next slice separates workflow advancement within `internal/forge`:
`workflowEngine` owns gate evaluation and the ordering of merge, transition,
dispatch, and completion. It receives a four-operation `workflowRecords`
interface and named callbacks, with neither `*App` nor a database handle.
`workflowStore` owns run/step reads and advancement writes; `workflowStages`
creates the stage records using only scanner names and targets. Stage updates,
events, and step creation still commit together before dispatch. The automatic
step registry now also determines which created steps are dispatched.
`workflow_wiring.go` connects policy/assertion checks and Git operations;
completion persistence and follow-up work now have the boundary below. The public `App.Advance`
entry point and authorization paths remain intact; required check names now
have stable alphabetical order. Engine tests need no App, database, or Git
checkout, and a PostgreSQL failure test verifies whole-stage rollback.
This is an advancement boundary, not the entire workflow domain extracted:
approvals, retries, and automatic landing still live on App,
and the types still share the Forge package.

Stage dispatch and restart recovery now share `workflowDispatcher`, which
owns its task tracking, active-stage deduplication, and stop/drain lifecycle.
It receives bound runners and `workflowDispatchRecords`, not App or SQL.
`workflowDispatchStore` rebuilds recovery plans from the pinned definition
and existing step rows, including completed prerequisites and expanded scan
points. Recovery preserves entry-sequence failure handling and parallel gate
scans without replaying completed steps or consulting current scanner bindings.
A stage whose automatic checks finished before advancement is also recovered;
the normal engine still enforces approvals and assertions. Skip and orphan
resume writes commit with their receipts; an agent's continuation mode is
recorded before its runner starts. `WaitBackground` now includes recovery
tasks. `StopWorkflowDispatch` closes admission and drains existing runners,
leaving queued work recoverable. It does not forcibly cancel runners, and
daemon-wide signal handling is unchanged. Rework, individual retries, and
other background workers remain outside this stage-execution owner.


Completion now has a transaction and recovery boundary in
`workflow_completion_store.go`: the conditional run update, `run.done`,
change shipping, automatic release receipt and link, child evidence/artifact
handoff, and required follow-up jobs commit together. Concurrent advancement
uses conditional stage/status writes so only one caller creates the next
stage or completion records. Git merging still precedes this transaction
and uses its existing recovery records; external effects are not made atomic
with PostgreSQL.

`workflow_completion.go` runs the follow-ups through the existing leased jobs
runtime. Cleanup, adoption, and parent advancement get an immediate attempt;
landing and narrative work wait for the daemon's workers (normally its next
five-second poll). Pending jobs and expired leases survive a restart. Job renewal and final
writes are conditional on the owning claim, so a late worker cannot overwrite
a newer attempt, including after an explicit retry resets the counter. The
normal three-attempt backoff policy applies; failed work is visible through
the jobs API, and advancing a completed run requeues its failed completion
jobs without recreating successful records. Cleanup and narrative retain
their existing best-effort behavior; durable invocation does not guarantee
external cleanup or generated text succeeded.

Accepted work records its landing run and the exact approval steps eligible
to inherit acceptance in the run-creation transaction. Retrying reuses that
run and cannot approve a later stage. Uplift adoption records the new revision
and the change's adoption marker in one transaction, and retry recognizes
an already-applied candidate. Legacy queued-landing intents are handed to
the same jobs runtime on startup; historical completed runs are not replayed
wholesale. PostgreSQL tests cover interrupted writes, concurrent winners,
child handoff, lost job leases, failed-job retry, landing acceptance scope,
and adoption replay. This strengthens completion consistency without
extracting all run creation or the jobs runtime from App.


Run creation now goes through `runStarter` with a named `runStartRequest`.
The owner has no App or database dependency; `run_start_wiring.go` resolves
workflows and receipts, and `runStartStore` owns the transaction. Ordinary
change starts, child calls, and accepted landings use this same path. The
transaction records the run, frozen inputs/adoption, receipts, first stage,
change attachment/state, and child-call or accepted-landing links together.
Project/change/parent locks reject competing ordinary starts and reuse the
same child or accepted landing on replay. A project adoption change between
resolution and creation refuses the stale start rather than pinning a
workflow from one revision to another revision's receipts.

New runs also record readiness in `run_starts`. Workspace preparation happens
only after commit; a preparation error returns the created run's identity,
leaves it unready, and stops dispatch. Preparation is shared with explicit
advancement and stage recovery, with concurrent preparation coalesced per
run. Readiness and `run.ready` commit together. Recovery can finish an
interrupted start, including approval-only roots and children without
automatic steps; completed steps retain their existing replay protection.
Child evidence is recorded before dispatch, so a fast child cannot have its
completion overwritten by late parent-link writes. Startup retains a call
with a running child and recovers that child; it does not erase the call link
as though the call itself were a dead subprocess. No start records are
backfilled for historical runs, and no new worker loop is introduced.

The new owner is tested without App or SQL. PostgreSQL tests cover rollback
of relationships and stages, concurrent starts, child/landing reuse, adoption
changes during resolution, failed preparation, and restart on both sides of
the readiness write. Workflow resolution, policy checks, Git workspaces,
and the existing wedged-landing supersession policy remain App adapters;
manual retries and whole-change send-back remain separate future slices.

### Abandonment

Run and whole-change abandonment share `abandonmentStore`, with a named
request and no App dependency. It takes the same project lock as creation
and completion, reads current state, and traverses the run tree inside one
transaction. Active descendants are included even through finished parents;
finished history is preserved. Run status, abandonment events, the applicable
change transition, audit entry, ticket revocation for a closed change, and
follow-up jobs commit together. Required read or write failures return to
the caller. Concurrent completion or abandonment has one winner, and a child
cannot be created after its parent is abandoned.

Abandoning a working run keeps the change open. Abandoning a landing returns
the change to work, whoever stops it: a person, a send-back, or taking the
stopped landing to chat. Once the landing has merged it cannot be abandoned.
Abandoning a child affects only that subtree. Whole-change abandonment closes
the change and revokes its outstanding Git tickets.

`abandonment.go` adapts HTTP and internal callers to that operation. Restart,
wedged-landing replacement, send-back, and chat handoff stop on abandonment
failure. These larger actions are not themselves single transactions.
Post-commit work uses the existing leased jobs runner: whole-change cleanup
is attempted immediately, and remains recoverable after a crash or workspace
removal error; run narrative refresh is queued. Cleanup retains the branch.
Container reaping and narrative generation retain their existing best-effort
behavior. Job status and explicit retry use the existing jobs API.

This boundary makes persisted state consistent; it does not add subprocess
cancellation or make an external Git merge transactional. Already-running
work can still finish its own step, while the run completion condition
prevents it from completing an abandoned run. PostgreSQL tests cover rollback,
subtree scope, duplicate requests, races with completion and run creation,
caller failures, and cleanup recovery.

### Rework after a verdict

`workflowReworkStore` owns approval votes and rework persistence without App
or execution dependencies. A selected machine failure now commits its
verdict, continuation mode, agent reset, and `step.rework` receipt together.
Human rejection includes the vote and approval reset in the same transaction;
previous rounds of votes remain in `previousDecisions`, and `lastRejection`
continues to supply the agent's feedback. A reopened approval refuses new
votes while its agent is still revising the work.

The store takes project and run locks before reading the source and producing
agent. It requires the current active stage, deduplicates the source verdict,
and retains the existing limit of three recorded rework rounds for automatic
requests. Late machine verdicts from checks started before the latest agent
attempt cannot request another revision of newer work. Existing runners still
choose which failures merit rework; infrastructure errors retain their policy.
Step timestamps retain database precision so same-second revisions can be
ordered when reopening stale approvals and checks.

Pending agent state is the durable dispatch intent. `workflow_rework.go`
admits it to `workflowDispatcher` only after commit. The dispatcher coalesces
rework requested by its own active task and admits it when that task returns;
a stop or crash leaves pending work for existing stage recovery. Agent start
also locks the run and claims only a pending step in an active stage, guarding
against duplicate dispatch and abandonment while waiting for a model slot.
No new table, job kind, worker loop, or process-cancellation policy is added.

PostgreSQL tests cover rollback through the final receipt, concurrent requests
and the round limit, vote history, late feedback, abandonment, agent admission,
and recovery before dispatch. Dispatcher tests cover a stage requesting its
own continuation. Manual retry, whole-change send-back, and remediation retry
coordination remain separate work.

Related: [deployment.md](../subsystems/deployment.md),
[authorization-capabilities.md](../subsystems/authorization-capabilities.md),
[application-forge-interfaces.md](application-forge-interfaces.md), and
atomic-change.md. Execution-specific findings and
extraction guidance: [execution-primitives.md](execution-primitives.md).
Candidate reusable mechanisms and independently useful subsets:
[separable-primitives.md](separable-primitives.md).

## 1. Position

Keep simple procedural code inside modules. Introduce firm boundaries where
responsibility, authority, state ownership, or external systems change.

The objective is **less unrelated code to understand before making a safe
change**, not more interfaces, smaller functions, or a particular directory
tree. A long, readable deployment procedure can be easier to maintain than
the same procedure dispersed across a dozen forwarding abstractions.

Human and agent contributors both benefit from explicit inputs, limited
dependencies, and local invariants. Neither should need to hold the entire
Forge implementation in context to change a cloud adapter.

This also follows the product's shape: Forge manages work products,
process, evidence, and history. An agent is an implementation of work within
that process, not the organizing principle of the backend. Durable
institutional knowledge belongs in product records and contracts, not in
an agent's private memory.

## 2. Refactor now, but selectively

There is enough implemented software to justify some boundaries now. More
features will otherwise acquire dependencies on the same large `App` and
make later extraction harder. There is not enough reason to freeze a
complete package architecture for everything still to come.

Use two different thresholds:

- **Clarify responsibility early.** Name the operation, its inputs, its
  state owner, and its failure behavior as soon as these are understood.
- **Generalize after evidence.** Introduce interchangeable implementations
  when existing consumers or providers reveal the common contract. Do not
  design a universal cloud, storage, or agent framework in anticipation.

Deployment is the best first domain-sized slice: Docker, Portainer, and DigitalOcean
already demonstrate real variation, and deploying, observing, recovering,
and retiring resources are enduring responsibilities even while details
change. The common boundary should express those responsibilities without
pretending the targets have identical capabilities.

Execution offers a smaller independent starting point: there is already a
shared process runner beneath scripts, tests, scanners, and agent steps.
Extracting and strengthening that primitive need not wait for deployment
reorganization. This does not mean unifying persistent chats and batch
steps into one lifecycle; see [execution-primitives.md](execution-primitives.md).

Do one bounded extraction alongside feature development. Finish it, assess
whether navigation and modification became easier, then choose the next.
Do not stop product work for a backend-wide rewrite. When a feature needs
an obviously missing boundary, include a small preparatory refactor; do not
make every feature pay for reorganizing its entire neighborhood.

## 3. What the current code tells us

These are observations from the September 6 review, not permanent counts
or a comprehensive defect inventory:

- Most backend implementation lives in `internal/forge`, with approximately
  148 non-test Go files and 89 test files. `App` gives methods access to
  database, configuration, execution, caches, secrets, and live sessions.
- `targets.go` already defines a `deployDriver` interface. The problem is
  not its absence: implementations receive `*App`, so the interface does
  not effectively limit their dependencies or responsibilities.
- Provider dispatch occurs in `driverFor`, `renderFor`, and
  `fulfilManagedNeed`. The DigitalOcean implementation also combines API
  transport, credential resolution, registry work, provisioning, rendering,
  and rollout observation.
- The Docker driver's `Reconcile` receives a deployment artifact but reads
  the current project checkout and contract instead. An interface parameter
  is not a dependable boundary if an implementation bypasses its meaning.
- `releaseBundleTo` returns HTTP status codes from application orchestration.
  `attemptNoter` exposes durable provider-identity recording as a callback
  that cannot return an error. These mix distinct responsibilities.
- Execution is partly shared already: `execute` runs local/Docker processes,
  `piInvoke` serves implementation and review steps, and
  `openrouterComplete` serves several tool-free model consumers. Persistent
  consoles have their own appropriate session lifecycle. The missing
  boundary is around these existing primitives, not a missing agent engine.

There are good foundations to preserve: the authorization decision
vocabulary, centralized step-runner dispatch, explicit SQL and transaction
helpers, and fake-provider HTTP tests. This is refinement, not starting over.

## 4. Organize around responsibilities

Keep one process and one database. Packages enforce code boundaries; they
do not imply microservices, separate deployment, or asynchronous messaging.

The following is a possible destination, not a tree to create in one change:

```text
internal/
  app/                    composition and process lifecycle
  httpapi/                HTTP decoding, responses, streaming
  mcp/                    MCP transport and delegation context
  workflow/               runs, steps, gates, advancement
  deployment/             environments, attempts, deployment lifecycle
  conformance/            assessments, findings, exceptions
  authorization/          principals, grants, decision function
  secrets/                resolution and controlled delivery
  contract/               application contract parsing and validation
  execution/              process execution and environment mechanics
  providers/
    digitalocean/         provider API and target implementation
    docker/               Docker target implementation
    portainer/            Portainer target implementation
  database/               connections, migrations, transaction plumbing
```

Each domain owns its operations and persistence code. Avoid replacing one
giant `forge` package with a giant `store` package and a giant `services`
package, both of which require knowledge of every feature.

Extract only the shared contract types actually needed. Do not create a
universal `models`, `common`, or `utils` package to resolve every import
cycle. A cycle often means ownership is unclear or the caller needs a much
smaller description of what it consumes.

The composition layer constructs concrete dependencies. Providers may
implement interfaces declared by deployment; deployment does not import
each provider. A provider registration switch in composition is reasonable.
Repeated provider-specific branching throughout orchestration is not the
desired endpoint.

Execution mechanics follow the same rule: they do not import workflow or
conformance to decide what a result means. Model transport and agent-runtime
adapters get explicit dependencies; their eventual package names should
follow the first extraction rather than dictate a new framework. Interactive
session ownership stays distinct from bounded process execution.

## 5. Make deployment the first complete example

### One application entry point

A deployment service should own application operations such as requesting
a deployment, observing it, resuming interrupted attempts, and teardown.
HTTP, MCP, workflow steps, and recovery invoke the same underlying rules.
Transport adapters translate typed results and domain errors into their
respective protocols; the service does not return HTTP status codes.

Authentication establishes the principal at the edge. Shared operations
must still enforce the applicable authorization decision, so a new caller
cannot accidentally bypass rules implemented only in an HTTP handler.
Workers carry the appropriate recorded authority and context rather than
becoming an implicit administrator.

### Explicit provider inputs

A driver receives a prepared request describing the selected target,
attempt and generation, immutable deployment input, necessary bindings,
and narrowly scoped credential access. It does not receive `*App`, a raw
Forge database handle, or permission to rediscover the application's
current desired state from a mutable checkout.

Provider code owns the provider's API vocabulary and mechanics. Deployment
owns Forge's intent, lifecycle, persistence, authorization, and receipts.
If a provider needs information, add the specific input or narrow consumer
interface; do not pass the application back in as an escape hatch.

Separate responsibilities where they are genuinely different:

- **Preparation:** build or publish images and resolve required inputs.
  External effects are explicit, with their own failure and retry handling.
- **Rendering:** transform resolved inputs into a target-specific plan.
  Keep this deterministic and free of network effects where possible;
  registry creation or image pushing should not hide inside `render`.
- **Deployment:** apply, observe, resume, and tear down the recorded
  deployment through the target API.
- **Provisioning:** fulfill supported state-service needs and record their
  ownership independently of the replaceable application deployment.

These need not all become interfaces or separate packages immediately.
Concrete functions are sufficient where there is no useful substitution.
Small optional interfaces can describe supported operations such as resource
inventory without making every driver implement every capability.

The shared contract specifies outcomes, identity, and recovery obligations,
not identical platform behavior. For example, health-check rendering may
legitimately differ between targets. Stronger verification than runtime
monitoring is acceptable; Forge owns correct target adaptation, rather
than penalizing application authors for following an organizational policy.

### Checkpoints are not logging

Human-readable progress and durable recovery checkpoints are different
channels. A checkpoint operation returns an error. A failure to record an
external resource's identity must not be silently treated as a log failure.

That does not make a remote API call and a database write atomic. Persist
intent before the effect where possible, use provider-supported idempotency
or reliable discovery, and explicitly handle the case where the remote
operation succeeded but the receipt could not be saved. Resume must not
blindly create another resource. The deployment design remains authoritative
for these lifecycle semantics.

Use typed requests, observations, and receipts at the shared boundary.
Provider-specific opaque handles can remain versioned serialized data,
decoded and validated by their owner. Do not spread provider JSON field
knowledge throughout the orchestration code. Resolved secrets must not be
serialized into plans or checkpoints intended as durable public evidence.

## 6. Keep SQL close to the operation

A domain can have a concrete `store.go` with explicit SQL. There is no need
for an interface per table, a generic repository framework, or an ORM as
part of this work.

Prefer operations such as `CreateAttempt`, `RecordProviderIdentity`, and
`CompleteAttempt` over a sequence of unrelated public CRUD calls whose
correct ordering every caller must remember. Document the transaction,
locking, and failure guarantees of each consequential operation.

Package boundaries must not fracture an existing atomic operation. Where
an invariant spans domains, choose one coordinating application operation
and an explicit shared transaction mechanism. Do not introduce a distributed
transaction problem inside a single-process, single-database application.

Keep Postgres migration and concurrency work separate from structural moves.
Moving code does not justify removing a write gate, weakening a lock, or
changing failure handling without the corresponding tests and review.

## 7. Own background work explicitly

The process composition layer owns startup and shutdown. Each domain owns
the workers and recovery loops responsible for its durable operations.
Requests may start work that outlives the request, but the service should
make its lifetime, cancellation, shutdown, and restart behavior explicit.

Avoid an ever-growing `App` whose mutexes, goroutines, and caches belong to
unrelated features. Avoid replacing it with an equally broad dependency
container passed into every service. A narrow callback that closes over
`App` can be a temporary extraction bridge, not the final dependency model.

## 8. Sequence and safeguards

There are two bounded starting points. Choose according to the work in
flight; neither is a prerequisite for the other. Do not open two broad
refactoring fronts across the same `App` files.

### Deployment extraction

Suggested order, with each item independently reviewable:

1. Add or preserve characterization tests for the chosen deployment path:
   accepted input, provider requests, persisted state, and failure behavior.
2. Extract the shared contract types needed by that path, preserving the
   existing parser, defaults, validation, and serialized forms.
3. Extract DigitalOcean API transport and configuration into a concrete
   client with explicit dependencies. Replace relevant global test hooks
   with instance-level dependencies as part of this bounded extraction.
4. Introduce the deployment service and its concrete persistence operations.
   Existing `App` entry points may temporarily forward to it; avoid having
   both paths independently implement the same lifecycle.
5. Move a complete provider path behind the explicit boundary, including
   observation and recovery. Record remaining legacy paths rather than
   claiming all deployment has been unified.
6. Bring the other implemented targets through the same boundary, adjusting
   it based on real differences. Only then use the result as a pattern for
   further domain extraction.

### Execution extraction

Start with the existing process runner: explicit dependencies, a caller-owned
context, and typed termination outcomes. Characterize existing behavior
before separately fixing unknown-backend fallback and cancellation semantics.
Then extract direct model transport from `narrative.go` and separate Pi
invocation mechanics from workflow records and live feeds. Consolidate
environment preparation only where actual consumers have matching needs.
Keep console detach/reconnect separate from batch cancellation. The detailed
inventory, responsibilities, and tests are in
[execution-primitives.md](execution-primitives.md).

### Safeguards for either path

Behavior-preserving movement and intentional behavior changes should be
separate changes. For example, making Docker honor the selected immutable
deployment input, or changing how checkpoint failure is handled, requires
an explicit correctness change and regression test, not a silent alteration
hidden in a package move. Urgent correctness fixes need not wait for extraction.

Use pure renderer tests, fake-provider API tests, shared behavioral tests
for genuinely shared driver obligations, and real-Postgres integration
tests for transactional operations. Exercise interrupted and retried
operations, not just the successful deploy. Preserve HTTP/MCP response
contracts, stored handles, audit identity, authorization, and recovery of
attempts created by older binaries. Do not add a data migration merely to
make internal names neater.

Coordinate extractions with concurrent feature work: identify the files
and domain being moved, finish a small slice, and avoid a long-lived rename
branch across the whole backend.

## 9. What to defer, and how to judge success

Defer a comprehensive workflow-engine rewrite, a public plugin ABI,
universal cloud abstractions, speculative interfaces for future services,
and wholesale movement of handlers or database queries. Do not create
empty packages for the proposed destination tree.

After the first slice, ask concrete questions:

- Can a DigitalOcean API change be made in its provider package and tests
  without understanding workflow advancement or the whole `App`?
- Can a deployment rule be changed once and apply to every entry point?
- Can a reader locate the transaction and recovery boundary without
  reconstructing a chain of incidental helper calls?
- Can a provider be tested without constructing an entire Forge instance?
- Can a new check reuse execution without rebuilding sandbox setup,
  cancellation, output collection, or termination classification?
- Can agent invocation be tested without constructing workflow records,
  while the workflow still owns verdicts, evidence, and retry decisions?
- Is there one authoritative implementation of each moved operation?

If not, improve that slice before multiplying the pattern. The appropriate
amount of abstraction is the amount that makes the next real modification
local, understandable, and testable.
