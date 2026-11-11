# Deployment target support

The implemented target kinds are `docker-host`, `portainer`, `do-app`,
`fly-machines` ([Fly Machines](#fly-machines)) and `s3-bucket`
([bucket websites](#bucket-websites)). Deployment owns target
records, offers, bindings and lifecycle. Drivers own rendering, provider calls
and observation.

## Target lifecycle

A target can be ready, unreachable or retired. A successful check can restore an
unreachable target; it cannot revive a retired one. Offering a target requires a
ready state. Operations also apply their caller and project eligibility rules.

Retiring is `POST /api/v1/targets/<name>/retire` with `{"confirm":"<name>"}`.
A body without the matching name, including no body at all, is refused with
that sentence. Retirement is refused while a standing deployment is bound to
the target; its offers go with it. An unused connector is removed only when
no target or repository history references its immutable connection identity.
Referenced connections remain, and explicit deletion refuses those references.
A retired target keeps its name with its history, and nothing revives it:
attaching another target under that name is refused with a 409 that says the
target was retired and a new name is needed.

Retired targets read `stopped` on their target pages. They count as neither
failing, unknown nor working in the Targets list and raise no target
conditions in Attention.

Attaching a Docker host, Portainer target or bucket website creates a new record;
concurrent attachments under one name cannot replace the winner's configuration.
The attachment and check receipts commit only with that new record. An unavailable
name lookup stops attachment before provider probing.

Attaching a DigitalOcean account creates its connector and target, initial
connector version and receipts in one transaction. Either existing name refuses
the operation, including an unchanged or managed connector and a retired target.
Use connector editing and its configuration sync to refresh an existing account;
attachment never overwrites it. Account and registry probes precede admission,
and a dry run writes neither record.

A target's record holds two kinds of fact. Its configuration comes from where
the target is declared: AppTown itself for the built-in host, the
cloud-account connector for an App Platform target (region, registry, sizes),
AppTown configuration for the intranet's Portainer. Its settings are what an
administrator states with `PATCH /api/v1/targets/<name>`: the domain, the
evals and rehearsals marks, persistent volumes, the needs mode and
the copy runner. AppTown records the targets it derives from configuration
at every start, and an App Platform target again whenever its connector is
saved (`Targets.Sync`). That sync supplies a setting's first value when it
creates the target; after that the setting is the administrator's and no sync
changes it: a domain the connector or configuration names is used only when
the target is created. New deployment addresses use the target's domain setting as their initial
configuration. An admitted deployment retains its own address snapshot, including
routing hosts, certificate domain and allowed origins. Later target-domain changes
do not retarget existing deployments; release plans show the selected deployment's
address. A new address requires a configured target domain. A sync that changes a configuration fact moves the
settings revision and records a `target.refreshed` receipt, with before and
after, to AppTown at a start or to the person who saved the connector; a
sync that changes nothing writes nothing. The built-in host takes no edits,
so all of its record is AppTown's own. A sync, a settings edit and a retirement each
take the targets table lock before the target's row, so overlapping ones
queue rather than deadlock; an edit started before a sync that changed the
record is refused as moved. A sync changes only the facts its source owns, so
it never drops a setting someone saved on the target (an App Platform target's
copy runner, for one).

Docker scope is resolved for the selected target. Remote host routing, credentials
and daemon reachability must come from that target's configuration rather than
an implicit global Docker client.

## Attached Docker connections and readings

Every attached Docker command, including inventory and deployment inspection,
uses the target's shared SSH master. At most three commands run per target so
commands holding several connections leave room for each other to finish.
The master is checked once before each command. Its Docker HTTP connections each acquire
one of ten session slots, OpenSSH's default `MaxSessions`. Waiting is cancellable;
a slot is released only after its `docker system dial-stdio` process closes.
A private local HTTP proxy closes upstream connections after each response so
idle sessions do not prevent remaining containers' stats streams from starting.
Long streams count against the same limit. There is no direct SSH fallback.

Capacity reads all running containers with one `docker stats --no-stream` call.
A container absent from the answer has unknown use under its tenant, with its
name in the reason. A failed refresh retains the previous value and successful
reading time, marked stale with the latest failure reason; each cycle retries it.
A successful read clears that reason. Attached hosts report container memory use,
Docker's memory total and its storage filesystem. Whole-host memory use and free
memory are unmeasured; container use alone cannot establish either.

## Portainer connection identity

A Portainer target references one integration by immutable connection UUID and
one verified destination: canonical API origin and base path, numeric endpoint
ID, and observed Docker daemon ID. Multiple targets and connections are allowed.
A display environment name does not select a different endpoint after association.
Changing that selection requires attaching another target.

Direct Docker endpoints and Docker agent endpoints establish this scope when
the observed daemon is standalone. Docker Swarm membership is refused.
Legacy async-edge environments cannot establish this standalone Docker scope.
They remain unavailable for provider effects, including explicit teardown without
a final copy; that choice does not substitute for verified provider identity.

Attachment observes the selected standalone endpoint before committing the
connection, target and audit receipt together. The configured intranet target
is recorded from configuration at each start; when its record has no scope, the
start observes it the same way through the `portainer-deploy` connection: the
endpoint its environment names, the daemon behind it and each existing
deployment's stack. Scope, connection and stack associations commit together
with a receipt to AppTown itself. Once a scope is recorded, no start observes
again. The association records current evidence, never historical creation or
deletion entitlement, and does not clear restored-instance holds. When the
observation fails (Portainer unreachable, a recorded stack missing, retained
resources unresolved), the start logs why and leaves the target without a
scope; releases and provider operations on it refuse until a later start
observes it. Any other Portainer target without a scope refuses provider
operations; attach a new target instead.

Ordinary provider operations resolve one credential snapshot, compare its API
base locally before authenticated requests, then verify endpoint and daemon
identity using that snapshot. A changed destination refuses; key rotation at the
same verified destination is allowed. Each cached Docker proxy request repeats
these checks. Queued release requests and retained-resource evidence preserve the
selected connection and scope. Requests without that evidence must be reissued.

New stacks carry the deployment UUID in `x-apptown-deployment`. Update and removal
read current stack metadata and a usable Compose file, matching endpoint, stack
ID, stack name and deployment marker. An explicitly recorded legacy association
supports valid unmarked Compose; missing or malformed proof refuses. Deletion
also requires the existing creation receipt. These checks establish continuity
at a configured provider, not authenticity against a malicious provider.
Portainer API requests refuse redirects so the API key stays at the configured
endpoint. Configure the final Portainer URL; even a redirect within the same
origin is refused. This rule applies to API requests, not response forwarding
through the Portainer Docker proxy.

## Driver differences

Docker runs the prepared application and local needs. Portainer applies the
rendered stack. DigitalOcean App Platform renders its provider spec and can fulfil
supported managed needs. A driver must reject unsupported contract features;
the existence of a type in the common contract does not prove every driver
implements it identically.

Recovery resumes observation of the admitted provider identity. Display observations
are separate from both deployment settlement and health enforcement. Kubernetes,
Cloud Run and other providers are not target kinds.

## What each target supports

In 1.0 the Docker host is the supported target. Portainer, App Platform,
Fly Machines and the bucket website are Experimental: they work as this table says against
their providers' APIs, but no qualification has released a real app to them
yet. The add-a-target page says so on each of them
(`stock/docs/guides/compatibility.md`, "Stock content in 1.0").

One table for the five target kinds. "Refused" means refused when the release
is asked for, before anything is prepared or any provider is called, in the
sentence the release page shows. The table is the rows of
`TestEachTargetRefusesWhatItCannotRunBeforeAnyEffect`
(`internal/apptown/deployment/target_matrix_test.go`), exactly: it drives the
rows the admission checks decide through those checks for each kind's stock
capabilities (`StockCapabilities`), and fails if a cell here differs.

| Contract or act | `docker-host` | `portainer` | `do-app` | `fly-machines` | `s3-bucket` | `aws-ecs` |
| --- | --- | --- | --- | --- | --- | --- |
| Several parts, one image | yes | yes | yes | yes | refused: serves only a static-only app | refused: single-part apps |
| Parts or init steps with different images | yes | yes | refused: App Platform runs one image per repository | yes | refused: static-only | refused: single-part apps |
| Init steps | yes | yes | yes, as pre-deploy jobs | yes, as one-off Machines before any part's | refused: static-only | yes, as one-off tasks before the service changes |
| Postgres | yes, a local emulator | yes, a local emulator | yes, a managed database | yes, a stock container on its own volume | refused: static-only | yes, a dedicated RDS instance |
| Object storage (s3) | yes, a local store: its bucket made before the app starts, a key for it alone | yes, a local store, as on a Docker host | yes, a Spaces bucket of its own and a key for it alone | refused: cannot run as a need container | refused: static-only | refused: not offered yet |
| Another need with a local emulator (redis) | yes, a local emulator | yes, a local emulator | refused: cannot provide it as a managed service | refused: cannot run as a need container | refused: static-only | refused: not offered yet |
| A data directory | yes, a named volume | yes, a named volume | refused: no persistent volumes | yes, a named volume; stop then start on release | refused: static-only | refused: no data directories |
| A static site given browser values by an integration | yes, as /_apptown/env.js | yes, as /_apptown/env.js | refused: cannot deliver browser values yet | refused: cannot deliver browser values yet | yes, published as /_apptown/env.js | refused: cannot deliver browser values yet |
| Secrets | each part's environment | each part's environment | each component's environment | the app's secrets, the same for every Machine; parts or init steps given different ones are refused before any effect | none: a site has no runtime | SSM SecureString values, immutable per generation and consumer |
| Declared resources | exact: each part's container gets its memory (swap included) and cpus | exact: each part's service gets mem_limit, memswap_limit and cpus | rounded up to the cheapest current instance size that covers each part; one no size covers is refused before any effect | rounded up to the smallest shared-CPU Machine that covers each part; one none covers is refused before any effect | none: a site has no runtime | rounded up to the smallest Fargate CPU and memory pair |
| A need's declared resources | exact on the need's service; a standing need takes a new size in place, or is remade on its volume when a limit is lifted | exact: the need's service gets mem_limit, memswap_limit and cpus | refused: the managed Postgres is made at the integration's database size | rounded up to a shared-CPU Machine; the standing need is updated in place on its volume | refused: static-only | refused: fixed dedicated RDS class |
| A part or need larger than the host | refused before any effect, previews included; the sum of limits may exceed the host | not checked: Portainer's endpoint is read for the capacity view only | rounds to a size, or is refused | rounds to a Machine size, or is refused | none: a site has no runtime | rounds to a Fargate size, or is refused |
| Capacity shown | fixed: memory and CPUs it has, the limits Docker enforces, whole-host memory and CPU use, the containers' use, and the disk holding Docker's data (its folder, when it was read, and whether new releases are refused) | fixed: as a Docker host, read through the endpoint's Docker proxy | elastic: each part's size and listed price, and an estimated monthly cost | elastic: each part's Machine size; prices are not read, Fly lists none AppTown can ask | none: a site has no runtime | elastic: each part’s Fargate size; cost not read |
| A host short of disk | a release or preview is refused before anything is pulled or started while Docker's data folder has less than 5% free (at least 1 GiB, at most 20 GiB) or cannot be read, read when it starts; what runs is not touched | as a Docker host: a release is refused before any stack is sent while the endpoint's Docker data folder has less than 5% free (at least 1 GiB, at most 20 GiB) or cannot be read, read through Portainer when it starts; what runs is not touched | not applicable: the platform's disks | not applicable: the platform's disks | not applicable | not applicable: the platform’s disks |
| Container logs | every container AppTown starts from this release on logs through `json-file`, rotated at 10 MiB, three files kept; one started earlier keeps its own until it is made again | the endpoint daemon's default; not set by AppTown in 1.0 | the platform's | the platform's | none: a site has no runtime | CloudWatch log group per deployment, retained 14 days |
| Limit events | out-of-memory kills and exits of every standing deployment's parts and needs, from the daemon's events | not read | not reported to AppTown | not reported to AppTown | none: a site has no runtime | stopped task reason, including out of memory |
| Releases after a landing | each follows-main deployment requests the landing’s bundle | follows-main or manual release of a digest bundle | as App Platform | as App Platform | as App Platform | as App Platform |
| Readiness witnessed | the container AppTown started; a static site's release identity | the stack was accepted; health unknown | the platform's deployment is active (its own probe); AppTown asks the front door only for a declared HTTP readiness path or a static site's release identity | each new Machine is started and passes its checks; AppTown asks the front door for a declared HTTP readiness path and a static site's release identity | the published files answer this release's identity | every running task is on the recorded revision and the target is healthy; the declared front-door path or static identity answers |
| An earlier release again | yes, its bundle by digest; data is not rolled back | yes; data is not rolled back | yes; data is not rolled back | yes, its images copied again by digest; data is not rolled back | yes | yes, its images copied by digest; data is not rolled back |
| Take-down | what AppTown recorded creating; its data volumes are deleted after a complete final backup, refused without one; what it did not create is named and kept | the stack AppTown recorded, then the volumes its deploys made after a complete final backup, refused without one unless a person chooses to keep the data; a volume it did not make is named and kept | the app and managed resources AppTown recorded; a need's bucket is deleted with its objects after a checked final copy or when its data is synthetic, otherwise kept and named, and its key deleted | the recorded Fly app and volumes after a complete final set; without a store, refused unless a person chooses to keep the app and volumes with all Machines stopped | the bucket AppTown recorded | the recorded service and resources; dedicated RDS and its network kept without a final checked set |
| Volume backups, restored in place | yes, each data volume and need | yes, each data volume and need, through Portainer's Docker proxy (an edge environment in async mode is refused) | no volumes | yes, in the backup store: declared need exports and stop-copy-start volume tars; database restore in place and files into a new volume | no data | no volumes |
| Managed database copies | not applicable | not applicable | yes, with a copy runner and backup store; restored into a new place, never in place | not applicable: Postgres is a container on a volume | not applicable | yes, one-off ECS tasks into the backup store; every database dumped or shown empty, with rdsadmin explicitly provider-owned |

What owns each guarantee through its real operation (ordering before effects,
witnessed outcomes):

- Static-only bucket: `TestABucketTargetServesOnlyAStaticOnlyApp`.
- One image per App Platform repository, refused before the provider is
  called: `TestAMultipartBundleOfDistinctImagesIsRefusedBeforeTheAppPlatformApp`.
- Needs App Platform cannot manage, and a data directory:
  `TestTargetRefusesContract`; refused at the release request, before it is
  accepted: `TestRequestReleaseRefusesUnreadableOrIncompatibleContract`, which
  also owns the browser-values refusal.
- Landings without local Docker: `TestNoLocalDockerRefusesOtherTargetsBeforeAnyBuild`.
- Volume backups on Docker hosts and in Portainer environments: `TestTargetHoldsBackups`; managed copies
  never restored in place: `TestAStoreCopyIsNeverRestoredInPlace`.
- Readiness: each driver's observation tests; App Platform asks the front
  door only when the contract says what to ask:
  `TestAppPlatformAsksTheFrontDoorOnlyWhenTheContractSaysWhat`. Portainer's
  "health unknown" is the driver's stated limit, not a witness.
- Take-down on a Docker host: final backup, then deletion:
  `TestTakeDownBacksUpWhatTheDeploymentHoldsThenDeletesIt`.

## What a target holds: the installation's mark

AppTown owns what carries its installation's mark, not a cloud account or a
host. The mark is the installation's id (`<dataDir>/instance-id`), the same id
the built-in database's container and volume carry. Each provider has one
carrier:

| Provider | Carrier | Given |
| --- | --- | --- |
| Docker host | label `apptown.instance` on containers, deployment volumes and networks | at creation |
| Portainer | stack variable `APPTOWN_INSTANCE` | at every create and update |
| App Platform app | app-wide spec variable `APPTOWN_INSTANCE` (apps take no tags) | at every release |
| Managed database cluster | DigitalOcean tag `apptown-instance:<id>` | at creation |
| Fly Machine | metadata `apptown_instance` (with `apptown_deployment` and `apptown_generation`) | at creation; needs keep their identity during updates |
| Fly app | marked when any of its Machines carries the mark (Fly apps take no tags) | follows from its Machines |
| Bucket website bucket | private object `_apptown/instance` holding the id (Spaces has no bucket tags) | at every release |

An inventory reader returns what the provider lists and marks anything without
this installation's mark as reported only to confirm a record. The comparison
is the same for every kind: a record matches a listed resource whether or not
it carries the mark, so a recorded resource is never hidden or called missing
for lacking it. Only a marked resource nothing records is "unrecorded" (not
accounted for), and only a marked one can be removed from the infrastructure
page. Another installation sharing the account or host, and anything a person
or script made (such as the database AppTown itself runs on), is not listed.

Resources made before the mark keep matching their records. At each start,
AppTown gives the mark in place to what deployment records name where the
provider allows it: managed database clusters get the tag and bucket-website
buckets the private object. The pass is idempotent, and one that marks anything
records an audit event, `deployment.resources-marked`, with the count marked in
that pass. Docker labels cannot be added to an existing object, and a
Portainer stack's variables or an App Platform spec change only with a
redeploy, so those gain the mark at their next release. Until then, once their
record goes, they are not reported as leftovers. The DigitalOcean registry is
one per account and is never reported as unrecorded; Spaces keys confirm
records only, except a left maker key, which shows by its name.

A target kind without an inventory reader reports what it holds as unknown:
the page says "This kind of target doesn't report what it holds" beside the
records, not an error, and the inventory job and attention skip it.

## Background infrastructure readings

Each active Docker target has one background reader loop, started with the
daemon and cancelled when its target retires. Unreachable targets keep their
loop; the built-in target has none when local Docker is disabled. Between laps
the loop resolves current target settings. It reads host, containers, volumes
and networks, and recorded images sequentially, then waits twenty seconds.
Only this loop stores the host reading. Capacity, detail, protection,
preview, runtime and health readers collect independently. A failed section
retains its last successful value and time; a section never read stays unknown. A missing-resource claim is set only by a collection
where all four inventory sections succeed, against records read in that same
collection. A record made later is never compared against that collection.

Before a Docker effect starts, its owner increments the host's act counter,
even if the effect subsequently fails, and increments it again when the effect
ends, so a lap that ran during the effect cannot leave the reading current
after it. A lap captures the counter before I/O.
The storing write advances its complete-lap counter only for a complete lap
whose counter still matches. Pages compare that counter with the current host
counter, including acts since the last store. The whole reading remains marked
as taken before the latest change until a complete lap begun after it succeeds;
section timestamps cannot clear the mark. While it is marked, the last list and
the missing-resource claims its own collection witnessed are kept and shown as
taken before the latest change; a record the list lacks is not called missing
unless that collection said so. Acts do not request an extra host reading. Foreground reads
run independently and never store host evidence. Attached hosts allow three
foreground commands alongside the loop's single command, under the shared
ten-session cap.

Durable jobs check other providers' inventory, every target's capacity and the
controller/worker filesystems every five minutes, even with no readers. Startup
queues that pass and starts the Docker reader loops. After an act finishes,
AppTown also queues capacity and every standing deployment's detail and protection
checks on that target. Non-Docker inventory is invalidated and queued at that
point; Docker inventory remains with its sole reader loop. Infrastructure pages and
Attention show recorded evidence and its age; a page never contacts a provider
or host or requests a check. The Docker Infrastructure row shows how long ago
it was read and how long it took. Attention warns when a Docker host's reading
is more than five minutes old; never read counts as old. It includes the age
and latest command error, if any. Two consecutive laps whose first command,
`docker info`, fails confirm that the host is not reachable. Attention lists
one host item with the first failure time, error and number of deployments that
cannot be checked, linking to the target's connection check. Unknown runtime,
data and resource conditions on that host fold into it. Confirmed conditions
remain listed. The first complete lap clears the host item.
Other providers invalidate inventory and
request a check after an act. Other inventory jobs have thirty seconds.
The scheduling pass attempts all targets and instance disks even when an
admission fails, and reports errors together.
Shared Docker and Portainer placement verification is part of the background
inventory check. Pages use that recorded verification; destructive removal still
verifies fresh provider identity and placement before acting.

Deployment stores target capacity and inventory in its database tables through
named generated queries. Data protection stores protection and provider-backup
readings; maintenance stores controller and worker filesystem readings. A new
service reads the same evidence after restart, bound to the same target settings
or deployment identity. There is no process-memory reading fallback.

Every Docker host, built-in or attached, reads whole-host memory and CPU through
its standing disk reader container, with `--cgroupns host`, no network and a
read-only data mount. A Docker host needs cgroup v2 for these readings. If the
reader's cgroup root has `memory.current`, the host is a slice of a larger
machine: memory use is `memory.current - inactive_file` from `memory.stat`, and
CPU use is the change in `cpu.stat`'s `usage_usec` divided by elapsed time,
expressed in cores. A numeric `memory.max` that differs from Docker's memory
total makes memory use unknown with the differing totals in the reason.

Otherwise, the cgroup v2 root describes a whole machine. Memory use is
`MemTotal - MemAvailable` from `/proc/meminfo`. CPU use is the busy fraction
between two `/proc/stat` samples, multiplied by online CPUs; idle and iowait
are excluded, and guest time is not counted twice. Only this route checks the
reader's memory and CPU totals against Docker's totals. A mismatch names the
possibility that `/proc` describes a virtualised host or VM.

Both routes sample CPU about one second apart, with the wait cancelled by the
caller's context. Totals always come from Docker. An unreadable or malformed
counter stays unknown with its reason; cgroup v1 readings are unknown with a
reason naming the cgroup v2 requirement. Container sums never replace whole-host
use.

Each successful capacity figure retains its own time. Machine totals (memory,
CPU count and disk size) stay valid for seven days. Live use, free disk, tenant
use, inventory and protection are fresh for fifteen minutes, then shown as
“last seen” with their age; after six hours they are unknown. Configured target
kind and region are target records, rather than probe results; changing target
settings invalidates prior observations. A failed check keeps successful values
and their times and records the failed attempt and its safe reason separately.
A partial successful check cannot renew an unread sibling figure.

For other providers, Attention raises an item for an old live reading after fifteen minutes only
when the latest check failed. Age alone raises no item until the six-hour unknown
limit. Known pressure and missing-resource findings retain their original
evidence. Other providers store the missing claims witnessed by their successful inventory
check. Docker stores missing-resource claims only when all four inventory sections
succeed in the same collection, against the records read in that collection.
A record made after a collection is never compared against it. Inventory age
uses the oldest inventory section time. Missing-resource claims stay, marked
old, while the reading predates an act. A first pending check contributes unknown
coverage; Docker host readings also raise the age warning described above.
Docker disk admission still reads the filesystem at admission; retained display
readings never authorize work.

## Docker target inventory

Docker inventory container detail uses the literal inspected state, such as
`running` or `exited`.

A Docker target's inventory compares recorded resource claims with its stored
host snapshot: AppTown-named containers, application (`apptown-data-`) and need
(`apptown-need-`) volumes, AppTown networks (named by the deployment's key), and recorded image references.
Images are not listed: a listing leaves out an image with no tag, such as one
pulled by digest alone. Docker is asked about each recorded reference, and the
reference is present only when Docker answers by that exact name (a
repository/digest reference by its digest; a tag cannot confirm a digest),
absent when Docker says it has no such image, and unknown otherwise, which
fails the read rather than calling the image missing. Unclaimed host images
are not listed as orphan cleanup candidates. The page adds no image/network removal
authority.

Named containers describe one logical slot, even when the deployment's teardown
ledger retains several replaced incarnation IDs. Presentation coalesces repeated
claims and retains distinct hosting attribution; the immutable ledger and removal
checks are unchanged. Presence by name is not evidence of the serving generation:
runtime revision observations check that separately. Different provider IDs and
image digests remain distinct.

A failed container, volume, network or image listing fails the observation rather
than publishing a successful partial list. The page shows the previous complete
answer with its timestamp and refresh error, or unassessed records if no answer
exists. A missing warning is a complete observation's absence of a recorded
resource, never an ignored listing failure.

## Static sites and readiness

A static site's release image is the pinned file server with the site's files
and the command that serves them (`docker.StaticSiteDockerfile`). Every
builder writes it: a Docker host's own build, the
worker's release build (one Dockerfile per static image, since the command
names its port) and the intranet's. No driver adds a command, so every target
runs the image the same way, and no target can serve the file server's default
page while the release reads released.

A static site may declare its build (`static: { build, target, output }`). Its
files come from that build instead of the tree: `docker build --output`
exports the target stage, every folder on the way to `output` and
everything in it must be a plain folder or file (`docker.ConfineStaticExport`;
a link is refused wherever it points), and that folder is packed into the
same self-serving image (`docker.PackStaticBuild`). A Docker host does this
for a change's preview (`StartAppContainer`) and a standing deployment
(`PrepareImages`), and the worker's
release task inside the task, with the same check in its shell. The export
runs on the selected daemon's own builder, for its own platform: every
build of a project's source on a daemon (`docker.Scope.Build`, which
`build:` parts use too) names that daemon's builder (`default` for a
`--host` daemon, else the context's own) and refuses one whose driver is not
`docker`, so neither a builder alias with a saved remote builder nor an
inherited `BUILDX_BUILDER` or `BUILDX_CONFIG` takes it elsewhere. Buildx
is required beside the docker CLI AppTown runs (locally, with `--host` for
an attached daemon): without it the build is refused, saying to install it.
The worker's task keeps its
private `apptown` builder. Recorded release images are
never rebuilt.

The file server answers two AppTown paths. `/_apptown/env.js` gives the page its
browser values (`APPTOWN_BROWSER_ENV`), and answers `{}` when the variable is
unset or empty, whatever the target set (App Platform sets none). `/_apptown/release` answers
`APPTOWN_RELEASE`, the public static part's release identity,
`<part>@<revision>` (`appcontract.Contract.PublicRelease`). AppTown sets it
from the selected source, never from a component's environment, on every
target: App Platform's renderer, a Docker host's boot (after the contract's
and the secrets' values, so neither can stand in for it) and Portainer's
stack (one key, set over any value the contract or a secret gives it). App Platform also sets it on its other static parts.

What each target's readiness witnesses:

| Kind | Witnessed |
| --- | --- |
| `docker-host` | the container AppTown started, by its labels and port. For a static site served from a revision (a landing, a promotion, a recovery), that `/_apptown/release` answers this release's identity, as on App Platform. For a preview of a worktree (no revision), and for a restore rehearsal, that `/_apptown/env.js` answers (only AppTown's file server configuration serves it) |
| `do-app` | the platform's deployment for this generation is active (the platform's own probe); AppTown asks the front door only when the contract declares an HTTP readiness path, and, when the public part is a static site, that `/_apptown/release` answers this release's identity. Otherwise the release log says which release answers is not witnessed |
| `portainer` | the stack was accepted; health is unknown. `/_apptown/release` answers, but nothing asks it |
| `fly-machines` | each new Machine is started and passes its checks (Fly's own); AppTown asks the front door for a declared HTTP readiness path and, when the public part is a static site, that `/_apptown/release` answers this release's identity. Neither declared leaves health unknown, and the release log says so |

A bundle cut before 3 October 2026 holds only the site's files, without
that configuration. Released again, it fails its readiness check on a Docker
host and on App Platform, because the file server's stock page does not
answer `/_apptown/release`; on Portainer nothing checks it.

## Where a release comes from

A landing whose project has a container registry, built on a worker or on its
Docker host, makes a release bundle by digest, and every target runs it from
the project's registry ([release bundles](deployment.md#release-bundles)):

| Kind | Receives a landing | Receives a promotion | Builds |
| --- | --- | --- | --- |
| `docker-host` (built-in or attached over ssh) | yes, `main` | yes | never; pulls the digests and confirms `RepoDigests` |
| `do-app` | no | yes | never; AppTown copies each digest into DOCR |
| `portainer` | no | yes | never; the stack names each digest |
| `fly-machines` | no | yes | never; AppTown copies each digest into the app's repository in Fly's registry |

Release previews boot a bundle by digest where change previews boot,
without a platform check: they rely on one platform per AppTown (the
worker's). Every release target's platform must be the bundle's: a Docker
host's daemon answers it, App Platform is `linux/amd64`, Portainer's
endpoint answers it; unknown or different is refused before any effect. In the no-local-Docker mode the
built-in host is unavailable and digest bundles may go to any of the
others.

App Platform keeps the copies in the account's one DOCR registry, one
repository per project, named after its slug. The repository is the
AppTown's: the first copy creates it and AppTown records it per project and
connector. A repository of that name AppTown did
not create refuses the release, at preflight and before any copy. When the project's last deployment on that
connector, on any App Platform target, is taken down, every image in the
repository is deleted. AppTown starts no garbage collection: while one runs
DOCR takes no pushes, so one started by a take-down would fail the next
release. The space, and the empty
repository's name (DOCR has no call to delete a repository and lists it
until a collection runs), are freed by a collection an operator runs or
DigitalOcean schedules. An image someone else pushed into AppTown's
repository is deleted with the rest: don't push to AppTown's
repositories. Attaching the target and checking it read the registry's plan
(subscription tier) and how many of its repositories are used from
DigitalOcean, and record the reading in the `target.checked` receipt; the
target's page shows the newest reading with when it was read ("basic plan,
5 of 5 repositories · plan full · read 3h ago"), or unknown with why when the
registry could not be read. A release refuses before any effect (no
database, no copy, no app) when the registry has no repository for the
project and no room for one: "registry plan full (N of N repositories, …);
upgrade the registry plan, or take a deployment down". A taken-down
project's repository is freed only once the registry's garbage collection
has run. A room that cannot be read is logged as unknown and the release
goes on; the copy itself is the authority. This is the driver's preflight
(`deploy.Preflighter`), run before a release fulfils its needs.

A push refused 401 or 403 at DOCR while DigitalOcean reports a garbage
collection running keeps the refusal and adds that a collection was running:
DOCR refuses pushes while one runs, so release again once it has ended, and
a refusal that persists after it ends is the credential's. Which of the two
caused it is not known, so neither is asserted. Who refused is read from
the refusing answer's host (DOCR's registry or its token service), not from
the step the copy was in: the copy reads the source's layers while it
pushes. A refusal by the release's own registry is said as it is.

On an AppTown without a worker, Docker hosts build from the committed tree.
With a container registry the images are then published, as above. With
none, the bundle records image IDs and is reused only on the target its
images live on.

## Object storage on App Platform

The `s3` need on a `do-app` target is a DigitalOcean Spaces bucket per
deployment, made by
the driver's need module (`internal/deploy/digitalocean/storage.go`):

- A release mints a full-access *maker* key named
  `apptown-maker-<slug>-<deployment>` with the account's API token (deleting any
  of that name first), makes the bucket through the S3 API with it, mints the
  deployment's `readwrite` key for that bucket, and deletes the maker key; one
  it could not delete is said in the release record. The bucket and key are
  on the deployment's owned list (`deployment_owned`, kinds `bucket` and
  `spaces-key`), not `need_instances`; the key's secret is in the built-in
  secret store under `need:deployment:<id>`. A later release reuses them.
