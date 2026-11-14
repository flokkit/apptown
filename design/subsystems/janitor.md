# Janitor standing consent

A standing approval records a person's consent for bounded routine updates. It
is separate from a vote on an individual Change. AppTown records the exact
meaning, project UUIDs, workflow purposes, selected environment and target UUIDs,
and the person who enabled it. Changes create new revisions; history is retained.

The janitor processes eligible dependency and pack updates under enabled standing consent.
Saving a rule authorizes the declared workflows within its bounds; an individual
Change still needs a fresh classification and all its required evidence.
No rule is enabled by default.

Instance consent covers present and future projects. Only an instance
administrator acting as a verified person can enable it. Its preview shows the
consent sentence, deployments that would receive automatic releases, projects
with repositories, and running images
with base-image findings. Projects whose main contract declares no tests are
marked as checking only that the app starts. Dynamic project membership does
not change the saved consent's meaning.

Instance consent selects published standing schedules for dependency maintenance,
and published standing publication triggers for Keep packs current. It pins their
approval purposes, rather than a separate workflow or cron list. One generated
sentence grants the schedules and repair limits each project's adopted packs
declare, including changes when a project adopts a new pack version. It shows
the current publication's titles, descriptions, cadence and repair sessions so a
person can inspect those values. A project on an older version runs that version.
Recorded revisions, sentences and digests remain unchanged.

Workflow owns queue state words, shared by Attention and both Janitor views.
Stuck dates use the UTC day. The Janitor page shows deployment holds beside
the project state; a hold does not hide queue activity.

Queues take their names and titles from adopted standing triggers. Procedures
show the repair limits of those workflows and the workflows they call. Pack
maintenance selects its publication workflow and queue from the prepared target
candidate, so an older adoption without a publication trigger can acquire one
through a governed adoption Change.


Keep packs current reassesses behind active projects nightly at 04:30 UTC,
as well as on enablement and publication. The `packs-catch-up` job reads the
current rule and calls Verification's existing pack-assessment operation.
Without an enabled rule it starts nothing. Projects with no packs can acquire
the platform pack. Identical queued candidates remain suppressed and stopped
queues remain stopped; nightly assessment does not grant another repair attempt.

A person's adoption Change uses the same publication procedure selected from
the candidate's exact workflow pins as automated pack work. A candidate without
an adoption procedure is refused before a Change opens, with a request to include
the platform pack. Ordinary adoption checks the recorded target in both its fix
and routine steps, without queue bookkeeping or standing consent. Its checks can
start the procedure's repair sessions. A yes in advance is asked and validated
against that candidate procedure and the landing procedure its Change resolves.
It counts only when no code changes and every required step passes outright.


At every use AppTown reads the project's current adoption, approval purposes,
issuer permissions, effective update level and release health. An agent updates every ecosystem it finds; adopted dependency guidance supplies
conventions and file classification, without restricting eligibility. The project choice records `off`, `routine` or `aggressive`, with the project,
person, time and reason, under the standing-approval capability. With no record,
the instance level applies. Setting `off` stops both schedules. The project
Janitor section shows the effective level beside “turn off” and the recorded choice.
Preview states the instance level, identifies projects with a different level,
and names parts whose adopted packs offer no major command.

People can also save rules for exact projects and release destinations. Full
delivery requires explicit environments, deployment authority and promotion
consent where required. Changes to the selected release scope require renewed
consent. “Accept and land only” cannot govern a project with a deployment that
follows main.

Only a verified person acting directly in their browser or personal command-line
instrument can enable or revise consent. Management, approval, landing and
release permissions remain distinct. Current project law and approval selectors
apply; unsupported quorums and independent-approval requirements refuse setup.
The working and landing workflow each declare one supported purpose with
`standing: {class: routine-update, means: ...}` on their approval step. This
declaration provides no authority and changes no ordinary human vote.

