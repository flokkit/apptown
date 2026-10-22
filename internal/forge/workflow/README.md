# Working on Workflow

Workflow owns Changes, Runs, Steps, decisions and evidence. A Change is the work
being proposed; a Run executes a pinned workflow definition against it; a Step is
one task or check in that run. The records and their state types live here, along
with the rules and transactions that change them.

## Start here

Construct `Service` once with `New(db, Dependencies{...})`. Both are in
[service.go](service.go). HTTP, MCP and other interactive callers submit commands
through that service with the verified caller in their context. Actor text in an
input is never permission.

`NewChanges(db, authority)` supplies checked `Change` reads and `Uplift` admission
without constructing a dispatcher. Preview and Adoption use it before runner
construction. `Service.Change` and `Service.Uplift` share those implementations:
current authority is checked, and uplift's Change, exact adoption target and
required receipts still commit in one project-locked transaction.

| What you want to understand | Entry point | Implementation to read next |
|---|---|---|
| Create or begin a Change | `Service.Create`, `Service.Begin` in [change_service.go](change_service.go) | [change_creation.go](change_creation.go), [change_transition.go](change_transition.go) |
| Start a Run | `Service.StartRun` in [run_service.go](run_service.go) | [starter.go](starter.go), [run_start_store.go](run_start_store.go) |
| Run one Step | `Service.ExecuteStep` in [execution.go](execution.go) | [step_attempt.go](step_attempt.go), [step_outcome.go](step_outcome.go) |
| Approve or override | `Service.Approve`, `Service.Override` in [decisions.go](decisions.go) | [decision_store.go](decision_store.go), [approval.go](approval.go) |
| Retry a Step | `Service.RetryStep` in [retry.go](retry.go) | The transaction and dispatch handoff are in the same file |
| Send completed work back | `Service.SendBack` in [sendback.go](sendback.go) | The same file composes abandonment and retry in one transaction |
| Run a stage's checks again | `Service.RecheckStage` in [stage_recheck.go](stage_recheck.go) | Only for a stage missing evidence; the gate's verdict in activity is in [gate_activity.go](gate_activity.go) |
| Recheck a landing | `Service.RecheckLanding` in [landing_recheck.go](landing_recheck.go) | The same file selects and resets the checks |
| Stop work | `Service.Abandon` in [abandonment.go](abandonment.go) | The same file owns the abandonment transaction |
| Read a Run or its history | `Service.Run`, `Snapshot`, `Runs`, `ChangeRuns` in [scoped_reads.go](scoped_reads.go) | [queries.go](queries.go), [record_store.go](record_store.go) |
| Start or stop background dispatch | [runtime.go](runtime.go) | [dispatcher.go](dispatcher.go) |

[authority.go](authority.go) maps internal operations to permissions and checks
the caller. [records.go](records.go) defines the stored records; the `*_state.go`
files define their state vocabulary. [objects.go](objects.go) holds the pure
record rules. Generated SQL lives in `internal/workflowsql`; application callers
do not use it directly.

A step's evidence is its runner's own JSON, except for the facts other owners
judge. Approvals are typed in [approval.go](approval.go). A check step's
receipts, check errors, undecided findings and placement are typed in
[check_evidence.go](check_evidence.go): runners write them with
`CheckEvidence.Outcome`, every reader uses `ReadCheckEvidence`, and evidence
it cannot read is reported as unreadable, never as nothing. The run snapshot
gives the page that reading as `Step.CheckReading`.

## Follow a retry

The caller invokes `service.RetryStep(ctx, RetryCommand{StepID: id, Mode:
RetryContinue, Feedback: note})`. The service owns this sequence:

1. Verify the caller, then read a `stepDecisionSnapshot` of the step's scope,
   attempt, run retry revision, stage, type and configuration.
2. Authorize retry against that observed project, Change and Run. No SQL
   transaction is held while asking the authority.
3. Start the transaction, lock the current Run and Step, and compare them with
   the authorized snapshot. A mismatch returns a retry conflict.
