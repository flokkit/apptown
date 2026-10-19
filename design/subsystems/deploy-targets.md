# Deployment target support

The implemented target kinds are `docker-host`, `portainer`, `do-app` and
`s3-bucket` ([bucket websites](#bucket-websites)). Deployment owns target
records, offers, bindings and lifecycle. Drivers own rendering, provider calls
and observation.

## Target lifecycle

A target can be ready, unreachable or retired. A successful check can restore an
unreachable target; it cannot revive a retired one. Offering a target requires a
ready state. Operations also apply their caller and project eligibility rules.

Retiring is `POST /api/v1/targets/<name>/retire` with `{"confirm":"<name>"}`.
A body without the matching name, including no body at all, is refused with
that sentence. Retirement is refused while a standing deployment is bound to
the target; its offers, and a connector no other target uses, go with it.
A retired target keeps its name with its history, and nothing revives it:
attaching another target under that name is refused with a 409 that says the
target was retired and a new name is needed.

A target's record holds two kinds of fact. Its configuration comes from where
the target is declared: the forge itself for the built-in host, the
cloud-account connector for an App Platform target (region, registry, sizes),
forge configuration for the intranet's Portainer. Its settings are what an
administrator states with `PATCH /api/v1/targets/<name>`: the domain, the
evals and rehearsals marks, persistent volumes, previews, the needs mode and
the copy runner. The forge records the targets it derives from configuration
at every start, and an App Platform target again whenever its connector is
saved (`Targets.Sync`). That sync supplies a setting's first value when it
creates the target; after that the setting is the administrator's and no sync
changes it: a domain the connector or configuration names is used only when
the target is created. Everything that uses the domain reads the target's
setting: an intranet release's hosts and address, the release plan's
"it will live at" address, and the origins the forge allows
(`Service.IntranetConfig`), so a release on an intranet target with its domain cleared is refused
until one is set. A sync that changes a configuration fact moves the
settings revision and records a `target.refreshed` receipt, with before and
after, to the forge at a start or to the person who saved the connector; a
sync that changes nothing writes nothing. The built-in host takes no edits,
so all of its record is the forge's own. A sync, a settings edit and a retirement each
take the targets table lock before the target's row, so overlapping ones
queue rather than deadlock; an edit started before a sync that changed the
record is refused as moved. A sync changes only the facts its source owns, so
it never drops a setting someone saved on the target (an App Platform target's
copy runner, for one).

Docker scope is resolved for the selected target. Remote host routing, credentials
and daemon reachability must come from that target's configuration rather than
an implicit global Docker client.

## Driver differences

Docker runs the prepared application and local needs. Portainer applies the
rendered stack. DigitalOcean App Platform renders its provider spec and can fulfil
supported managed needs. A driver must reject unsupported contract features;
the existence of a type in the common contract does not prove every driver
implements it identically.

Recovery resumes observation of the admitted provider identity. Display observations
are separate from both deployment settlement and health enforcement. Kubernetes,
ECS, Cloud Run and other providers are not target kinds.

## What each target supports

In 1.0 the Docker host is the supported target. Portainer, App Platform and
the bucket website are Experimental: they work as this table says against
their providers' APIs, but no qualification has released a real app to them
yet. The add-a-target page says so on each of the three
(`stock/docs/guides/compatibility.md`, "Stock content in 1.0").

One table for the four target kinds. "Refused" means refused when the release
is asked for, before anything is prepared or any provider is called, in the
sentence the release page shows. The table is the rows of
`TestEachTargetRefusesWhatItCannotRunBeforeAnyEffect`
(`internal/forge/deployment/target_matrix_test.go`), exactly: it drives the
rows the admission checks decide through those checks for each kind's stock
capabilities (`StockCapabilities`), and fails if a cell here differs.

| Contract or act | `docker-host` | `portainer` | `do-app` | `s3-bucket` |
| --- | --- | --- | --- | --- |
| Several parts, one image | yes | yes | yes | refused: serves only a static-only app |
| Parts or init steps with different images | yes | yes | refused: App Platform runs one image per repository | refused: static-only |
| Init steps | yes | yes | yes, as pre-deploy jobs | refused: static-only |
| Postgres | yes, a local emulator | yes, a local emulator | yes, a managed database | refused: static-only |
| Object storage (s3) | yes, a local store: its bucket made before the app starts, a key for it alone | yes, a local store, as on a Docker host | yes, a Spaces bucket of its own and a key for it alone | refused: static-only |
| Another need with a local emulator (redis) | yes, a local emulator | yes, a local emulator | refused: cannot provide it as a managed service | refused: static-only |
| A data directory | yes, a named volume | yes, a named volume | refused: no persistent volumes | refused: static-only |
| A static site from an earlier-form bundle | yes, served with the file server's command | yes, as on a Docker host | refused: release again for a self-serving image | yes, its files are published |
| A static site given browser values by an integration | yes, as /_forge/env.js | refused: cannot deliver browser values yet | refused: cannot deliver browser values yet | yes, published as /_forge/env.js |
| Secrets | each part's environment | each part's environment | each component's environment | none: a site has no runtime |
| Declared resources | exact: each part's container gets its memory (swap included) and cpus | exact: each part's service gets mem_limit, memswap_limit and cpus | rounded up to the cheapest current instance size that covers each part; one no size covers is refused before any effect | none: a site has no runtime |
| A need's declared resources | exact on the need's service; a standing need takes a new size in place, or is remade on its volume when a limit is lifted | exact: the need's service gets mem_limit, memswap_limit and cpus | refused: the managed Postgres is made at the integration's database size | refused: static-only |
| A part or need larger than the host | refused before any effect, previews included; the sum of limits may exceed the host | not checked: Portainer's endpoint is read for the capacity view only | rounds to a size, or is refused | none: a site has no runtime |
| Capacity shown | fixed: memory and CPUs it has, the limits Docker enforces, the containers' use, and the disk holding Docker's data (its folder, when it was read, and whether new releases are refused) | fixed: what the endpoint has, and AppTown's own deployments' limits; use and disk not read | elastic: each part's size and listed price, and an estimated monthly cost | none: a site has no runtime |
| A host short of disk | a release or preview is refused before anything is pulled or started while Docker's data folder has less than 5% free (at least 1 GiB, at most 20 GiB) or cannot be read, read when it starts; what runs is not touched | not checked: disk is not read through Portainer | not applicable: the platform's disks | not applicable |
| Container logs | every container AppTown starts from this release on logs through `json-file`, rotated at 10 MiB, three files kept; one started earlier keeps its own until it is made again | the endpoint daemon's default; not set by AppTown in 1.0 | the platform's | none: a site has no runtime |
| Limit events | out-of-memory kills and exits of every standing deployment's parts and needs, from the daemon's events | not read | not reported to AppTown | none: a site has no runtime |
| Receives a landing | yes: a landing makes `main` on a Docker host | by promotion; an AppTown with no local Docker refuses a landing to it | as Portainer | as Portainer |
| Readiness witnessed | the container AppTown started; a static site's release identity | the stack was accepted; health unknown | the platform's deployment is active (its own probe); AppTown asks the front door only for a declared HTTP readiness path or a static site's release identity | the published files answer this release's identity |
| An earlier release again | yes, its bundle by digest; data is not rolled back | yes; data is not rolled back | yes; data is not rolled back | yes |
| Take-down | what AppTown recorded creating; its data volumes are deleted after a complete final backup, refused without one; what it did not create is named and kept | the stack AppTown recorded, then the volumes its deploys made after a complete final backup, refused without one unless a person chooses to keep the data; a volume it did not make is named and kept | the app and managed resources AppTown recorded; a need's bucket is deleted with its objects after a checked final copy or when its data is synthetic, otherwise kept and named, and its key deleted | the bucket AppTown recorded |
| Volume backups, restored in place | yes, each data volume and need | yes, each data volume and need, through Portainer's Docker proxy (an edge environment in async mode is refused) | no volumes | no data |
| Managed database copies | not applicable | not applicable | yes, with a copy runner and backup store; restored into a new place, never in place | not applicable |

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

## Docker target inventory

A target's inventory compares recorded resource claims with a complete scoped
Docker observation: Forge-named containers, application (`forge-data-`) and need
(`forge-need-`) volumes, Forge networks, and recorded image references. Immutable
repository/digest references require an exact alias from Docker; local-build tags
are observed separately and cannot confirm a digest. Unclaimed host images are
not listed as orphan cleanup candidates. The page adds no image/network removal
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
(`PrepareImages`), the intranet release on its own scope, and the worker's
release task inside the task, with the same check in its shell. The export
runs on the selected daemon's own builder, for its own platform: every
build of a project's source on a daemon (`docker.Scope.Build`, which
`build:` parts use too) names that daemon's builder (`default` for a
`--host` daemon, else the context's own) and refuses one whose driver is not
`docker`, so neither a builder alias with a saved remote builder nor an
inherited `BUILDX_BUILDER` or `BUILDX_CONFIG` takes it elsewhere. Without
buildx the daemon's legacy builder builds. The worker's task keeps its
private `forge` builder. Recorded release images are
never rebuilt.

The file server answers two forge paths. `/_forge/env.js` gives the page its
browser values (`FORGE_BROWSER_ENV`), and answers `{}` when the variable is
unset or empty, whatever the target set (App Platform sets none). `/_forge/release` answers
`FORGE_RELEASE`, the public static part's release identity,
`<part>@<revision>` (`appcontract.Contract.PublicRelease`). The forge sets it
from the selected source, never from a component's environment, on every
target: App Platform's renderer, a Docker host's boot (after the contract's
and the secrets' values, so neither can stand in for it) and Portainer's
stack (one key, set over any value the contract or a secret gives it). App Platform also sets it on its other static parts.

What each target's readiness witnesses:

| Kind | Witnessed |
| --- | --- |
| `docker-host` | the container the forge started, by its labels and port. For a static site served from a revision (a landing, a promotion, a recovery) whose image serves itself, that `/_forge/release` answers this release's identity, as on App Platform. For an earlier-form bundle, or a preview of a worktree (no revision), that `/_forge/env.js` answers (only the forge's file server configuration serves it) |
| `do-app` | the platform's deployment for this generation is active (the platform's own probe); the forge asks the front door only when the contract declares an HTTP readiness path, and, when the public part is a static site, that `/_forge/release` answers this release's identity. Otherwise the release log says which release answers is not witnessed |
| `portainer` | the stack was accepted; health is unknown. `/_forge/release` answers, but nothing asks it |