A rule names routine (patch and minor) or aggressive (including major) dependencies, verified same-version base-image
updates, security fixes within those limits, or governed routine way-of-working
transitions. Dependency classification keeps the package set, sources and
attributes equal and versions inside the selected bound. Application repairs
are judged by the workflow's checks. A saved rule with different shown meaning
becomes ineligible until a person previews and confirms the new meaning; its
original history remains unchanged. Missing or nonpassing evidence is never success.

The enabling principal is recorded by UUID. A renamed person retains identity;
a new account with the old name cannot inherit consent. Current eligibility is
shown separately from enabled state. Database or policy unavailability is an
error, not an eligible rule. Disabling retains history and stops future claims;
an external effect already claimed may finish. The final merge claim locks the
rule head before reading current consent and keeps that lock until commit,
serializing the claim with Disable. A manager can disable consent
after the issuer loses deployment permission.

Supported instance restore disables every enabled imported rule after schema
migration and before application recovery. Original consent remains in history.
Re-enabling requires a new local human confirmation. There is no standing-rule
import endpoint.

The CLI provides `apptown janitor list`, `preview`, `enable`, `disable` and
`history`. Preview accepts a definition through `--file`; enable requires the
exact returned `--confirm` digest. Revising requires `--rule` and `--revision`.
Disable also requires the expected revision. Commands emit JSON and never start
a release. If a save response is lost, inspect current rules before submitting
another new rule; an unknown response is not proof the save failed.

## Janitor page

`/janitor` is the home for standing rules and their existing preview, enable and
revoke operations. Instance readers can read the rules; management retains its
verified-person and capability checks. The Packs page links here. Project rows
open the project's Janitor section, which links back to this page.

The page shows each rule's bounds, current scope, automatic release destinations
and original grant. Project choices retain the person, time and reason under
their rule. Repair sentences read the adopted `janitor-fix`, `janitor-update`
and `janitor-adopt` definitions. Queue sentences use the same stop bound as
admission, separately for security, dependency and pack work. Shared queue rules
appear once per adopted version; current stopped work appears with project state.
The release settings table lists automatic destinations covered by an enabled
rule and reads their current settings; release holds use the existing ledger
classifier.

Workflow owns the instance project-state and landed-Change reads, fenced by the
caller's readable project set before paging. Deployment owns the instance settings,
holds and Change-release membership reads. The HTTP handler joins owner results;
it reads no tables and owns the response types. Last-seven-days history and paged
older history use landing time, with deterministic identity ordering for
simultaneous landings. Package
moves come from retained attempts; pack versions come from the Change's immutable
adoption candidate. Original grant attribution reads the recorded rule revision,
including for failed work whose Change is purged. Every attribution names the recorded rule’s kind and scope, its revision and issuer, with a link to Janitor. Project scopes name only projects the caller can read and count the other projects. The Change heading shows that original firing grant separately from later acceptance votes, including when the votes use a different rule. Vote labels read the referenced revision from the standing-rule history. Missing or stale scan evidence
and failed refreshes remain visibly different from clean or empty answers.

The Janitor project set contains ordinary projects with no lifecycle row.
Attention's Scans source uses the same set, returning known empty coverage for
deleted projects and eval copies. An eligibility read failure remains an error.
When a scan is not known, Workflow asks the scheduler whether the adopted
schedules scan source. Unreadable definitions remain errors. If none scans,
the reason says that the packs schedule no scan, and Attention explains that
adopting the platform pack would scan it. A current complete scan remains known
regardless of whether the project still schedules scans.

## Inspecting a candidate

`apptown janitor assess RULE --run RUN` inspects an exact run without accepting,
landing or deploying it. The caller needs current read and check-execution
permission. The rule must still be eligible for that project and workflow.
Active writers, unsettled tasks, dirty or unpublished work, changed source and
changed consent refuse an assessment. Inspection never repairs or saves source.

Dependency inspection compares toolchain and runtime inputs on the committed base
and candidate, including mise declarations, version files, Dockerfile base
operands and contract image/build inputs. Changes require a person. It classifies
every changed path using adopted dependency-file globs and the observed input
paths. An empty candidate is not applicable. The classifier is `agent-update-v1`.

