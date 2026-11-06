# Workflow state and ownership

## The model

A Change is a unit of work and its repository identity. A Run executes a pinned
workflow definition for that work or a standalone purpose. A Stage materializes
its declared steps and gates. A Step is a persistent unit of execution or decision;
an attempt identifies one admitted execution of it. Retrying a step does not give
an old runner authority over the new attempt.

Workflow owns `ChangeState`, `RunState` and `StepState`. Their zero values are
invalid; parse/string/transition operations live beside them. A Change is in
progress, landing, shipped or abandoned. A Run is running, done or
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

An active run needs its own recorded start before workspace preparation, and
committed readiness before runner invocation, claims, assertion observation,
child admission, retries or advancement. An unready start runs actual preparation
and pins before recording readiness. Missing start evidence refuses these acts;
it never authorizes reconstructing preparation or fabricating `run.ready`.
Abandon such an active run and start new work through the ordinary operations.
Completed historical runs remain readable and can supply a new continuation or
accepted landing. The new run has its own start and preparation; its source
history is unchanged. Settlement of an already-admitted outcome remains possible;
if readiness prevents follow-on rework, the outcome is kept and that refusal is
reported without resetting the agent.

An ordinary row read cannot manufacture a valid claim. A failed settlement is an
error, not evidence that a desired status persisted. Telemetry observes this
result; it is not the authoritative work record.

## Handover checks

After an agent finishes, AppTown runs the stage's checks. Every `fail`,
including an assertion, asks the stage's agent to repair the work with the
failing step's full evidence. The engine applies this rule; runners report
outcomes rather than choose retries. A `needs_human` outcome names the person's
actions; an `error` says AppTown or infrastructure could not run the check.
Both outcomes hold the stage: later checks stay pending until the person
resolves it, except for the bounded publication recovery below. Work blocked by an earlier
failure also stays pending, including a review waiting for boot; it runs once
the blocker is resolved. A check that did not run cannot let the gate through.

AppTown sends failures back automatically up to three times since the last
person's retry. A further failure waits for a person. Each `step.rework` event
keeps its check and a short reason so the change page can show what was tried
after later checks replace their evidence. The continuing agent receives the
complete evidence from the check that requested its revision.

When a completed agent's commit does not build on the change branch tip,
AppTown gives that agent one automatic round to rebuild its work as new commits
on top of the tip. The refusal and continuation commit together through the
same rework mechanism. This allowance is separate from failed-check rounds and
resets on a person's retry or send-back. A second refusal stays `needs_human`
with instructions to preserve the unpublished commit, reconcile with the branch
and retry. Other publication refusals wait for a person immediately; unknown
publication outcomes remain errors.

An agent may instead finish its declared report with a `Blockers:` field:
following bullets, each on one line, name everything it cannot do and what it
needs from a person. This holds the whole change, even when it committed edits
or described them as complete. No rounds are spent and subsequent checks stay
pending. A person's retry with a blocker-free report follows the ordinary
checked path. The change page lists the person's actions together.
Reports omit the field when there are no blockers. Bullets saying only `None`,
`N/A` or `No blockers` (ignoring case and trailing punctuation) declare no
blockers, as does `Blockers: none` on one line.

Every stock stage that boots an app runs the landing `tests` step against the
agent’s committed work before boot, guidance conformance and review. The `build`
command uses the `implement` workflow. The reproduction stage of `fix-bug`
commits an intentionally failing test and is judged before the fix stage runs
the declared tests. Test failures use the bounded rework loop above. The tests
run without Docker access, with declared toolchains and services and throwaway
secrets, just as at landing. Landing still runs its own tests under current law.
The change’s recorded checks and acceptance dialog show these results.
Missing declarations follow landing’s applicability rule: a matching toolchain
requires tests; otherwise the step reports not applicable.

