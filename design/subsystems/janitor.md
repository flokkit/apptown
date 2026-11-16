# Janitor choices

Janitor settings records what to keep updated: dependencies (`off`, `routine`
for patch and minor, or `aggressive` including major), security fixes, base images,
toolchain patches for security fixes, and packs. Security fixes require dependency
updates; toolchain patches require security fixes. Everything starts off.

One current record in Workflow's standing-rule tables contains those choices,
the verified person's principal, name and time. Saves append a revision at the
expected head under a lock. Only a verified person acting directly with
`instance:manage-standing-approval` may save. An agent cannot give consent.
The choices contain no workflow names, versions or approval meanings. Each Change
records its procedures, assessment, checks and vote, including its standing meaning.

Project choices remain `off`, `routine` or `aggressive`, with person, time and
reason. A project-level off excludes all Janitor work. Instance dependencies off
excludes dependency schedules and security fixes, while packs can remain on.
With no project choice, schedules use the instance dependency level. A project's
level never turns on an instance dependency choice that is off.

At each effect AppTown resolves the adopted working and landing procedures, or
the frozen target for pack adoption. A working procedure needs a standing trigger:
publication requires packs; schedule requires an effective dependency level.
Current issuer account, approve and land permissions, approval selectors,
independent-approval policy, repository reach and release health still apply.
Changing a workflow's version or standing meaning does not invalidate choices.
A save between acceptance and the merge claim refuses landing. The merge claim
locks the rule head through commit; an already claimed effect may finish.

Packs reassess behind projects nightly at 04:30 UTC, on saving with packs on and
on publication. The `packs-catch-up` job reads current choices and calls
Verification's pack assessment. Identical queued candidates remain suppressed and
stopped queues remain stopped. Projects without packs can acquire the platform pack.
A person's adoption Change uses the candidate's frozen publication procedure;
ordinary adoption checks its target without standing consent. A yes in advance
requires unchanged code and every required step met.

Supported instance restore turns the current choices off before recovery and
preserves earlier revisions. Historical Change attribution reads its exact rule
ID and revision. Current decisions never select from historical heads.

The API provides `GET /api/v1/janitor/choices`, `PUT /api/v1/janitor/choices`
with `{choices, expectedRevision}`, `GET /api/v1/janitor/choices/history` and
`POST /api/v1/janitor/assess` with `{run}`. The CLI provides `janitor choices`,
`janitor set --dependencies LEVEL [--security-fixes] [--base-images]
[--toolchain-patches] [--packs] --revision N`, `janitor history` and
`janitor assess --run RUN`. A lost save reply requires reading current choices
before another save.

## Janitor page

`/janitor` shows work that needs a person and recent landed Changes. Only
projects with standing queues are listed or counted. Unknown scans belong to
Attention and the project's scan history. An unreadable update schedule on a
project with queues offers Run now and shows the scheduler's refusal.

`/janitor/settings` shows the choices, who set them and when, project choices
and automatic release destinations. Switches and the dependency select save on
change, with one full-choice write at the read revision. Controls are disabled
while saving or while the read is not current. A conflict reloads the current
choices and shows the refusal without resubmitting. Attribution follows the reread.

Workflow owns the instance project-state and landed-Change reads, fenced by the
caller's readable project set before paging. Deployment owns the instance settings,
holds and Change-release membership reads. The HTTP handler joins owner results;
it reads no tables and owns the response types. Last-seven-days history and paged
older history use landing time, with deterministic identity ordering for
simultaneous landings. Package moves come from retained attempts; pack moves
compare the Change's immutable target candidate with the previous landed
uplift's target in the same project, including a person's adoption. Only added
or version-changed packs are listed; a first adoption lists all packs.
Original grant attribution reads the recorded rule revision,
including for failed work whose Change is purged. Every attribution names the recorded choices, revision and issuer, with a link to Janitor. The Change heading shows that original firing grant and its time on every tab, separately from later acceptance votes, including when the votes use a different rule. Vote labels read the referenced revision from the standing-rule history. Missing or stale scan evidence
and failed refreshes remain visibly different from clean or empty answers.

