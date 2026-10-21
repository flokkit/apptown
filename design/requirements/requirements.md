# What AppTown does for people: the supported workflows

This is the business-requirements view of the product — every journey a
person can complete, who does it, how, and what counts as done — written
in plain English and kept true to the current code and guides. The
design documents under `design/` say why; the guides under
`stock/docs/guides` say how in more detail; this says what.
Scenarios people will try that AppTown cannot carry yet are listed in
`not-yet.md`, in the same form, so the two documents together say what is
supported and what is not. The nouns the use cases lean on are defined in
`concepts.md`, including where two words mean one thing.
The end-to-end journeys that cross roles — a change from wish to shipped,
adopting a new way of working, bringing a service in — are the **key use
cases**, in `key-use-cases.md`; they are composed from the identifiers
here.

## How this document is written

Each journey is a **use case** in the casual form: who wants it, what
they get, the steps as the product presents them, and what has to be true
at the end. Where a rule matters more than a flow, it is stated as a
requirement in the form *when X, AppTown does Y*. Each has a home in
the product so it can be checked against the screen.

The journeys are grouped by **who is responsible**, because that is the
divide that matters in the product:

- **Part A — Running AppTown.** The instance's administrators: the
  people who connect the outside world (clouds, scanners, model
  providers, source control), decide who exists and what they may do, and
  keep AppTown's own machinery honest.
- **Part B — Owning and governing software.** Owners of a project and the
  standards people: what a project is, where it runs, what rules it is
  held to, what it plugs into, and who may promote it.
- **Part C — Contributing to software.** The people (and their agents)
  who make changes: ask, work, preview, accept, land, and read what
  happened.
- **Part D — AppTown's own duties.** What happens by itself.

Identifiers (A2, B13, C7) are permanent: a journey keeps its identifier
when the document is reorganised, so other documents may cite it.

The actors:

- **instance administrator** — runs AppTown: attaches clouds,
  connectors, scanners and people. Where a role is narrower the text says
  *integration administrator* or *identity administrator*;
- **owner** — a project's owner (`project:configure` and above);
- **standards administrator** — publishes ways of working and approves
  weakening them;
- **release manager** — may promote to protected deployments;
- **maintainer** — someone who works on a project and accepts changes;
- **the helper** — AppTown's own coding agent, acting for a person;
- **a coding agent** — a person's own agent (Claude Code and the like)
  connected over MCP, acting within what that person allowed;
- **AppTown** — what happens by itself.

Four words the journeys lean on, defined once because they are easy to
confuse. A **workflow** is a written procedure: stages of steps with
gates, stored and versioned in the instance library. A **lane** is a
workflow a project offers for working a change — quick-change, implement,
fix-bug and the rest — chosen by the person who starts the change; the
AppTown's own workflows (landing, shipping, wiring a service in or out) are
system workflows, never lanes. A **way of working** is everything a
project has adopted: its packs, its guidance blocks, the lanes it offers,
its policies. A **pack** is a published way of working that a project can
be put on. So a pack contains workflows, a lane is a workflow in a
particular role, and a way of working is the whole set a project holds.

# Part A — Running AppTown

For instance administrators. Everything here is defined once for the
whole instance and consumed by every project.

## Setting up and connecting the outside world

**A1 Set AppTown up.** A fresh AppTown must not be silently inert. On
first run an administrator supplies the one unavoidable thing, a model
provider key; the agent image is built from nothing; an open-auth
listener on a non-loopback address is flagged loudly until auth is
configured. Done when: the dashboard shows the setup complete and a
project can be created. *Setup page; Dashboard banner.*

**A2 Connect a system integration.** An administrator connects an
outside service from the catalog: a model provider, a package registry or
mirror, source control (GitLab), a secrets store (the built-in store or
Infisical), an executor or deploy host (Docker), error reporting
(GlitchTip), customer feedback, a cloud account (A6), or a scanner (A4).
Each binding is tested before it is saved, has a health check and a
version, and holds the names of secrets, never their values. Bindings the
AppTown configured itself, from flags or stock manifests, are marked
managed: they can be viewed, tested and cloned, not edited, and re-sync at
boot. *Integrations → Connect.*

