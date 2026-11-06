# Workflow execution

Workflow owns admission, claims, state transitions and settlement. This package
orders the external work performed by selected runners and records its results
through `workflow.Execution`.

- `Executor` runs agents and reviews. Its constructor takes the model,
  scoped credentials, environment, harness and accounting collaborators it needs.
- `Commands` runs scripts and tests. Its constructor takes a process runner,
  progress, repository publication and panic reporting. It needs no model,
  harness, provider credential or session service.
- `agentenv` supplies shared environment policy. The Pi adapter owns Pi format
  and configuration; `execution.Runner` owns process/container mechanics.

Follow `internal/apptown/work_wiring.go` to the bound method, then `script.go`,
`tests.go`, `agent.go` or `review.go`. Script execution claims work, resolves its
source, invokes the process, records evidence and then publishes repository work.
Tests execute a committed tree and preserve the existing base-failure policy.
The different ordering is deliberate; there is no universal runner algorithm.

An agent's or reviewer's outcome is recorded after its paid invocation and
publication. Provider failure headlines come from `inference.Failure`; the Pi
adapter supplies the failed turn's identity and raw transcript evidence.
When a completed agent's work does not build on the published branch tip, the
refusal queues one automatic continuation through Workflow's rework mechanism.
The feedback tells the agent to preserve the unpublished commit and rebuild its
work as new commits on top of the branch tip. A local continuation can work in
the refused folder to reconcile it; preparation publishes neither that source
nor a main merge. Publication still requires a fast-forward.
The allowance is recorded with the rework receipt and resets when a person
retries or sends work back. A second refusal waits at `needs_human` with the
refusal and recovery action. Other explicit publication refusals wait for human
help immediately. The unpublished commit stays available.
Publication failures whose outcome is unknown remain errors, not refusals.
A failed recording is retried with the same outcome, under the
attempt fence and a bounded backoff (`settleProduced`); the invocation is never
repeated for it. Past the bound the step is an error saying its result could
not be recorded; if even that fails, it stays running for startup repair.

Scan/guidance policy remains in Verification; sharing an execution contract
does not make every runner one owner.

`Shipping` claims build and preview steps. A build stages the committed tree,
builds runtime images without booting a deployment or executing init steps, and
records a runtime bundle under the step's attempt fence. Projects without a
runtime record a clean-tree build. Previews boot separately through Preview.

With a worker (`ReleaseBuilds`, `release_build.go`), the build runs one task in
private Docker with source and generated build files, no deployment data,
environment, services or registry credentials. Deployment publishes the images
and commits the digest bundle under the build attempt. Without a worker,
Deployment builds images on its selected Docker build host and records those
image IDs, publishing them when the project has a registry. A restart ends a
build step instead of continuing it.

Work commits landing completion before Deployment requests the bundle for each
follows-main destination. Deployment's release execution owns authorization,
pre-release copying, provider effects and continuity checks. Build runners
receive no backup requester or release executor.

`Repositories` runs reconciliation and SCM merge-request steps. It resolves
Workflow source, invokes SCM/provider operations, then records evidence. The
root retains startup hook installation, but supplies no App to either runner.

`Calls` interpolates nested workflow inputs and invokes `Execution.Call`. Shared
preparation comes from `workcommands.Preparation`; Workflow owns child admission,
parent linkage and subsequent dispatch. No runner registry retains App.

Pi's capture bounds each tool-result string in the event stream to 16 KiB
of its start and end, plus a notice naming the bytes cut. This includes copies
repeated in later events, so build logs do not crowd out the final answer.
The events file omits `tool_execution_update` events because each repeats
cumulative output superseded by `tool_execution_end`. The live feed still
receives bounded updates. Assistant messages stay whole. Session transcripts
and the worker's result-file read limits are separate and unchanged.

Cache-warm usage counts toward spend, but only assistant message output counts
as a model answer. Agent and review attempts with zero assistant output tokens in the invocations read
for their outcome end as errors, even when a report or written files exist. The
reason includes Pi's last provider error when present. Output counts across
retries and follow-ups in memory; worker recovery reads only the newest
invocation and can reject an attempt whose earlier invocation answered.

Each paid invocation must record its usage before its answer can count. A failed
usage write makes an agent or review step an error, records the incomplete-usage
reason in its evidence, and prevents an empty-turn retry or review follow-up.
The transcript and parsed output remain available for diagnosis; rerunning a
model is not a way to repair missing accounting.

Steps declaring `needs: true` receive a prompt block naming the need services
started beside them and their connection environment variable names. The step's
`needs` evidence records those names or the refusal reason, without connection
values. A refused need is reported to the agent with an instruction to file
`apptown complain` and identify the tests it could not run.

Preview screenshots include failed stylesheet, script, image and font requests
per page and viewport, with host and path plus HTTP status or network error.
The approval panel and preview evidence show the failure count and a folded
list. Review briefs read the latest completed preview's screenshots and failures
from its evidence. Both review runners supply the recorded PNGs as read-only
files under `/apptown/in/preview/`. The prompt directs reviewers to open these
images to judge appearance and report when appearance cannot be assessed.
