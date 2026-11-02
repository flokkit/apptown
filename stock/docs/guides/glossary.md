# Glossary

Terms used throughout AppTown. One meaning each.

## The work

**Instance** — one installation of AppTown. It hosts the projects. Pages call
it just AppTown; the command-line tool is still called `apptown`.
Governance, security controls, deployment targets, system integrations and
ways of working are defined at the instance level and consumed by projects.

**Project** — a codebase with its own repository, workflows, and settings.
Changes, runs, deployments, and releases each belong to one project.

**Deleted project** — a project whose deletion has taken its deployments
down. It takes no new work and is kept, read-only, with all its backups,
for 30 days; **Restore project** brings it back until its purge starts.
While its deployments are being taken down it reads **deleting**.

**Purge** — the end of a deleted project: its backups, repository and
records are removed, and its GitLab copy is renamed and archived. Its
receipts and usage stay.

**Change** — a unit of work with its own branch. However it starts — a
chat, the detailed form, a catalog change, or an imported branch — it
accumulates commits and ends shipped or abandoned. A change whose workflow
keeps a record instead of commits (research, a product spec) closes when
its record is accepted; nothing lands, and its lifecycle record says **finished** rather
than shipped. A change AppTown closed because its run found nothing to
change (a dependency update with nothing to update) stops with that reason in
its details. Landing runs the checks and approval before merging; a failed step
blocks the run. The web keeps work and evidence states visible, with details
one click away.

**Chat (helper)** — a conversational agent session on a project or a change.
A change chat edits that change's working tree directly. A
project chat has three jobs: answer questions, help set
the project up, and start work — a change, or a catalog change — handing
you the link. It never builds. Anyone who can read the project can look at
a chat; acting on it (sending a message, ending it) needs the right to work
on the project's changes.

**Run** — one execution of a workflow on a project. A run moves through the
workflow's stages, performs its steps, and records everything it does.

**Overview** — a project's front page: the Open / Change / Release doors,
its releases, what is happening now, and its history.

**Home** — the instance's front page: one
sentence on the situation, then what needs you, what is running across every
project, projects, recently finished.

## Status words

Health and observation chips use these words. Their explanations give the reason
and what would resolve uncertainty or failure. Work and evidence keep their own
words: waiting for you, in progress, deploying, landed, passed, excepted, stopped,
abandoned and update available. Low disk is an amber warning.

| Chip | Meaning |
| --- | --- |
| working | The configured thing works. |
| failed | Something set up broke, a check failed, or a required dependency is missing. This is the only red status. |
| not set up | An optional thing has no configuration yet. It is muted, not failed. |
| not checked yet | A check has not run yet. |
| last seen <age> | The reading is old but still usable. Its age alone is not failure. |
| not applicable | This check does not apply. |
| unknown | No usable reading exists, or the reading is past its shelf life. This never means working. |

Event times use your own time zone, for example `30 Oct, 17:26`. Freshness
uses a relative age such as `checked 5m ago` for fresh readings and `last seen 5m ago` for old ones. An integration with no background check reads **no automatic check** in muted text. A manual connection test keeps its last result and age; an old result stays marked old. A confirmed inventory resource is
**present**; a volume, network or image has no running health check, so its
health is **not applicable**. Recorded ownership remains separate.

**Attention** — everything needing a person's action that the viewer can read:
hosts and deployments, backups, failed integrations, confirmed app-check
problems and app checks whose answer is not known. Each listed row counts as
one item, including a row summarizing several app-check outcomes. An attempted check that
cannot give an answer counts under `not known`, with its reason. Confirmed
problems count under `to fix`. Checks not run yet and integrations without
background checks do not count. The header shows `3 to fix · 1 not known`,
omits zero parts, and says `All quiet` when both are zero. Unknown coverage is
separate from confirmed problems. Open Attention for each named resource, its
reason and next step.

## Workflows