The routine check sends the complete diff, the approved level, any security
targets and the classified paths to the checks model through the diff-rubric
evaluator. The workflow supplies the rubric. Pass and fail record the model's
judgement and reasoning; unclear or unavailable is an error, never met and never
sent to repair. A failure can go to the dormant repair agent. The receipt keeps
session, model, rubric and diff identities. Every required check must still be
met for standing acceptance; a person may accept work that routine refuses.

The assessment records source commits, tree, rule and run revisions, and a digest
of the classified paths. It re-observes the source before returning. Standing
processing uses a fresh assessment before casting a typed vote; assessment does
not accept, land or deploy. Targeted fixes require security permission and the
source scanner's parsed observation that the named target is gone. Missing,
failed, detached or not-applicable scanners leave that target unassessed.

Base-image inspection supports one root Dockerfile selected by an unchanged
application contract: one app build at `.`, no needs, durable app data, init,
static build or profile overrides. Its single `FROM` names an explicit
`linux/amd64` or `linux/arm64` platform and
`docker.io/library/node:VERSION-VARIANT@sha256:DIGEST`. The stable three-part
Node version and variant must remain identical; only the digest may change.
Multiple stages, arguments, parser directives, continuations and heredocs
require a person. Every other Dockerfile byte is compared.

The image owner reads both immutable manifests and configurations, refuses
indexes and changed runtime configuration, and confirms the platform. The
existing check executor observes Node's actual version in both prepared images,
with entrypoints cleared, network disabled and no project files or commands.
The prepared image ID must match the verified configuration digest. Pulls use
request-owned anonymous authentication configuration on the selected daemon;
an executing host with `DOCKER_AUTH_CONFIG` set cannot supply this observation.
Existing executor tool caches remain available, and remote scratch cleanup is
still unobserved. A matching tag or publisher label alone is insufficient.
Other images and storage-engine transitions are unsupported.

Security targets come from the failed source scan. An observing running-image
scan cannot start a fix. Classification never substitutes for the required tests,
start-up or scans, and an inspection result alone provides no authority.

## Keeping packs current

“Keep packs current” is a separate standing rule with category `waysOfWorking`.
It has no dependency update level or cron. Publication starts a what-if as
`apptown-system`, recording the rule and revision. Enabling starts a fresh
assessment for each project below a newest publication, even when a person
already assessed it. Each candidate moves all of that project’s outdated packs
to their newest publications together. Every project with a repository belongs
on the platform pack, so one that holds none of it is below every platform
publication and gets it the same way. A later publication assesses the whole
outstanding set again, so replacement work retains the other pack updates. A
what-if reports impact; it grants no acceptance.

A finished assessment opens an automated adoption Change in queue `packs`.
`janitor-adopt` runs the `adopt` fix, up to two repair sessions, tests, start-up,
scans, guidance checks, `routine` and standing acceptance. Its declaration is
the publication, and its classifier checks that the immutable target remains
the assessed candidate. New checks and agent-written application code use the
same acceptance and landing ladder as other fixes. A check that judges only
the standing deployment (`memory-within`) does not apply to a Change's run and
records not-applicable with that reason; reviews judge it. A standing vote
advances the run as a person's vote does. There is no separate
classification of publication intervals.

Eligibility requires an active project below the publication, current issuer
authority, matching approval meanings, a project level other than `off` and no
release hold. Projects move to newer published packs when this rule is on. Attempts retain rule
attribution; the queue fingerprints main's commit and the adoption candidate.
An empty source diff still has its adoption to land.

Every required check must be met. `workflow.CheckReceipt.Met` treats a check
judged not applicable with its reason as met; an error, waived check or unknown
answer is not met. What-if, the adoption condition, yes in advance, standing
acceptance and landing preserve that meaning. A person's yes in advance still
requires no code changes.

## Scheduled scans and updates

The platform pack carries `janitor-scan` and `janitor-update`. A project runs the
versions its adoption pins. The security schedule observes source and running
images, and calls `janitor-fix` only for a new finding that an adopted update fix
covers. Scanner errors never trigger the call. The update schedule updates the
repository through an `agent/pi` update session. Node, Go, Python with uv,
Ruby and Maven guidance describes commands for every workspace and sub-project.
A project without a declaration for its ecosystem still updates; its changed
files are labelled other and judged with the complete diff.