- The app spec gives the contract's variables, the secret as a `SECRET` env.
- AppTown copies the bucket's objects like a database: from the
  copy runner, as the deployment's own key (`BucketConnection`), into the
  backup store, and checks a copy by filling a new store of the kind on the
  copy runner. A start from a copy, a restored copy and a restore test fill
  the new deployment's own bucket from it; a set with no copy of the
  bucket refuses a start that needs one.
- Take-down with a final copy deletes the app first, keeping the bucket
  and its key (`TearDownStorage` is its own step), copies and checks, then
  deletes the key and the bucket with its objects. Without a final copy
  (none can be taken, and a person chose to go without), it deletes the key
  and keeps the bucket as a retained resource; synthetic data goes with it.
  A restored copy's bucket and key go when it does.
- The account inventory lists Spaces keys (a left maker key reads
  unrecorded); buckets are not listed there, and a recorded key names its
  bucket. Its bucket carries no installation mark, so a bucket-website
  target on the same store does not report it.

## Fly Machines

A `fly-machines` target is one Fly.io org and region
(`internal/deploy/fly`). Each standing deployment is a Fly app of its own,
with one Machine per part.

- **Attaching** names the AppTown secret holding the token (`tokenFrom`,
  `FLY_API_TOKEN` by default; it is read from AppTown's own secrets at each
  use, never stored) and a region. The org is the token's own answer
  (`GET /v1/tokens/current`): a token that reaches one org uses it, one
  that reaches several must name it (`org`), and a token for apps alone is
  refused because it cannot create an app. The region must be an available
  Machines region. A dry run asks Fly and records nothing.
  Attaching records the connector (capability `cloud-account`, provider
  `fly`) and the target together. The token is an org token: it can manage
  every app in the org, including ones AppTown did not make, and attaching
  says so.
