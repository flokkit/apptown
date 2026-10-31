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
  Unread memory or CPU figures leave capacity coverage incomplete without adding
  a disk condition. The disk cache lasts five minutes. Disk evidence is stale after seven
  minutes: the cache lifetime plus the one-minute observer interval and a
  one-minute scheduling margin.
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

The read path uses local records and cached owner observations. It queues bounded
background work on cold or expired reads and returns promptly. Disk, worker,
capacity and protection jobs coalesce unfinished requests, limit refresh requests
to once a minute and retain last successful evidence separately from later
failed attempts. A deployment host awaiting its first disk check contributes
unknown coverage without an Attention item. Failed checks and stale readings
raise conditions that name the host; an unstarted check does not. Inventory reuses its existing refresh job and invalidation
boundary. Deployment runtime uses its existing background observation owner.

Owner evidence older than two minutes is stale, except deployment-host disk
evidence, which has the seven-minute limit above. Stricter owner limits still
apply. Inner disk and native-backup cache times survive wrapper refreshes.
Target configuration and deployment identity changes reject late answers for
older subjects. A fresh successful observation clears only the conditions it
actually reassesses.

The shared web query polls every thirty seconds while visible, stops when signed
out and uses the existing identity reset and cancellation rules. Last answers
remain readable under refresh failure with an out-of-date mark. Unknown source
coverage remains visible beside the count, including when the condition list is
empty. Attention has no incident state, acknowledgement or automatic cleanup.
