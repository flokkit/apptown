# Work's internal API

`internal/forge/workflow` owns Changes, Runs, Steps and their lifecycle operations.
`Service` is the caller-facing entry point. Forge binds verified identity and
assembles external effects. Stores and generated SQL are private. Work never
imports Forge or a shared models package.

Work is not the only path to Work data: some consumers (Delivery, agent and
console adapters, project handlers, MCP) read Work's SQL directly, and trusted
record consumers call the owner's `Read*In` functions rather than
caller-authorized Service queries.

## Records and assertions

Forge uses Work's Source, MergeSource, ReadyStep, DispatchPlan, Reworked and
merge-result records directly, with no local conversions.

Work connects Assertions to Merges itself; Forge does not relay merge assertions
back into the same service. RefreshAssertions owns
current-stage selection, evaluation, stale-check reset and redispatch, with current
caller checks. Independent assertion failures are collected and returned while
other eligible checks continue; failed reset transactions never trigger dispatch.

Forge binds caller identity, supplies executors and cross-domain receipt
participants, and translates transport errors. The automatic executor
implementations live outside Work, largely on App.

## Caller authority and execution authority

Service exposes typed Change creation/edit/import/uplift, Run start/landing/
abandonment, Step retry, send-back, approval, override and advancement commands,
and Change/Run detail/list/trace/activity/artifact queries, closed-Change history,
open Changes without active Runs and running-Step observations. HTTP and MCP bind their
verified caller through Forge's authorization adapter. A command's actor string is
receipt attribution, never evidence of permission.

The authority port receives an operation and its stored project/Change/Run/Step
scope. It supplies attribution and inference ownership after checking current
policy. User commands cannot choose parent/acceptance IDs or billing identity.
Child, accepted-landing and remediation work use explicitly admitted internal
operations. MCP retains the person's client attribution in receipts without using
that label as authority. Disabled accounts, current grants, OAuth scope limits,
instrument project restrictions and approval eligibility remain enforced.

Bootstrap's explicit open policy allows public reads before an administrator
exists. Once bootstrapped, an arbitrary HTTP actor header cannot establish
authority. The default Auditor grant to the all-users group deliberately allows signed-in project reads until an administrator revokes it. Approval checks preserve current groups,
named approvers and author separation.

Queries identify minimal routing scope before checking permission. Detail and
artifact reads recheck stored associations. Batched activity refuses the entire
result if a root is unavailable or unauthorized; it follows only current,
same-project child-call attempts. Global lists select readable projects before
filtering/ordering/limiting records. Content reads are batched; permission is
evaluated per project.

Already-admitted owners such as Starter, StepAttempts, Advancer, Landings and
Merges are execution APIs, not alternative authenticated user APIs. Their records
and plans convey observations, not a Step attempt claim. Their named transaction
participants compose with another owner on an explicitly supplied transaction.
New user transports should bind Service rather than call these owners directly.

## Records and transitions

Change, Run and Step are canonical typed records. Their state codecs and legal
transitions belong to Work. Derived activity has one classifier rather than a
second stored lifecycle. An optional Run state preserves the existing empty-string
representation when a Change has no associated Run.

Commands carry concrete scope and intent. Private stores lock and compare current
project, association, stage, attempt, retry revision and exact relevant source or
configuration before mutation. Required writes check affected rows, including
trigger-suppressed writes. A required receipt failing rolls back its state change.
Stored configuration, evidence and external receipts forwarded by lifecycle
operations retain exact JSON or binary bytes. Those round trips do not decode
through generic maps or floating-point numbers. Workflow declarations use
`schema.Spec`, whose extensible `Step.With` configuration is a map; that parser
is not typed.

CreateChangeIn and other explicitly admitted participants do not establish user
permission. Change creation followed by separate root Run creation can partly
succeed; only `CreateAndRun` admits both atomically.

## Run start and workspace preparation

Work owns root admission, scoped preflight, pinned declaration, lineage/adoption
selection, call-depth/callable-pipeline checks, artifact requirements and first-stage
materialization. Prepared declarations and receipt bytes are frozen before lock
waits. Scanner facts come through one named participant on the admitted connection;
failed inventory reads never mean an empty scanner set.

Starter orders commit, workspace preparation, readiness and dispatch. Its three
runtime ports resolve a definition, prepare a workspace and dispatch committed
work. Preparation coalesces per Run with cancellable waiters and a ten-minute
ceiling. A canceled attempt stays unready for an explicit retry. Scoped preparation
rechecks the expected Run before executing external work. Unrelated Runs do not
share a wait. No new session expiry applies to long-lived Runs or chats.

## Step attempts, decisions and reruns

StepAttempts admits a runner under project/Run/Step locks, checking the current
stage, state, attempt, override and exact configuration. It writes the new attempt
and required start receipt atomically, then issues a private marker bound to that
Step and Run. Only that committed marker can settle the current attempt. Finish
and its evidence/receipt commit together; automatic rework joins that transaction.

Approval decisions own quorum, current/previous votes, rejection feedback and
withdrawals while preserving unknown metadata. Service binds the caller before
the private decision store rechecks scope/configuration/attempt/revision. Overrides
require a reason. Rework remains bounded; a failed rework receipt rolls back its
triggering vote or machine outcome. Stale verdict refresh and selected landing
rechecks preserve human decisions and update the Run revision with their receipts.
An earlier advancement observation cannot settle the newly retried work.

Explicit Step retry has a typed retry mode and feedback. It checks prerequisites
and concurrent runners, preserves conversation identity, and records prior
evidence and overrides before resetting the selected work. Active-stage send-back
uses the same admitted retry participant.