The Janitor project set contains ordinary projects with no lifecycle row.
Attention's Scans source uses the same set, returning known empty coverage for
deleted projects and eval copies. An eligibility read failure remains an error.
When a scan is not known, Workflow asks the scheduler whether the adopted
schedules scan source. Unreadable definitions remain errors. If none scans,
the reason says that the packs schedule no scan; the project’s scan history shows it and
Attention does not, since the project chose it. A current complete scan remains known
regardless of whether the project still schedules scans.

## Inspecting a candidate

`apptown janitor assess --run RUN` inspects an exact run without accepting,
landing or deploying it. The caller needs current read and check-execution
permission. The rule must still be eligible for that project and workflow.
Active writers, unsettled tasks, dirty or unpublished work, changed source and
changed consent refuse an assessment. Inspection never repairs or saves source.

Dependency inspection compares toolchain and runtime inputs on the committed base
and candidate, including mise declarations, version files, Dockerfile base
operands and contract image/build inputs. With base images on, external-image FROM operands may
change version or digest while keeping the repository and tag variant unchanged.
Flags, aliases, stage count and order, ARGs and parser directives stay unchanged.
Stage references, scratch and operands containing `$` remain byte-identical.
Docker instructions are read across single-backslash continuations, including
trailing spaces or tabs; comment and blank lines inside a continuation are
skipped. Continuations on instructions such as LABEL and RUN do not prevent
image updates. Files with multiline or backslashed FROM/ARG instructions,
terminal runs of two or more backslashes, an escape parser directive, heredocs,
control bytes or backticks are compared byte for byte. Exact three-part toolchain
versions may move within their minor only for runs with security targets and toolchain patches on.
Other toolchain and build input changes require a person. It classifies
every changed path using adopted dependency-file globs and the observed input
paths. An empty candidate is not applicable. The classifier is `agent-update-v1`.

The routine check sends the complete diff, the approved level, any security
targets and the classified paths to the checks model through the diff-rubric
evaluator. The workflow supplies the rubric. Pass and fail record the model's
judgement and reasoning; unclear or unavailable is an error, never met and never
sent to repair. For an empty change it judges every passing agent step's final
message that declared nothing to change, labelled in step order. A report must
include each part's release-listing command and output and every FROM's tag
listing; an uncommitted report file cannot supply that account.
A finished judge session without a valid known verdict is an AppTown fault.
Queue check errors whose receipts are all AppTown faults retry through the
step-retry operation at most twice per step. Retry receipts name the engine,
retain each error's evidence and explain why. The third error holds the queue;
errors concerning the candidate or its bounds hold immediately.
A failure can go to the dormant repair agent. The receipt keeps
session, model, rubric and diff identities. Every required check must still be
met for standing acceptance; a person may accept work that routine refuses.
A landing's standing evidence includes every stage before the merge. A landing
with more than one stage merges when it enters its final stage, so that stage
is excluded. A single-stage landing merges at completion, so its stage is
included. The completion recheck uses the same pre-merge evidence. Post-merge
steps still follow the final stage's own gate.

The assessment records source commits, tree, rule and run revisions, the choices and classified paths. It re-observes the source before returning. Standing
processing uses a fresh assessment before casting a typed vote; assessment does
not accept, land or deploy. Targeted fixes require security permission and the
reporting scanner's parsed observation that the named target is gone. Missing,
failed, detached or not-applicable scanners leave that target unassessed.

Security targets come from failed source and running-image scans selected by
the adopted call's `whenFailed` with `findings: fixable`. Source findings require
an adopted ecosystem update declaration. Running-image findings must meet the
step's severity threshold, belong to the project's public part, and have no
matching package and identifier in the stage's source observations. Findings
from other parts remain for a person. Targets retain the reporting binding and
scan point. Source targets require their source scanner; image targets require
their image scanner at the candidate image check. A detached or inapplicable
scanner cannot clear a target. A remaining target fails regardless of baseline
subtraction or severity. Classification never substitutes for required tests,
start-up or scans, and an inspection result alone provides no authority.