- **Names and the mark.** A deployment's app is `at-<i>-<key>`: `<i>` is
  the first 8 hex digits of the SHA-256 of the installation's id and
  `<key>` is its key (`deploy.HostKey`). Fly app names are global across
  Fly, so two installations never reach for one name; a name another app
  holds is refused, never adopted. The app is made with Fly's idempotency
  key (the deployment's id), so asking again returns the same app, and it
  is recorded on the deployment's owned list (`fly-app`) the moment Fly
  answers. Every Machine carries `apptown_instance`, `apptown_deployment`
  and `apptown_generation` in its metadata.
- **A release** copies each part's and init step's image by digest into
  `registry.fly.io/<app>` (a Machine pulls only from Fly's registry or a
  public one), sets the app's secrets in one change (unsetting any the
  release no longer gives), runs each init step as a one-off Machine and
  reads its exit from Fly's events (a non-zero exit fails the release
  before any part's Machine is made), gives the app a shared IPv4 and an
  IPv6 address once, then makes a new Machine for each part and waits for
  it to start and pass its checks. Only after the front door is witnessed
  are the previous generation's Machines destroyed. A part with a data
  directory stops and is destroyed before its replacement mounts the same
  volume; a failed replacement restores the previous config on that volume.
  Parts without data keep serving until the new generation is ready.
  A standing need is updated in place with its complete config and existing
  mount, preserving its private address for the previous release.
