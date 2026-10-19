# Workflow state and ownership

## The model

A Change is proposed work and its repository identity. A Run executes a pinned
workflow definition for that work or a standalone purpose. A Stage materializes
its declared steps and gates. A Step is a persistent unit of execution or decision;
an attempt identifies one admitted execution of it. Retrying a step does not give
an old runner authority over the new attempt.

Workflow owns `ChangeState`, `RunState` and `StepState`. Their zero values are
invalid; parse/string/transition operations live beside them. A Change can be
proposed, in progress, landing, shipped or abandoned. A Run is running, done or
abandoned. Step outcomes are a different vocabulary. Do not infer these rules
from free-form activity text, a UI label or a deployment state. Stored words
decode strictly: an unknown word is an error, never read as a default, terminal
or successful state.

A run pins its workflow definition. Editing a library workflow does not rewrite
that run. A run retry revision fences decisions made before reset/rework; it is
separate from a step's attempt number. Reads return typed snapshots, not authority
to mutate the records or an entire eagerly loaded object graph.

## Admission and external work

1. A transport establishes a verified caller. Workflow checks the named operation
   and its resource; root and instrument handling follow the shared authority
   policy, with domain eligibility checked in Workflow.
2. `workcommands.Preparation` resolves the selected adoption, workflow and source.
   Repository effects and independent reads do not run inside the admission write
   transaction. The admission owner rechecks the facts that must remain current.
3. `Service.CreateAndRun` commits the Change and initial Run together for invocation
   paths. Creating a proposal without a run remains a distinct operation. Admission
   atomicity does not make subsequent workspace preparation or dispatch atomic.
4. The dispatcher starts work after commit. A refused shutdown handoff is reported,
   not treated as successful dispatch. Preparation/dispatch failures retain their
   recorded identity and explicit failure handling.
5. A runner receives `Execution`, then calls `Claim`. Workflow rechecks current
   run/stage, configuration and attempt under lock. External work belongs to the
   runner owner. `Execution.Finish` accepts only the admitted claim and records
   the outcome and required receipts transactionally.

An ordinary row read cannot manufacture a valid claim. A failed settlement is an
error, not evidence that a desired status persisted. Telemetry observes this
result; it is not the authoritative work record.

## Decisions, reruns and completion

Approvals, overrides and retries recheck the relevant decision snapshot and
current authority. Root's approval eligibility exemption does not bypass quorum
or stale-decision checks. Retry, automatic rework, send-back and landing recheck
share revision fencing but have different eligibility and preservation rules.
They are not one command with interchangeable flags.

A running run's detail snapshot lists its next acts (`RunSnapshot.Next`):
`approve` for the approvals it waits on, `override` for failed or errored checks
blocking its stage, `retry` for stopped steps a step run accepts again, and
`rework` for another round of the stage agent, each naming its step; and the
run acts `recheck` (missing evidence a rerun can record), `advance` and
`restart-landing` (a landing whose rules changed). The change page's projection
carries the change acts from `ChangeActs`: `land` (in progress, nothing
running, no landing queued) and `send-back` (open, blocked, not merged). A
landing queued for accepted work is the stated fact `LandingQueued`. They say what is available, not who may do it: every command
still checks authority and freshness when it arrives. Clients submit these acts
rather than pick targets from step types or statuses, and branch on a
refusal's code, never its sentence (another change landing is
`landing-occupied`).

`RetryStep` operates on eligible step work; `SendBack` is the Change operation
for returning completed work for revision. Rework retains feedback and records
its budgeted transition together with the failed verdict. Resets invalidate only
what the operation's dependency rules require. Historical definitions and evidence
are not silently replaced by the current project defaults.

Advancement checks the recorded gate and revision. Completion, required receipts,
parent-call handoff and required follow-up intent commit together. Parent handoff
checks the exact child/call identity so a stale child cannot finish a newer call.
Git and provider effects are outside these database transactions. SCM and
Deployment retain their own effect identities and settlement fences.

A stage's gate reads the checks its `stage.entered` receipt says it expects, and
a stage keeps the gate rules it was entered under: a newer evidence rule never
judges an older stage more loosely, and its scan steps never stand in for the
record of which scanners it expected. A stage that cannot pass as recorded
(its gate names missing evidence) is shown as blocked with that reason wherever
the run's activity appears, and while it would otherwise ask a person to
approve. `RecheckStage` is its way on: it plans the stage again from the
run's pinned definition, records a new receipt of expected checks, and starts a
new attempt of every check and approval in the stage, keeping the earlier
results in `step.invalidated` receipts. It refuses a stage with nothing missing;
single steps are retried instead. Without it, a stage entered before the
record of its expected scanners existed would wait forever as "ready to
advance".

Durable completion/abandonment effects run through Jobs and `workcommands` after
commit. Queue delivery is not proof of a successful domain outcome. Repeated
advancement of completed work can request retained failed follow-ups without
recreating successful completion records; queue history retention limits what
can be retried that way.

## Restart and stopping

Construction is not recovery. Normal daemon startup performs required database
repair before writable operation, then the owning recovery operations inspect
admitted work. Pending automatic work can resume from its pinned definition.
Interrupted external execution is not universally reattached or reconstructed;
explicit retry/send-back and owner-specific inspection remain distinct paths. Recovery re-asks
each settled stage's gate, since its answer can change; the system's repeat of
an identical refusal, and the recovery notice before it, are not recorded again.
A person's attempt to advance is always recorded.

The dispatcher closes admission before waiting for its running work. The daemon
has one bounded shutdown deadline; a timed-out wait does not establish that an
external process ended. Persistent console lifetime is separate from a workflow
attempt. See [daemon lifecycle](daemon-lifecycle.md) and [Jobs](jobs.md).

## Reads and package navigation

Workflow's SQL queries map stored rows into typed records and projections.
Lists and aggregates apply authorization scope before limits and counts. A read
projection grants no execution or transition authority.

The [package guide](../../internal/forge/workflow/README.md) lists the public
operations and their implementation files. Owner tests cover claims, stale
attempts, atomic admission and completion, retry, send-back and parent handoff.
Storage tests retain independent SQL assertions where representation matters.

## Implementation references

- [internal/forge/workflow/service.go](../../internal/forge/workflow/service.go)
- [internal/forge/workflow/run_admission.go](../../internal/forge/workflow/run_admission.go)
- [internal/forge/workflow/execution.go](../../internal/forge/workflow/execution.go)
- [internal/forge/workflow/change_create_run_test.go](../../internal/forge/workflow/change_create_run_test.go)
- [internal/forge/workflow/retry_transactions_test.go](../../internal/forge/workflow/retry_transactions_test.go)