Completed-history send-back creates a new Run from the original pinned procedure,
materialized stage and scanner expansion. Exact artifacts and context are inherited;
outcomes, overrides and votes are fresh. The completed source remains history.
Continuation links, inference ownership, readiness, feedback and receipts are atomic.
A known source inference owner is retained; genuinely unattributed history uses
its explicit source-Run attribution. Continuations do not bypass disabled-owner
inference checks. It edits no application source.

## Advancement, assertions and completion

Advancer uses the pinned stage definition and recorded checks. Assertion evaluation
belongs to Work; external SCM observations release the SQL connection and then
recheck their prepared scope before settlement. Stage transition and completion
compare the expected Run, including its retry revision. Stale observations return
the current outcome or a conflict rather than advancing another stage. A fallback
retains Run/project/Change/parent-call identity. Caller-facing advancement also
revalidates that identity before publishing a blocked or completed observation.

Merges owns current-law enforcement, source admission and merge-gate mutation.
The SCM claim is private and bound to its DB/Run/source. The external driver runs
outside the Work transaction. A successful merge observation cannot clear a gate
for changed source or retry state. Ways of Working interprets policy through a
named port; Work owns its law gate and required receipts.

Run completion, Change progression, child results and required follow-up receipts
settle atomically. One named completion participant creates the Delivery-owned
release and durable jobs in that transaction. A requested release requires a
positive receipt. Work neither owns Delivery's tables nor performs deployment
inside its transaction. Explicit follow-up retry locks the expected completed Run
in Work and supplies that same transaction to the job participant. The participant
retains River row locks and JobRetryTx, owns no DB handle and opens no second
transaction. Changed identity/state/revision refuses retry before the participant.

Completion follow-ups consume typed tasks. Parent advancement requires the actual
completed child, same-project parent, owned call Step and current attempt/result.
Changing any of those associations invalidates the handoff. A replay cannot
advance a newer parent stage.

Landings owns accepted-source handoff and its one-time acceptance/adoption
receipts. It checks the selected Change/project before availability and uses the
canonical Starter and Advancer. Continuation supersession and the original landing
stage/revision fence prevent stale acceptance from advancing newer work. Forge
supplies availability and dispatches the committed result.

## Dispatch and shutdown

DispatchPlans reads pinned Runs and materialized Steps in one repeatable snapshot.
Recorded scanner expansion and entry/gate ordering survive changes to today's
bindings. Plans grant no execution claim; StepAttempts admits actual work.
Dispatcher's runner registry is copied at construction. Its three collaborators
are runner effects, preparation/advancement and error reporting. Its store is
private, not an exported repository interface.

Entry steps remain ordered; scanners at one gate point may execute together.
Current Step state is checked before and after execution. Skips and orphan resets
retain their required receipts. Existing restart eligibility is unchanged: pending
work and specifically interrupted attempts can resume; arbitrary failures require
an explicit rerun. A failed restart notice stops execution and is reported.
Rework requests made by an active stage are coalesced and admitted after it returns.
Wait includes any next stage dispatched by accepted work.

BeginStop closes admission synchronously and returns the single drain signal.
Forge shares its existing shutdown deadline across dispatch and other workers.
Already-running effects retain their own cancellation/timeout policy; joining them
does not create an unbounded host shutdown. Panics retain stack diagnostics and
Forge's incident reporting, and cannot advance the interrupted dispatch. There is
no additional recovery framework, polling loop or worker.

## Change narrative and external boundaries

Narratives reads typed source/evidence in one snapshot and owns title/note
publication. It rechecks source and previous summary before updating. Human-pinned
titles remain protected. Delivery release notes join through a named transaction
participant; any failed participant/audit write rolls back publication. Model calls,
Git inspection and Delivery evidence remain external effects.

Forge's workflow adapters contain no direct database calls. Adoption revision
pairs use the Ways of Working owner twice in the supplied transaction, checking
both project identities; its reader validates each adoption record, with no
local SQL fallback.

Jobs owns typed run-operation selection and explicit retry. It locks all matched
River rows in ID order until the supplied transaction ends, then retries failed
jobs through River's JobRetryTx. Completed jobs and unrelated operations/runs are
left alone; selected active jobs count as pending. River owns queue state and
attempt policy. No second connection or worker is created by the participant.

Startup interruption, remediation supersession summaries and uplift adoption
stamps use named Work transaction participants. Startup checks every selected
Step write and preserves only the matching same-project live child call; its
counts become a report only after the whole startup transaction commits.
Remediation requires the same-project abandoned Change. Uplift replay and stamping
require the same-project shipped uplift Change, while Ways of Working retains
revision/provenance checks and its adoption receipt in that same transaction.

Inference observes its issued project/run scope, live step attempt and durable
owner through one Work statement snapshot. Work owns lifecycle interpretation and
run attribution; Forge retains runtime/network and disabled-account
policy. Continued and long-lived consoles use their own lifecycle, without a Work
run-liveness check or a new expiry.

Remediation consumes canonical Run/Step execution observations, comparing them
again under its project lock before settling its own attempt and receipts. Work
owns preparation/rework facts, occupancy and locked supersession eligibility.
Scoped bulk Change states and pending-Step observations serve candidate filtering
and displays without per-item queries. Separate owner reads can add one round trip
to a display; they do not replace the locked settlement check.

Overview reads enter Service, retain authorized project IDs in the final queries,
and reject cross-project associations. Root-run history aggregates share one scan;
child durations are excluded and merge receipts remain scoped to the Change.
Forge owns display formatting and Git inspection. Read failures are reported,
not presented as empty activity.

