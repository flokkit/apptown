# Janitor standing consent

A standing approval records a person's consent for bounded routine updates. It
is separate from a vote on an individual Change. AppTown records the exact
meaning, project UUIDs, workflow purposes, selected environment and target UUIDs,
and the person who enabled it. Changes create new revisions; history is retained.

Automatic processing is not available. Saving consent does not update, accept,
land or deploy software. No existing approval or imported rule enables it.

The CLI and API manage saved consent and its history. The project page shows the two update queues, recent tries, and the current scan
summary and findings, with a link to Conformance for scan history. Consent can be disabled, revised or read with its history. A revision
previews the exact consent before saving. Full delivery
requires explicit environments for every selected project, deployment authority
and promotion consent where required. Nothing selects a default environment on
the person's behalf. “Accept and land only” explicitly leaves running software
unchanged. Administrative rules cover a finite captured project set; they do not
expand to later projects.

Only a verified person acting directly in their browser or personal command-line
instrument can enable or revise consent. Management, approval, landing and
release permissions remain distinct. Current project law and approval selectors
apply; unsupported quorums and independent-approval requirements refuse setup.
The working and landing workflow each declare one supported purpose with
`standing: {class: routine-update, means: ...}` on their approval step. This
declaration provides no authority and changes no ordinary human vote.

A rule names bounded patch/minor dependencies, verified same-version base-image
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
an external effect already claimed may finish. A manager can disable consent
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

## Inspecting a candidate

`apptown janitor assess RULE --run RUN` inspects an exact run without accepting,
landing or deploying it. The caller needs current read and check-execution
permission. The rule must still be eligible for that project and workflow.
Active writers, unsettled tasks, dirty or unpublished work, changed source and
changed consent refuse an assessment. Inspection never repairs or saves source.

Dependency inspection runs each adopted `fixes` entry's `list` command on separate
copies of the committed base and candidate, using the check executor. The
standard listing carries ecosystem, package, version, source and attributes.
The package set and sources stay equal, attributes stay equal, and moved
versions advance within the selected patch or minor bound. A 0.x version stays
in its minor line. A targeted security fix also reaches each finding's `FixedIn`.
A major, new package, changed source or changed attribute fails classification.
A missing or failed listing, malformed output or non-semver version is an error.
Application files beside the update are judged by checks, not a file allowlist.
An empty candidate is not applicable.

The result records the source commits, tree, rule and run revisions, and a digest
of the observed listings. The source is checked again after execution. Missing
or incomplete observations cannot qualify a candidate. Classification does not
accept, land or deploy it.

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
start-up or scans, and no inspection result enables automatic acceptance.

## Classifying publication transitions

A current standards manager can preview two exact forward versions of a guidance
block or pack, then explicitly classify their displayed meaning as routine.
`apptown janitor publication preview --file transition.json` returns the exact
before/after material, source and signer facts, explanation and digest. The file
names `kind` (`block` or `pack`), publication UUID `id`, `from` and `to` versions.
Inspect the complete material, then use `publication classify --file
transition.json --confirm DIGEST --request UUID`. Keep that request UUID for any
retry of the same human act. Equal retries return the original receipt, including
its revocation; changed input conflicts. A later new act requires a new UUID.

The initial supported class changes guidance body text only. Executable checks,
workflows, approval rules, policies, capability requirements, needs, services,
secrets, resources, tools, starter settings and component membership must remain
unchanged. A pack may advance its pinned guidance while preserving those inputs;
its complete included publication tree participates. Unresolved or unsupported
publication material refuses classification. A recommendation is not consent.

`publication history block|pack UUID` reads that publication's classification
history. `publication revoke CLASSIFICATION_UUID` records a one-way revocation.
Both require current standards authority. An agent, run, chat or delegated token
cannot supply the enabling human act. Insert and audit commit together. Restored
classifications are disabled before application recovery and require new local
human consent; their historical meaning is retained.

Assessment reads the actual Change target and current adoption, not a submitted
subset or a display summary. Every selected pack and guidance transition needs
exact coverage, with unchanged membership, order, disabled state, tiers,
workflows, policies and capability requirements. Existing capability requirements
must remain available. A pack classification covers only its exact nested
publication intervals. Revoked consent, changed material or a no-longer-eligible
issuer refuses assessment. Responses carry selected classification UUIDs,
digests and shown meaning; full material remains in publication preview/history.

A nonempty, fully covered adoption target may have an empty Git diff. An ordinary
empty Change is insufficient. Combined npm, image and adoption updates must
explain every source and procedure change independently; adoption consent cannot
cover unrelated application edits. The source, rule and adoption consent are
checked again after inspection. These short-lived checks produce observations,
not authority held until a later effect. `processingAvailable` remains false.

## Scheduled scans and updates

The platform pack carries `janitor-scan` and `janitor-update`. A project runs the
versions its adoption pins. The security schedule observes source and running
images, and calls `janitor-fix` only for a new finding that an adopted update fix
covers. Scanner errors never trigger the call. The weekly schedule updates the
parts through the language packs' declared commands. Node, Go, Python with uv,
and Ruby ship update declarations and standard-list converters.

The `fix` step dispatches by `kind`; `update` is the supported kind. Whole updates
use `run`; targeted updates use `runFor` with the caller's targets, never `run`.
Each command runs after `mise install` in its part's folder and publishes its
commit through the script-step path. Missing coverage or commands are errors;
an ordinary nonzero exit is a failure naming its part.

A repair agent with `repairRounds: N` stays dormant until a check fails. Its
session count is bounded over the run's lifetime. The stock workflows allow two
sessions, then leave the Change for a person. Every acceptance waits for a person.
`processingAvailable` remains false.

The `security` and `updates` queues keep separate histories. A proposal records
main, the adopted versions, the listing after the declared change, and its
targets. A proposal identical to the queue's last held or skipped one stops
before tests. A distinct proposal replaces earlier untouched work; human-touched
work remains ordinary. A landed Change, a person's skip, or a person's touch of
held work ends the failure episode; quiet scans and repeated proposals do not.
Three uninterrupted failed tries stop the queue. A stopped security queue can
try a new fixable finding at the scan's failure threshold.

The project page shows queue state, moved packages, recent tries linked to their
Changes, and paths to retry, chat or skip the update. Stuck and stopped queues
each contribute one attention item. A skip retains its fingerprint and targets.
Attempts and firing records name the schedule as origin. Rule IDs and revisions
remain empty until standing processing supplies that authority.

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
Missing scans and scans older than three days appear in Attention as not known.
Running-image-only findings do not create Attention items.

Scheduled Changes record their origin and a compact summary at creation. A
person's edit, chat or other work on a Change, or starting its landing, makes
it ordinary work permanently. Automated queue Changes stay out of the Change
list. Their existing Change pages remain reachable through
`apptown janitor attempts -p SLUG`. An observation-only
queue Change runs every scan even after findings or scanner errors, then closes
when its scans settle, retaining their actual outcomes. Ordinary runs stop on
failure.

The Change owner purges untouched abandoned automated Changes after references
and unreleased tasks no longer retain them. Disposal is claimed before removing
branches, workspaces, transcripts and execution rows. Failed cleanup is retried
by settlement and the reaper. Conformance keeps scan observations independently,
including exact subjects, scanner binding version, configuration and hash,
observation time, verdict, normalized findings and unknown reasons. Compact
summaries continue to reference those observations after disposal.
