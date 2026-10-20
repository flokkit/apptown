# Task attempts

`tasks` records the task a step attempt runs, before any provider effect, so a
restarted AppTown adopts it instead of running it twice. It has no workflow or provider knowledge; `workflowexec.TaskRunner`
coordinates both, through an `execution.TaskDriver` and the administrator's
task profile (locally `local-docker`: AppTown's own Docker, default network,
no limits imposed; with a configured worker, `worker`: the worker's Docker,
[design/operations/workers.md](../../../design/operations/workers.md)).

An attempt is keyed by its operation (`step-<id>-attempt-<n>`) and carries the
request digest, backend and provider name derived from the operation. A task
on a worker also records its source: the published commit it was given,
which its returned commits must build on. Its
record moves forward only:

1. `reserved`: recorded before the provider is asked; reserving the same
   operation again returns it, and a different request under the same key is
   a conflict.
2. The provider identity (a container ID) is recorded before the task starts,
   and cannot be replaced.
3. `started`, then `collecting` with the exit code (or a timeout), then
   `settled` once the step's outcome is recorded; `unknown` when the outcome
   cannot be established; `refused` when the backend refused it before
   creating anything, recorded before the step fails; `held` when its result
   was not kept, so its task and workspace on the backend hold the only copy
   of its work.

Each change of state is a named operation of `Records`, and each accepts
only its legal prior states; the caller chooses which applies, this owner
decides whether the record allows it:

| Operation | Records | Accepted from |
|---|---|---|
| `Start` | started | reserved, started, unknown |
| `Exited(code)` | collecting, with the code | reserved, started, unknown; never over a timeout |
| `TimedOut` | collecting, code -1, `timed_out`, and the stop, in one write | reserved, started, unknown |
| `Refuse` | refused | reserved, started, unknown |
| `RecordUnknown` | unknown | reserved, started, collecting, unknown |
| `Hold` | held | reserved, started, collecting, unknown |
| `Settle(code)` | settled | reserved, started, collecting, unknown |
| `SettleNothingHeld` | settled and released | any state but settled or held |

Settled is final. Held is final for AppTown: nothing settles or releases
it, its shared workspace stays, and its run stays open, until a person
inspects the backend ([workers.md](../../../design/operations/workers.md),
"Held workspaces"). Refused only settles with nothing held. A recorded exit
code and timeout are never erased: an operation given no code keeps the
recorded one, so a timeout whose stop only a restarted AppTown confirms
settles as a timeout. `detail` explains the last transition to people;
nothing decides by it. Any other transition returns `ErrSettled`, `ErrHeld`
or `ErrNotAccepted`. Such a refusal is a decision, not a failed write: a
caller does not try it again or carry on as if it were recorded; it reads
the record again and acts on that (`workflowexec`'s `endAsRecorded`).

An attempt also records the admission its task was given (profile, limits,
network policy, expiry), its deadline, when its wall-clock budget ends, and
the backend it was admitted to by identity (this installation's containers
on a Docker daemon named by its address, or a worker at its address). A recovered task keeps
all three: a restart never renews its budget, nothing is started after the
deadline (the admission's grace is for observing and stopping), a task seen
to have exited keeps its result however long AppTown was down, and an
attempt recorded for another backend than AppTown's current one is left
alone. Every call to a backend is bounded: before the deadline an
unanswering backend is asked again until it; after it, the task gets one
brief look and is otherwise stopped with its outcome unknown. The request digest is deterministic (sorted
environment, no labels) and is also the task's identity on its backend: a
backend holding a different request under the key refuses it rather than
handing it over.

A step attempt may run more than one program: each run is an
**invocation**, one row keyed by `(step, attempt, invocation)`. A script's
attempt has one, `script`. An invocation may name a **workspace** on the
backend it shares with the attempt's other invocations (empty: the task owns
its own, as a script's does); the worker keeps a shared workspace when a task
in it is released, and removes it only on request, once no task in it
remains (`DELETE /v1/workspaces/{key}`).

The request a task is admitted with is kept by AppTown from its
reservation, before the backend is asked for anything, in
`<data>/tasks/<operation>/request.json` (0600, written whole, never
replaced). Every later send of it (after a lost reply, or by a restarted
forge) sends those bytes, never a request rebuilt from settings that may have
changed since; its digest must still be the recorded one. A task recorded
before requests were kept has its request rebuilt and refused if it changed.

A settled task is released (its container, and its own workspace, removed)
and the release recorded only once the backend confirms it. One it does not
confirm is tried again every five minutes by AppTown's scheduler, among
its reaping duties (`workflowexec.TaskRunner.ReleaseSettled`), on the attempt's own
backend, until confirmed; then the kept request is removed. An attempt of
which nothing is held (refused before anything was created, or another
request's task holds its key) is settled released in one write, so nothing
is ever released under its key.

A stop is recorded (`desired = stop`) before the task is stopped, so a
restarted AppTown carries it out; the task is stopped before the step's
failure is written, so a failed write never leaves it running. A step's
outcome is always recorded before its attempt is settled and its task
removed; if that write fails, the task and its record stay as they are, the
workflow does not run the steps after it, and a restarted AppTown collects it
again. A task found only by its key is stopped or removed only if it was made
for the attempt's request: a key held by another request's task is refused
and that task is never touched.

At startup, required repair keeps the steps whose attempt has an unsettled
task instead of failing them, except a release build's
(`ReleaseBuildInvocation`): a restart ends its deploy step, and its task,
no longer its step's, is stopped, settled and released. `ResumePendingAutoSteps` then sorts the
unsettled attempts: a refused one has its step failed, if still its, and
is settled, whatever the backend, since nothing exists to observe; one whose
step no longer belongs to it is stopped and settled; one recorded for
another backend than this AppTown's is left alone
and reported; the rest are handed to the workflow's dispatcher, which
continues each in its place in its stage (`workflowexec.RecoverScript`) and
continues the run once it settles, within the dispatcher's own waiting and
shutdown. Continuing a task observes it: a created container is started, a
running one waited for, an exited one collected, and one that disappeared is
an unknown outcome. A backend that does not answer is asked again until the
attempt's deadline; nothing is started under an attempt that may already have
run, and a lost start reply is settled by observing the task.

Local Docker starts a task only if it never ran, and serializes starts of
one task within a process, so overlapping starts run it once. Script tasks
also run their command behind a run-once guard (a marker file in the
container's `/tmp`), for a start issued by another process. Local Docker task containers are named with a namespace
derived from AppTown's data directory, so two AppTown installations sharing a daemon do
not see each other's tasks.

Docker script steps use tasks, and so do agent steps on an AppTown with a
worker: each run of Pi in an attempt is an invocation (`agent`, then at most
one `agent-retry`), sharing the attempt's workspace, driven one at a time by
the same runner; a restarted AppTown continues the attempt's newest
invocation (`workflowexec.RecoverAgent`). A shared workspace is released
once every invocation in it is settled and released, and its release is
recorded when the backend confirms it (`workspace_released_at`, on every
row naming it); one not confirmed is tried again with the other releases. A
held invocation (its outcome unknown) keeps its workspace. The kept request
of an invocation carries its input blobs beside it (`blobs/<sum>.tar`), so a
recovered request can send any the backend no longer holds. A local script,
and every other step type, still runs without a task record: startup repair
fails its step as before, and a container it started is not adopted.