4. Check that the stage is active, the step is automatic and its prerequisites
   permit retry. Preserve the intended configuration/session/feedback, reset the
   step and dependent verdicts, invalidate the old run decision, and write the
   receipts. A failed write rolls the transaction back.
5. Only after commit, hand the pending work to the dispatcher. Shutdown can
   refuse dispatch; pending work remains subject to the existing restart policy.

The snapshot comparison is shared with approval and override in
[decision_store.go](decision_store.go). Each command keeps its own error messages.
The stricter assertion comparison in [assertions.go](assertions.go) is a different
rule and is not interchangeable with this snapshot.

Useful executable examples are existing tests:

- `TestRetryServiceRequiresAuthorityAndRechecksItsSnapshot` in
  [retry_test.go](retry_test.go): caller admission and concurrent scope changes.
- `TestRetryRequiresEveryWriteAndReceipt` in that file: transaction rollback.
- `TestWorkflowRetryEntryPrerequisitesAndSkippedDependents` in
  [retry_transactions_test.go](retry_transactions_test.go): an admitted workflow
  with a producer and dependent checks.

Run them with the repository's configured test database:

```sh
go test ./internal/forge/workflow -run 'TestRetry|TestWorkflowRetry' -parallel=8
```

## Commands, transaction participants and runtime hooks

These are different contracts even when they are methods on the same service:

- **Commands** such as `RetryStep` and `Approve` authorize the caller and own the
  complete operation.
- **Transaction participants** such as `AdmitRunIn`, `StopScope.AbandonIn` and
  `RecheckLandingIn` join an operation already authorized by its owning service.
  They verify attribution; the caller owns authorization, commit and rollback.
  Dispatch happens after commit, never while that caller's transaction is open.
- **Runtime hooks** such as `DispatchStarted`, `ResumePending` and `BeginStop`
  serve the daemon. They are not user-command authorization boundaries.
  `BeginShutdown` is `BeginStop` when AppTown itself stops: from then on
  `Execution.Settle` records only a pass. The processes a stopping forge's
  steps run are killed with it, so any other verdict is left unrecorded, the
  step stays running, and startup repair takes it up as it would after a
  crash. Tests that stop dispatch to run steps themselves call `Stop`, which
  does not change what is recorded.

Keep public command inputs, results and dependency contracts exported even if
callers only use inferred values. Keep implementation components private where
possible. The remaining stage-construction test dependency is documented below;
its callers need a fixture redesign before it can be made private safely.
Do not add a new constructor simply to let a transport test bypass the service.

Ordinary Workflow tests should use the admission helpers in
[admission_fixture_test.go](admission_fixture_test.go). Owner persistence tests
may need the deliberately sparse records in
[stored_record_fixture_test.go](stored_record_fixture_test.go). Keep independent
SQL assertions where they protect representation or transaction behavior.

The advancement, dispatch-plan, narrative and landing-recheck components and
constructors are now private. Root tests exercise `AdvanceFrom`,
`PublishNarrative`, `RecheckLanding` and actual dispatch instead of rebuilding those
components. Their private invariant tests stay here.

`NewStages` remains exported for two Remediation test files that construct stage
history while testing withdrawal and consistent observations across owners.
There is no production caller outside Workflow. It is a specifically retained
fixture dependency, not the recommended admission API. Replacing those histories
requires preserving their exact old/current-stage and malformed-quorum guarantees;
do not hide them behind a new exported fixture API or silently weaken them.

## Stop work at the merge boundary

`Abandon` and `SendBack` acquire SCM's repository lock before opening their
transaction. Under the project row lock, they check every run of the change
against SCM's durable intents. Pending intent requires inspection; a recorded
merge requires delivery or a fix forward. Neither permits closure.

A cross-domain operation such as remediation calls `PrepareStop` before opening
its transaction, defers `StopScope.Close`, and calls `StopScope.AbandonIn` inside
the transaction. Commit or rollback must finish before `Close`. The participant
checks attribution and scope; the surrounding command owns authorization.
Never acquire repository serialization while holding a SQL transaction.