**Workflow** — a named, versioned procedure: ordered stages of steps, with
a gate to pass before leaving each stage. Stored as YAML in the library
(Ways of working › Workflows), in three tiers: **Core** (the short list
most projects use), **Extras** (for particular situations), **System**
(AppTown runs these itself — landing, shipping, wiring integrations in
and out — nobody picks them). A project *selects* the lanes it uses under
Settings › Way of working; nothing is copied, and every run records which
library version ran.

**Lane** — a workflow a person picks as a way of working on a change
(quick-change, implement, fix-bug, dependency-uplift, …), as opposed to a
system workflow AppTown runs for every project.

**Scan point** — a step in a system workflow that says `on: source` or
`on: image` instead of naming a scanner. It runs every attached scanner
with that target, each as its own check. Attach a scanner and every
landing gains it; no workflow is edited.

**Stage** — an ordered section of a workflow. A run occupies one stage at a
time.

**Step** — a single action inside a stage: an agent task, a script, a scan,
a check, or an approval.

**Gate** — the condition for leaving a stage. By default every step in the
stage must pass.

**Approval** — a step that waits for people to decide yes or no. It names
its approvers — people or groups — and can require more than one OK
(quorum). One rejection stops it; every vote is kept. A merge into main
or a deploy needs a person's approval, or a landing started by someone
allowed to land.

**This change adds** — the list an approval of a commit shows first: the
secrets, services and app parts the commit declares that the point where
its branch meets main did not. Read from the app's declarations; recorded
with the decision.