**A3 Test, clone and disconnect a system integration.** An administrator
tests a binding at any time; clones a managed one under a new name to
customise it; disconnects a binding they made. AppTown refuses to
disconnect a connector while any project still references it (detach and
retire those first, B29 and B30), and refuses for managed bindings, which
would come back at boot. *Integrations → binding → Test, Clone, Remove.*

**A4 Attach a scanner.** An administrator attaches a scanning tool as a
binding: the image to run, its command, where it writes its report, the
report's format (SARIF, OSV or CycloneDX) and what kind of thing it finds
(vulnerabilities, secrets, an SBOM). The stock scanners — osv-scanner,
gitleaks, syft, trivy — are seeded at boot. Every landing's scan points
run every attached scanner as its own check; no workflow is edited. The
ceiling on what a scan may be told to ignore is set by policy (B18).
*Integrations → Scanners.*

**A5 Bind model slots.** Map the names workflows use (fast, review,
implement) to real models, per profile. *Integrations → Model slots.*

**A6 Add a deployment target.** An instance administrator adds a place
AppTown can put software, through one door for every kind: a Docker
host reached over SSH (a droplet, a box under the stairs), a Portainer
environment, or a DigitalOcean account whose App Platform is the target
(AWS and Google Cloud to follow; Kubernetes designed, not built). The
door is a short wizard: the kind, then the connection with credentials
named as secrets AppTown already holds, then the offer and the facts
AppTown cannot read off the target (the apps domain, whether it keeps
disks, whether previews may bind, how needs are met). The target must
answer before anything is recorded, and the words it answered with are
kept. A cloud account's connector is made with its target and goes when
the last target on it is retired. *Infrastructure → Add a deployment
target.*

**A7 Offer a target.** An administrator offers a target to a group, or
to everyone, with caps (deployments per project, audiences, data
classes). Built-in targets are offered by construction. *Infrastructure →
target → Offers; Integrations → Deploy targets.*

