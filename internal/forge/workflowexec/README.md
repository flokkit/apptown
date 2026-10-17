# Workflow execution

Workflow owns admission, claims, state transitions and settlement. This package
orders the external work performed by selected runners and records its results
through `workflow.Execution`.

- `Drafter` drafts a workflow with one tool-free model call through the
  inference owner (`inference.Gateway.Complete`); it starts no process.
- `Executor` runs agents and reviews. Its constructor takes the model,
  scoped credentials, environment, harness and accounting collaborators it needs.
- `Commands` runs scripts and tests. Its constructor takes a process runner,
  progress, repository publication and panic reporting. It needs no model,
  harness, provider credential or session service.
- `agentenv` supplies shared environment policy. The Pi adapter owns Pi format
  and configuration; `execution.Runner` owns process/container mechanics.

Follow `internal/forge/work_wiring.go` to the bound method, then `script.go`,
`tests.go`, `agent.go` or `review.go`. Script execution claims work, resolves its
source, invokes the process, records evidence and then publishes repository work.
Tests execute a committed tree and preserve the existing base-failure policy.
The different ordering is deliberate; there is no universal runner algorithm.

An agent's or reviewer's outcome is recorded after its paid invocation and
publication. A failed recording is retried with the same outcome, under the
attempt fence and a bounded backoff (`settleProduced`); the invocation is never
repeated for it. Past the bound the step is an error saying its result could
not be recorded; if even that fails, it stays running for startup repair.

Scan/guidance policy remains in Verification; sharing an execution contract
does not make every runner one owner.

`Shipping` claims deploy/preview steps, selects their source, invokes Deployment,
and translates the result into evidence and settlement. Deployment owns provider
execution and generation guards; a narrow backup requester admits the existing
durable follow-up. No model service or App is available to these runners.
On a forge with a worker (`ReleaseBuilds`, `release_build.go`) a landing's
deploy step first refuses what would stop the release (no container
registry, no `-private-docker`, `main` not on a Docker host, an unknown or
mismatched platform), then runs one `release-build` task through
`TaskRunner` (private Docker, the commit as source, generated build files,
no environment or credential), has Deployment publish its images and commit
the digest bundle under the step's ownership, settles and releases the task,
and only then deploys that bundle, pinned on the generation. A restart ends
a deploy step instead of continuing it.

`Repositories` runs reconciliation and SCM merge-request steps. It resolves
Workflow source, invokes SCM/provider operations, then records evidence. The
root retains startup hook installation, but supplies no App to either runner.

`Calls` interpolates nested workflow inputs and invokes `Execution.Call`. Shared
preparation comes from `workcommands.Preparation`; Workflow owns child admission,
parent linkage and subsequent dispatch. No runner registry retains App.
