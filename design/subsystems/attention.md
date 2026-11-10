# Operational attention

Attention collects current operational conditions in the resources the caller
can read. Its page, the authenticated top-bar count and Home's Operations
summary use the same response from `GET /api/v1/attention`. The count includes warnings, advisories, failed integrations and confirmed
app-check problem rows. Each named row counts once, even when it contains several findings. The header says `<n> to fix · <m> not known`, omits zero
parts and says `All quiet` only when both counts are zero. Unknown items do
not also count as confirmed problems. Approvals, blocked Changes and the caller's failed
operations keep their existing Home groups.

The `attention` owner resolves current project and instance grants before
selecting sources. Infrastructure and backup sources use deployment read grants;
integrations use the same instance project-read grant as their list; app checks
use project conformance-read grants. Denied sources are omitted. Each read checks authority again. Project access grants no
instance-wide disk, worker or inventory visibility. An authority lookup failure
fails closed; an unavailable source is reported separately from the conditions
it can still describe. Neither a failed read nor incomplete observation coverage
means all clear.

App-check conditions use the evaluator classification supplied by Conformance.
Undecided AI evaluations stay on project Conformance, with their reasons and
actions. Evidence-related uncertainty, provider session failures and caps generate no
Attention or Home item. AppTown setup and execution failures remain visible
with the reason and next action, as do deterministic operational errors and
source-read failures. Stored classifications identify these failures rather
than inferring them from their text.

## Conditions and evidence

- Controller, worker and deployment-host disks retain each filesystem's own
  observation time. Disk policy belongs to `diskspace`; controller storage is
  advisory and does not protect controller admission. Worker disk refusal blocks
  new tasks. Docker-host refusal blocks new releases and previews. Portainer readings are
  advisory and do not protect release admission. A deployment-host disk condition
  uses only the disk’s known free and total figures and its own reading time.
  Failed memory or CPU checks leave capacity coverage unknown without adding
  a disk condition. Whole-host memory and CPU readings on Docker hosts are
  unknown with a reason when the reader cannot supply them. Coverage includes
  a safe reason naming the failed check; provider errors remain on the owning
  resource's authorized detail view. Background disk checks run every five minutes. A failed check retains successful values. Live evidence is fresh for fifteen
  minutes and remains known for six hours; machine totals remain valid for seven
  days. Each figure keeps its own successful timestamp.
- Worker availability uses the authenticated worker profile protocol. A missing
  agent image does not establish that the worker is unreachable. An unassessable
  response retains earlier unavailable evidence as stale.
- Inventory compares current recorded resource claims with a complete provider
  result. Missing-resource conditions use immutable ownership where available,
  target configuration and deployment generation. Unknown or stale inventories
  cannot establish new missing conditions. An unchanged subject keeps its prior
  warning during failed refreshes, even when a sibling changes.
- Deployment conditions distinguish the latest desired attempt from the observed
  serving generation. A failed update can leave the previous generation running.
  Crash-loop receipts are selected for the exact deployment and observed
  generation before limiting the ledger query. A fresh running observation
  supersedes an old hold. An absent runtime without recorded stop intent is
  reported as uncertain intent, not proof of an unintentional outage; the
  absence itself is a known, current reading, so the source stays known. A
  deployment with nothing applied yet raises no runtime condition and does not
  make its source unknown.
- Data protection supplies copy cadence and disposable-data classification.
  Required production copies can be missing, stale or unassessed. Untested
  recovery is advisory; recorded failed and interrupted/unknown recovery results
  remain distinct. A successful copy alone does not establish application
  recovery.

Conditions link to existing infrastructure or deployment evidence and actions.
Those actions enforce their own current permissions. Global items contain safe
summaries, not provider errors, credentials, logs or storage paths.

Integration readings come from the integration owner. Kinds with automatic
checks are collected by jobs every hour and requested when a connection is saved.
Attention never calls a provider. An unused connection without configuration is not set up; partial
configuration or a required missing connection is failed. Only integrations
with a background check whose latest result failed contribute a row. Evidence
past its six-hour shelf life contributes a not-known row; pending first checks
do not. Kinds without a meaningful check read `no automatic check` and never
count. Test connection runs the same check immediately and follows the same
freshness rule. Failed connection rows name directly affected apps.

Conformance supplies one query of stored verdicts, exact confirmations, active
exceptions, currently adopted checks and recorded main for the authorized
project selection. Attention polling does not read Git or open a transaction
per project. Verdicts from before recorded main changed cannot establish current
problems. Confirmed findings and checks that run and fail contribute a problem row.
Checks that cannot give an answer contribute a separate not-known row, with
the recorded reasons. Neither row includes checks not run yet. The row names its inner count,
and counts as one item in the header, Attention summary and Home.