Every witnessed `tests` result keeps its inputs in the step's evidence in
Details: the committed Git tree hash, test declarations and AppTown version,
plus each command's container image digest, installed toolchain versions
reported by mise, and declared services' container image digests. Image digests
are Docker's content-addressed image IDs read from the containers that ran.
Unreadable inputs are recorded as `unknown`. Each command has its own record
because toolchain ranges and image tags can resolve differently between
commands. Work-phase and landing tests both run afresh; inputs do not enable
result reuse.

Every stock stage that boots an app checks guidance conformance before review.
Its agents are instructed to commit, run `apptown check`, run `apptown dev up`,
open affected pages, and report what ran and what came back. AppTown's own
checks still judge the committed work.

## Decisions, reruns and completion

Approvals, overrides and retries recheck the relevant decision snapshot and
current authority. Root's approval eligibility exemption does not bypass quorum
or stale-decision checks. Retry, automatic rework, send-back and landing recheck
share revision fencing but have different eligibility and preservation rules.
They are not one command with interchangeable flags.

`WaitingOn` is the workflow owner's attention projection after classification.
It names the holding run and step, including a child's, counts current failed
checks without stale or overridden verdicts, and supplies the phrase and tone
shown on Home, project lists, the change head and `apptown show`, `changes`
and `wait`. A failed check takes precedence over a later pending approval;
an error says the check could not run. Other holds retain their explanation.
Ready work says "ready to continue", accepted work awaiting its landing says
"queued to land", and an open change without running work says "chat open".

The projection's `you` answers the current reader's eligibility for relevant
acts using command permissions. Approval eligibility includes current-round
vote identity: a person who already voted is not asked again while a quorum
waits. Deciders come from the effective approval expressions, including project
defaults, and retain person, group or role kind. Denied eligibility is `no`;
unavailable eligibility is `unknown`. Work, chat, queued and closed attention
report `no` because they have no current act. Offered acts alone establish neither.
Home's `/activity` response supplies one server partition by `you=yes`, each
change once. Personal work is unlimited; other work keeps the newest 20 visible
running roots, including readable work awaiting somebody else. The web
renders it without a second partition. `/dashboard` supplies recent decisions
and the caller's ended operations.

Recovery buttons still come from offered acts. After three rework rounds chat
is the preferred recovery action when another agent round is offered. The
checks shield reads recorded step results independently of attention wording.

A running run's detail snapshot lists its next acts (`RunSnapshot.Next`):
`approve` for the approvals it waits on, `override` for failed or errored checks
blocking its stage, `retry` for stopped steps a step run accepts again, and
`rework` for another round of the stage agent, each naming its step; and the
run acts `advance` and
`restart-landing` (a landing whose rules changed). The change page's projection
carries the change acts from `ChangeActs`: `land` (in progress, an existing run,
nothing running, no landing queued), `send-back` (open, blocked, not merged),
and `abandon` for a reader who may abandon the blocked change. A no-run open
chat change also offers `abandon` to a reader who may abandon it; landing
starts from its chat. A landing queued for accepted work is the stated fact
`LandingQueued`. Apart from `abandon`, the acts say what is available, not who
may do it: every command still checks authority and freshness when it arrives. Clients submit these acts
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
approve. Every stage records its expected checks when it is entered, so in
normal use nothing is missing. A run whose stage is missing evidence cannot
advance: abandon it.

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

The [package guide](../../internal/apptown/workflow/README.md) lists the public
operations and their implementation files. Owner tests cover claims, stale
attempts, atomic admission and completion, retry, send-back and parent handoff.
Storage tests retain independent SQL assertions where representation matters.

## Implementation references

- [internal/apptown/workflow/service.go](../../internal/apptown/workflow/service.go)
- [internal/apptown/workflow/run_admission.go](../../internal/apptown/workflow/run_admission.go)
- [internal/apptown/workflow/execution.go](../../internal/apptown/workflow/execution.go)
- [internal/apptown/workflow/change_create_run_test.go](../../internal/apptown/workflow/change_create_run_test.go)
- [internal/apptown/workflow/retry_transactions_test.go](../../internal/apptown/workflow/retry_transactions_test.go)

