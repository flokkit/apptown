# Execution primitives: shared mechanics, distinct product lifecycles

Status: **review findings and proposed direction, 2026-09-06**, reviewed independently.
Companion to [code-structure.md](code-structure.md). The inventory describes
the reviewed implementation; the boundaries and sequence below are proposals,
not a new implemented runtime or a replacement workflow engine.

Implementation update (2026-09-09): process execution now lives in
`internal/execution`. Its `Runner.Run` accepts an explicit context, backend,
and request, and returns output, exit code, termination cause, and any cleanup
error. It has no dependency on Forge's database, workflows, or `App`.
`internal/forge/executor.go` supplies Forge's environment, mounts, and ownership
labels; callers still interpret execution results and record domain evidence.
Local cancellation kills the owned process group on Unix; Docker cancellation
removes the owned container with an independent, bounded cleanup context.
Output capture and streaming fragments are bounded. Workflow callers currently
use background contexts because their jobs outlive HTTP requests; this extraction
does not add a workflow cancellation lifecycle. Agent sessions, model requests,
and deployment orchestration remain separate follow-on work. The inventory below
records the earlier review state.

Implementation update (2026-09-14): Work now invokes every workflow runner with
`workflow.Execution`. Work supplies detached run/step input, owns the delayed
attempt claim and records typed outcomes, including atomic failure/rework.
Runners retain tool execution and publish their artifacts before returning;
Work then refreshes evidence. Stage advancement remains in the dispatcher, after
ordered entry work and scanner groups. A workflow call uses the original parent
snapshot to admit a child whose completion owns the call result. These changes
remove `App.startStep`, `finishStep`, `failWithRework`, `refreshAssertions`,
`failUnstartedStep` and `stillWanted`; they do not change chat lifetime or the
shutdown deadline.

Related: [architecture.md](architecture.md), [console.md](../subsystems/console.md),
[container-lifecycle.md](../subsystems/container-lifecycle.md), [model package](../../internal/forge/model/doc.go),
[authorization-capabilities.md](../subsystems/authorization-capabilities.md), and
[application-forge-interfaces.md](application-forge-interfaces.md).

## 1. Position

Forge already has useful execution primitives. Make their boundaries
explicit and extend their reuse rather than introduce a second orchestration
system around agents.

Three facts must remain distinct: **a process exited successfully, an agent
finished its turn, and a check passed**. Execution reports what happened;
the product operation interprets the outcome and records its meaning.

Checks, chats, and agent work share mechanics, not necessarily lifecycles.
A chat that survives disconnection should not be represented as a batch
command with an enormous timeout. A tool-free model judgment should not
need an agent sandbox. Workflow sequencing stays in the workflow engine.

## 2. Existing primitives

Paths below are relative to `internal/forge` in the reviewed code.

| Primitive | Implementation | Current consumers and behavior |
|---|---|---|
| Process execution | `executor.go`: `ExecRequest`, `execute`, `ExecOutcome` | Local and Docker execution for scripts, tests, scanners, conformance checks, and agent processes; output capture and streaming, timeout handling. |
| Bounded agent invocation | `agent.go`: `piCall`, `piInvoke`, `piResult` | Implementation and agent-review steps; ambient environment and CLI staging, transcript parsing, live summaries, usage recording, limited empty-output retry. |
| Tool-free model request | `narrative.go`: `openrouterComplete` | Narratives, briefs, drafting, and conformance judgments; direct OpenRouter request returning text and usage. |
| Persistent interactive session | `console_session.go`, `console_container.go`, `console_pisession.go` | Session identity and recovery, container lifecycle, tmux/PTY attachment, incremental transcript reading. |
| Workflow execution records | `engine.go`: `startStep`, `finishStep` | Transactional recording of step state and associated events; completion includes evidence. |

Additional building blocks already exist for model-slot resolution,
ambient toolchains, credentials, sidecars, private Docker daemons, workspace
harvesting, and container ownership labels. Their presence matters: this is
not a proposal to replace all execution with a new library.

## 3. Four responsibilities

### Execution environment

Answer: **where does this work happen, and what can it access?**

Own the mechanics of workspace placement and access mode, mounts, toolchain
preparation, network attachment, credential delivery, sidecars, and cleanup.
Today these are distributed across executor helpers, agent sandbox setup,
console provisioning, and individual check runners.

The product operation selects an authorized environment configuration;
the environment implementation realizes it. Merely requesting a mount or
credential must not grant authority to use it. Keep local execution's lack
of isolation explicit, and do not claim an unsupported restriction is enforced.

Share the preparations that genuinely match. A temporary check workspace
and a persistent conversation workspace have different retention and
ownership requirements. Environment lifetime need not equal process lifetime.

### Process execution

Answer: **start this command, observe it, and tell me how it ended**.

