# Product objects and ownership

## Project, work and conversation

A project groups repository identity, settings, adopted ways of working and its
work history. Project owns its records and visibility-aware reads; Projectlifecycle
coordinates creation, name reservation, settings edits, pack application and
removal across owners. Updating settings commits one project mutation; external
repository/container effects are not made atomic by sharing that entry point.
A new project's repository may start from existing code (a Git bundle or an
https repository): only its default branch and tags come, and every record
starts fresh ([procedure](../operations/start-from-existing-code.md)).

Visible project reads resolve current authority before querying the visible set,
counts or pages. Idle projects remain visible even without runs. Empty visibility
means no projects, not an unfiltered query. Dashboard runtime badges use Deployment's
cached observations, not synchronous provider calls. See [authority](auth.md) and
[deployment runtime](deployment.md) for failure and staleness semantics.

A Change is a unit of repository work. Runs are executions of pinned workflow
definitions; steps and attempts record the work performed within them. Retrying a
step, sending work back and creating a continuation have distinct identities and
rules. A Change can outlive a particular run. The [workflow guide](workflow-domain.md)
owns lifecycle and settlement guarantees.

A chat is a durable conversation with its own identity and workspace authority.
It is not identical to its container or to a run. Project chats read a snapshot;
change chats edit a worktree. Conversation composes Forge scope with Console's
session lifecycle. See [chats and runtime](console.md).

## Shared standards and evidence

Ways of working publishes versioned blocks, workflows and packs. Adoption records
what a project selected, with historical receipts. Stock sync publishes available
content; it does not silently adopt that content for every project. A pinned run
definition remains separate from the project's next selected workflow version.

Checks evaluate evidence. Conformance records judgments and human dispositions;
Verification executes and compares; Remediation turns selected findings into
workflow work. An exception, a passing check and an approval are different facts.
See [quality](quality.md) for the call sequence and limitations.

An integration binding configures a capability; a project attachment selects its
use. An application need (such as Postgres) is a runtime declaration, not an
integration permission. A secret binding describes delivery for a deployment;
it is not a grant of all provider credentials to a helper. See
[integrations and secrets](integration-runtime.md).

## Where work runs

Without a worker, agents, checks, previews and chats run in containers on the
AppTown host's Docker, and a script step without `executor: docker` runs as a
local process there. With `-worker-url`, agent, review, tests and Docker script
steps, checks, change previews, release builds and new change chats (by
default) run on `apptown-worker`, a separate machine that shares no files with
AppTown. A step the worker cannot run is refused, not moved to the host; a
local script step is refused. Project chats, step continuations, change chats
placed on AppTown or already running there, and deployments on the built-in
Docker host stay on the AppTown host. With `-no-local-docker` AppTown host has
no Docker and runs no project process, and refuses what the worker cannot run.
AppTown stays the owner of every record either way: the worker holds tasks by
key and returns commits and outcomes, and AppTown settles them. See
[workers](../operations/workers.md), [`internal/execution`](../../internal/execution/README.md)
and [`internal/worker`](../../internal/worker/README.md).

## Running and shipping

A bundle records release material. A deployment is a named placement on a target;
a release and its attempt record the selected material and outcome. Desired
generation, settled generation, observed health and last-known display state
answer different questions. A deployment owns exactly what AppTown recorded
creating for it; taking it down deletes that. On a Docker host its data goes
only after a complete final backup; other targets keep their own data, which is
listed rather than deleted.
See [deployment runtime](deployment.md#removing-a-deployment) and
[deployment targets](deploy-targets.md).

Identity supplies callers and current authority. Audit records selected decisions
and effects. Usage records workload inference; telemetry observes operation of
the daemon. None is a substitute for the others: telemetry is not a durable
application ledger or proof of permission. See [telemetry](telemetry.md).

For Go entry points and storage boundaries, continue with the
[ownership map](../architecture/domains.md), then the owning package's guide.

## Implementation references

- [internal/forge/projectlifecycle/service.go](../../internal/forge/projectlifecycle/service.go)
- [internal/forge/workflow/service.go](../../internal/forge/workflow/service.go)
- [internal/forge/conversation/admission.go](../../internal/forge/conversation/admission.go)
- [internal/forge/adoption/service.go](../../internal/forge/adoption/service.go)
- [internal/forge/deployment/service.go](../../internal/forge/deployment/service.go)