## Keeping packs current

Pack adoption repairs may change the declarations in `.apptown/app.yaml` that
the new pack's failing checks require. Dependency updates and security fixes
keep that file unchanged. The repair skill distinguishes these intents.

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

The security workflow also declares `uses: rebuild` with
`whenFailed: [scan-running]`. It waits for those scans to finish and reads
this run's current-attempt observations. Verification applies `scanGate` and
the named scan's `failOn`, then compares each fingerprint with the newest
usable bundle observation of the same digest and scanner. A finding absent
from that baseline is due. No usable baseline is unknown and is due; an
erroring running scan supplies no findings for rebuilding.

The step asks Deployment's `RequestRebuild` for each due deployment following
main with a running image. Deployment owns authority, approval, routine holds,
windows and release exclusion. The step passes with requested release numbers
and their findings, or is not applicable with a reason. Findings already seen
at build do not cause daily rebuilds. If the rebuilt bundle scan errors, a
later successful running scan can request another rebuild of the same commit.
The next usable bundle scan bounds that repetition. Omitting the authored
step from an adopted workflow disables this remedy without an engine switch.

The producer starts the queue attempt and retains its targets before its session.
Routine runs after the producer and before tests, start-up and scans. Routine
permits patch and minor releases, keeping 0.x packages within their minor;
aggressive permits newer majors. The model judges every version and source
change, direct dependencies and the minimum application repairs a moved package
needs. AppTown independently witnesses protected inputs and required checks.
When an update changes nothing, routine judges the update agent's report against
the committed tree. A passing judgement lets the empty update close without approval
once the other checks pass.
A targeted fix fails while its source scanner still reports the package and any
shared identifier, regardless of severity, baseline or exceptions.

A `fix` step dispatches pack adoption. A pinned workflow's dependency `fix` step
errors: this pack version’s dependency update is no longer supported; adopt the
current platform pack. Existing approvals keep their recorded meaning. After a
project adopts a workflow with a different meaning, its old approval is ineligible;
a person accepts that Change under its current meaning.

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
before tests. A fresh proposal replaces earlier untouched work; human-touched
work remains ordinary and stays visible to its queue while open, including the
acceptance after an override. If its later check fails or errors with nothing
running, the queue remains `decide`: AppTown does not automatically retry a
person's Change. The decision comes from the run's current next acts. A landed Change, a person's skip or Try again,
or a closed human-touched Change ends the episode; quiet scans and repeated
proposals do not. Check failures and requests for a person record `held`;
AppTown errors record `faulted` and do not suppress identical proposals.
Three uninterrupted failed tries stop the queue. A stopped security queue
suppresses the faulted targets in its current episode and can try a new
fixable finding at the scan's failure threshold.

The Janitor overview leads with “N projects need you”, “Keeping N projects up to
date”, or “Janitor is off” when choices are absent or all off. Needs you lists
`decide` and `stopped` queues, release holds and unreadable update schedules.
Decision rows show what the proposal does, then the current decision's reason.
Pack proposals name moved packs; source proposals show classified files. Only a
fingerprinted empty source proposal says “Makes no change”. An override uses
the check's first explanatory sentence without citations, capitalised, or
“A check did not pass” / “A check could not finish” when it has no prose.
Approval says “Waiting for your acceptance”; a promoted Change with no decision
act says “Waiting for a decision on the change”. Accept uses the
supplied `decide` action and step: the approval form or a check override with a
required reason. Close skips the update and suppresses the same proposal.
Stopped rows offer Try again. Release holds open the deployment. Unreadable
schedules offer Run now. `retrying` queues remain AppTown's work and do not appear on the overview.

Recent updates lists project, what changed and when, opening the Change.
Dependency rows show package moves; pack rows name only added or version-changed
ways of working without versions. Settings contains only What to keep updated, including project
exceptions and automatic releases. Deployment pages own release windows.