Accept explicit dependencies and a caller-owned context. Support output
streaming, bounded capture, waiting, and cancellation. Return a typed outcome
distinguishing normal exit and exit code, timeout, cancellation, and failure
to launch. Define how cleanup failures are surfaced without losing the
original outcome.

Keep this layer unaware of workflow approval, conformance verdicts, model
usage, and application database records. Reject an unknown backend. It is
not permission to execute on the Forge host.

Cancellation must concern the owned workload, not merely its launcher:
stopping a Docker client does not establish that its container stopped.
Likewise, specify and test what happens to child processes for a local run.
Stopping an attachment to an interactive session is a separate operation.

### Model and agent adapters

Answer: **perform this model interaction using the selected runtime**.

Keep distinct APIs for tool-free completion, bounded agent invocation, and
persistent interactive sessions. Share appropriate model configuration,
credential resolution, usage types, and transcript interpretation without
forcing every consumer through the same lifecycle.

Adapters own provider/runtime transport and output interpretation. They
return usage and execution facts or emit narrowly defined events; they do
not require an entire `App`, `Run`, or `Step` merely to call a model. The
owning product service attaches these facts to the right durable records.

Preserve requested and actually served model identity where available.
Keep provider failures distinct from invalid structured output and from a
valid answer that reports a failed check.

### Product operations

Answer: **what work is being done, and what counts as success?**

Running a check, implementing a change, reviewing a diff, and conducting a
conversation retain their own input preparation, authorization, acceptance
rules, evidence, and lifecycle. These operations consume execution primitives.

Retry policy must be explicit at the appropriate level. A command or agent
may have changed files or external state even when its final output is
missing. Do not add blanket automatic retries to the process runner.
Preserve and characterize existing retry behavior before deliberately
changing it. Admission limits and accounting ownership should also be
explicit; do not assume batch-agent slots cover chats or direct model calls.

## 4. Concrete gaps in the reviewed implementation

- `execute` constructs a timeout from `context.Background()`. Its caller
  cannot propagate cancellation through that API.
- Its executor switch falls back to local execution for any value other
  than Docker, including an unrecognized backend name.
- `ExecOutcome` contains `RunErr` and `TimedOut`, but no explicit exit code
  or cancellation classification. `RunScript` inspects `*exec.ExitError`,
  coupling interpretation to the local process representation.
- `piInvoke` accepts workflow `Run` and `Step` objects and directly updates
  live feeds, retry events, and usage records. Useful invocation mechanics
  are coupled to their current product consumer.
- `openrouterComplete` lives in `narrative.go` despite several unrelated
  consumers. It resolves credentials through `App` and does not accept a
  caller context.
- Console and step environment preparation have overlapping mechanics but
  no explicit shared environment boundary. Their distinct lifecycles are
  valid; duplication should be judged at the preparation level.
- Test substitution for `piRunner` and conformance execution uses package
  variables. Instance-level dependencies would make ownership clearer and
  avoid shared mutable hooks when tests execute concurrently.

These are code-review findings, not claims that every item has caused a
production incident. In particular, the separate console implementation is
not itself a defect.

## 5. Incremental sequence

1. Characterize the existing runner's command construction, output,
   timeout, and cleanup behavior. Extract it with explicit dependencies;
   it should be testable without a Forge database.
2. In separately identified behavior changes, add caller-context support,
   typed termination outcomes, and rejection of unknown backends. Preserve
   deliberately selected local execution and documented defaults.
3. Extract direct model transport from narratives. Keep prompt construction,
   conformance verdict parsing, and evidence recording with their consumers.
4. Separate Pi invocation from workflow storage/live-feed wiring. Retain a
   thin workflow adapter so existing step behavior has one implementation.
5. Consolidate matching environment setup incrementally. Preserve console
   session identity, transcript continuity, and detach/reconnect semantics.

Do not require a generic scheduler, public execution plugin ABI, remote
worker protocol, or universal agent-session API for this work. Do not turn
every helper into an interface. The process runner is a smaller independent
starting point than the deployment extraction in `code-structure.md`.

## 6. Verification and completion criteria

Test successful and nonzero exits, launch failure, timeout, caller
cancellation, workload cleanup, output capture limits, and unknown-backend
rejection. Verify that failing to launch is not reported as a failed check.

For model adapters, test provider failure, malformed structured output,
empty output, retry accounting, and preservation of model identity. For
sessions, test that detaching a viewer does not cancel the conversation,
while explicit stop and recovery retain their intended behavior.

Preserve product evidence, usage attribution, authorization, and secret
handling through extraction. Keep durable completion recording separate
from best-effort progress delivery; moving database writes out of an
adapter must not accidentally make those writes optional.

The result is successful when a new check can reuse execution without
rebuilding process management, and when an agent-runtime change can be
tested without constructing a workflow—while the workflow remains the
authority on what the work means and whether it is accepted.
