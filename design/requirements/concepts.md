# The concepts: what each thing is, how to think about it, and where it appears

This is product vocabulary, not the Go model or a blanket implementation
claim. Start with the
[current product model](../subsystems/product-model.md), [authority](../subsystems/auth.md)
and [workflow guarantees](../subsystems/workflow-domain.md). Companion to `requirements.md`, mined from
it: every noun the use cases lean on, with what it is, the picture to hold
in your head, how one comes into being and goes away, the rules that bind
it, and the sections it appears in. The shipped glossary
(`stock/docs/guides/glossary.md`) gives one sentence per term for people
using the product; this is the layer underneath, for people designing
it. Where the requirements use two words for one thing, or one word for
two things, the entry says so and links them.

## Words that need linking first

The requirements were written by several hands over several days and use
more than one word for some things. The table is the map; the entries
below use the first word in each row.

| We say | We also say | It is |
|---|---|---|
| **lane** | workflow | a [workflow](#workflow) a project offers for working a change; see [lane](#lane). Not a [way of working](#way-of-working), which is the whole adopted set; the forge's own workflows are [system workflows](#system-workflow), never lanes |
| **pack** | way of working, stock pack, institution pack, platform pack | a published bundle; see [pack](#pack). "Way of working" glosses the pack in B Projects and B The project's way of working, and names the settings tab where the whole adopted set (packs plus a project's own blocks) is edited in B11 and B17 |
| **project** | app (C Changes, B Services), repository; sometimes heard as "product" | one codebase the forge governs; "app" is the running software; see [project](#project) |
| **change** | ordinary change, catalogued change, uplift change, fix, work | the unit of work; see [change](#change) |
| **helper** | the forge's own coding agent, agent, agent session, chat, console, terminal, instrument | the forge's agent acting for a person; the chat is where you talk to it; a console is its container; see [helper](#helper) |
| **deployment** | environment, instance, standing environment | a project's app standing on one target under a name; see [deployment](#deployment). Not a [tier](#tier): its audience, data class and feed say who it serves. The act of putting a bundle on it is a [deploy](#deploy) |
| **target** | place, host, endpoint, "the cloud" | where a deployment stands, reached through one [provider](#provider); see [target and offer](#target-and-offer). "Cloud" is also the name of a [tier](#tier), which is a different thing: the tier says who a release reaches, the target says where it runs |
| **accept** | approve | two different acts: a maintainer *accepts* a change (C7); an approver *approves* a step (C13, B22). Acceptance starts the landing and counts for the landing's approval where the accepter may give it; see [acceptance](#acceptance) and [approval](#approval) |
| **acceptance** (a change's) | the check's expectation carried as acceptance (B21) | second sense: what an uplift or remediation change must prove before it may land; see [acceptance criteria](#acceptance-criteria) |
| **adopt** | update, prepare and adopt; "pull" | move a project to a new revision (B13). Two unrelated uses: *adopt a branch* (C11) opens a change on existing work; *adopt-integration* (B27) wires a service in |
| **uplift** | adopt through a change (B13) | a change that carries a move to a new revision. Unrelated: the *dependency-uplift* lane (C4, B14) updates dependencies |
| **fleet** | the store (A12); sometimes heard as "estate" | every project on the instance; see [fleet](#fleet) |
| **need** | service (B24), dependency | what an app declares it needs to run (a database); see [need](#need). Not the same as an [integration](#integration), which B Services also calls a service |
| **integration** | service (A2, B26–A3), connector, binding, capability | a connection to an outside service; see [integration](#integration) |
| **capability** | — | two senses: what an integration provides (error reporting) in B Services, and a unit of authority (`change:approve`) in A People and authority; see [integration](#integration) and [capability](#capability) |
| **provider** | cloud provider (A6), the provider (B28) | two senses: a cloud account's vendor (DigitalOcean) and an integration's vendor (one error tracker) |
| **token** | credential, key | two senses: an API token minted for a person (C21) and a provider's secret held in a store (A6, B26) |
| **exception** | suppression | two records: an exception accepts a conformance finding with an expiry and a second approver (B22); a suppression accepts a scanner finding with a reason (C17); see [finding](#finding) |
| **release** | release record (C13), release number | a numbered promotion; the record cut at landing is a [bundle](#bundle), not yet a release; see [release](#release) |
| **receipt** | record, ledger, evidence, audit log, trace | related but distinct; see [evidence](#evidence) and [receipt](#receipt) |
| **stock** | shipped with the forge, vendor | what the binary carries; opposite of institution-authored; see [library](#library) |

## People and agents

### Actor
Who a journey is for. The requirements name **owner** (`project:configure`
and above), **maintainer** (works on a project and accepts changes),
**release manager** (may promote to protected deployments), **standards
administrator** (publishes ways of working, approves weakening),
**instance administrator** (clouds, connectors, people; B Services and A People and authority also say
*integration administrator* and *identity administrator* for the same
seat, scoped by role), the [helper](#helper), a [coding agent](#coding-agent),
and **the forge** itself. *Mental model:* a role is a job title; a
[capability](#capability) is the key on the ring. *Appears:* the preamble,
every use case.

### Helper
The forge's own coding agent, running `pi` in a container on the person's
behalf. *Mental model:* a workshop assistant who edits the change's own
workspace directly and reads the record through a scoped CLI, but
cannot touch `main` or approve anything. The **chat** is where you talk to
it (a project chat is the front desk; a change chat is the workshop); the
**terminal** is the same session seen raw; a **console** is its container.
*Lifecycle:* a session starts when a person opens the chat, persists
across restarts; disposable runtime resources may be reaped independently of
the saved conversation and its checkout ownership. *Rules:* an instrument is
bounded by its one project and its table of allowed acts (the rules under A People and authority); it
files a complaint when the forge gets in its way. *Appears:* C2, C5,
C7, C8, C24.

### Coding agent
A person's own agent (Claude Code and the like) connected over MCP.
*Mental model:* the person's hands at a distance, holding a narrower tool
set than the person: it opens and works changes and hands them off; it
never approves, lands, releases or administers. *Lifecycle:* connected by
OAuth, listed and revoked under People. *Appears:* C9, C20, C21, the table Who can drive each journey.

### Account, group, role, grant, capability {#capability}
An **account** is a sign-in; a **group** is a named set of people that may
own projects and may be an approver; a **role** is a named bundle of
capabilities at instance, group or project scope; a **grant** gives a role
to a person or group; a **capability** is one allowed act. *Mental model:*
roles explain, capabilities decide. The original requirement describes a receipt for every authority decision.
Current implementation requires receipts on consequential paths; ordinary read
checks do not emit a durable receipt for each decision. This is a scope difference
to resolve against accepted authorization policy, not evidence that every decision
is currently persisted. *Rules:* approvals belong to people; instruments never hold more than
their person. *Appears:* B3, C19–A11. Not the B Services sense of capability,
which is what an [integration](#integration) provides.

### Token
A named API credential minted under a person's name for the CLI or a
script; its actions are recorded as theirs; revoked from the same list.
*Appears:* C21, the table Who can drive each journey. Not a provider's secret, which is held in a
[secrets store](#secret).

## Projects and code

### Instance
One deployment of the forge, hosting the projects. Ways of working,
targets, integrations, people and policies are defined here and consumed
by projects. *Mental model:* the institution; projects are its members.
*Appears:* A13, A3, A11, D.

### Project
One codebase the forge governs: its own repository (the **hub**), selected
lanes, adopted ways of working, deployments, secrets, members and
decisions. Called the **app** when it is running. *Lifecycle:* started from a pack or
imported (B1, B2), configured, worked through changes, deleted only
with no run in flight and no standing deployment (B5). *Rules:* only the
forge writes to the hub; dirt there is an integrity event (A14).
*Appears:* B Projects, and as the scope of everything else.

### Repository, hub, mirror, main
The hub is the project's repository in the forge's data directory; `main`
lives there and is written only by a [landing](#landing). Every workspace
is a self-contained clone with no push remote. A **mirror** is an optional
GitLab copy that landed branches are pushed to; never the gate. *Appears:*
B4, A14, C9, C11.

### Runtime contract
`.forge/app.yaml`: the app's own containers (build, image or static, one
public) and its [needs](#need). *Mental model:* the one file that says
how to run me; the forge never invents packaging and never writes a
Dockerfile. *Lifecycle:* written by the excavate lane on import or by the
first change, versioned with the code, read at preview, landing, deploy
and release. *Appears:* B2, C6, C12, B6, B24.

### Decision (ADR)
A short record of a project decision and why, kept under its settings.
*Lifecycle:* draft, proposed, accepted, superseded. *Rules:* agents read
accepted decisions before they build; a lane may refuse to start without
one. *Appears:* B15.

## Work

### Change
The unit of work: one branch, one workspace clone, one story, one human
[acceptance](#acceptance). *Mental model:* a vehicle that enters by one of
the doors (a sentence, the chat, the detailed form, the catalog, an
imported branch, a coding agent) and leaves only through the landing.
*Lifecycle:* in progress, landing, then shipped or abandoned; the branch
is kept for the record. Varieties named in the requirements: an
**ordinary change**, a **catalogued change** (C3), an **uplift change**
(B13), a **fix** or remediation change (B21, B23), a wiring change (B27),
a retiring change (B30). *Rules:* nothing reaches `main` except through a
change; a change is judged on what it introduced. *Appears:* C Changes and
everywhere work happens.

### Lane
A [workflow](#workflow) a project offers for working a change, chosen by
the person who starts the change: quick-change, implement, fix-bug,
plan-execute, product-spec, research, excavate, dependency-uplift. So a
lane *is* a workflow — one in the "working" role, offered on this project.
*Mental model:* lanes on a road; the change is the vehicle; the lane sets
how much scrutiny the work gets before the person is asked to accept;
every lane ends at the same acceptance and the same landing. The forge's
own workflows (landing, shipping, wiring a service in or out) are
[system workflows](#system-workflow): nobody picks them, so they are not
lanes. *Lifecycle:* authored or shipped as stock, versioned on every
edit, offered by a project under its way of working, invoked on a change,
snapshotted into each run, deprecated but never deleted while a run cites
it. *Appears:* B1, B2, C4, B12, B17, B14.

### Way of working
Everything a project has adopted: the [packs](#pack) it is on, its own
[guidance blocks](#guidance-block), the [lanes](#lane) it offers, its
[policies](#policy) — written down as its [adoption revision](#adoption-revision).
*Mental model:* the house rules and the house procedures, together. A
pack is a *published* way of working a project can be put on; a project's
way of working is one or more packs plus its own additions and
exclusions. The settings tab of the same name is where it is edited.
*Appears:* B11–B13, B20.

### Workflow
A versioned YAML procedure in the instance [library](#library): ordered
stages of steps, each stage left only through its gate. Two roles: a
**working** workflow is a [lane](#lane); a **landing** workflow is a
[system workflow](#system-workflow). *Rules:* validated on save against
the schema and the policies; a run snapshots the definition and records
its origin (`stock/quick-change@10`); a project selects, never copies.
*Appears:* B17, A13, B14.

### System workflow
A workflow marked `system: true` that the apptown runs for every project
and nobody picks: land, ship, adopt-integration, retire-integration. The
instance may **bind** a custom one in place of a stock one for every
project at once (a system binding). The platform pack pins them.
*Appears:* C13, A13, B27, B30.

### Run
One execution of a workflow on a project, usually on a change. *Mental
model:* a story with chapters (stages), each chapter a set of acts
(steps), and a door out of each chapter (the gate). A run may call another
workflow as a **child run** in the same workspace, which inherits its
parent's revision. *Lifecycle:* created with a frozen definition, moves
through stages, ends done, failed or cancelled; interrupted landings and
deployments are recovered at startup. *Appears:* B5, C5, C13, B17, B The project's way of working
rules, C22, D.

### Stage, step, gate
A **stage** is an ordered section of a run; a **step** is one act inside
it (agent task, review, scan, tests, boot, conformance, approval,
deploy); the **gate** is the condition for leaving the stage, by default
every step passed. *Rules:* when a gate fails, the producing step is
reworked with the gate's evidence as feedback, up to three times, then a
person is asked; a step may be overridden with a reason, recorded
forever. *Appears:* C4, C5, the rules under C Changes, C13, C15.

### Check
One test a gate reads. Four families in the requirements: the project's
own **tests** (C13); **scanners** bound to the instance, run at scan
points on source or image (C13, C17, A2); the way of working's
**guidance checks**, written into blocks (B13, C16, B19); and the **boot**
of the app itself (C6). A guidance check is a shell test, a tool run, a
rubric a model judges, or a probe against the running app, with a
severity. *Appears:* C13, B13, C16, C15, B19.

### Reviewer
An independent agent step that checks the implementer's claim against the
real diff and fails the change when the claims misrepresent it; also
confirms a retirement left nothing behind. *Appears:* the rules under C Changes, C15, B30.

### Preview
A temporary deployment serving a change's working tree at its own
address, with the needs it declares provided by emulators. Also the
**boot** gate (the same cold start as a check) and, in the chat, the
**rehearsal** after a turn. The **preview tier** also hosts a look at an
old release (C14). *Lifecycle:* one per change; rebuilt when the tree
changes; torn down when the change closes. *Appears:* C2, C6, C10, C14,
C18, D.

### Acceptance
A maintainer's decision that a change is good: accept, or send back with
words. *Mental model:* the one human judgment every change must pass;
sending back reworks it with that feedback in the same session.
*Rules:* accepting starts the landing by itself. It counts for the
landing's human [approval](#approval) where the accepter could approve that
step themselves, was shown its sentence beside the acceptance, and the
landing's checks raised nothing new; otherwise the landing asks again. *Appears:* the intro to C Changes, C2, C7.

### Acceptance criteria
Second sense of the word: what an uplift or remediation change must prove
before it may land — the target revision's new checks (B13) or the
check's own expectation (B21) — and which must pass outright, never
excused as pre-existing. *Appears:* B13, B21.

### Approval
A step that waits for named people or groups to say yes, against a
written **standard of approval** stating what an OK asserts; one
rejection stops it; every vote is kept; a quorum may be required.
*Rules:* approvals belong to people; an exception is approved by someone
other than the requester. *Appears:* C13, B22, the rules under A People and authority.

### Feedback and rework
Words sent back with a rejection, or a reviewer's findings, delivered to
the producing step when it runs again. *Appears:* C7, the rules under C Changes.

### Catalog
Canned, versioned change definitions that can be run on any project with
parameters; a draft is promoted to the catalog after evaluation.
*Appears:* C3.

### Attachment
A file or picture dropped into the chat or onto a change, kept with it
and read by the helper. *Appears:* C8.

## Landing and shipping

### Landing
The only path from a change's branch to `main`: catch up with `main`,
tests, source scanners, the way of working's checks, the image scan, the
approval, the current-rules check, the merge, the deploy
to the `main` deployment, the release record. *Mental model:* the
customs post; everything passes through it and nothing else opens the
border. *Rules:* if the rules change while a landing waits, it pauses and
must be restarted, never grandfathered; interrupted landings are
recovered at startup. *Appears:* C7, C13, B13, B21, C15, D.

### Bundle
The record cut at every landing of exactly what landed: the commit, the
contract, the built parts and their digests. *Mental model:* the sealed
box a release ships; a release is a bundle put somewhere. *Appears:* B9,
B10, D.

### Release
A numbered promotion of something already on `main` to a wider tier or
another deployment, with its log, who did it and what shipped. *Rules:*
a release to a protected deployment takes the release grant; refused
while another to the same deployment is on its way; the release page is
the receipt; an old release can be looked at, never restored (C14).
*Appears:* C1, B8–C14.

### Deploy
The act of putting a bundle on a deployment through its target, as a
numbered generation with a log written as it happens; rolling back is
deploying an earlier bundle. *Rules:* after a restart the forge resumes
from what the provider had already named or fails honestly; it never
adopts a deployment on a guess. *Appears:* B9, B10, the rules under B Deployments and releases, B7.

## Where things run

### Deployment
A project's app standing on one target under a name: the `main`
deployment every repo-backed project has, the intranet, or a **standing
deployment** on a cloud target with an audience and a data class. A
**protected deployment** (partners, public, production data) takes the
release grant. The interface, API paths, CLI and database all say
deployment. A deployment is not a [tier](#tier): its
audience, data class and feed say who it serves. *Lifecycle:* created against an offered target, deployed to,
torn down; what holds data is left behind as [retained](#retained).
*Appears:* C13, B9, C18, B6, B7, B24.

### Tier
A rung of the ladder: preview, main, intranet, cloud. Moving right is a
release and reaches more people. The **departures board** lists recent
moves. *Appears:* B8, C14, C18.

### Target and offer
A **target** is a place the forge can deploy to through one provider: the
built-in Docker host, a Portainer endpoint, an App Platform team. An
**offer** gives a target to a group or everyone with caps (deployments
per project, audiences, data classes); built-in targets are offered by
construction. *Rules:* a deployment may only bind a target the project
was offered, and only if the contract can run there. *Appears:* B9,
A6–B6.

### Provider
A credentialed connection to an infrastructure API — the
[integration](#integration) noun with an infrastructure kind: a cloud
account, a Kubernetes cluster, a Docker daemon over TLS, a Portainer
instance. You **connect** a provider; nothing deploys *to* a provider —
its [targets](#target-and-offer) do the hosting. Instance- or
group-scoped, health-checked, its credential a secret reference resolved
at use time. *Appears:* A6, C18; `../subsystems/deployment.md` §2.1.

### Cloud account
A provider kind (DigitalOcean today) connected with a token held in a secrets
store; its targets appear. The **infrastructure view** shows what is at
each provider and what it hosts. *Appears:* C18, A6.

### Handle
What a provider creates for one environment when it is deployed: one
DigitalOcean app, one Cloud Run service, one Portainer stack, one
container on the Docker host. Provisioned environment state, never a
[target](#target-and-offer): a target exists before any environment is
bound to it, a handle exists only for its environment and goes when it
is torn down (what held data is [retained](#retained)). *Appears:*
`../subsystems/deployment.md` §2.3.

### Renderer and driver
The pair a target's kind names: the **renderer** turns the runtime
contract into that platform's shape (a compose file, an App Platform spec),
the **driver** pushes it there and reads back the handle. The kinds are
`docker-host`, `portainer`, `do-app` and `s3-bucket`. *Appears:*
`../subsystems/deploy-targets.md`, `../subsystems/deployment.md` §2.4.

### Retained
A resource that held data and was left at the provider when its
environment was torn down, listed with owner and intent until a person
marks it released or kept. *Appears:* B7.

### Fleet
Every project on the instance, seen together: by tier under Environments,
by standing under Conformance, by adoption under Ways of working. Sometimes heard as "estate". *Appears:* C18, B20, B23.

### Need
What an app declares it needs to run (Postgres, Redis, S3) in its
contract; every environment fulfils it its own way: an emulator in
previews, a managed instance in the cloud, a container on a host. B Services
calls this a *service* in B24; it is not an [integration](#integration).
*Rules:* state lives in a need, never a file on disk. *Appears:* C6,
C12, B24, D.

### Secret
A value the app reads, declared by name with its source store; values
reach the running app and the target, never the record. A **secrets
store** is the built-in file store or Infisical. *Appears:* A6, B25.

## Ways of working and conformance

### Pack
A published way of working: guidance blocks at pinned versions, lanes,
policies, a toolchain, starter files. Packs have versions with exact
contents; a version is cut when the pins change. **Stock** packs ship
with the forge; **institution** packs are authored here; the **platform**
pack is what running behind the forge asks of any app and is on every
repo-backed project, never offered in the wizard. *Lifecycle:* published,
versioned, offered in the wizard, adopted by projects, turned off (never
deleted). *Appears:* B1, B2, the intro to B The project's way of working, B11, B13, B16, A12.

### Guidance block
The smallest unit of a way of working: one markdown document with a
name, tags, the blocks it conflicts with, and its checks. Its body steers
every agent step; its checks judge the tree. *Lifecycle:* versioned
append-only; a stock change cuts a new version and moves no project.
*Rules:* two blocks that contradict each other cannot both be adopted.
*Appears:* B11, B12, B16, B19.

### Library
The instance store of guidance blocks, workflows, packs and policies.
*Mental model:* a registry, not a scope: nothing steers a run unless the
project's revision names it. Stock entries sync at boot, keeping
institution-authored copies. *Appears:* B16, B17, D.

### Adoption revision
The exact way of working a project is on, written down as an immutable
receipt: every block at its version and position, every workflow at its
library version, the packs, the additions and exclusions. *Mental model:*
a lockfile with a history. *Lifecycle:* a new revision on every save
(B12), on adopt (B13), or when an uplift change lands; every run records
the revision it read and a child run inherits its parent's. **Prepare**
shows what a move would change before it is made; a **condition** (a
contradiction, drift, a pack named but not held) is what a person must
resolve. *Appears:* B11–B13, the rules under B Publishing the rules, B20, D.

### Drift
A project whose current revision no longer matches what its receipt says
it should hold, found by the forge's snapshot before a release touches
the library; quarantined, never promoted. *Appears:* B11, D.

### Policy
A rule about workflows, in plain words anyone can follow: a scan before any
deploy, a scanner must be bound, nobody may raise the scan ceiling.
Checked when a workflow is saved and again at merge (the current-rules
check). Carried by packs, adopted by projects. *Appears:* B1, the intro to B The project's way of working,
B17, B18.

### Conformance
Whether a project meets the ways of working it adopted. A **review**
judges every project's `main` read-only and is a saved record; a
project's **standing** is one sentence plus its checks; its **worklist**
is what a person would have to do. *Appears:* B20, C16.

### Finding
One issue a check or a scanner reported, tracked until fixed, excepted or
suppressed. At a change's gate a finding is **introduced**, **preexisting**
(already on `main`, reported not blamed) or **undecided** (a boolean check
failing on both trees, handed to the reviewer). An **exception** accepts a
conformance finding with a scope, a justification, an expiry and a second
approver; a **suppression** accepts a scanner finding with a reason and an
author, exportable as OpenVEX. *Appears:* the rules under C Changes, B21–B22, C17.

### Remediation
An ordinary change opened from a finding, carrying the check's expectation
as its acceptance criteria, landing through the ordinary gates. At fleet
scale a **worker pool** works a queue of them one change per project at a
time; the queue can be paused, resumed, retried or skipped and survives a
restart. *Appears:* B21, B23.

## Integrations

### Integration
A connection between a product and an outside service, by **capability**
(error reporting, customer feedback, a registry, a service kind): made
through a **connector** the instance offers or directly with the
provider's own key. The connection details (**contributions**) reach the
app's environment; **wiring in** is the change that makes the code read
them. *Lifecycle:* attached, wired in, swapped (attach another connector
for the same capability; the slot has room for one), detached (contributions
stop, nothing deprovisioned), retired (the code removed and reviewed),
and the connector offboarded from the instance only when nothing
references it. B Services calls all of this a *service*, because the Services page does. *Appears:* A2,
B26–A8.

### Service kind
A declared kind of thing a product can plug into: its capability, the
fields a connection needs, what it contributes. Defined as YAML by an
administrator; appears as attachable. *Appears:* B26, A8.

### Model slot and profile
A **slot** names a purpose (fast, review, implement); the instance maps
slots to real models per **profile** (how demanding a project's work is);
every receipt names the cell that won. *Appears:* B3, A5.

### Scanner
A tool integration that judges source or an image at a scan point;
bound at the instance, run by every landing. *Appears:* C13, C17, A2.

## The record

### Evidence
What a step leaves behind for a gate to read: output, verdicts, logs,
the steering text and its hash, the model and usage. An **artifact** is a
file a run attaches (a report, an SBOM, a release record). *Rules:* a
check without evidence is not a pass; gates read evidence, not intentions.
*Appears:* the intro to C Changes, C5, the rules under C Changes, C22.

### Receipt
The durable answer to "what exactly happened and under what": a run's
origin and revision, a release's commit and digest, an authorization
decision's capability and grant, a deployment's generation. *Mental
model:* the till receipt; kept, never edited. *Appears:* B9, A13, A10,
C22, D.

### Ledger, audit log, trace
The **ledger** is every run's steps, evidence and events; the **audit
log** is state changes made outside runs (who did what, when); a **trace**
is one run's complete record. *Appears:* A10, C22.

### Usage
Tokens and cost, accounted to each project by day. *Appears:* C23, D.

### Complaint
A report, from a person or an agent, that the forge itself got in the
way; filed to a box that people read and answer. *Appears:* C24.