The project page shows queue state, moved packages, recent tries linked to their
Changes, and paths to retry, chat or skip the update. Queues are `retrying`
while AppTown faults have tries left, `decide` when a person must decide, and
`stopped` when tries are exhausted. `decide` supplies the existing approval or
check override action and its step. Only `decide` and `stopped` contribute an
attention item. A skip retains its fingerprint and targets.
A person who may open and start work in a project can fire one of its
carried schedules now (`POST /api/v1/projects/{slug}/schedules/{workflow}/run`;
the general entry for any carried schedule). The firing is the scheduler's
own: the Change is opened through the schedule under the eligible rule, and
`schedule.run-requested` records the person. A busy or stopped queue, a missing
schedule or a missing eligible rule is refused and records no firing.
**Run now** in the project's Janitor section fires every adopted standing queue
through its nightly start.
`POST /api/v1/projects/{slug}/janitor/run` selects all queues or one named queue.
`apptown janitor run -p PROJECT [-queue QUEUE] [-again]` uses this operation.
Publication queues use the nightly pack catch-up, restricted to that project,
after the standing owner checks project eligibility. The request records the
person and the assessment. Nightly cadence and consent remain the default.

**Try again** ends the named queue's
current episode without suppressing its proposals. Deciding and updating queues
keep their Change links and progress. A refused **Run now** can explicitly offer
closure of the busy Change. It also closes that queue's open automated
Change, requiring current close authority and the exact Change shown. Retained
attempts keep their reasons, fingerprints and failed flags as history with
outcome `retried`; Skip still suppresses the same proposal. A failed subsequent
start leaves the episode ended; the next nightly firing uses normal admission.
The request resolves all matching eligible starts before resetting the named
queue once, then runs those starts. No eligible start means no reset.
Ordinary adoption Changes are not closed by this act. Their existing land or
abandon action remains necessary when they block pack updates.

Schedules declaring `standing: true` fire only under an enabled, currently
eligible rule. Without one, the schedule read says it waits for a standing
approval, with no next firing; its last recorded firing remains visible. This
is a normal off state. Unreadable workflows
and invalid cron expressions remain schedule problems. The scheduler reads each project's adopted standing declaration. Its
`cron` applies at routine level; `aggressiveCron`, when declared, applies at
aggressive level. Both are read in UTC. The scheduler's own admission read
supplies the queue admission and the earliest next update firing. Busy queues retain their future firing time; stopped
queues have none. Target-discovering schedules can still scan a stopped queue.
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
the latter records an error observation. Observations of different trees or
digests remain historical; a source scan alone never proves a running finding fixed.
The current source scan is the newest run that scanned main's whole tree. A
landing scans only the commits it adds with a scanner that declares a
`historyCommand`. For such a scanner, the answer is its newest whole-tree
observation of the same tree with the same scanner name, binding version and
configuration hash, so a landing does not hide a complete earlier scan.
Source observations record both the commit and its tree; equal trees answer
main even when landing creates a new merge commit. Observations without a tree
stay historical until a new scan answers main. The landing's own errors stay
current, and no other scanner takes an older answer. If catch-up changes the
tree, the history scanner stays unanswered until a whole-tree scan of that tree.
After building its release bundle, the landing scans every part and init image
by digest in one `scan-bundle` step. A bundle with local image IDs, or a run
without a bundle, records not applicable with its reason. Findings and scanner
errors do not block the release, but the step finishes before the run completes.
Each running part takes its answers from the newest run that observed its exact
digest, whether in a bundle scan or a running scan. Answers for a digest are
never combined across runs. A project without deployments needs only a complete
source scan. Local image-ID bundles remain not known with their reason.
A scan whose scanner could not judge the software, and a last scan older than
three days, appear in Attention as not known; the detail names the scanner's
reason or the date of the last scan. Code or images newer than the last scan
are not an Attention item: the next scheduled scan answers them, and the
project’s scan history shows them as not known meanwhile.
A decide or stopped queue's Attention item carries the first line of its error
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