This protects local merges. Provider merges still lack an intent before their
remote effect; an unrecorded remote outcome needs separate handling.

## Who works where

This section is the current statement of how workers share a change.
`scm`'s [guide](../scm/README.md) covers publication and merging in
detail.

### One holder per change

A change has at most one **holder**, the one worker allowed to change it:

- a chat (`chat:<id>`);
- a writing run, any root run that is not a landing (`run:<id>`);
- a desktop checkout's ticket (`ticket:<id>`);
- a momentary request (`request:<kind>-…`): a landing's admission, a
  desktop checkout or the services editor, when nobody else holds the
  change and it must publish or write the folder for a moment.

The holder is the change branch's `scm` authority (`branch_authority`). Its
generation advances whenever a different holder takes it (taking again as
the same holder changes nothing). `TakeChangeIn` and `HolderEndedIn`
(`change_holder.go`) are the one rule for it.

**Taking.** Every worker takes the change in a transaction before anything
touches its folder: opening a chat, starting a writing run (including a
continuation that sends work back), issuing a ticket, and a momentary
request (`TakeRequestIn`). While another holder has not ended, the take is
refused with `ChangeHeld`, which says who holds the change. Opening a chat
on a change returns the chat that holds it; opens of one change wait for
each other. A chat that does not hold its change never runs: its launch is
refused.

**No handovers.** A holder is never taken from its owner. It is free only
once its owner has ended, judged from the owners' records whenever someone
asks (`Presence`); anything unknown has not ended:

- a run: its tree has finished or been abandoned, no step is running and
  no task is unsettled;
- a chat: it is neither registered, nor held while it opens or ends
  (`console.Service.Hold`), nor recorded live on disk, and its agent and
  private Docker daemon are confirmed absent (`console.Service.Gone`). An
  end that cannot remove the runtime fails and leaves the chat live; so
  does an end asked while the chat's agent is starting;
- a ticket: revoked, expired, deleted, or released by its sync;
- a momentary request: not running in this process.

Then, whoever held it, nothing may still be writing the folder. Every
container that mounts a change's folder writable is labelled with it
(`forge.writes=<folder>`): an agent, a review's wrap-up, a script task, the
agent's private Docker daemon (and with it anything the agent started
there), a chat and the chat's daemon. The holder has ended only once Docker,
asked at that moment, lists no such container; any it lists are removed
first, and a Docker that cannot be asked confirms nothing. This finds what
no record does: a runtime whose record was lost, or one an earlier process
started. A refusal for this reason says something an earlier worker started
is still running. Only a take under the branch's lock removes anything;
checks made before it (a checkout's, a landing's) read records and ask
Docker, and remove nothing.

Within a run, each attempt of an agent or review step starts only once
nothing is left writing the folder but the containers of other steps at
work now, running in their run's current stage (`forge.step=<id>` says
whose; a step that cannot be read refuses the attempt): an earlier attempt of this step,
its private Docker daemon, or a sibling step's container that outlived it
is removed first, or the attempt fails. Steps that write one folder do not overlap: an agent, review or script
step takes its folder's lock, in AppTown's process, before it claims its
attempt, and holds it until it has finished, so a second step asked for
meanwhile waits its turn and then claims only if its run is still at that
step. Script recovery takes the same lock and then confirms its attempt is
still current. A script, like an agent, first removes what is left writing
the folder (a container that outlived its step or AppTown, however its
step is recorded) or refuses; recovery does the same before starting a task
or collecting and publishing its result, keeping only the task's own
container, and leaves a task it cannot yet check recorded for later.
Every attempt of a step, a script's included, and a script task that
recovery would start for the first time, also waits while any other script
task of the run is unsettled, whatever its step's state: its stop could
fail while the attempt waits to start. A script checks again after
recording its own task, so two admitted at once see each other and neither
starts. The other task stays unsettled until its stop is resolved. A task given a
commit ran on a worker's own checkout and does not count. So a restart
never resets the folder under a surviving writer. A review's wrap-up waits for the reviewer before
it; an agent with a private Docker daemon is not re-run in place after an
empty turn (a retry starts it afresh).

