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

## Conditions and evidence

- Controller, worker and deployment-host disks retain each filesystem's own
  observation time. Disk policy belongs to `diskspace`; controller storage is
  advisory and does not protect controller admission. Worker disk refusal blocks
  new tasks. Docker-host refusal blocks new releases and previews. Portainer readings are
  advisory and do not protect release admission. A deployment-host disk condition
  uses only the disk’s known free and total figures and its own reading time.
  Failed memory or CPU checks leave capacity coverage unknown without adding
  a disk condition. Optional figures that the host cannot supply are absent,
  including whole-host memory use on attached Docker hosts. Coverage includes
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
  reported as uncertain intent, not proof of an unintentional outage.
- Data protection supplies copy cadence and disposable-data classification.
  Required production copies can be missing, stale or unassessed. Untested
  recovery is advisory; recorded failed and interrupted/unknown recovery results
  remain distinct. A successful copy alone does not establish application
  recovery.

Conditions link to existing infrastructure or deployment evidence and actions.
Those actions enforce their own current permissions. Global items contain safe
summaries, not provider errors, credentials, logs or storage paths.

Integration readings come from the integration owner. Kinds with automatic
checks are collected by jobs every five minutes. Attention never calls a
provider. An unused connection without configuration is not set up; partial
configuration or a required missing connection is failed. Only integrations
with a background check whose latest result failed contribute a row; no
integration contributes unknown coverage. Kinds without a background check
read `no automatic check` in muted style. A manual Test connection result keeps
its last state and age, marked old after fifteen minutes, without expiring into
unknown. Failed connection rows name directly affected apps.

Conformance supplies one query of stored verdicts, exact confirmations, active
exceptions, currently adopted checks and recorded main for the authorized
project selection. Attention polling does not read Git or open a transaction
per project. Verdicts from before recorded main changed cannot establish current
problems. Only confirmed findings and broken checks contribute a project row;
unrun and unassessed projects contribute nothing. The row names its inner count,
and counts as one item in the header, Attention summary and Home.

The page groups named links under Hosts and deployments, Backups, Integrations
and App checks. Unknown coverage names its resource, reason and next step.

## Observation lifecycle

The read path uses local records and recorded owner observations. Inventory,
capacity, controller/worker disks and protection observations are collected by
durable jobs every five minutes, including while nobody reads the pages.
Startup queues the first pass. Readers never request these checks or contact
providers. After AppTown changes a provider, it queues that target's inventory
check immediately and marks the old reading as predating the act. An overlapping
pre-act check cannot renew that evidence or establish newly missing resources.
Invalidation alone does not create a resources-not-known item.

Owners persist successful readings and failed attempts in their databases.
Restarting AppTown does not create a pending first check. Failed checks retain
successful evidence and its original time. Live evidence older than fifteen
minutes shows its age; an Attention item is raised only when the latest check
failed, naming that check and its safe reason. An old successful reading without
a failed check raises nothing until it becomes unknown after six hours. Machine
size expires after seven days. Unknown remains distinct from an old known value
and from a successful empty answer. Pending first checks contribute unknown
coverage without an Attention item.

Inventory retains witnessed missing claims with the stored observation, filtered
against the currently recorded subject identity. Native backup evidence retains
its own reading time; a newer protection wrapper cannot renew it. A failed
held-data assessment fails its collection job and retains the diagnostic in the
authorized Jobs view. Attention reads owner evidence directly, with no process
memory fallback when an owner read fails.

Worker availability still requests bounded background observation from reads;
age alone creates no worker-unavailable item. Deployment runtime uses its existing
background observation owner. Target configuration and deployment identity changes
reject late answers for older subjects. A fresh successful observation clears
only the conditions it actually reassesses.

The shared web query polls every thirty seconds while visible, stops when signed
out and uses the existing identity reset and cancellation rules. Last answers
remain readable under refresh failure with an out-of-date mark. Unknown source
coverage remains visible beside the count, including when the condition list is
empty. Attention has no incident state, acknowledgement or automatic cleanup.