A Docker host whose `docker info` command fails on two consecutive reader laps
appears once in Hosts and deployments as not reachable, with the first failure
time, error and number of deployments that cannot be checked. It links to the
target page and its connection check, and lights that group's top-bar indicator.
While it is down, unknown runtime, data and resource conditions fold into the
host item. An All checks row folds only when every subject in it is on a down
host; rows that also cover healthy hosts remain visible. Host reachability is
read once per host in each Attention response. Confirmed failed releases, crash holds and check problems remain
listed. A complete reader lap clears the outage. Project-only readers keep their
deployment coverage because they cannot read the instance host item.

The page groups named links under Hosts and deployments, Backups, Integrations
and App checks. Unknown coverage names its resource, reason and next step.
Each source carries how many of its items are confirmed and how many are not
known. All checks shows a source with confirmed items as “N to fix”, linked to
its group, and one with unknown items as unknown; neither is listed as working.

## Observation lifecycle

The read path uses local records and recorded owner observations. Inventory,
capacity, deployment detail and controller/worker disks are collected by durable
jobs every five minutes, including while nobody reads the pages. Protection and
connection health are collected hourly. Settled backup attempts request fresh
protection readings, whether they succeed or fail.
Startup queues the first pass. Readers never request these checks or contact
providers. After an act, AppTown queues deployment detail and protection for
every standing deployment on the target, and the target's capacity. Provider
inventory is also queued and invalidated for non-Docker targets; Docker host
inventory remains the host reader loop's responsibility. An overlapping
pre-act check cannot renew that evidence or establish newly missing resources.
Invalidation alone does not create a resources-not-known item.

Owners persist successful readings and failed attempts in their databases.
Restarting AppTown does not create a pending first check. Failed checks retain
successful evidence and its original time. Live evidence older than fifteen
minutes shows its age; protection and connection health stay current for
seventy-five minutes to allow an hourly check plus scheduling slack. An Attention
item is raised only when the latest check failed, naming that check and its safe
reason. An old successful reading without
a failed check raises nothing until it becomes unknown after six hours. Machine
size expires after seven days. Unknown remains distinct from an old known value
and from a successful empty answer. Pending first checks contribute unknown
coverage without an Attention item. Backup coverage names deployments by their
public display name, or their name when no display name is set. A first check
says backups have not been checked yet; a source waiting for its first check has
no reading time and names the deployment it waits for. Failed attempts retain their safe reason
and last attempt time; the page distinguishes that time from a successful
reading. Each failed reading records its own cause and next step; the
background schedule is named only where trying again can help.

Inventory retains witnessed missing claims with the stored observation, filtered
against the currently recorded subject identity. Native backup evidence is stale after fifteen minutes in both deployment detail
and protection coverage. It retains its own reading time; a newer protection wrapper cannot renew it. A failed
held-data assessment fails its collection job and retains the diagnostic in the
authorized Jobs view. Attention reads owner evidence directly, with no process
memory fallback when an owner read fails.

Worker availability still requests bounded background observation from reads;
age alone creates no worker-unavailable item. Deployment runtime uses its existing
background observation owner. A deployment's last act is the latest of its
successful attempt, its confirmed start, stop or restart, and its other acts (a
health restart, a crash-loop stop, a restore, a failed or uncertain attempt).
For two minutes after that act, a runtime reading begun before it, or one taken
while a start, stop or restart is under way, raises no runtime item: not a
missing status, not the stopped advisory, not an unhealthy runtime. This
includes first releases and rebuilds. Metadata edits do not restart this
interval. Attempt failures and crash holds remain visible during it. Once applied, a
pending first runtime reading cannot wait indefinitely. Protection remains pending
until a deployment has a successful attempt. An applied deployment with an empty
release revision is still assessed. Readings waiting after a change contribute no
Attention items; retained payloads from the old identity are for display only.
Target configuration and deployment identity changes
reject late answers for older subjects. A fresh successful observation clears
only the conditions it actually reassesses.

The shared web query polls every thirty seconds while visible, stops when signed
out and uses the existing identity reset and cancellation rules. Last answers
remain readable under refresh failure with an out-of-date mark. Unknown source
coverage remains visible beside the count, including when the condition list is
empty. Attention has no incident state, acknowledgement or automatic cleanup.