A release (a ticket's sync) records the holder `released`. Taking a stuck run
to chat stops the run first; the chat takes the change once the run has
ended.

**Closing.** A landed or abandoned change takes no more work. Its chats are
ended when it closes, a chat on it never runs again, and uncommitted files in
its folder are not kept.

**Startup.** Before any work resumes, every restored chat that does not
hold its change is ended, its transcript kept. One whose runtime cannot be
removed never runs again, and its runtime's label keeps anyone from taking
the change until it is gone; if another worker already holds that change,
AppTown refuses to start. A chat whose record could not be restored is not
seen at startup; its runtime's label protects its change the same way.

### One working copy

A change's folder (`workspaces/change-<id>`) is its one working copy, used by
whoever holds the change (`Source.Workspace`). Opening a chat and preparing a
writing run first bring the folder to the branch as published
(`GitWorkspace.BringToBranch`): anything left uncommitted is committed as the
previous worker's, the folder is fast-forwarded, and only then is a run's
restart point fixed (`PrepareRun`; a continuation fixes its own). A folder
that has diverged from its published branch is left for a chat to merge
(`PublishedRef`); a run is refused. A desktop checkout, a landing's admission
and the services editor do not prepare the folder this way: they publish, or
write, only what they must, and leave other files as they are.

### Work travels as commits

Only the holder publishes the change's branch, under its own grant:

- a run's steps, as they pass;
- a chat's commits, when it ends, and when a landing or a recheck it asks
  for needs them. Uncommitted files stay in the folder and are named as not
  included. A publication that fails when a chat ends (the branch moved, or
  the checks refused its history) is logged and leaves the commits in the
  folder; the chat still ends;
- a desktop push, while its ticket is valid at the moment of publication;
- a momentary request, for the folder's committed work.

Every publication is checked in `scm` for what it brings in: nothing under
`.apptown` but `.apptown/app.yaml`, and size limits per file and per publication
([how new work and its size are counted](../scm/README.md)). Secrets are not
scanned here.

### Desktop checkouts

A checkout is refused when the change's folder is not on the change's
branch. It publishes the folder's committed work (a momentary request), then
issues a ticket that takes the change; the caller's own earlier ticket is
replaced. The desktop pushes to the ticket's incoming ref, and the push is
published only if the ticket still holds the change and is still valid. A
push does not release the change: only a sync does. Sync is for the
caller's own still-valid holding ticket; it refuses a folder with
uncommitted files, or one no longer at the tip the caller expected, then
adopts the published branch into the folder and releases the change. It
holds the repository lock throughout, so the next worker waits for it.

### A landing reads

A landing takes no holder and holds no grant, so it cannot publish. One
landing runs at a time in a project. At admission the change's committed
work is published first (the holding chat's grant, or a momentary request's
when nobody holds the change). A run or a ticket holding the change refuses
the landing, and so does uncommitted work with nothing committed. The
candidate is the branch as published; it must already contain the default
branch (the reconcile step only checks and pins). Scripts, tests and scans,
in the landing and in workflows it calls, check copies of the candidate.
Agent steps and workflow calls cannot be bound to the candidate, so a
passed one refuses the merge ([scm](../scm/README.md#checked-landing)). A
source scanner that declares a `historyCommand`, as stock gitleaks does,
checks every commit the landing adds, from target to candidate, after
verification reads that range in the project's repository (a range it
cannot read errors the scan); its findings gate as found, with no
comparison against main's tree. A tree-only scanner sees only the
candidate's files, and a scan point with no scanner attached passes. A merge request may land it only when GitLab guarantees the
candidate lands exactly (see `scm`).

Change-level comparisons (the change's diff, commits and narrative) read the
change's folder, as the change's working copy.