Every bundle cut since then records that its static images serve themselves
(`selfServing` in the bundle's origin). A bundle without that record is the
earlier form: its static image holds only the files. A Docker host and
Portainer run it with `docker.StaticServerCommand`, as they always did, so
recovering or redeploying an old static release still works. App Platform
runs an image as it is, so it refuses an earlier-form static bundle when the
release is asked for, before any call to DigitalOcean: release again to get
a self-serving image. Both forms serve `/_forge/env.js`, which a Docker host
boot of an earlier-form static site requires.

## Where a release comes from

On a forge with a worker, every target runs a release bundle by digest from
the project's registry ([release bundles](deployment.md#release-bundles)):

| Kind | Receives a landing | Receives a promotion | Builds |
| --- | --- | --- | --- |
| `docker-host` (built-in or attached over ssh) | yes, `main` | yes | never; pulls the digests and confirms `RepoDigests` |
| `do-app` | no | yes | never; the forge copies each digest into DOCR |
| `portainer` | no | yes | never; the stack names each digest |

Release previews boot a bundle by digest where change previews boot,
without a platform check: they rely on one platform per forge (the
worker's). Every release target's platform must be the bundle's: a Docker
host's daemon answers it, App Platform is `linux/amd64`, Portainer's
endpoint answers it; unknown or different is refused before any effect. In the no-local-Docker mode the
built-in host is unavailable and digest bundles may go to any of the
others.

App Platform keeps the copies in the account's one DOCR registry, one
repository per project, named after its slug. The repository is the
forge's: the first copy creates it and the forge records it per project and
connector. A repository of that name the forge did
not create refuses the release, at preflight and before any copy; the
one-time ownership inspection can propose one an earlier release made, for
an administrator to approve. When the project's last deployment on that
connector, on any App Platform target, is taken down, every image in the
repository is deleted. The forge starts no garbage collection: while one runs
DOCR takes no pushes, so one started by a take-down would fail the next
release. The space, and the empty
repository's name (DOCR has no call to delete a repository and lists it
until a collection runs), are freed by a collection an operator runs or
DigitalOcean schedules. An image someone else pushed into the forge's
repository is deleted with the rest: don't push to the forge's
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

On a forge without a worker, Docker hosts build from the committed tree and
record image IDs; such a bundle is reused only on the target its images live
on.

## Object storage on App Platform

The `s3` need on a `do-app` target is a DigitalOcean Spaces bucket per
deployment, made by
the driver's need module (`internal/deploy/digitalocean/storage.go`):

- A release mints a full-access *maker* key named
  `forge-maker-<slug>-<deployment>` with the account's API token (deleting any
  of that name first), makes the bucket through the S3 API with it, mints the
  deployment's `readwrite` key for that bucket, and deletes the maker key; one
  it could not delete is said in the release record. The bucket and key are
  on the deployment's owned list (`deployment_owned`, kinds `bucket` and
  `spaces-key`), not `need_instances`; the key's secret is in the built-in
  secret store under `need:deployment:<id>`. A later release reuses them.