The producer starts the queue attempt and retains its targets before its session.
Routine runs after the producer and before tests, start-up and scans. Routine
permits patch and minor releases, keeping 0.x packages within their minor;
aggressive permits newer majors. The model judges every version and source
change, direct dependencies and the minimum application repairs a moved package
needs. AppTown independently witnesses protected inputs and required checks.
A targeted fix fails while its source scanner still reports the package and any
shared identifier, regardless of severity, baseline or exceptions.

A `fix` step dispatches pack adoption. A pinned workflow's dependency `fix` step
errors: this pack version’s dependency update is no longer supported; adopt the
current platform pack. Existing approvals keep their recorded meaning. After a
project adopts a workflow with a different meaning, its old approval is ineligible;
a person uses Revoke, Preview and Enable on the Janitor page to consent again.

A repair agent with `repairRounds: N` stays dormant until a check fails. Its
session count is bounded over the run's lifetime. The stock workflows allow two
sessions, then leave the Change for a person. At the final working approval,
a declared standing purpose can queue `standing-acceptance` for the exact run,
step and retry revision. The job assesses the committed candidate, then under
the project lock rereads the rule, current eligibility, candidate and evidence.
It records a typed standing vote or `acceptance.standing.withheld`.

Standing evidence covers required checks in every executed stage of the root's
captured procedure and every check in runs its steps called. A check is met only
when it passed or is not applicable with a stated reason. Exceptions, overrides,
preexisting failures and missing evidence require a person. Not-needed calls
and dormant repair steps are excused. Application code written during repair is
judged by those same checks. Landing requires every landing check met and reads
current consent again at the merge claim. The approver is recorded as “the
janitor, under standing approval <rule> (rev N), granted by <issuer>”; the same
attribution follows the release event.

Each queue declared by an adopted standing workflow keeps its own history. A proposal records
main, the adopted versions, its first candidate tree, classified files and
targets. Repair keeps that first fingerprint. A proposal identical to the queue's last held or skipped one stops
before tests. A distinct proposal replaces earlier untouched work; human-touched
work remains ordinary. A landed Change, a person's skip, or a person's touch of
held work ends the failure episode; quiet scans and repeated proposals do not.
Three uninterrupted failed tries stop the queue. A stopped security queue can
try a new fixable finding at the scan's failure threshold.

The project page shows queue state, moved packages, recent tries linked to their
Changes, and paths to retry, chat or skip the update. Stuck and stopped queues
each contribute one attention item. A skip retains its fingerprint and targets.
A person who may open and start work in a project can fire one of its
carried schedules now (`POST /api/v1/projects/{slug}/schedules/{workflow}/run`;
the Janitor page selects the standing schedule that discovers fixable targets). The firing is the scheduler's
own: the Change is opened through the schedule under the eligible rule, and
`schedule.run-requested` records the person. A busy or stopped queue, a missing
schedule or a missing eligible rule is refused and records no firing.
Schedules declaring `standing: true` fire only under an enabled, currently
eligible rule. Without one, the schedule read says it waits for a standing
approval, with no next firing; its last recorded firing remains visible. This
is a normal off state. Unreadable workflows
and invalid cron expressions remain schedule problems. The scheduler reads each project's adopted standing declaration. Its
`cron` applies at routine level; `aggressiveCron`, when declared, applies at
aggressive level. Both are read in UTC. The scheduler's own admission read
supplies Scan now's refusal and the earliest next firing of other standing
schedules for Next update. Busy queues retain their future firing time; stopped
queues have none. Target-discovering schedules can still scan a stopped queue.
Project-scoped rules also authorize their selected workflows' declared schedules.
The effective level also supplies the routine check and standing assessment's bound.
A vote records
the assessed level; a changed level refuses acceptance or landing. Firing and
attempt records capture its rule ID and revision before acceptance, preserving
them through rule revision and Change disposal.