- **What it refuses before any effect**: a need other than Postgres,
  a liveness probe (Fly's checks route traffic
  and do not restart a Machine), a readiness probe with a threshold or an
  exact status (Fly counts none and passes any 2xx),
  parts or init steps given different secrets (Fly gives every Machine of
  an app all of its secrets), a release not published by digest, a
  restored copy (no gate), a static site given browser values, and a
  deployment whose audience is not public (its `fly.dev` address answers
  anyone and AppTown puts no sign-in in front of it; the refusal says to
  set the deployment's audience to public in its Settings, or make a
  deployment on another target).
- **Data folders and Postgres.** A data directory uses a recorded volume
  `at_<key>_<part>`. A need's volume is `at_<key>_n_<need>`. Volumes start at
  1 GB and grow automatically to 10 GB. Postgres runs the catalogue's stock
  image and start script as a standing need Machine, with the deployment's
  generated password. Its cluster is in `pgdata` under the volume mount (`PGDATA`), because a Fly volume's root holds `lost+found` and `initdb` refuses a non-empty folder. Its catalogue health command runs through Machines
  exec as `/bin/sh -c`; only an explicit successful exit allows init and
  parts to proceed. Its private address is `<id>.vm.<app>.internal`, used
  in both `PGHOST` and `DATABASE_URL`. An image version change is refused
  before effects because its data files belong to the standing image.