**Standard of approval** — the required sentence on every approval step
stating what an OK means there (for example "I read the code and it meets
our norms"). It is shown when deciding and recorded with the decision.

**Pacing** — how a workflow advances. "Paced by people" waits at gates for a
human; "runs straight through" advances on its own when checks pass.

**Catalog change** — a prepared change that can be run on any project, with
parameters. AppTown ships them with each release.

## Shipping

**Landing** — the only path from a change's branch to main: landing gates,
merge, deploy, release record. "Land" is the verb.

**Release** — a promotion of something already proven on main to a wider
tier. Numbered per project; each release keeps a timestamped log, the
commit, and the image digest it shipped.

**Runtime contract** — the file `.apptown/app.yaml`: `app:`, the app's own
containers, each `build:`/`image:`/`static:`, one `public: true`; and
`needs:`, the services (postgres, s3, …) provided per deployment. No
contract, no deploy. [The app contract](contract-reference.md)
says what it promises.

**Capacity** — how full a deployment target is. A fixed host shows what it can
give containers, the limits they hold, what they use and its disk. An elastic
one (App Platform) grows, and shows each part's size and an estimated cost.

**Boot check** — AppTown starting an app cold from its contract to prove
it comes up, before a person is asked to try it.

**Model profile** — a named set of model choices that a project uses, such
as `standard` or `high`. Its four basics are Primary · strong (`default`),
Primary · fast (`primary-fast`), Secondary · strong (`secondary-strong`) and
Secondary · fast (`secondary-fast`). The primary models do the work; the
secondary models give a second opinion. Individual tasks are the advanced
level and can override those choices. An unset secondary uses the primary
of the same speed; an unset fast primary uses `default`.

**Task (model)** — the purpose for which AppTown asks for a model, such as
planning, writing code or review. Workflows name it as a slot, for example
`model: review`. Each task follows a chain to a basic model choice unless
set individually. AppTown tries the task's cell in the project's model
profile, then in `standard`, before following its fallback. Reviews use the
strong secondary; quick independent checks ask for the fast secondary.
Every chain ends at `default`, the strong primary.

**Profile** — the one named set of differences a deployment uses: in
`app.yaml`, parts' resources and environment, and needs' resources or catalogue size; in
`secretspec.toml`, its expected secrets. `default` is the base; previews use
`preview` when declared.

**Limit event** — what a Docker host's daemon reported of a deployment's
part or need: killed out of memory, or exited without anyone stopping it. An
exit is an observation, not proof a limit was hit. Read from the event log, so
a gap is possible: an empty list is never proof that nothing happened.

**Preview** — a temporary deployment serving a change's working tree. One
per change; removed when the change closes. Boots on the worker when AppTown has one.

**Departures** — the log of movement through the tiers, newest first: the
first tab of the Deployments page. The second tab is the fleet as a table,
one row per app, tiers as columns.

## Where things run

**Tier** — a rung of the promotion ladder: preview, main, intranet.
Moving right is a release, and each rung reaches more people.

**Deployment** — a named, standing copy of a project's app on a deployment
target. `main` is fed by every landing. It stays until a new release
replaces it or someone takes it down.

**Deployment target** — a place deployments run: the built-in Docker host,
a Docker host attached over SSH, a Portainer environment, DigitalOcean App
Platform, or a bucket website (static sites only). An administrator
attaches targets and offers them to projects.

**Take down** — remove a deployment and everything AppTown recorded
creating for it. On a Docker host its data is backed up first, and that
final backup is kept with the project.

**Worker** — a separate machine with its own Docker that runs most project
work off AppTown's own host: steps, checks, previews, release builds, and
new change chats by default. Project chats, chats opened on AppTown and
deployments on the built-in Docker host stay on AppTown.

**Main (tier)** — the standing copy of each project's main branch, updated
by landings.

**Intranet (tier)** — the internal network. Released apps run on the
platform, survive reboots, and are reachable by everyone inside.

## Quality and record

**Guidance** — the house rules agents work under, imported per project as
versioned blocks. Some blocks carry checks; some carry a skill, a how-to an
agent loads only when it needs it.

**Check** — an assessment of an app against a declared rule. A check that runs
and finds a problem fails. An attempted check that cannot give an answer is not known,
with its reason, and never counts as passed or as a confirmed problem. A gate
that requires that check still refuses until it has the required evidence.
Deferred checks remain not yet assessed. For example, a deployment check
waits for a standing deployment, and an image check waits for its image.
These deferrals raise no Attention row.

**Conformance** — whether an app meets the ways of working it has adopted.
Checked two ways: as a step when a change lands, and by a read-only
**review** of every app's committed main (the Conformance page). A review is
a saved record you can open later; a project's **standing** is one plain
sentence plus a bar of its checks.

**Policy** — a rule enforced when workflows are saved, e.g. nothing deploys
without a security scan in an earlier stage.

**Scan** — a tool-based check of the workspace: dependencies, leaked
secrets, SBOM.

**Not applicable** — what a check or scan says when it applies to nothing
the app's contract declares: no part built with its tool, no static part,
no image. It gives the reason, is never a pass or an error, does not stop a
change by itself, and is listed on the change so it is never read as
passed. What a check applies to is its `appliesTo`; what a part is built
with is its `tools:` in `.apptown/app.yaml`.

**Finding** — one issue reported by a scan or a review, tracked until it is
fixed or suppressed.

A confirmed app-check problem is a finding confirmed by a person or reported
by a deterministic check. A check that runs and fails on its own terms also
counts as a confirmed problem. A check that cannot give an answer produces
no confirmed problem.

**Suppression** — a recorded decision to accept a finding, with a reason and
an author.

**Complaint** — a report, usually from an agent, that AppTown itself got
in the way: what happened, why it mattered, what they wish had been true.
Filing one triggers nothing; the list exists to be read and acted on by
people.

**Decision (ADR)** — architecture decision record: a short document stating
a decision and why it was made. Kept under a project's Settings › Decisions;
agents read them before they build.

**Artifact** — a file or record a run produces and attaches as evidence: a
report, an SBOM, a release record.

**Evidence** — what a step leaves behind to show what happened. Gates read
evidence, not intentions.

**Audit log** — the record of state changes made outside runs: who did what,
when.

## Platform

**Pack (way of working)** — a bundle of guidance, workflows, policies, tools,
services and starter files. A project takes one or more packs and gets all of
them; packs that disagree on something a project has one of (a tool's
version line, the toolchain, a service, a policy, starter files) cannot be
taken together. A server app takes `server-base`, one backend pack, `postgres`
if it keeps records, and a UI pack if it has a browser UI. An admin can turn a
pack off under Ways of working › Packs: it stays in the store but is not
offered when a project is made. Stock packs cannot be deleted; they can be
turned off. To change a stock pack, copy it into a pack of your own.

**Recommended version** — the version of a pack that new projects get and
that projects on another version are offered. Saving a pack makes a new
version without recommending it; a person recommends it on the pack's page.
An AppTown release's new stock versions wait the same way. No project moves
until its owner adopts the version.

**What-if** — a pack version's checks run on projects' main beside the
checks they adopted, to see what would newly fail or pass; by default on
every project on an earlier version of the pack. Hypothetical: it never
changes a project's standing and is not evidence for a gate.

**Need** — a service the app declares under `needs:` in its
`.apptown/app.yaml` (a database, an object store, AI models). It is part of the
code, so changing it is a change. A need is *fulfilled* either by the
deployment's target (postgres runs beside the app on a Docker host; App
Platform makes a managed database) or through an integration (`llm` through
OpenRouter). The app reads the same variables either way.