**A15 See the infrastructure.** An administrator sees every deployment
target as a card: its kind, status, how many deployments stand on it,
what it holds (containers and data volumes, stacks, apps and databases),
when AppTown last asked it, its backups and who it is offered to. One
click in is the target's inventory, where each resource is **recorded**
(made by AppTown for a deployment), **retained** (left behind by a
torn-down deployment), **unrecorded** (present at the target, absent from
AppTown's records) or **missing** (recorded, gone from the target). An
administrator may remove an unrecorded resource from the page; AppTown
never removes what it recorded. AppTown's own database and the need
services it runs count as recorded. *Infrastructure; target → Inventory.*

**A16 Check, edit and retire a target.** From a target's page an
administrator asks it to answer now, changes the facts the owner states
(domain, disks, previews, needs), and retires it. Retiring is refused
for the built-in host and while any deployment is bound to the target,
which are named; offers are withdrawn with it. Every check, edit and
retirement is a row in the target's history. *Infrastructure → target.*

**A8 Define a service kind.** An integration administrator declares a
new kind of thing an app can plug into (its capability, the fields a
connection needs, what it contributes to the app's environment) as YAML,
and it appears as attachable. *Integrations → Service kinds;
`design/subsystems/plugins.md`.*

## People and authority

**A9 Manage people and groups.** An identity administrator creates
users and groups; a group owns projects. *People.*

**A10 Grant authority.** Stock roles at instance, group and project
scope (viewer, contributor, maintainer, approver, owner; the
administrators; release manager; auditor); every decision is receipted
with the capability and the grant that allowed it. *People → grants;
audit log.*

**A11 Say where administrators may sign in.** An instance administrator
lists the networks from which administrative sign-in is accepted.
*People → Where administrators may sign in.*

**Rules.** Approvals belong to people. An instrument (a console, a step
agent) is bounded by its one project and its table of allowed acts.

## AppTown's own jobs, customised

**A12 Turn a pack off.** An administrator stops offering a pack in the
wizard; it stays in the store and projects on it are untouched. Stock
packs can be turned off, never deleted. *Ways of working → Packs.*

**A13 Stand your own landing in for AppTown's.** The instance binds a
custom system workflow (one marked `system: true`) in place of a stock
one — landing runs `our-land` — for every project at once, with a
receipt. *Ways of working → Workflows → System.*

**A14 Resolve an integrity event.** When the project's repository is
found dirty (only AppTown writes there, so dirt means something went
around it), the project page shows the diff and an administrator keeps or
throws it away; the decision is recorded. *Project → banner.*

# Part B — Owning and governing software

For a project's owners, the standards administrators who publish the
rules, and the release managers who promote. This is where a project is
defined, placed, governed and connected.

## Projects

**B1 Start a project.** An owner or project creator picks a name and a
**pack** (a way of working: Go service, Django, Rails, browser game…) in
the wizard. Done when: the repository exists with the pack's guidance,
lanes and policies applied, the platform pack on it, and a first change
ready to open. *Projects → New.*

**B2 Bring an existing repository in.** An owner imports a repository
and picks a pack. Done when: the code is on `main` with a runtime
contract, and the excavate lane can write its requirements. *Projects →
New → import.*

**B3 Configure a project.** An owner sets its name, icon, namespace,
group, members and model profile. *Project → Settings → General,
Members.*

**B4 Keep a copy on GitLab.** At creation, or later from the project,
an owner asks for a GitLab copy; landed branches are pushed there. GitLab
is a mirror, never the gate. *Projects → New → Repository; `apptown`
attach-remote.*

**B5 Delete a project.** An owner deletes a project with no run in
flight; its records go with it. Standing deployments must be torn down
first (B7). *Settings → General → Delete.*

## Deployments and releases

**B6 Create a deployment.** An owner creates a standing deployment
bound to a target the project was offered, with its audience and data
class; AppTown refuses what the offer does not allow and what the
contract cannot run there. *Project → Settings → Deployments.*

**B7 Tear a deployment down.** An owner removes it; what
holds data stays at the provider and is listed as retained, with owner
and intent, until a person marks it released or kept. *Settings →
Deployments → Tear down; Left behind.*

**B8 Release to the intranet.** A maintainer presses Release; AppTown
takes what already landed and puts it on the intranet at the project's
address, recording release number, who, and what shipped. *Project →
Release; release page.*

**B9 Deploy a release bundle to another deployment.** A release manager
(or maintainer, for an unprotected deployment) deploys the latest bundle
to a standing deployment on a cloud target; the release page is the
receipt, with the deploy's log as it is written. *Project → Settings
→ Deployments → Deploy latest.*

**B10 Roll back.** Deploy an earlier bundle to the same deployment.
*Release page → bundles.*

**B31 Back up a deployment's state.** An owner takes a backup of a
standing deployment now: the app's data volume and each need that keeps
data, copied the way its own tool copies it (Postgres by `pg_dump`,
otherwise the volume as a tar), as one set with a manifest. AppTown
also takes one every day and after every release, and keeps the newest
seven per deployment. Only deployments on a Docker host AppTown talks
to are backed up; a Portainer environment or a cloud target keeps its
own. *Deployments → Backups → take now.*

**B32 Restore a deployment from a backup.** An owner restores a set:
AppTown copies what is there now to a safety set first, stops the app,
restores each item, starts it and waits until it answers. The deployment's
name is typed to confirm. *Deployments → Backups → restore.*

**Rules.** A protected deployment (partners, public, or production data)
takes the release grant. A release is refused while another to the same
deployment is on its way. If AppTown restarts mid-deploy it resumes
from what the provider had already named, or fails honestly; it never
adopts a deployment on a guess.

## The project's way of working

A **pack** is a published way of working: guidance blocks, lanes,
policies, a toolchain. Packs have versions with exact contents; a project
is on one or more versions plus blocks of its own.

**B11 See what a project is on.** The owner sees its packs and versions,
its own blocks, any condition (a contradiction, drift, a pack it names
but is not on), and the revision history. *Project → Settings → Way of
working → Guidance.*

**B12 Edit guidance and lanes.** An owner adds, orders, disables or
removes blocks and ticks lanes; each save is a new revision. *Same.*

**B13 Adopt an update.** When a pack has a newer version, the owner
prepares it and sees what would change — blocks brought, moved, dropped;
lanes; the checks the app would newly be held to — then adopts now, or
adopts through an uplift change: an ordinary change that must pass the
new checks and moves the project when it lands. *Same → Updates available.*

**B14 Run a lane on a schedule or from a webhook.** A lane may declare a
cron schedule (the monthly dependency uplift) or accept a webhook; each
fire opens a change attributed to the schedule or the caller. *Workflow
YAML `on:`; `POST /hooks/{project}/{workflow}`.*

**B15 Record a decision.** An owner starts a decision (ADR) as a draft,
proposes and accepts it, supersedes it later; agents read accepted
decisions before they build, and a lane may refuse to start without one.
*Project → Settings → Decisions; decision page.*

## Publishing the rules

**B16 Publish a way of working.** A standards administrator changes stock
guidance or packs (in the repository) or authors institution packs and
blocks; a release cuts new versions and moves no project. *Ways of
working; Packs; Guidance library.*

**B17 Author a workflow.** A standards administrator writes a lane or
clones a stock one, validated on save against the schema and the
policies; a project selects it under its way of working; every run
records the library version it ran. *Ways of working → Workflows.*

**B18 Author a policy.** A standards administrator writes a rule about
workflows (a scan before any deploy; a scanner must be bound; nobody may
raise the scan ceiling) in plain words anyone can follow; packs carry it and
projects adopt it. *Ways of working → Policies.*

**B19 Add a check.** A standards administrator writes a check into a
guidance block: a shell test, a tool run (gofmt, semgrep, a framework's
self-check), a rubric a model judges, or a probe against the running app.
*Guidance library.*

**Rules.** A release never silently changes what a project is held to.
Two blocks that contradict each other cannot both be adopted; prepare
refuses one. Dropping a gating block a selected pack carries takes the
standards authority. Every run records the exact revision it read, and a
child run inherits its parent's.

## Conformance

**B20 Review the fleet.** A standards administrator runs a fleet review;
every project's `main` is judged against what it adopted, read-only. Done
when: each project has a standing and a worklist. *Conformance → Run a
review.*

**B21 Remediate.** From a finding, open one ordinary change per project
carrying the check's own expectation as acceptance; it lands through the
ordinary gates, and the check must pass outright. *Conformance → Fix;
Remediations.*

**B22 Accept a finding on the record.** An exception — accepted risk,
deferred, compensating control — with a scope, a justification and an
expiry, approved by someone other than the requester. *Conformance →
finding → except.*

**B23 Run remediation at fleet scale.** A standards administrator opens
fixes for every finding across the fleet; a worker pool works them one
change per project at a time; the queue can be paused, resumed, retried
or skipped item by item, and survives a restart. *Conformance →
Remediations; Fix all findings.*

## Services and secrets

**B24 Give a project a service.** An owner declares what the app needs
(Postgres, Redis, S3) in its contract; every deployment fulfils it — an
emulator in previews, a managed instance in the cloud. *Project → Settings
→ Services.*

**B25 Give a project its secrets.** An owner declares which secrets the
app reads and where each comes from; values reach the running app, never
the record. *Project → Settings → Secrets.*

**B26 Attach a service to a project.** An owner connects a capability
the app plugs into (error reporting, customer feedback, a registry, a
service kind an administrator defined) through a connector the instance
offers, or directly by pasting the provider's own key. The connection
details reach the app's environment from then on; nothing in the code
changes yet. One integration per capability per project. *Settings →
Services → Connected to; the wizard's Services step.*

**B27 Wire it in.** From the service card, the owner opens the change
that makes the code read the connection, as the connector's steering
prescribes; it is reviewed and lands like any change, and the card shows
*wired in* once it ships. Done when: the app reports through the service
in a real deployment (the boom test hook proves it by hand). *Settings
→ Services → Wire it in; Change ▸ adopt-integration.*

**B28 Swap a provider.** To move a project from one provider of a
capability to another (one error tracker to another), the owner attaches
the new connector — it takes the capability's one slot, so the old
binding is replaced and the contributions switch — and then wires it in;
that one change moves the code and removes the old provider's wiring.
There is no separate swap verb: a migration is a change that swaps the
integration, never a steady state with two providers. *Settings →
Services.*

**B29 Detach a service from a project.** The owner detaches; the
connection details stop reaching the app and the detach is recorded.
Nothing on the provider's side is deleted. *Settings → Services →
Detach.*

**B30 Retire a service from a project.** The system workflow removes the
code that used a detached service and an independent reviewer confirms
nothing references it. *Change ▸ retire-integration.*

**Not yet.** Offboarding is one project at a time: nothing opens the
retire change on every project that used a connector, the way remediation
does for findings. Detaching never deprovisions what a connector created
at the provider; that is a deliberate gap awaiting its own confirmed verb.

# Part C — Contributing to software

For maintainers, contributors and their agents: the people who make
changes and read what happened.

## Changes: the core loop

A **Change** is the unit of work: a branch, a run of a lane, evidence,
and one human acceptance. Nothing reaches `main` except through a Change.

**C1 See a project at a glance.** Anyone who may read it sees what is
running, the latest change, what is waiting on them, and the two big
doors: Change and Release. *Project → Overview.*

**C2 Ask for a change in words.** A maintainer opens the chat, says what
they want, and the helper opens a Change, works it on its own branch,
boots a preview, and reports. Done when: the person can open the preview,
read the report and the diff, and accept or send back. *Project → Chat;
Change page.*

**C3 Run a prepared change from the catalog.** A maintainer picks a
catalogued change (a fix the institution has tested before) and runs it
with its parameters. Done as C2. *Project → Overview → Change ▸
catalog.*

**C4 Choose the lane.** A maintainer starts a specific lane:
`quick-change` (small, one glance), `implement` (a capability, with
self-verification), `fix-bug` (reproduce first, then fix), `plan-execute`
(approve the plan, then judge against it), `product-spec`, `research`,
`excavate`, `dependency-uplift`. Each lane's gates are its own; a lane
is selected per project under its way of working. *Change ▸ detailed.*

**C5 Watch a change while it runs.** The change page shows each step,
its evidence, and a live terminal of what the agent is doing; afterwards
the terminal folds to a summary. *Change page.*

**C6 Preview a change.** Every change with a runnable contract boots a
preview at its own address, with the needs it declares (a database, a
cache) provided. Done when: the preview answers at its address. *Change
page → Open.*

**C7 Accept or send back.** A maintainer accepts, or sends the change
back with words; sending back reworks it with that feedback in the same
agent session. Accepting is the decision: the landing starts by itself.
*Change page.*

**C8 Attach a file or a picture.** A maintainer drops a screenshot or a
file into the chat or onto the change; the helper reads it and it is kept
with the change. *Chat; Change page → attachments.*

**C9 Work a change from a desktop.** A person or their coding agent
checks the change's branch out with a short-lived credential, pushes, and
syncs; AppTown's gates judge the pushed tree. *MCP `checkout_change`,
`sync_change`; guide: connect-a-coding-agent.*

**C10 Abandon a change.** A maintainer abandons; the preview is torn
down, the branch kept for the record. *Change page.*

**C11 Adopt an existing branch as a change.** A maintainer names a branch
already in the repository and AppTown opens a change on it, so work
done elsewhere lands through the same gates. *Settings → General → Adopt
a branch; `apptown import`.*

**C12 Run the app on your own machine.** A developer runs `apptown dev up`
in a checkout; AppTown generates a compose file from the runtime
contract, with the app's needs as local containers. Compose is generated
output, never the contract. *CLI `apptown dev`.*

**Rules.** When a gate fails, the producing step is reworked with the
gate's evidence as feedback, up to three times, then a person is asked.
When a change's claims misrepresent its diff, the reviewer fails it. A
change is judged on what it introduced: a finding that already fails on
`main` is reported, not blamed on the change (§6.4).

## Landing, and being judged fairly

**C13 Land a change.** After acceptance the landing workflow runs: catch up
with `main`, tests, the source scanners, the way of working's checks, the
image scan, one human approval (the acceptance counts for it where the
accepter could approve it, was shown its sentence and the checks raised
nothing new), merge, deploy to the
project's `main` deployment, release record. Done when: `main`
carries the merge and the standing deployment serves it. *Land run on
the change page.*

**C14 Look at an old release.** A maintainer boots the exact bytes of a
past release on the preview tier beside today's version, with fresh
throwaway services and a blank data volume: a look, never a restore.
*Release page → preview this release.*

**C15 Judge a change fairly.** The gate splits findings into introduced,
preexisting (already on `main`, reported not blamed) and undecided (a
boolean check failing on both trees, handed to the reviewer). *Change
page → guidance-conformance step; `apptown check`.*

**C16 See one project's standing.** Anyone who may read it sees the
checks it passes, fails, or could not be judged on, and what a person
would have to do. *Conformance → project.*

**C17 Handle security findings.** A maintainer reads every finding the
scanners raised on the project, suppresses one with a reason and an
author, and exports the suppressions as OpenVEX for anyone downstream.
*Project → Security.*

## Yourself, your agents, and the record

**C18 See where everything runs.** Anyone sees the fleet by tier
(previews, main, intranet, cloud), the departures board of recent moves,
and the infrastructure view of what is at each cloud provider and what it
hosts, checked live and cached. *Deployments.*

**C19 Sign in.** Local accounts, SSO, or the admin token. *Login.*

**C20 Let an agent act for you.** A person connects their own coding
agent over MCP; it reads projects, opens and works changes, and hands off
for approval, never approving, landing or administering. *Docs → connect
a coding agent.*

**C21 Mint an API token.** A person mints a named token for the CLI or a
script; its actions are recorded under their name; they revoke it from
the same list. Connected coding agents' grants are listed and revoked
beside it. *People → API tokens; Connected coding agents; `apptown token`.*

**C22 Read the ledger.** Every run's steps, evidence and events; every
change's diff, report and receipts; the audit log of who did what.
*Change page; run trace; audit.*

**C23 Watch usage.** Tokens and cost by project and by day. *Usage.*

**C24 Complain.** A person or an agent files a complaint when AppTown
gets in the way; the box is read and answered. *Complaints.*

**C25 Read the docs.** Guides, the contract reference, the glossary.
*Docs.*

**C26 Know what the helper can use.** A maintainer or workflow author can
distinguish the environments provided to project chats, change chats and
workflow steps: workspace permissions, development tools, private Docker,
services, credentials and what survives a restart or cleanup. The documentation
states conditional and best-effort capabilities explicitly. Done when: the
person can choose the appropriate working surface and verify a required tool or
service without assuming that every agent has the same environment.
*Docs → What chats and steps can use.* The supported guarantees and proposed
service extension are in [runtime environments](runtime-environments.md).

# Part D — AppTown's own duties

Things AppTown does without being asked, each with a receipt:

- boots previews and standing deployments with their declared needs and
  keeps them healthy;
- snapshots what every project is on before any release can touch the
  library, and quarantines drift;
- recovers interrupted landings, deployments and adoptions at startup,
  never applying anything twice;
- cuts release bundles at landing and backfills them for what already
  ships;
- syncs stock guidance, workflows, packs, policies and integrations at
  boot, keeping institution-authored copies;
- reaps previews of abandoned or shipped changes and containers of
  finished runs;
- backs up every standing deployment on a Docker host it talks to, daily
  and after each release, keeping the newest seven;
- asks every deployment target what it holds and keeps the answer, so
  the Infrastructure page says what is unrecorded or missing;
- rebuilds a standing deployment on a Docker host whose container is gone
  at boot, never a preview and never a cloud or Portainer deployment;
- runs each of these as a **job** (`design/subsystems/jobs.md`): one
  open job per thing, taken over if AppTown restarts mid-way, its
  receipt or its failure listed under Infrastructure as *AppTown's own
  work*;
- accounts every model call to its project.

## Who can drive each journey

Three surfaces reach the product: the web UI, the `apptown` CLI (with a
token minted under a person's name, carrying that person's full
authority), and a person's own coding agent over MCP (acting as the
person who connected it, within a deliberately narrower set of tools that
never approves, lands, releases or administers). The UI reaches every
journey; the other two do not, and the gaps are part of each journey's
definition.

| Journeys | Web UI | `apptown` CLI | Agent over MCP |
|---|---|---|---|
| A Running AppTown: setup, integrations, scanners, targets and infrastructure, people, AppTown's own jobs | all | `integrations`, `token` | none |
| B Projects: start, import, configure, mirror, delete | all | `new`, `import`, `pack` | start a project (with a pack) only |
| B Deployments, backups and releases | all | `release` | none |
| B The project's way of working; publishing the rules; decisions | all | `adopt`, `pack`, `decision`, `invoke` | read the adopted guidance only |
| B Conformance | all | `check`, `findings` | none |
| B Services and secrets | all | `integrations`, `adopt`, `retire` | none |
| C Changes: the core loop | all | all | all except the chat door |
| C Landing and being judged fairly | all | `land`, `approve`, `reject`, `override`, `check` | request landing; run a change's checks |
| C Yourself, your agents, and the record | all | `token`, `trace`, `runs`, `status`, `log`, `diff`, `watch`, `transcript` | read only, which is most of it |
| D AppTown's own duties | receipts visible | receipts visible | receipts visible |

**Rules.** An agent over MCP acts in the person's name and never holds
more than the person does; the tools it is given are the tools an agent
should have, not everything the person may do. The CLI is the person's
own hand: an agent driving it with the person's token can approve and
land, and that is the person's choice, recorded under their name.
Deployments, targets, services and people are administered in the UI only; no
verb or tool creates a deployment, attaches a service or grants a role.

## Standard ways of writing requirements, and which this uses

- **Use cases** (Cockburn): actor, goal, preconditions, main flow,
  extensions, postconditions. Best for journeys with steps and gates,
  which is most of AppTown. This document uses the *casual* form.
- **User stories** (*as a … I want … so that …*): light, good for a
  backlog, weak on rules and flows.
- **Given / When / Then** (Gherkin, BDD): executable acceptance
  scenarios; the right form when a requirement becomes a test.
- **EARS** requirement statements (*when X, the system shall Y*;
  *while*, *where*, *if … then*): precise single sentences; the "Rules"
  lines above are this form in plain words.
- **Jobs to be done**: the situation and the outcome a person is hiring
  the product for; good for positioning, too coarse for gates.
- **BPMN / swim lanes**: process diagrams across roles; useful for the
  landing and the adoption boundary, not for a whole product.
- **Capability maps**: a tree of what the product can do, no flows; the
  section headings here are one.

For AppTown, use cases with rule sentences fit best: the product is a
set of governed journeys, each ending in a receipt, and the rules are
what the gates enforce.