- **Backups and restore.** Sets require an attached backup store. One-off
  Machines export Postgres live as the app's user, and copy a data folder
  by stopping and destroying its part, mounting the volume to tar it, then
  recreating the previous part even after an upload failure. Jobs receive
  one presigned object URL, never the store's key pair. AppTown reads the
  uploaded object with its own key and records its size and SHA-256.
  Restore takes a safety set and stops every app writer, restores Postgres
  in place and runs its declared verification, and fills a new volume for
  a data folder. The old volume stays until the new part passes readiness.
  Copies are unencrypted. Retention and rehearsals use the shared set rules.
- **Runtime**: restart is offered; start and stop are not, as on App
  Platform. Fly restarts a part's Machine whenever its process exits,
  without limit (restart policy `always`), as every other target restarts
  a part. Fly's proxy never starts a Machine (autostart off) and never
  stops one (autostop off).
- **Observation** reads the app's Machines and judges the current
  generation against the parts it recorded before making any (a release
  interrupted part way is finished only when every one of them stands
  ready): a part with no Machine is failed; all started and passing is
  active and healthy; one starting is rolling; one
  started with a failing check is unhealthy; one stopped or failed is
  failed (nothing on this target asks for a stop). A Machine on a host Fly
  cannot reach is unknown, never stopped or healthy, and a read Fly did
  not answer is an error, not an empty app.
