# Operational attention

Attention collects current operational conditions in the resources the caller
can read. Its page, the authenticated top-bar count and Home's Operations
summary use the same response from `GET /api/v1/attention`. The count includes
warnings and advisories. Approvals, blocked Changes and the caller's failed
operations keep their existing Home groups.

The `attention` owner resolves current project and instance grants before
selecting sources. Each read checks authority again. Project access grants no
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
  resource's authorized detail view. Background disk checks run every five minutes. A failed check makes the
  reading unknown. Successful evidence is stale after twelve minutes: two missed
  checks plus two minutes for queueing and collection.
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

## Observation lifecycle

The read path uses local records and recorded owner observations. Inventory,
capacity, controller/worker disks and protection observations are collected by
durable jobs every five minutes, including while nobody reads the pages.
Startup queues the first pass. Readers never request these checks or contact
providers. After AppTown changes a provider, it queues that target's inventory
check immediately and marks the old reading as predating the act. An overlapping
pre-act check cannot renew that evidence or establish newly missing resources.
Invalidation alone does not create a resources-not-known item.

Failed checks retain the last successful evidence with its own observation time.
A failed check or evidence older than twelve minutes raises an unknown condition;
unknown differs from a successful empty reading. Pending first disk, inventory,
app status and backup checks contribute unknown coverage without an Attention
item. Owners expose attempt and observation times so coverage does not depend on
time since startup. Native backup evidence retains its own reading time. Protection coverage names
the failed data, copy or provider-backup check without exposing provider errors.
A failed reassessment can retain earlier evidence as stale before twelve minutes;
that marks the failed reassessment, not an age-based expiry. A failed held-data
assessment also fails its collection job, retaining the diagnostic in the
authorized Jobs view.

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