## Routine releases and recovery

A deployment's **Routine updates** setting is automatic, a release window
(days, local times and time zone), or when a person says. It applies to requests
from typed standing landings. Automatic requests run immediately; windows queue
for their next start and check the current window and hold again before claiming
execution. Protected deployments always need a person's approval.

A follows-main deployment with a setting other than person holds its project
back if an open releasing attempt has uncertain progress, its latest settled
release failed, or its latest settled release succeeded with a bundle older
than the newest bundle any settled release there carried. Waiting, queued and
normally running releases neither create nor clear a hold. Recovery keeps an
existing hold until success on the newest settled bundle or newer code. A
rollback to an older bundle keeps the hold even after success; Try again on the
failed current bundle can clear it. Selection provenance alone does not decide
release health.

The same ledger read governs schedule firing, standing votes, routine release
admission and window claims. A routine request to a held deployment waits for a
person. The project page shows the deployment and failed release with **Roll
back** first, then **Try again**, using the deployment’s display label. Roll back
opens that release’s existing rollback confirmation only when its backup has
recorded running-release evidence. Links retain the deployment’s stable name.
A waiting release names the approval role and offers **Approve** and **Cancel**
when a role is required. Otherwise it shows why a person is needed (the routine
update setting or a release hold) and offers **Release** and **Cancel**. Both
paths show the assent sentence; current release and promotion authority still
apply. A newer failed release does not hide an older waiting decision. Release
history and the release page show the same waiting reason.

## Scan observations and disposal

The project's Janitor section shows the current scan summary and findings.
Conformance's Last scan lists each scanner subject, result and observation time,
with the newest ten earlier observations below. An aggregate Not known and its
reason appear above the results whenever the current scan is incomplete or
unavailable. Commits and image digests show
twelve characters, with the full value in their title. A running finding belongs
to the project when main's latest usable source scan also reports its package
and identifier. Other findings stay
in the running image only: a base-image update or a newer release may fix them.
Each source observation retains the scanner bindings expected by its run,
captured when execution starts. Retained usable answers must cover every expected
scanner before running findings can be classified; disposable workflow steps
do not establish completeness. A partial source scan or a scanner that found nothing it could judge classifies nothing;
the latter records an error observation. Observations of older commits or
digests remain historical; a landing never proves a running finding fixed.
The current source scan is the newest run that scanned main's whole tree. A
landing scans only the commits it adds with a scanner that declares a
`historyCommand`. For such a scanner, the answer is its newest whole-tree
observation of the same commit, so a landing does not hide a complete earlier
scan. The landing's own errors stay current, and no other scanner takes an
older answer.
Missing scans and scans older than three days appear in Attention as not known.
The detail names the case: the code not scanned, the scanner without a result,
the deployment whose image is not scanned, or the date of the last scan.
A stuck or stopped queue's Attention item carries the first line of its error
and opens its change, which holds the rest.
Running-image-only findings do not create Attention items.

Scheduled Changes record their origin and a compact summary at creation. A
person's edit, chat or other work on a Change, or starting its landing, makes
it ordinary work permanently. Automated queue Changes stay out of the Change
list. Their existing Change pages remain reachable through
`apptown janitor attempts -p SLUG`. An observation-only
queue Change runs every scan even after findings or scanner errors, then closes
when its scans settle, retaining their actual outcomes. A conditional call with
no fixable targets records not applicable with its reason. It needs no child
run to qualify as standing evidence. Ordinary runs stop on failure.

The Change owner purges untouched abandoned automated Changes after references
and unreleased tasks no longer retain them. ADR and complaint links keep their
referenced Changes and Runs available. Each owner supplies its retention query;
schema reference coverage and root wiring are checked for completeness. Disposal is claimed before removing
branches, workspaces, transcripts and execution rows. Failed cleanup is retried
by settlement and the reaper. Conformance keeps scan observations independently,
including exact subjects, scanner binding version, configuration and hash,
observation time, verdict, normalized findings and unknown reasons. Compact
summaries continue to reference those observations after disposal.