- The app spec gives the contract's variables, the secret as a `SECRET` env.
- The forge copies the bucket's objects like a database: from the
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
  unrecorded); buckets are not listed, and a recorded key names its bucket.

## Bucket websites

An `s3-bucket` target is an S3-compatible store (DigitalOcean Spaces first)
that serves static-only apps as files: one public static part, no init
steps, no needs, released by digest. Anything else is refused with a
sentence.

- **Attaching** names the store's endpoint, region, website address
  template (`https://{bucket}.{region}-static.digitaloceanspaces.com`) and
  the secrets holding its key pair (`accessKeyFrom`, `secretKeyFrom`). The
  store must accept the key before the target is saved. The forge creates
  buckets, so the key is a full-access key: it can read, write and
  configure every bucket in its team. Attaching and checking say so, and
  recommend a key (and, where other data must be out of reach, a team) for
  the forge alone.
- **Each standing deployment owns one bucket**, created by the forge on its
  first release and recorded on the deployment (`Deployments.RecordOwned`)
  before anything else touches it. A taken name is refused, never adopted.
  Don't store anything else in a deployment's bucket: taking the deployment
  down deletes it.
- **A release** reads the site's files out of the release image
  (`oci.ReadTree`, `/srv`, no links, at most 200 MB), adds `/_forge/env.js`
  with the project's browser values and `/_forge/release` with the
  publication identity `<part>@<revision>+<set>` (`staticsite`), and uploads
  assets, then HTML and `env.js` (`no-store`), then the marker, each
  public-read. It is released when the website address, asked past caches,
  answers exactly that identity. Not answering within the witness budget
  leaves the attempt uncertain; recovery and **Check again** only witness,
  never upload. An upload refused before the marker fails the release as an
  incomplete publication: the address still answers the previous one.
  After a witnessed release, objects in neither this nor the previous
  publication are deleted, so an open tab can still load the previous
  release's hashed files.
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

## Implementation references

- [internal/forge/deployment/target.go](../../internal/forge/deployment/target.go)
- [internal/forge/deployment/target_state.go](../../internal/forge/deployment/target_state.go)
- [internal/forge/deployment/target_providers.go](../../internal/forge/deployment/target_providers.go)
- [internal/deploy/driver.go](../../internal/deploy/driver.go)