**Service** — what a need gets in a deployment or a preview: this
deployment's own postgres, bucket or AI models key. Settings › Services lists
the services the app uses, and what fulfils each.

**Integration** — a named connection to an outside service (GitLab,
Infisical, Portainer, GlitchTip, OpenRouter), set up on the Integrations page.
Its configuration holds the names of secrets, never their values. An
integration may fulfil a need, or a project *attaches* it directly under
Settings › Services › "Connected to" (error reporting).

**Wired in** — an attached integration that the app's code actually uses.
Attaching only injects the connection details; "wire it in" opens the
change that makes the code read them. The card shows the state from the
app's side — *not wired in yet*, *wiring* (the change in progress), *wired
in* (it shipped) — and, for error reporting, when the provider last saw an
event.

**Test hook** — the house-standard way to prove error reporting works in
any deployment by hand: a server app serves `GET /_apptown/boom`; a browser
app or game fires on the typed sequence `boom!!!`. Both send one tagged
"apptown test boom" event. Not authenticated, on purpose.

**Registry** — the container image store: releases push to it, the platform
pulls from it.

**Origin** — what a run actually ran, recorded on the run, e.g.
`stock/quick-change@10` (shipped with AppTown) or `lib/our-land@2`
(yours). Guidance imports carry the same kind of receipt.

**System binding** — the instance's choice of which library workflow
stands in for a stock system one (landing runs `our-land`, say). Set on
the System tab of Ways of working › Workflows; a stand-in must declare
`system: true`. This is how an admin customises AppTown's own jobs for
every project at once.

**Current-rules check** — if the rules change while a landing is waiting,
it pauses at the merge instead of finishing under the old rules. Restart
the landing to run under the current rules, or override with a reason.

**Workspace** — the directory a run or change works in. A change's
workspace persists for the life of the change.

**Slug** — a project's short lowercase identifier, used in URLs and paths.

**Actor** — the person or system a recorded action is attributed to.

**Account** — a person's sign-in on the instance: username, password, and
a role (admin or member). Managed on the People page.

**Invite** — a one-time link an administrator gives a new person so they
choose their own password and are signed in. It is for one account, lasts
seven days unless the administrator says otherwise, and can be withdrawn on
the People page. AppTown sends no email; the administrator passes the link on.

**Group** — a named set of people. An approval step may name a group as
its approver; any member's approval counts, and the record keeps the
person's name.

**Role** — a named set of things a person may do, such as instance
administrator, auditor, project creator, or a project's owner or
contributor.

**Grant** — a role given to a person or a group, on the whole AppTown
(**People → Roles on this AppTown**) or on one project (its **Settings →
Members**). Nothing is implied: a power nobody was granted, nobody holds.
A new AppTown grants every signed-in person the Auditor role, which reads
everything; revoking that grant makes each project readable by its members
only ([People, invites, roles and grants](people-and-roles.md)).

**Member (project)** — a person or group granted a role on a project.

**Janitor** — the named actor for governed routine maintenance. Standing consent
records who authorized bounded update classes and their exact project and delivery
scope. Consent management is available; automatic processing is not. Saving a
rule does not update running software.