- **Inventory** lists the org's apps and Machines. An app is this
  installation's only when one of its Machines carries the mark; any other
  app (another installation's, one a person made) is reported only to
  confirm a record and never offered for removal.
- **Take-down** deletes the app the records name, with its Machines,
  addresses, secrets and images, after checking that the app under that
  name still has the recorded id and that every Machine in it carries this
  deployment's mark. A complete final set covers its volumes before deletion.
  Without a store or a copyable need, or when the deployment's newest set
  is a final set that failed, take-down is refused unless the person
  chooses to keep data; a choice made while a final copy could be taken is
  refused, to be confirmed again. A deployment never released records no
  app and holds no data, so it is removed with no final set. The app and volumes then stay as retained resources, with part
  Machines removed and need Machines stopped. Unrecorded volumes or a
  Machine without the deployment's mark stop the take-down.

## Bucket websites

An `s3-bucket` target is an S3-compatible store (DigitalOcean Spaces first)
that serves static-only apps as files: one public static part, no init
steps, no needs, released by digest. Anything else is refused with a
sentence.

- **Attaching** names the store's endpoint, region, website address
  template (`https://{bucket}.{region}-static.digitaloceanspaces.com`) and
  the secrets holding its key pair (`accessKeyFrom`, `secretKeyFrom`). The
  store must accept the key before the target is saved. AppTown creates
  buckets, so the key is a full-access key: it can read, write and
  configure every bucket in its team. Attaching and checking say so, and
  recommend a key (and, where other data must be out of reach, a team) for
  AppTown alone.
- **Each standing deployment owns one bucket**, created by AppTown on its
  first release and recorded on the deployment (`Deployments.RecordOwned`)
  before anything else touches it. A taken name is refused, never adopted.
  Don't store anything else in a deployment's bucket: taking the deployment
  down deletes it.
- **A release** reads the site's files out of the release image
  (`oci.ReadTree`, `/srv`, no links, at most 200 MB), adds `/_apptown/env.js`
  with the project's browser values and `/_apptown/release` with the
  publication identity `<part>@<revision>+<set>` (`staticsite`), and uploads
  assets, then HTML and `env.js`, then the marker, each public-read and
  each with the static site's Cache-Control (below). It is released when the website address, asked past caches,
  answers exactly that identity. Not answering within the witness budget
  leaves the attempt uncertain; recovery and **Refresh status** only witness,
  never upload. An upload refused before the marker fails the release as an
  incomplete publication: the address still answers the previous one.
  After a witnessed release, objects in neither this nor the previous
  publication are deleted, so an open tab can still load the previous
  release's hashed files. Every release also writes the private
  `_apptown/instance` object, the installation's mark: the website does not
  serve it, a site's own file of that name is dropped, and the clean-up keeps
  it.
- **Inventory** lists the store's buckets; a bucket whose mark names this
  installation and that no deployment records is not accounted for, and any
  other bucket is reported only to confirm a record. A mark that cannot be
  read fails the read; it is never taken as absent.
- **What visitors can see**: entry files change one at a time, so a page
  can briefly get new HTML with old browser values; a file whose name is
  the same in both releases can change under an open page; a page the
  release removed stays reachable until the next clean-up.
- **Take-down** deletes every object, then the bucket, and witnesses that
  the address answers 404. No answer, a server error or any other status
  is not evidence that the site stopped. If any step fails, or the 404 is
  not seen, the deployment stays, and the error says the site may still be
  public at its address and what is left.
- A marker answer whose body could not be read whole is no answer: it
  neither witnesses a release nor reads healthy.
- Spaces serves bucket websites without its CDN, and custom domains are not
  supported yet.
- **Runtime**: a bucket website runs no containers, so its deployment page
  shows no CPU, memory or container columns and no Start, Stop or Restart.
  Its part reads `serving` when its address answers this release's marker.

## Static site caching

Every static site is served with one Cache-Control rule
(`appcontract.StaticCacheControl`), the same on a bucket website (set on each
upload) and on the Docker-host file server (`docker.StaticServerCommand`,
Caddy `path_regexp` with the same expression):

- `/_apptown/env.js` and `/_apptown/release`: `no-store`.
- A content-hashed asset: `public, max-age=31536000, immutable`. A name is
  content-hashed when it is `<stem>.<hash>.<ext>` or `<stem>-<hash>.<ext>`,
  `<ext>` is a build output type (scripts, styles, wasm, fonts, images, source
  maps), and `<hash>` is 8 characters of `[A-Za-z0-9_-]` with a digit (Vite,
  Rollup, Create React App) or 16 or more lowercase hex characters (webpack).
- Every other file, HTML included: `no-cache`. The browser revalidates it
  with its ETag, so a new release's page never runs with the last release's
  unhashed styles or script.

The rule is conservative: a hash it misses only costs a revalidation. A
hand-made name that matches (`app-version2.js`) and changes in place keeps its
old content in browsers for a year; give such files another name. A CDN in
front of a static site would also need the release to purge the changed
unhashed paths. Neither target has one today, so no purge is made.

## Implementation references

- [internal/apptown/deployment/target.go](../../internal/apptown/deployment/target.go)
- [internal/apptown/deployment/target_state.go](../../internal/apptown/deployment/target_state.go)
- [internal/apptown/deployment/target_providers.go](../../internal/apptown/deployment/target_providers.go)
- [internal/deploy/driver.go](../../internal/deploy/driver.go)
- [internal/deploy/fly](../../internal/deploy/fly)

Release images are published to the instance hub and copied to target stores.
Docker and Portainer confirm retained digests locally; App Platform and Fly
confirm the destination digest and platform before reading the hub. Each store
keeps the running release, last three successful releases, seven days of
successful releases and releases named by kept backups. Bucket websites hold
files and need the hub for rollback. See the
[image guide](../../stock/docs/guides/images.md) for retention and garbage collection.

## AWS ECS

`aws-ecs` (AWS ECS on Fargate) is behind the AWS ECS beta switch. It runs one
part on Linux/AMD64, with an optional dedicated PostgreSQL RDS instance. Multiple
parts, shared databases, object storage, data directories, gated placements and
signed-in audiences are refused before provider effects. See the
[AWS setup guide](../../stock/docs/guides/aws-target-setup.md) for prerequisites,
explicit attach fields and the matching CloudFormation and Terraform samples.

The administrator supplies an existing cluster, subnets, HTTPS listener and task
execution role. AppTown checks the expected account before other AWS calls and
uses only an explicit access-key pair resolved from instance secrets. AppTown
manages its tagged, recorded deployment resources; it does not manage the estate,
DNS or stack outputs. The target reports its load balancer and DNS reading.

Releases copy digest images to a deployment repository, write generation-specific
SSM parameters and run init tasks before changing the service. Running task
revisions and target health confirm convergence. A failed rollout returns to the
recorded previous revision, or stops the service on its first release. Costs that
cannot be read are unknown.

Data jobs export into scratch files before uploading into AppTown's backup store.
A set includes its verified database manifest, a dump of every database with user
tables and a repeated empty check for every other database. Only `rdsadmin` is
provider-owned. Failed listing, counting, dumping, upload or drill keeps data.
The existing copy verification command is unchanged. RDS snapshots are the
provider's own snapshots, separate from AppTown backup sets.

A final take-down stops writers and removes the listener rule before taking and
checking the set. Only complete coverage allows deletion of the RDS instance.
An explicit take-down without a final copy retains the instance, subnet group and
database security group, with continuing costs. Deleting an instance also deletes
its automatic snapshots. Fargate's default 20 GB ephemeral storage bounds exports.
