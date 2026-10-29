# Deployment runtime

Deployment owns targets, named placements, releases, attempts and the
resources their placements created or retained. Drivers perform external effects through `internal/deploy`. The
[package documentation](../../internal/apptown/deployment/doc.go) owns API and
transaction contracts; the [package map](../../internal/apptown/deployment/README.md)
shows where to change them.

Previews of changes, runs and releases are the
[preview](../../internal/apptown/preview/doc.go) owner's:
boot admission and status, booting here or on the worker, what each preview
serves, discarding and reconciling them, and their AI models keys
(`preview_owned`). A change preview's placement row stays a Deployment row,
written through `Deployments.RecordPreview` and `ForgetPreview`. Deployment's
`Hosts` (daemons), `Environment` (what a boot is given, the eval rule) and
`Providers` (drivers and inventories) are built once by the composition root
and shared by both owners; none refers back to Deployment's `Service`.

Copies, restores, drills, rehearsals, restored copies and take-down
orchestration are the [dataprotection](../../internal/apptown/dataprotection/doc.go)
owner's since DS-03 of the same proposal, with the backup, drill, rehearsal
and restored-copy tables. A take-down's settlement (need instances and what
the target left becoming retained resources, the release it served ending,
the row going, the receipt) stays Deployment's (`SelectRemovalIn`,
`RemoveRecordedIn`), as do a restored copy's placement row and the record
that a deployment started from a copy; data protection fills it, as
Deployment's `CopyStarter`. The sections below describe the behaviour, which
did not change with the owner.

## Deployment procedure

Standing placements execute `compatible-update`, the name for the existing
release admission, preparation, provider update and settlement procedure. The
name does not certify application backwards compatibility or promise automatic
application/data rollback or a verified complete recovery set. Makers and
operators must choose application changes suitable for this procedure.

The existing configuration API (`POST /api/v1/projects/{slug}/deployments`)
accepts `deploymentPolicy`. Omitted, null, empty and explicit `compatible-update`
use the implemented default on new and reconfigured placements. Unknown nonempty
values and non-string JSON are invalid (400). Configuration and the named deploy
request reject unknown keys, so a misspelled safety choice is not ignored.
`deploymentPolicy` belongs only to configuration, not a per-release/bundle
override; supplying it at `.../deployments/{name}/deploy` is invalid (400).

`protected-maintenance` names a planned procedure: quiescence, a verified complete
recovery set, private validation and reopening. It is unavailable. An authorized
selection is refused before source preparation, settings writes or provider
effects: 422, readable `error`, and `X-AppTown-Deny-Code:
deployment_policy_not_implemented`. The refusal tells the caller to use
`compatible-update`; existing settings remain committed as they were. Caller
visibility and binding authority are checked before policy-specific refusal.

Exactly one procedure can execute, so no policy column or per-generation setting
is stored. Existing and new standing deployment reads report the enforced
`deploymentPolicy: compatible-update`, and accepted configuration receipts name
it. Reserved choices are never saved. Adding an executable second procedure
requires an append-only migration/backfill; previews and restored copies do not
claim a selected standing update procedure. CLI/MCP command arguments, app
contracts and bundle data are unchanged.

## Requesting and executing a release

A manual request selects a deployment and release bundle. Deployment checks the
caller's deployment permission and, for a protected placement, promotion
permission. It resolves the selected contract, rechecks placement and commits the
release and queued job together. The returned acceptance means queued work.

The worker later claims that request, stages its source and resolves secrets.
Managed-resource provisioning and artifact rendering can contact providers during
this preparation. The attempt is then admitted before the driver reconciles the
deployment; its generation guards do not cover those earlier effects. A repeat delivery of a claimed
request does not repeat preparation or prove that the original succeeded.
Inspect the release and attempt outcome. Workflow and preview paths have their
own entry points and use the package's attempt and settlement rules.

**Preflight** (`preflight.go`). Before anything is built
for a release, `Service.Preflight` judges the selected commit's staged
contract against its deployment with delivery's own rules: the target's
refusal of the effective contract (profile, overrides and memory defaults
applied), browser values, needs fulfilled by registering, and
`secret.Service.Check`, which is `Resolve` without its effects (a
generated secret not yet bound counts as provided and is not minted; no
value is returned). It creates no database, registers no key, runs no init
and boots nothing; values a need injects are not settings. A refusal is a
`PreflightError` whose category is `missing-settings` (names and the
consumers they would reach, never values), `refused` (the owner's words) or
`unassessable` (a store or record that could not be read: neither a pass nor
a claim that a value is absent), bound to the deployment, target, commit
and check time. The landing's deploy step calls it before its release-build
task is reserved, and records the refusal in its log and evidence;
`ExecuteRelease` calls the same check before secrets are resolved or a
need registered. It is early, not final: boot and `ExecuteRelease`
resolve authority and settings again, so a setting removed during a build
still refuses there. Existing target and registry selection at landing is
unchanged. A deployment policy other than `compatible-update` cannot be
saved, so the check has no policy to refuse.

**Needs fulfilled by registering.** A need whose dependency
kind has a `register:` block is not asked of the target (`refusesContract`,
`reconcileNeeds` skip it; a static-only bucket still refuses any need). There is
one adapter, `openrouter-key` (`llm`): the project must have
attached AI models, or the boot is refused before any provider call; the
deployment or preview then gets its own OpenRouter key, kept on its owned
list, and the kind's `inject` templates render `LLM_API_KEY` and
`LLM_BASE_URL` into every part's values. A Docker host registers as
its boot reads the app's secrets; any other target gets the values with the
release's secrets. The adapters the project attachment still fulfils
(`glitchtip-project`, `authentik-oidc`, `otlp-endpoint`) are left as they
were; any other adapter refuses. Intranet releases refuse the need. The
card reads a key's allowance from OpenRouter on its own route
(`GET …/deployments/{name}/ai-models`); unknown is never zero.

## Selected source and reproducibility

The application declaration is native `.apptown/app.yaml`. Contract parsing and
runtime preparation are separate operations. Missing inputs are errors; AppTown
does not infer them from an older format or current project head.

Docker execution stages the selected committed tree and stores its prepared
request with the attempt. Rebuild restores those inputs; for a digest bundle
it pulls the recorded digests instead of building. Prepared paths must stay
inside the staged tree; settlement records source and image identity. This does
not freeze upstream image tags, package downloads, secret values or target
configuration. An earlier revision can still fail on unavailable dependencies or
its own bugs. See the [contract reference](../../stock/docs/guides/contract-reference.md).

## Deploy step progress

A landing's deploy step exposes phase messages through its existing live log:
source and target selection, worker image build, publication of each image,
runtime configuration, host image preparation, dependency startup, initialization
and app parts, readiness and visitor-page checks, and release recording. An elapsed
heartbeat continues during long operations. Messages describe work being entered;
the step outcome and deployment observation establish success. They contain names,
not environment values, credentials, command arguments or raw build output.
The existing progress feed retains its last 500 lines and captures that tail for
the step's log on return, including failure. This is best-effort
history: a daemon crash can lose the in-memory tail, and restart settlement and
provider observations remain authoritative. Detailed BuildKit and container output
are not streamed by these phase messages.

Once a landing's deploy attempt is admitted, it runs to its own outcome.
Abandoning the change while it deploys does not cut the deploy short into an
outcome no one knows: the deploy finishes and settles applied or failed, and
the step's own outcome is then refused because its run has ended. A step whose
interruption is noticed before admission deploys nothing. A deploy under way when AppTown
stops is found still running at startup and observed.

## Release bundles

A release bundle is what a landing produced and every later release ships.
It has one of two forms.

- **By digest** (an AppTown with a worker, or a project with a container
  registry on an AppTown without one). With a worker, the landing's
  deploy step has the worker build every image once, from the landed commit,
  in private Docker. Without one, the landing builds on its Docker host as
  below, and AppTown saves each image it booted (`docker save`) and
  publishes it the same way. A worker changes where a release is built, not
  what it produces. AppTown, not the worker, publishes each image to the
  project's container-registry attachment (the one container-registry
  connector, attached at that landing, when the project has none) and reads
  it back; the bundle
  records each part and init image as `<repository>@sha256:<digest>` with
  the repository, the platform (`os/arch`), where it was built (the
  worker with the profile it measured, or the Docker host's name, role
  `docker-host`), and the run, step and attempt.
  It is committed once, in a transaction that checks the attempt still owns
  the step, and never changes afterwards. Landing deployment inputs carry
  the bundle's ID, so a generation runs exactly the bundle it was admitted
  with. A landing built on its Docker host publishes after the boot, so its
  generation names the run, not the bundle. If that publication fails, the
  step ends with an error that says the new version is running and was not
  published, and no bundle is recorded.
- **By image ID** (an AppTown without a worker whose project has no
  container registry, and every bundle recorded before). The images are built on the target and the bundle records the
  daemon-local image IDs and the target they were read on.

**Consumers of a digest bundle**, all by digest, none building:
- a landing, to `main` on a Docker host (built-in or attached), which pulls
  the digests with a per-operation registry login and confirms them in the
  image's `RepoDigests`;
- promotion to any other Docker host, and recovery of a standing deployment
  whose container or images are gone;
- App Platform, by promotion: AppTown copies each digest into DOCR and
  names DOCR's confirmed digest in the app spec. App Platform runs one
  version of a DOCR repository per app: a spec naming two digests of one
  repository is accepted by its propose endpoint and refused at create
  ("not configured for this app"), whether or not the images name their
  registry, while the same two digests from two repositories are accepted. A
  refused create can still leave an app with no deployment on the account.
  A release whose parts and init steps are different images (they all go
  to the project's one repository) is therefore refused for App Platform
  when it is asked for, before any provider effect: no database, copy or
  app is made (`digitalocean.MultiImageRefusal`, checked with the bundle's
  other admission rules). The refusal says how many images it uses and to
  deploy it to a Docker host target instead; AppTown cannot put the images
  in different repositories. The driver also refuses such a spec
  before asking for the app;
- Portainer, by promotion: the stack names every part and init image by
  digest. Named and project-default releases use the same durable admission
  and execution. `Environment.portainerStack` renders the app's needs as on a
  Docker host, with persistent data and registered credentials owned by the
  deployment. Both paths refuse image IDs and unknown or mismatched platforms,
  record the admitted address, and ask for a backup after a successful release.
  A default release selects the project's explicit deployment UUID and checks
  its revision during admission. Configuration is a separate action; releasing
  does not create an implicit environment. Portainer deployment names remain
  restricted to the target's configured tier; another name is refused before
  a release is numbered;
- release previews, booted where change previews boot.

A release's detail (`GET /api/v1/projects/{slug}/releases/{number}` and its
page) shows its bundle's form: by digest with the registry, platform and each
part and init image's `<repository>@sha256:…`, or by image ID. A person can
tell from it before promoting whether the release can go to App Platform.

A landing deploys only to a Docker host; App Platform and Portainer are
promotion targets. A registry that refuses or cannot be reached is an
error. A digest the registry confirms missing is refused, never rebuilt:
registry retention decides how long a release can be redeployed. Docker
hosts, App Platform and Portainer refuse a target whose platform is
unknown or not the bundle's. Release previews do not check: they rely on
the rule of one platform per AppTown (the worker's), as an accepted
limitation.
Readable registry tags (`change-<id>-<image>`, `release-<n>-<image>`) are
for people; the digest is the record. The operating details, refusals and
rollout procedure are in [workers](../operations/workers.md#releases-built-on-the-worker).

## Uncertain outcomes and restart

Provider effects occur outside SQL transactions. Generation checks prevent stale
results replacing current records, but cannot undo an external effect or stop an
old call already in flight. A failed provider-identity checkpoint stops execution.

An unknown result remains uncertain and queues observation atomically. Recovery
uses `Resume` on the recorded identity; it cannot recreate or reapply a missing
deployment. [Job retry limits](jobs.md) bound automatic observation attempts.
Exhausting them does not prove the provider failed.

On a Docker host, a release whose boot fails in any step ends failed: a
service that never gets healthy, a migration that exits non-zero, an app
container that does not start, a readiness probe or front-page check that
does not pass. The boot may have changed the host before it failed (stopped
the old app, started services, run a migration), and failing does not undo
that. Releasing again replaces the failed app container and runs the boot
again, reusing standing services and their data. A take-down removes the
container under the deployment's name whichever generation started it, and
handles everything else on the deployment's list under its usual rules.

A Docker attempt becomes uncertain only when AppTown lost track of it: it
stopped mid-boot, the host stopped answering, or the outcome could not be
written. `Resume` then settles it from the boot record and what the host
says. The container the attempt recorded starting healthy, running as
recorded, settles it applied. No record of a healthy start, or that
container gone, replaced or stopped, settles it failed. Only a host that
cannot be asked (or whose answer cannot be read) leaves it uncertain for a
later look. One case reads wrong: if AppTown stops after a healthy boot but
before writing its record, the attempt settles failed while the new
container runs. The next release replaces it.

A new App Platform app's provider-issued name (`*.ondigitalocean.app`) can be
missing from DNS for minutes after the app goes active, and a resolver that
asked too early keeps that answer for the zone's negative-cache time (the
`ondigitalocean.app` SOA minimum, 1800 seconds). DigitalOcean's VPC resolver
answers from several caches that disagree, so in that window a name flips
between "no such host" and its addresses from one look-up to the next, and the
A and AAAA answers come and go separately; without allowing for it, the probes
and recoveries are spent inside the window and a person has to press Check
again. Until 35 minutes after the platform created
the app (`ProviderNameGrace`: the 30-minute negative cache plus 5 for the
platform to publish the name, measured from the app's `created_at`, so a
resumed attempt keeps the same bound), each request of the front door's
checks (the readiness probe and the release witness) is judged on its own: a
request that failed with "no such host", or with no route to any address the
name gave (an IPv6-only answer on a host without IPv6), is asked again a poll
interval later and does not spend one of the check's six tries. A request the
front door answered counts as before, so a transient 503 or the wrong release
keeps its normal allowance, and a good answer ends the check at once. Go's
dialer already tries both address families, so an IPv6 address AppTown
cannot reach does not fail a request while an IPv4 one connects.

A gated copy's front door is not asked by the driver: inside the grace it waits
for the name to be in DNS (asking every 30 seconds) and then hands the address
to the checks through the gate. Those checks ask through the system resolver
and are not covered by the grace; AppTown's own gate checks (`checkGate`)
ask once each, so a flip there fails the restore test. After the grace a
name that does not answer spends the checks' tries as before: a failed probe
leaves the attempt uncertain, and a failed witness fails the release. Other DNS failures, other host names (a custom domain) and an app
whose creation time the platform did not give get no grace. An attempt's job
budget (20 minutes) can end inside the grace; recovery resumes against the
same bound.

Startup checks settlement consistency and queues interrupted attempts without
polling providers. A deployment whose own records contradict each other (an
attempt still open while the deployment reads settled, or a release still
releasing after its attempt ended) does not stop startup and is not
reconstructed there. Startup marks that one deployment as
needing attention: its readiness becomes uncertain, an open attempt becomes
uncertain and is queued for observation, and the ledger records
`deployment.needs-attention` once. Other deployments and the rest of startup
go on. Only a failure to read the records, or to record the mark, stops it.

**Check again** (`POST /api/v1/projects/{slug}/deployments/{name}/check`, the
button on an uncertain deployment's card; `deployment:operate`) is the
person's repair. It observes; it never deploys. An uncertain attempt is
observed as recovery observes it (`Resume` on the recorded boot proof): the
container it booted running with the recorded image settles it applied; no
record of a healthy start, or that container gone, replaced or stopped,
settles it failed; and a host that cannot be asked leaves it uncertain,
still needing attention. A release left releasing follows its
attempt: failed or superseded fails it, an attempt a newer generation
replaced is superseded, and a done attempt of the current generation is
observed the same way. Settlement is the usual one, with its release receipts;
the ledger records `deployment.checked-again` with who pressed it and what was
observed. Missing evidence is still not retry authority.

## After a whole-AppTown restore

`apptownd restore` ([AppTown's own backups](../../stock/docs/guides/apptown-backups.md))
leaves a hold on deployments whose records may no longer match what runs
(`deployments.restore_check`, `restore_check.go`):

- `unconfirmed`: a standing App Platform or Portainer deployment whose
  provider has not been read since. A minute job (`restore-check`) asks the
  driver's own observation, which now also names what runs
  (`deploy.Observation.Running`: App Platform's active deployment, the
  Portainer app container's image). The recorded one confirms it; another
  is adopted as a release with origin `observed-after-restore` (never
  `shipped`; App Platform's handle takes the running deployment, the
  revision becomes unknown); no app makes it `gone`; an unreadable provider
  or an open attempt leaves it held.
- `gone`: no app at the provider. A person takes it down.
- `filling`: a built-in deployment whose rebuild puts its data back. The
  rebuild's Docker boot runs data protection's fill after the needs and
  before any init step, part or the public app (`BootRequest.Fill`,
  `RestoreFiller`); only a whole set fills, and a partial or missing one
  fails the boot with nothing of the app started. The rebuild job settles
  the fill and lifts the hold however the rebuild ends.

A held deployment admits no attempt (`Outcomes.Admit` refuses it, except a
`filling` deployment's rebuild), no release (`CreateReleaseIn`, and
`ExecuteRelease` before any preparation, for releases queued before the
backup), no data restore and, while `unconfirmed`, no take-down. After an
adoption, the bundle of the release AppTown last shipped there may be
deployed by hand, also on a landing-fed deployment (`handBundle`).

## Releasing over data AppTown cannot copy

A release can change the data a deployment holds (a migration, code that
rewrites stored objects), and nothing in the contract says whether it will.
Before admitting a named or default release through the shared
`RequestRelease` admission, Deployment asks data
protection whether it can copy what that deployment holds now
(`DataCopyCheck`, answered by `dataprotection` `CannotCopy`). It cannot when
the held-data plan cannot be read, when an item in it cannot be copied (a
need whose kind the catalogue cannot read), or when the target cannot take a
copy (an App Platform target with no backup store or copy runner; a Portainer
environment AppTown cannot reach for data, such as an async edge agent). A
deployment that has served no release holds nothing yet and is not asked;
one that holds no data is not affected; a release of the bundle the
deployment already runs (an operator's override redeploying it) brings no new
code and is not asked. Such a release is refused, naming
why and how to go on. A person may release anyway with `acceptUncopiedData`
(`apptown release --accept-uncopied-data`): the release's first log line then
says who chose it and why the data could not be copied. There is no
freshness rule: data AppTown can copy is copied nightly and after each
release, and the protection view shows a stale copy.

## Removing a deployment

A deployment owns what AppTown recorded creating for it. Each creation is recorded on the deployment's list
(`deployment_owned`) as the provider confirms it, through `deploy.Owner`:

- **Docker host**: the containers its boot ran (by ID), the stack's network
  when the boot made it, the app's data volume and each need's named data
  volume (`apptown-need-dep-<key>-<need>`) when the boot created them, and
  the release images it pulled or built. See **Names on a host** below.
- **App Platform**: the app (its recorded handle) and, per project and
  connector, the DOCR repository the first copy created (`owned_repositories`).
  A repository of the project's name AppTown did not create refuses the
  release; it is never adopted. The gate image of a restored copy goes to
  one shared repository per registry, `apptown_gate` (no project slug has an
  underscore), made by the first gated copy on the account and counted
  against the registry plan by the room check. It is not a deployment's: no
  take-down empties it, and the gate images in it, pinned by digest, stay
  for the next copy; one an older gate release left stays until an operator
  deletes it. One of that name AppTown did not make is used: the copy is
  by digest and DOCR must answer that digest.
- **Portainer**: the stack, when AppTown's own create made it.
- **S3-compatible bucket** (`s3-bucket`): the bucket, recorded on the
  deployment's handle by AppTown's own create; take-down empties and
  deletes it. A static site holds no data.
- **AI models key** (any target; kind `openrouter-key`): the OpenRouter key
  the register step made for an app that needs `llm`, recorded
  as `<connector>/<hash>` as soon as OpenRouter confirms it. The target
  driver never sees it: after the driver's teardown AppTown revokes it
  and deletes its stored value; a revoke that fails is left, with why.
  A preview's key is on Preview's `preview_owned` by the preview's name and
  is revoked once no container of the preview stands where previews run (the
  worker's list, or this AppTown's own Docker) and it is not booting
  (`preview.Service.ReleaseEndedPreviewKeys`, every minute). A revoke that
  fails becomes a retained row (`Retained.RecordPreviewKeyIn`) in the same
  transaction that forgets the key on `preview_owned`. The place is asked, not the
  AppTown's in-memory record of what it served: the age reaper and project
  deletion remove previews without touching that record (review LI-01).
  A place that cannot be asked keeps every key it might hold.

Nothing found, reused or matched by name is recorded, so it is never
deleted.

### Names on a host

What AppTown makes for a deployment on a Docker host, a Portainer
environment, App Platform or an S3 bucket is named from the deployment's
**key**: the last 12 hex digits of its id, which a unique index keeps
unique within an AppTown. Two deployments never share a name, whatever
their project and deployment names (`shop`/`eu-main` and `shop-eu`/`main`
once did), and a deployment made again under an old name gets new
resources. On a Docker host its stack is `dep-<key>`: the app container
`apptown-dep-<key>`, services `apptown-svc-dep-<key>-<name>`, init containers
`apptown-init-dep-<key>-<name>`, the network `apptown-net-dep-<key>`, need
volumes `apptown-need-dep-<key>-<need>`, the app's data volume
`apptown-data-apptown-dep-<key>`. A Portainer stack is `apptown-dep-<key>`;
an App Platform app `apptown-<key>`, its cluster `apptown-db-<key>`
(tagged with the project and deployment), its Spaces keys
`apptown-maker-<key>` and `apptown-app-<key>`; a bucket
`apptown-<key>-<suffix>`. The Infrastructure page names each resource's
project and deployment. Every Docker label key is `apptown.*`. A standing
deployment's app container, services, network and volumes also carry
`apptown.project=<slug>` and `apptown.deployment.name=<name>`, so a person
reading `docker ps` or `docker volume ls` can tell whose they are. They are
for people only: AppTown never reads them for identity, ownership or
removal, which go by the deployment's id and key.

`DELETE /api/v1/projects/{slug}/deployments/{name}` (`deployment:teardown`)
takes the deployment down. An App Platform deployment holding a managed
database AppTown copies, with a backup store attached and a copy runner
named on its target, is taken down by a data operation instead (answered
202 with `Location` and `{operationId, state, url}`; see **Protecting data
off-host** below). Project deletion admits such a deployment's take-down
the same way, `main` included, and runs it itself when it claims it first
(else waits for the worker that has it): one that does not end succeeded
and verified refuses the deletion with what the operation left
(`TakeDownWithProject`). An eval copy's deployments get no final copy or
backup on any target: their data is synthetic. Any other
deployment, App Platform without a store included, goes as follows:

1. **Final backup** (Docker hosts; `dataprotection/final_backup.go`). It is planned by the
   same held-data planner as ordinary backups ("Backups and restore"): each
   data volume on the list plus each the running release declares, looked
   up on the host, whatever the contract at head declares. It is planned
   before anything stops: a data volume on the list that is still there and
   has no way to be copied (its kind or mount cannot be read, nothing on the
   list mounts it), a need container on the list that keeps data in its own
   files or whose kind cannot be read (checked for every listed need
   container, whatever the release says: teardown deletes it, and data in
   its files goes with it), anything the daemon cannot be asked about (only its own "no
   such" answer is absence), or two items for one file refuses, having
   changed nothing (`ErrFinalBackupIncomplete`). Then every application
   part on the list is stopped and the set taken; the app's listed volume
   is copied as files. Each copy names the volume it was taken from, and
   certifies that volume only. Only a set holding a good copy of every
   listed data volume lets the data go; otherwise the parts are started
   again and nothing is removed. A held volume off the list (made before
   AppTown recorded what it creates) is copied into the same set, never
   deleted, and retained with why and whether the set holds a copy; its
   failed copy refuses nothing. It is retained as what the host answered
   for: its volume, or the need container whose own files hold the data;
   a need whose kind cannot be read is retained only by a volume or
   container that is there. With the release unread, the list alone is
   planned. Writers outside the deployment's parts are the operator's to
   stop first.
2. **Teardown.** The driver removes the recorded generation's serving thing
   (container, app, stack); failing that fails the removal. Then it deletes
   the rest of the list: on a Docker host containers (never with `-v`: a
   container's anonymous volumes go only if the list names them), network,
   data volumes (only after a complete backup) and images no container
   uses. Something the daemon cannot be asked about is left, with why. App Platform
   deletes every image in the project's repository and starts garbage
   collection when this was the last deployment of the project on that
   connector (DOCR keeps the empty repository's name until then).
   On Portainer the driver deletes the stack, then each volume on the list
   (one AppTown's deploy made: absent before it, present after) when the
   final set covers it or the data is synthetic; a volume the stack
   declares that is not on the list is copied into the final set and kept
   (retained with whether the set holds a copy). An environment AppTown
   cannot reach for data refuses the take-down unless a person chooses to
   go without a final copy (`WithoutFinalCopy`); its volumes then stay.
3. **Record.** In one transaction the deployment's record and list go, its
   managed need instances (the App Platform database) and whatever the
   teardown left (kept data, failed deletes, each with why) become retained
   rows, and `deployment.torn-down` names what was deleted and the final set.

The response names what was deleted, the final set (its size) and what was
left. A final set (trigger `take-down`) is not counted in the newest-seven
rule; like every set of the project it is kept with the project and goes
when the project is purged ([project-deletion.md](project-deletion.md)), and
it restores into any deployment of the project:
its volumes are not the destination's, so restore maps the set's data
volume to the destination's app volume and each need to the need of the
same name when that is unambiguous (`placeCopies`, below), and refuses
before any change when an item has nowhere certain to go.

**Backups and restore** (Docker hosts; `dataprotection/backup_execution.go`,
`dataprotection/backup_witness.go`, `dataprotection/backup_move.go`; what a set
holds: `dataprotection/held_data.go`).
A set copies every data volume the standing deployment holds: each on its
list plus each its running release declares (the contract at
`releaseSha`, read through the release sources: the app's `dataDir`
volume and each need that keeps data), merged by volume ID and each asked
of the daemon. The project's head plays no part. A need is copied in the
container that mounts exactly its volume (a listed need container or the
release's service container); a declared need whose named volume is
absent is copied in its service container and bound to the volume it
mounts there; one that mounts no volume there is an item not copied, each
need its own (an empty identity is never a match or a merge). A held volume nothing can copy, or a declared need whose
kind cannot be read, is an item not copied, with why: the set is partial.
`ErrNothingToBackUp` only when the list and the release were both read
and neither names data; no recorded release, an unreadable contract, list
or daemon is `ErrDataNotKnown`, and data named but none of it on the host
is `ErrHeldDataAbsent`. The nightly sweep skips only the first; the
protection view reads unknown for the others. **A copy belongs to one
volume:** each item records its `Volume` and its file is named for it
(two items with one file refuse the set); `placeCopies` puts a set's
copies back by the volume each was taken from, else by role only when
the set holds one copy of that role and the deployment one volume of it,
and otherwise refuses the restore before any change. A protection row
counts only a copy of its own volume, or a copy with no volume recorded
placed by that same rule; a row not copied counts none. A need's kind declares
its copy once, under `copy:` (`ext`, `export`, `verify`; `restore` and
`restoreStopped` to put it back in place; `format`, `client` and `import`
together for a copy that fills a new place; `env`, the connection its
commands read; `caveat`). On a Docker host `export` runs in the need's own
container (`docker exec`), given the connection that container was started
with (its `local.env` read back by inspect, over the kind's defaults, at
127.0.0.1; a copy with no `env` is given none and the container is not
asked). The in-place restore (`copy.restore`) and the check after it
(`copy.verify`) run there with the same connection, so Postgres's restore
is checked by the same verify a managed fill passes. Only a copy that
fills a new place records its `format`, image and user, the same kind of
copy a managed database's is (Postgres; Redis's copy fills none). A
`copy:` with no export (`{}` or only a `caveat`) is a tar of the kind's data
directory. A kind with no `copy:` is not copied (the item is unresolved: the
set is partial and says so) and its deployment is not restored, since
`restoreMoves` refuses an unresolved destination; nothing copies it some
other way. A restore puts each item back only as its own record says: its
digest, restore command, mode, check and caveat. An item recorded without a
digest, or a need without a restore command (an older set), is
not restored, never put back by the destination kind's command of today. **A copy out of a container is held to its trailer:**
Docker can lose the end of a container's stdout and still report exit 0,
so every export, data-directory tar and AppTown's own database dump runs
framed (`containerresult.Framed`, through `docker.Scope.RunStep`): after
the command, the container prints the count and sha256 of what it sent,
and a copy whose trailer is missing or does not match what arrived is a
failed item, never a good copy. A kind's commands therefore run under
`sh` with `mkfifo`, `tee`, `wc`, `sha256sum`, `tr` and `cut`, which
busybox and coreutils both have. A copy staged on a rehearsal host is
checked there against the set's size and digest the same way. **Where a set is kept:** in the backup store whenever one is
attached (staged under `backups/.staging/take-*` while it uploads; each
file is held to its size and digest there, and the manifest is written
beside it once the set has its id); on AppTown's disk otherwise, where
losing that disk loses it (the protection note says so). Retention counts a deployment's ordinary
sets once across both places (`keepFor`: 14 production, 7 otherwise) and
removes each where it is (`PruneSets`). An in-place restore of a store set
reads every copy back to staging, held to its digest, before the safety
set is taken; a managed database's copy is never restored in place. The
daily sweep moves disk sets into an attached store (`MoveSetsToStore`:
files checked against the record, uploaded, manifest written, row changed
by `MoveBackupToStore`, then the folder removed, all under the exclusive
file lock), and clears `.staging/take-*` and `restore-*` folders older than
a day. A crash after the row changes leaves the old folder for a person
(running an AppTown, "Deployment backups"). Each item records its copy method: `declared`, `files` (nothing
was writing: a take-down's stopped app volume) or `live-files` (a running
service's files, which may be inconsistent: the app volume of an ordinary
set, the tar fallback). A restore records per item what witnessed it:
`verified` (the kind's `verify` ran in the need after the restore, with the
copy on stdin, and passed), `loaded` (a declared restore ran in the running
service and succeeded, and the set records no check), `copied` (files put in
place, not verified: the app volume, the fallback), `failed` or
`skipped`; the restore is `restored`, `restored, not verified` or `partly
restored` accordingly, in the operation's result (`RestoreResult`, under
`restore`) and the `restored` receipt. **A restore is an operation**
(`deployment.restore`, `dataprotection/restore_operation.go`): the
door (`RequestRestore`, deploy grant) refuses what would refuse it before
any change and admits it holding the deployment's data (`DataHold`; a
release is not held off); the worker runs it once, asking the admitting
credential again before the safety set and before each item. It records
each step, the safety set, each container before it is stopped and again
once started, and each item's outcome as it ends; its `restored` receipt is
written by its settlement. When AppTown stops in the middle, the restart
settles it unknown with what it confirmed, and the kind's interruption
words name the containers that *may* be stopped (a stop recorded, no start
recorded: their state is not known), the items restored and the safety
set; the receipt says `status: unknown` with `mayBeStopped`. Nothing starts
them again: a restore's writer on the host can outlive AppTown, so a
person checks the host and releases the deployment again. A kind may declare `restoreStopped`: the need's container is
stopped, the restore runs in a throwaway container of its image with
`--volumes-from` it, and the need is started again whatever the restore
did; such a kind must declare `verify` (Parse refuses it otherwise), since
putting files in place is not the service loading them. Redis uses it.
Object storage declares a copy through the S3 API (`format: s3-objects`): a Python
program in its copy's `env`, run in its `client` image beside the store
(`copy.runIn: client`: a throwaway container sharing the need container's
network, since the gateway's image has no copy tool; the connection's
`127.0.0.1` is the store's either way). Its export is a tar of every
object's bytes and headers; its restore puts the bucket back exactly and
its verify checks keys, sizes, headers and bytes (the one-part ETag, else
the object read back). A set from before holds a tar of `/data`, which goes
back by its recorded command in the store's own container, `copied`. What
a kind's copy leaves out it states as `copy.caveat` (object storage: ACLs,
tags, versions, a parts-uploaded object's ETag): a caveat travels with each copy (`BackupItem.caveat`)
and each restored item (`RestoredItem.caveat`, in the response and the
receipt), and the Backups page shows it with the set, in the restore
question and in the result. A declared need whose kind cannot
be read (not in the catalog, or a stored kind that does not parse) is a
failed item of the set, with why, so the set is partial rather than "ok"
without that data, and a restore into that deployment is refused. The
stock sync stores a shipped kind file that does not parse in place of the
previous definition and returns `ErrStockUnreadable` naming it, which boot
logs; it never keeps the older definition in use.

**On Portainer** (`dataprotection/held_portainer.go`, `deploy/portainer/dockerproxy.go`,
`deploy/docker/step.go`). The same planner, commands and records as a
Docker host's, through Portainer's Docker proxy for the environment
(`/api/endpoints/<id>/docker`). `Providers.PortainerDataScope` serves that
proxy on a private Unix socket (one per target and environment, for the
AppTown's life): each request goes to Portainer with the target's key, read
at that moment, over Go's TLS, so the key is in no file, argument or
environment variable. The environment is read first: one that is not a
Docker environment, an edge agent in async mode (Portainer gives it no
Docker proxy) or a Docker that does not answer refuses with
`portainer.ErrCannotBackUp` and why, which reads as not known. The proxy
carries plain requests and answers, not attached streams, so the scope is
`NoStreams` and every streamed step (`docker.Step`: an export, a restore, a
check, a file copy of a volume) runs as a job: a container made for it (an
exec becomes a container of the running one's image and environment that
joins its network and mounts its volumes), its input copied in, started,
waited for, its exit status read, its output copied out, removed whatever
happened; the copy is written inside the job first. What it holds is the
volumes on its list (those its deploys made: `portainer.Execution` records a
volume the compose declares that was absent before the deploy and present
after) and those its current stack declares; the stack file classifies: a
volume a need's service mounts at its kind's data path is that need's,
copied by the kind's copy in that service's container (found by compose's
labels); any other is copied as files. A restore or a final set stops the
stack's non-need containers that run (and starts exactly those again: a
completed `init-` container, or a worker someone stopped, stays as it was;
`stopParts` does the same on a Docker host). The app's readiness after a
restore is not probed: AppTown has no address to ask on Portainer. Sets
are taken after each LAN release and by the nightly sweep.

**Protecting data off-host** (cloud targets; `dataprotection/backup_store.go`,
`dataprotection/operations.go`, `dataprotection/managed_copy.go`, `dataprotection/data_operations.go`,
`dataprotection/start_from_copy.go`, `dataprotection/protection.go`). Three owners: the dependency kind declares what a copy is
(`copy:` — format, extension, client image, export, import, verify, and
the connection `env` its commands read, from the app user's host, port,
database, user, password and sslmode); the provider says how AppTown
reaches an instance (`cloud.<provider>.reach`: only `connect`, from the
target's named copy runner, as the app's user) and what protection of its
own it has (`native`, read for recency); AppTown keeps copies in the one
backup store. Postgres declares all three for DigitalOcean; on a Docker
host its copy is the same export (see "Backups and restore"), restored in
place by `copy.restore` and checked by the same `copy.verify`. `Parse`
refuses a kind that declares a `local.backup` block, naming `copy:`: ignored,
it would turn a database's copy into a tar of its live files. **One data list:** what a deployment holds is answered by
`heldData` on every target AppTown copies (a Docker host's list plus
release, looked up on the host; a managed target's recorded managed
instances and the object storage buckets on its owned list, `heldManaged`), as one item
type. A bucket is copied by the s3 kind's copy, as the deployment's own key
for it (`deploy.BucketData.BucketConnection`), and its check fills a new
store of the kind started on the copy runner for the check and removed
after, never the provider's Spaces: it shows the copy comes back into an S3
store. A take-down stops the app with the owned list minus the bucket and
its key, copies and checks, deletes clusters (database items only), then
calls the driver's `TearDownStorage` with the bucket and key, `BackedUp`
only for a set holding a good copy of each bucket; a failed copy or check
ends the take-down with the app gone and the data, key and records kept. A
restored copy's take-down removes its own bucket and key as synthetic. An
instance AppTown cannot
copy (its kind unread, no copy reached there, its record naming no
instance, database and user) is a row with why, never left out, and a
list that cannot be read makes protection read unknown, with its copy,
check and final-copy doors "not known". **A managed need's identity
holds:** once its record names an instance, a database and a user,
`NeedStore.RecordIntent` refuses a fulfilment asking for another cluster
name, database or user (`ErrIdentityHeld`, a conflict the DigitalOcean
driver ends the release on before making anything) and leaves the record
as it is when the same identity is asked again. Copies take what the
record names, so a release that changed it and then failed would
otherwise leave every copy on a new, empty database while the app ran on
the old one. A project address change on a deployment with a managed
database therefore refuses its next release; data moves by a new
deployment started from a copy.

**Which sets fill a new place.** `storeSet` is the shared rule: a whole
set in the store, not expired. Each path then accounts for every good
item, refusing by name what it cannot fill, never skipping it. A managed
start from a copy and a restored copy (`managedFillable`) take only
portable need copies: a Docker set that also holds the app's files or a
Redis copy is refused, and `FinalCopies` does not offer it. A rehearsal
fills each need's portable copy into its new need, the app's files into
its new data volume (unpacked before the app starts), and starts a need
the release declares disposable empty; any other item fails it, by name.

- **The store.** A platform administrator attaches one existing private
  bucket in an S3-compatible store (`PUT /api/v1/backup-store`:
  endpoint, region, bucket and the names of the secrets holding its key
  pair). It is attached only after a private object was written, read back,
  deleted and seen gone. Copies are written private, streamed with their
  SHA-256 as the signed payload hash, and held to the store's own size and
  MD5 after upload; a manifest goes beside each set. A set in the store has
  `store: store` and records the endpoint and bucket it was written to; it
  is read and pruned there only, and another bucket cannot be attached while
  any unpruned set lives in the attached one (the same bucket can, with new
  key names). That check is not serialised with copies: change buckets only
  when no copy, check or take-down is running (the deployment cards and
  the home page show them). A copy that finishes in the old bucket after
  the change is recorded there and is never read from the new one; it
  reads as unavailable until the old bucket is attached again. The
  store's key never enters a copy job; whoever holds it can read the copies.
- **A copy** runs the kind's export in a throwaway container of its client
  image on the target's copy runner (a Docker host named in the target's
  settings, `copyRunner`), given the app user's connection by name only: the
  password is read from the provider at job time and reaches the container
  through the Docker CLI's environment, never an argument or a record. Each
  item names the instance and database it certifies (`instance`,
  `database`), the user and image it ran as, and its format.
  Without a copy runner nothing that runs there starts: a copy, a drill, a
  restore test or restored copy, or a start from a copy is refused with
  `ErrNoCopyRunner` (reason code `no-copy-runner`, its sentence the
  refusal's words), and so is a take-down of a deployment holding that
  data (`NoFinalCopy`, code `no-final-copy`, its sentence carrying the
  same reason) — at the take-down door and at project deletion — unless
  the person chooses to take it down without a final copy
  (`withoutFinalCopy`, naming the deployment: the card's "Take it down
  without a final copy", `apptown data take-down --without-final-copy`).
  Then the app goes, the cluster stays as a retained resource, and
  `deployment.torn-down` records the choice, who made it and why there was
  no copy. A choice that arrives once a final copy can be taken is refused
  (`final-copy-possible`): that take-down would delete the cluster, so the
  person confirms again. The protection read says which it would be
  (`takeDown`, `operations.finalCopy`); the card re-reads it when the
  question opens and offers the choice only on a current answer.
  The deployment's protection reports, under `operations`, each operation
  as its own door would admit it now (`copy`, `check`, `restore`,
  `rehearsal`, `startFromCopy`, `finalCopy`), with the refusal's code and
  sentence: a check also needs a copy in the store, and a rehearsal reads
  a set wherever it is kept and needs a copy runner only when it runs on
  one. The target view reports
  `copies` the same way, so the card greys each button and the target page
  says why from AppTown's answer rather than guessing.
- **A copy is only ever filled into a new, empty database** the provider
  makes (`deploy.ManagedData.NewDatabase` refuses a name the cluster holds;
  it never adopts one) and hands to the app's user; the kind's import runs
  as that user and its verify checks it there. A **drill** fills the
  newest copy into `<database>_check_<operation>` on the same cluster,
  checks it and drops it (a bucket's into a new store on the copy runner),
  and records, item by item, whether it came back, from which set, into
  which place and as whom (`backup_drills`). It reports pass or fail and
  why, never the data. A set in the store is never restored in place
  (`RestoreBackup` refuses it).
- **Data operations.** A copy (`deployment.copy`), a drill
  (`deployment.drill`), a take-down (`deployment.take-down`), a rehearsal
  (`deployment.rehearsal`) and a restored copy's start and end
  (`deployment.restored-copy`, `deployment.restored-copy-end`) are
  operations in the operations ledger (`internal/apptown/operations`). Deployment registers the kinds and
  admits each under the project's release lock, then the ledger's
  admission lock, then its own eligibility reads; the row, its River job
  and its receipt commit together, and a worker on the `operations` queue
  runs it once, for up to two hours. Its subject is
  `project:<id>/deployment:<id>`. A copy, drill, rehearsal or restored copy
  holds `<subject>/data`, so one data operation runs per deployment; a
  take-down holds the whole subject. Release creation checks
  `<subject>/release` under the same project lock: no release is minted, by
  any door, while a take-down is live, and a take-down is not admitted
  while a release or attempt is in flight. Data operations do not keep a
  release out. Asking again while one is live is a 409 naming it. It ends
  succeeded (verified, not-verified, or neither for a restored copy's end),
  failed, unknown or cancelled. Before each consequential effect (the
  table in the operations package doc names each one), the kind asks
  whether the credential that admitted it still stands
  (`Identity.RequireStill`); refused, it ends failed, "authority
  withdrawn", without that effect. Removing what the operation itself made
  is not asked again. A provider effect with no answer AppTown can trust
  (a timeout, a lost response, a 5xx) leaves the step and the operation
  unknown; only a provider's refusal is a failure. A drill whose copy came
  back but whose database could not be removed fails (or is unknown),
  keeping its verification. AppTown's own scheduled copies and the
  restored-copy sweep act under system bindings. A restart settles a
  running operation unknown, naming the steps it confirmed and the one it
  was in; it is never resumed. Cancel stops any queued one at once; a
  running take-down stops, as asked, before deleting the app, before its
  final copy, before checking it and before each cluster, and ends
  cancelled. A running copy, drill, rehearsal or restored copy is not
  stopped part way. An ended operation waits
  on the person who asked, on the home page, until they put it away.
- **Take-down on App Platform.** 1. the app is deleted, so nothing writes
  (it is rebuilt from the records); 2. the final copy goes to the store;
  3. it is filled into a new database on the same cluster, checked, and
  dropped; 4. the drill is recorded; 5. the cluster is deleted, only when it
  holds nothing but this deployment's checked data: an open retained row
  naming a database on it, or any database the provider lists that the
  final copy does not cover and that the kind's `copy.empty` check, run as
  the app's user, does not show empty (`defaultdb` included: its name
  proves nothing; for Postgres empty means no table, materialized view,
  view, foreign table or sequence outside the system schemas, whoever owns
  it, and no large object; only an extension's own view passes), refuses;
  the deletion step's detail, the operation's result (`shownEmpty`) and
  `deployment.torn-down` say which databases were shown empty, on which
  copy runner and as whom ("defaultdb shown empty (the postgres kind's
  empty check, on demo-docker as app)"), or that there were none;
  6. the records go, the instance is not retained, and `deployment.torn-down`
  names what was deleted and the final set. A step after the first that
  falls short leaves the data and the deployment's record standing: the
  operation reads "app removed, data kept; deploy again to serve".
  DigitalOcean deletes its own backups with the cluster, so after a
  take-down the final copy (kept with the project) is the only one.
- **Starting from a copy.** A new deployment on an App Platform target may
  name a set (`startFrom`, offered as "the final copy of <name>", from
  `GET /projects/{slug}/final-copies`). Its first release, after the
  provider has made its cluster and database and before the app is made,
  has the provider make the database new and empty, fills it from the copy
  as the app's user and checks it; the deployment records the set and when
  it was filled. The release records the copy its caller was authorised
  for and fills that or nothing, and a start cannot be chosen while a
  release is in flight. `deployment.started-from-copy` names the release, the
  set and each copy's digest, and the instance and database each came from
  and went into. A deployment that has run is never filled; one fed by
  every landing cannot start from a copy. The first release fills only
  from a copy each item of which (each good copy with a portable format,
  the ones the fill reads) was shown to come back by its last check of that
  set (`backup_drills` by set and item); otherwise it is refused before
  anything is made, naming each item and its last check or "never
  checked". A person may release anyway with `acceptUncheckedCopy`
  (`apptown release --accept-unchecked-copy`): the release's first log line
  then says who chose it and which items were not shown to come back. The
  offer says "checked" only when every such item was.
- **Authority.** Putting a copy's data into a deployment needs
  `deployment:restore` (granted where `deployment:teardown` is; no scope or
  instrument carries it, so no agent does) in the copy's project and in the
  destination's, checked when the start is chosen and again when the
  release that fills it is asked for; a drill needs it in the deployment's
  project. A set keeps its source's data class, and a production copy goes
  only into a production deployment. A copy on request needs
  `deployment:deploy`.
- **Protection state** (`GET .../deployments/{name}/protection`, the
  deployment card, `apptown data`): per item, AppTown's copies — fresh
  (within a day plus six hours), stale (older, or the last copy failed),
  unknown (the last copy was interrupted) or none — with the newest; the
  provider's backups as it answers now, or unknown with why; and the
  item's own last drill. The copies are those taken of this deployment's own instance and
  database, and the operations and drill those that ran on this deployment
  (the operation's subject and `backup_drills.deployment_id`): a deployment re-created under a
  taken-down one's name does not show the old one's as its own, though
  where it started from (`startFrom`) is shown. Production data without a
  store reads "not protected off-host".
  **Coverage.** Every answer carries `coverage`: `assessed` names each item
  AppTown assessed (its need with its volume or place) and `notAssessed`
  names, with why, each item it could not (its kind or place unread), the
  deployment's own data when what it holds is unknown or not read on its
  target kind, and always data the app keeps outside the needs it
  declares (`OutsideNotAssessed`: a bucket or a database it reaches with
  its own settings; a bucket its s3 need gives it is an assessed item). AppTown cannot tell from an app's settings which outside
  stores hold its data, so no answer is a whole application's protection:
  a fresh copy, a passed check, rehearsal or restore test speaks for the
  assessed items only. The backup store holds AppTown's copies and is
  never an app's own bucket. A deployment holding no data AppTown knows
  of still says what is not assessed; the card and `apptown data` show the
  same lists beside the evidence.
  The daily sweep starts a copy of every production App Platform deployment
  ready for one, and drills it when any item it would fill has no drill
  on this deployment, or its own last drill failed or is a week old;
  ordinary store sets keep the newest 14 (production) or 7, final copies 30
  days.

**Restore rehearsals** (`dataprotection/rehearsal.go`, `docker/sealed.go`).
A rehearsal is a data operation on the deployment it rehearses
(`rehearsal`), started on request only: it needs `deployment:restore` in
that project, and no agent carries it. There is no scheduled rehearsal, and a
take-down does not offer one. Where it runs (`rehearsalHost`):
- on the one attached Docker host a platform administrator keeps for
  rehearsals (`ingress.rehearsals`, set by a target edit whose receipt
  keeps `RehearsalHostMeaning`), when one is kept. Whether a host is kept is
  decided before its status: a kept host that does not answer refuses the
  rehearsal, and several kept hosts refuse it too. The built-in host and
  provider targets are never kept, and a host kept for evals or rehearsals
  takes nothing else (`Target.Reserved`);
- with none kept, where the deployment's data already is: a Docker-host
  deployment's own host (the built-in host included), or an App Platform
  target's copy runner. Anyone who controls that host can already read the
  data, so the rehearsal adds no reader; it does share the host's CPU,
  memory and disk while it runs. The record says so (`sharedHost`);
- otherwise (a Portainer environment, a static bucket, App Platform with no
  copy runner) nowhere: `ErrNoRehearsalHost`.

Steps:
1. choose, under the shared backups lock a restore takes: the copy (the
   newest good set, in the store or on AppTown's disk, or one named), the
   release the deployment serves now (a digest bundle, except on the host
   the deployment runs on, which holds the release's images by ID) and the
   host;
2. record the place it will make, all of it on the rehearsal host (the
   volume `apptown-rehearsal-<op>-copy`, the stack `rehearsal-<op>` and the
   app `apptown-rehearsal-<op>`), in `rehearsal_places` before making it,
   with whether the host is shared;
3. stream each copy from the store, or from its set's folder on AppTown's
   disk, through AppTown's memory into that volume, held to the set's size
   and digest; the backups lock is released once every copy is staged. No
   new copy is written to AppTown's own disk, where a project's script
   steps run; then
   boot sealed (`docker.Sealed`): its own network,
   no published port, no ingress, the needs started empty and filled by the
   kind's `copy.import` and `copy.verify` on that network as the emulator's
   user, then the init steps and the public part only, with the source
   deployment's address as the only `APPTOWN_ORIGINS`, no integrations, and
   secrets resolved by `secret.ResolveRehearsal` (bound secrets only when a
   non-inheriting `rehearsal` profile names them, and never for an entry
   that declares its own `default`, which is delivered instead; every
   generated secret fresh);
4. ask, from a probe container on that network that returns only a status
   code: readiness, the front page, and the contract's `rehearsal.checks`.
   Readiness and data checks are judged on their own path's answer, so a
   redirect to a sign-in page fails a data check; only the front page follows
   same-host redirects, still to the rehearsal's container;
5. record the outcome (`restore_rehearsals`, `deployment.rehearsed`):
   passed, passed without a data check, or failed with the step; each check's
   status; what was withheld; the parts not started; that outbound
   network is not blocked; and whether the host was shared (`sharedHost`,
   false for a kept host, so records from before the fallback read as they
   were made);
6. take down the app with its volumes, the stack and its network, and the
   staged copy's volume; anything left is reported, and the operation is not
   marked cleared.

A rehearsal is not stopped part way (`ErrRehearsalNotStopped`); it takes
itself down when it ends.

Only safe facts are recorded: step names, the command that ran and its exit
status, and status codes; never a command's output, a container's log or a
response body. A restart settles a running rehearsal unknown and keeps its
recorded place; the daily sweep removes what every ended, uncleared
rehearsal owns. The boundary is the host: anything on it, or on its network,
can reach the rehearsal. AppTown's part is to stream the copy through its
memory. The protection answer gives the last rehearsal operation's answer:
recorded, or failed or unknown when it ended without a record (an older
recorded rehearsal is kept beside it as `previous`, never shown as the
latest, with `sharedHost` from its recorded place); or "never rehearsed" /
"never rehearsed: no rehearsal place". On AppTown's start the preview reaper
removes a rehearsal's app container on the built-in host (it is labelled as
a preview); the rehearsal was already settled unknown, and the daily sweep
removes the rest.

**Restored copies** (`dataprotection/restored_copy.go`, `deploy.Gate`, `internal/gate`,
`cmd/apptown-gate`). A restored copy is a temporary deployment (kind
`restored-copy`) of a standing production deployment's current release, by
digest, on the source's own target, started from a chosen store copy, behind
AppTown's access gate. Every restored copy is a **restore test**: nobody
is admitted, and it is taken down as soon as its checks are recorded.
- **Where.** App Platform targets that make their databases only: the
  renderer places the gate (an internal service has no route, and its health
  check runs on its private port). Anything else
  is refused. AppTown needs `-restore-gate-image <ref@sha256:...>` (built
  by `make gate-image`) and a `-public-host` origin. The gate image is
  copied by digest into the registry's `apptown_gate` repository, never the
  project's: App Platform refuses an app naming two versions of one
  repository. A copy therefore needs room for that repository too (the
  starter plan's one repository is not enough).
- **Start** (operation `deployment.restored-copy` on the source; not stopped part way): a
  `restored_copies` row, then a deployments row `copy-<id>` (never listed:
  listings, health, conformance, placements and copies skip the kind), its
  need fulfilled (a new cluster), filled by start-from-copy's fill, rendered
  gated and deployed through the ordinary attempt (no release row: the
  project's release history is untouched), then checked through the gate.
- **The gate** is the app's only public component and only ingress, health
  checked over TCP; the public part runs with `internal_ports` only, workers
  do not start, integrations and registered needs are not given, and
  secrets follow the rehearsal profile rule. The gate secret is made per copy,
  delivered as the provider's `SECRET` to the gate alone, held in memory
  during the start, and kept only as its SHA-256.
- **Checks** run from AppTown to the gate's address with the secret in
  `AppTown-Restore-Token`: readiness, the front page, `rehearsal.checks`; no
  redirects followed, only status codes kept.
- **The gate check** (`checkGate`) follows, on every copy,
  so a restore test proves the door without a person signing in. From
  outside, AppTown asks the readiness path (which just answered its
  probe) with no token and with a wrong one, and enters with a made-up
  ticket. Each must be the gate's own refusal: the gate marks every refusal
  `AppTown-Gate: refused` and strips that header from whatever the app sends,
  so an app's 404 behind a gate that let the request through never passes
  (review GC-01). The 404s must also be empty and `no-store`; the entry
  refusal is 403. Only status codes and those facts are kept.
  It then issues a one-use ticket to itself (kept as its SHA-256, lasting
  a minute, `check_operation` naming the copy's start), enters with it once
  through the gate, which redeems it at `POST /gate/redeem` (303 and a
  session), presents it again (403), and asks the readiness path with the
  session. The ticket lives only in the operation's memory and is used up
  by the check. Redeeming a check ticket asks nobody's grant: it lets in
  only while the start that issued it is running and only for that copy,
  for two minutes, and its receipt is `deployment.gate-checked` by
  `apptown-system`. Each check is a step of the
  operation and a fact (`facts.gate`) on the copy and the
  `deployment.restore-tested` receipt; the card's restore-test line says
  how many passed and names each that failed. Any failure fails the copy
  (step "checking the gate").
- The copy is taken down as soon as its checks are recorded, whatever
  they found.
- **Take-down** (operation `deployment.restored-copy-end` on the copy, by a person with
  `deployment:restore`, or the `restored-copy-sweep` job every five minutes
  for every copy whose start has ended and left it standing): the app by the identity its attempt recorded, the clusters
  made for the copy (never one its source holds), then its records. No
  final copy. What a start made but never recorded is found by name and
  deleted only when it is provably the copy's: a cluster with the copy's
  project and deployment tags, an app whose gate carries the copy's own
  random mark (`app_marker`, written before anything is made). An app of
  that name without the mark is another's (two projects' names can join to
  the same app name) and is said, never deleted. A lookup that fails keeps
  the copy standing. What could not be removed is said on the card and tried again.
  A deployment is not taken down while a copy of it stands
  (`ErrRestoredCopiesStand`), so a project's deletion stops there too.
- **Safe facts only**: step names, the platform's own sentences, exit
  statuses and status codes. A gated deployment's failing component logs
  are not fetched, and a fill's command output is not kept.
- The card and `apptown data` show the last restore test apart from the last
  rehearsal, with AppTown's own check of its gate ("gate: N of M checks
  passed", and each that failed with its path and status), each copy
  still standing (being tested, or what its take-down left), what it can still do outside
  (`RestoredCopyWarning`) and who outside AppTown can read it
  (`ProviderBoundary`).

A retained row is settled `released` once a person has deleted it by hand.
While a row holding data is open, a new deployment of that
name on that target is refused (`ErrNameHeldByData`). The release the
deployment was serving gets a last line in its log: "taken down — <who>
deleted the deployment <name> on <target>; it no longer serves this
release".

A release is live (`isLive` on its detail) only while it is the newest
released on its deployment and that deployment still stands; a deployment
made again under the same name is another deployment. In the release list
and on a change, `serving` is true only when the release is live by that same
rule (`Releases.Live`) and its own deployment (by the id the release
recorded) is observed running: the same commit or landing running on another
deployment, or on a deployment made again under the same name, does not
count. A deployment on a Docker host is observed by asking its daemon for
its container; one on a provider's platform (App Platform), which has no
container, by the background observer's latest fresh observation, the one its
deployment listing shows. Until that observer has looked, it is unknown, "not
observed yet". The list does not match a deployment's landing run or commit,
which a promotion to App Platform has neither of.

## Display observations and health

Dashboard reads use authorized cached observations; a cache miss does not contact
a provider. Samples include previews and recheck placement and generation after
provider I/O. They cannot settle attempts or authorize commands.

Sweeps start immediately and wait 15 seconds between runs. Docker and cloud each
have up to four workers. Provider calls have five-second deadlines, the sampling
phase a ten-second budget, and identity revalidation up to five seconds. Oldest
or unattempted placements go first. These deadlines require operations that observe cancellation.

A target whose inventory can observe its whole fleet (`deploy.FleetObserver`)
is one job per sweep, not one per deployment. Portainer is one: one list of
the environment's app containers, through its Docker proxy, says for each
stack whether its `app` container runs. Running is running; a stopped or
missing app container is stopped; a failed list is unknown for every
deployment on the target. Current releases retain deployment, target, generation,
artifact and component identities. Their observation also verifies applied
Compose content, component labels and actual image digests. Historical stacks
without that expectation can report running state, but cannot prove release
health.

Samples are stale after 45 seconds or a refresh error. A failed refresh may retain
stale last-known state. Missing data or an in-flight replacement is unknown,
not stopped. Routing and live commands perform their own checks.

Portainer release readiness uses the admitted public HTTP predicate and exact
container identity. A pinned diagnostic runs in that container's network
namespace without application mounts, host networking or an application-image
tool dependency. It checks declared headers/status, static release identity and
startup grace, then rechecks component identity. It does not follow redirects or
claim external DNS, TLS or routed reachability. Public liveness intent is refused;
Portainer does not silently turn it into readiness. Init completion and declared
worker health also belong to the expected generation. Successful API apply or a
running container alone cannot complete a release. Recovery observes the retained
expectation without applying again and rechecks the original requester's current
authority.

A separate Portainer maintenance loop samples only settled standing generations.
Its narrow diagnostic permit rechecks the current project lifecycle, placement,
provider scope, artifact, restore hold and absence of an unfinished attempt.
It cannot apply, restart or remove the workload or settle a release. Display
reads never trigger diagnostics. The health cache exposes generation, artifact
and observation time only for a fresh matching runtime snapshot; restart,
replacement, failed cleanup and stale or unavailable samples leave health unknown.
The release handle retains historical evidence but does not make it fresh.

The separate [Docker health controller](healthchecks.md) can act on settled
standing deployments. Its probe policy currently comes from the project
repository, not frozen attempt inputs; unreadable declarations use a default
probe. [Maintenance](container-lifecycle.md) protects persistent resources and
active or uncertain execution. [Daemon shutdown](daemon-lifecycle.md) has one
bounded wait; remote operations may outlive it.

## Runtime diagnostics

A deployment's card has a Diagnostics line (`GET
/projects/{slug}/deployments/{name}/diagnostics`, the deployment read grant). It
is about the generation the deployment settled on: its parts and needs are the
ones that generation's applied record names, never the project's newest
declaration. Each part's container on a Docker host is inspected on the request
(five seconds each): running, stopped or absent, its last exit code (only when
not running), whether the kernel killed it out of memory, Docker's restart
counter for that container (a deploy starts a new one), its start and finish
times, and its Docker health check, "not configured" when it has none. A read
that fails is unknown, never stopped, absent or a clean exit: it shows the
facts last read for that generation's containers with when they were read and
when the failed read was tried. Those facts are kept in memory only, per
deployment, and settling on a new generation drops them; a container replaced
under the same name shows only when a read answers (its ID changes). Each part
names the generation its container is proven to run: the public part's
container by its identity labels; any other part's (which a rollout replaces
before the public part, and a failed rollout can leave) only when the
deployment, read after the containers, is still settled on its recorded
generation with no deploy open. Otherwise the view says why it is unsettled
and the part's generation is not known. Failure reasons are AppTown's words;
what a host's client wrote is not shown.

It also carries the background observer's state with its age, the last day's
limit events, and the health controller's crash-loop hold on the generation,
read from its receipt (`deployment.crash-loop-held`): a confirmed stop and an
attempted one are told apart, and a stopped part alone is never called held.
Limit events record the generation they happened under (migration 152): the
one the container's identity labels name (the public part's container), else
the one the deployment had settled on when the event came after it settled and
no later than the sweep's check that found it still settled, with no deploy
open (the sweep's event cutoff is taken before those checks); otherwise not
known. Events from before migration 152 have none. A target
without containers (App Platform, Portainer) says it does not report them and
lists the parts as unknown.

A part's recent log (`GET .../logs?part=`) is read only when someone asks, by
someone holding `deployment:operate` on the project, since a log can hold the
app's secrets: reading the deployment is not enough. The part must be one of
the settled generation's; AppTown names the container, inspects it and reads
that container by its ID, and says which generation it is proven to run by the
rule above. One read is `docker logs --tail 200 --timestamps` within ten
seconds, no follow, cut to its newest 64 KiB starting at a whole line, and
says when older output may exist. The log is returned to the caller and kept
nowhere: no receipt or artifact holds it, the response says `Cache-Control:
private, no-store`, and the web client holds it in the open view's state only,
outside its read and mutation caches. AppTown
does not redact secrets from it. Diagnostics observe; they never restart,
stop or settle anything.

## Disk space and container logs

A boot on a Docker host (a release, a recovery rebuild, a preview) reads the
filesystem of the daemon's data folder before it pulls, builds or starts
anything (`docker.AdmitDisk`): `docker info` names the folder and a
throwaway `df` container of the pinned busybox image reads it. Below the
floor, a twentieth of the filesystem but at least 1 GiB and at most 20 GiB, or when the
disk cannot be read, the boot is refused in its resolve phase with what is
free; the attempt fails with nothing changed on the host and the deployment
keeps serving its generation. The read is made for each boot; the capacity
view's five-minute reading is not trusted for admission. It is a headroom
guard, not a reservation: two boots at once, or images larger than the free
space, can still fill the disk, and a pull or start that then fails fails as
before. Stop, take-down and removal never read the disk. A host below its warning threshold (a tenth free, capped at
40 GiB and never below the refusal floor) is shown low; below the floor its capacity and each deployment's
card on it say new releases are refused. AppTown's own data folder and
its worker's disks are shown on Infrastructure (`GET
/infrastructure/disks`, `maintenance.Disks`), each its own reading.

Every container a boot starts (the public part, the other parts, a
preview's throwaway needs, a standing need, an init step) logs through
Docker's `json-file` driver rotated at 10 MiB with three files kept
(`execution.LogFlags`): at most about 30 MiB a container, its oldest output
discarded. The daemon's own defaults are not changed. A container started
without these flags keeps its own logging until it is made again: the public part
and the other parts are replaced by the next deploy; an init step runs anew;
a standing need's container is kept across deploys (its data is the point)
and takes the bound only when it is made again (its image or settings
moved, a limit lifted, or a take-down and a new deployment). It is never
recreated just to add the option. An operator who wants it sooner can
truncate that container's log file on the host (`truncate -s 0` on the path
`docker inspect --format '{{.LogPath}}'` names), which loses that output.

## Supported scope

[Target support](deploy-targets.md) lists the implemented drivers and their
differences. Terraform orchestration, arbitrary shared-service provisioning and
further cloud drivers are not built.

## Fresh-generation memory defaults

Environment resolves positive memory defaults only for fresh container-backed
execution, after the selected profile and operator overrides. Adoption supplies
the primary pack's exact published version in one snapshot; Dependency supplies
actual service-need catalogue defaults. Missing valid optional defaults fall
back to 512M; unreadable owners/publications refuse execution. Supplied workflow
ServiceSpecs use their positive memory or AppTown fallback, with ambiguous
assembled identities refused before effects. Their capped specs reach both local
and worker admission before creation or preview replacement.

AppliedProfile retains the default source and exact effective cap, plus a separate
supplied-service record. Source artifact Body/Hash stays the original selection;
rebuild applies recorded service caps through transient selected Source values.
Replay never fills an old zero cap from current defaults. Resource/capacity views
include supplied sidecars and keep applied declarations/defaults distinct from
observed use/limits and unknowns. App Platform's finite sizes/managed databases and
bucket hosting are excluded from container default resolution.

Explicit `memory: none` is a recorded operator override on Docker/Portainer,
protected by the existing deployment/release authority and reason requirement.
`memory_unlimited` distinguishes it from zero/omitted override fields and clear. It survives committed-memory omission until an
explicit clear, and applied records preserve it for replay. Defaults and
unlimited are visible on the resource card. A static application's declaration
does not exempt AppTown's own file server from the caps.

Supplied workflow services remain visible in deployment resource rows with their
`suppliedService` origin. Their limits are set in the workflow ServiceSpec
declaration; the app/need override action excludes them. This origin does not
grant authority or broaden the existing override owner's eligible parts.

## Catalogue-owned need sizes

A need may select `size` in its base declaration or a profile. The dependency
owner resolves the name to a local-container memory cap and complete command,
after profile selection and before execution. `sizes` in a dependency kind owns
those presets; each declares description, positive finite `resources.memory` and
literal `command` argv. Presets cannot alter images, connections, credentials,
data directories or copy behavior. Only MariaDB `small` (128M) ships initially.
Unsized declarations retain their previous behavior. `size` is reserved, never
an interpolated parameter or a `properties` escape. Effective source size and
resources on the same need conflict. Managed App Platform targets refuse sizes;
sharing, provider mappings and expert tuning are not implemented.

Validation checks every profile. Both local compose modes, previews and releases
use the dependency owner's resolution. Prepared services carry the resolved
command and cap through the worker boundary. AppliedPart retains the size name
and exact command alongside effective resources; replay uses that snapshot, not
the current catalogue. A pre-size applied record does not reinterpret an old
ignored source selector. Resource views expose the name separately from the cap
and operator override; an existing cap override does not change server settings.

Standing Docker service drift compares explicitly declared commands before
updating caps. Unreadable command state refuses execution. Changed commands on
real, non-production or unknown data follow the existing refusal policy, before
resizing or removal; synthetic data follows disposable replacement. No automatic
real-data retuning or maintenance is supplied. Commands omitted to use image
entrypoint defaults retain previous behavior; clearing an explicit command to an
image default is not detected by this check. A service whose kind declares
`configAtStart` (its start script sets everything it is configured with at
every start: object storage, Postgres, MariaDB, Redis) is stopped and remade
on its own volume whatever its data class (stopped first, so Redis writes
what it holds); one that also declares `keepsImage` (Postgres, MariaDB,
Redis: their files are their image's) takes the refusal policy above when
its image moved.

Need credentials. A kind's `generate:` names the credentials each deployment
and each preview gets its own value for (`dependency.LocalRuntime.Credentials`):
the Docker host's releases and landing deploys and Portainer's stack use the
deployment's (`deployment.NeedSecretsScope`), every preview its own by name
(`PreviewNeedScope`, chosen when it is prepared, so its identity is stable),
a rehearsal and a drill of a copy their operation's. The values are derived
from the built-in store's key (`secret.Vault.Derived`) and never stored; the
start scripts set them at every start, so a standing need takes its owner's
at its next deploy. A copy of a running need reads its credentials from its
container (`dataprotection.containerConnection`), so a container not yet
converted, and a restore into another deployment, use what that container
was started with. AppTown's own step servers and `apptown dev` use the
catalogue's values. A boot replaces the values it generated with `[secret]`
in a container's output before it cuts it short for a failure, and in the
failure it returns (`docker.Scope.Redact`, `Host.Boot`). Rotation is not
built.
The stock Postgres script makes the app's user an ordinary owner at every
start (converting a server whose app user is the superuser) and sets the
AppTown's password; the servers started beside AppTown's own steps make the user a
superuser (`stepEnv`, `dependency.LocalRuntime.Steps`).

### Database engine and volume evidence

A standing Docker need whose kind declares `keepsImage` records its actual
immutable engine image, data mount destination and volume incarnation in `deployment_storage_bindings`.
Deployment owns these records separately from `deployment_owned`: knowing which
engine may open data grants no permission to delete it. The incarnation is the
named volume's daemon creation timestamp (preserved without rounding) and driver,
within the deployment's target. It detects ordinary same-name replacement; it is
not protection against a privileged administrator forging daemon state.

Before starting even an existing stopped engine, or removing a container to
change configuration or limits, Docker compares the actual mount, image and
volume incarnation with this evidence. A matching binding permits recreation
without the old container, using the exact recorded image ID. The requested
image declaration must remain unchanged. A moved mutable tag never chooses new
engine bytes. An unavailable exact local image refuses recovery; there is no tag
fallback or automatic engine upgrade.

An unbound existing volume requires a matching existing container to witness its
actual image and mount. The owner records that observation before any writable
start or container removal. Without that witness, retained data refuses a start,
including synthetic data. New storage requires a witnessed absence, creation and
complete volume/image inspection, then a committed binding before engine start.
Unreadable Docker or database evidence refuses the operation. A successful
idempotent volume-create command by itself does not prove storage was new.

Confirmed owned-volume deletion retires its binding; failed deletion preserves
it. Deployment removal transfers every surviving binding into retained-resource
handles in the same transaction before deleting the deployment, including
observed volumes that AppTown never owned. Failed transfer prevents settlement.

For a whole-instance logical restore, data protection checks the recorded whole
backup and file digests before boot. Only its admitted `filling` rebuild receives
the separate storage-transition collaborator. Every engine-data target must be
absent before any needs start. This boot creates and inspects the targets, and the
owner compares the source binding and records source/target provenance in the
same transaction that establishes the new engine and incarnation. Logical import
then runs before the application starts. Ordinary reconcile cannot make this
transition. Physical copies additionally require the copy-time immutable engine observation
to match the source binding, and use that exact engine. The copied need and volume
must match the source; a mutable client image reference is not evidence. Older
physical copies without this provenance refuse automatic fill.

A custom catalogue change cannot discard an existing binding by clearing its
engine flag or data path. Standing-service preflight refuses that change before
provider effects, and administrative preview reports the conflicting binding.
Recovery also compares a nonempty changed mount destination with the recorded
path, including when the original container is gone. Physical restore preserves
that source path before creating its target; logical import records its explicit
fresh target's intended path. Ordinary unbound non-engine stores remain outside
this engine binding contract. Application dataDir uses a separate named-volume
identity and cannot select a need's retained volume.

Existing running engines can be inventoried with
`GET /api/v1/projects/{slug}/deployments/{name}/storage-bindings` and adopted with
`POST` to the same path (an empty body or `{}`). Preview needs deployment-read;
adoption needs deployment-operate and records the authenticated actor. Adoption
accepts no supplied engine evidence or override. It re-reads the exact settled
source revision and observes every persistent engine's actual image, mounted
volume and incarnation. A missing resource or conflicting observation refuses the
whole adoption. Preview never establishes bindings; repeated matching adoption
is idempotent, including containers started by their bound image ID.

Both operations require a settled Docker deployment with no open attempt,
release in flight, restore hold or take-down. Project and deployment admission
locks remain held during bounded inspection and atomic recording. They do not
start, pull, create, stop or delete Docker resources. Before upgrading an existing
instance, preserve a paired controller database/files recovery point and inventory
its persistent needs. Qualifying an installed candidate requires adoption and
readback of every applicable deployment before resuming writable reconciliation;
a restart that skips running containers is not adoption evidence. Conflicts retain
the original data and require explicit operator diagnosis.

A failed restore or process exit after target creation can leave storage requiring
explicit operator recovery. A later restore boot refuses preexisting engine-data
volumes, even one matching the source or an earlier target. It does not silently
reimport into, delete or rebind that data. Preserve the volume and source/target
audit evidence when diagnosing such a failure; retry requires an independently
verified recovery procedure.

The tier map is a runtime view of current deployment placements, not the release
ledger. Main and the configured release deployment use their own target's runtime
observation and current address. Unknown or unavailable observations are distinct
from stopped deployments; removed placements no longer appear. A replacement
under the same name has its own observation. Release numbers and historical
addresses remain on the departures board, rather than implying present runtime
state in the tier map.

## Customer names and release preferences

Connections, targets and deployments have a display label separate from their
machine key and UUID. Editing a label changes presentation only: it does not
rename URLs, provider resources, Git references, stored release requests or
receipts. Label edits compare the label the person read and record the change in
the same transaction. Connection credential/configuration changes retain the
connection's label.

A project's release preference stores one deployment UUID and a revision, or an
explicitly cleared choice. It never chooses the first provider, silently falls
back to `main`, or substitutes another deployment after deletion. The preference
is visible independently of release execution; each release command still names
its existing destination. Choosing a preference requires current deployment-bind
authority and an eligible standing, non-landing deployment in that project.
Existing provider placement and target-offer rules also apply. Reads retain an
unavailable choice so the person can clear or replace it.

Changing a preferred deployment into an ineligible role, or retiring its target,
requires clearing or replacing the preference first. Committed removal clears
that UUID and advances the preference revision in the same settlement. Failed
removal settlement preserves the preference. Editors retain unsaved choices and
the revision they started from when newer readings arrive; conflicts keep the
draft until the person saves successfully or discards it.

### Deployment addresses

A Portainer deployment owns its desired ingress independently of the project's
name, its repository, and the target's display label. The first address admission
records `app-<deployment UUID without hyphens>.<applications domain>` once. Later
project renames and target domain edits do not recompute that address. A customer
hostname is optional; it is served alongside the generated address and becomes
the preferred URL. Other providers retain their existing address owners.

Address edits require current deployment configuration authority and the revision
read before editing. A release that includes an address edit needs that authority
in addition to release authority. The release CLI reads the current address revision
before submitting a custom hostname; an unavailable read sends no release.
Admission records the address, hostname claims,
audit receipt and immutable release input together. A failure rolls them all back.
Queued execution and rebuild refuse missing or stale address snapshots before
provider preparation. The rendered host rules and certificate domain come from
that snapshot. The deployment's serving URL remains the last provider observation;
saving desired settings does not claim that traffic has already moved.

Hostname claims use normalized DNS names across the installation. Generated UUID
hostnames are reserved for their deployments. Editing or clearing a custom name
keeps earlier claims, because old traffic may still reach them. Confirmed deployment
removal releases its claims in the removal settlement transaction. Unknown or
failed teardown retains them.

An upgraded deployment keeps its raw former project hostname, target domain and
stored URL as evidence. Its address inventory starts unverified. An operator with
configuration authority inspects the recorded stack through the target's verified
connection and confirms the exact observed host rules and certificate domain.
Confirmation rereads the provider, checks stack ownership and the expected local
revisions, and records the observation with its receipt. It performs no remote
write and does not lift a restored-copy hold. Unsupported rules, missing stacks,
ambiguous ownership and changed observations refuse confirmation. The existing
provider addresses remain unchanged.

Historical duplicate claims retain every claimant, so no deployment wins by
migration order. Conflicted hostnames refuse release until the conflicting
resources are resolved. Unverified legacy inventories block acquisition of new
custom hostnames; they do not block fresh UUID-generated addresses. Old pending
release inputs without an address snapshot require a new release after the
inventory is confirmed. Historical release records remain readable.

### Release preference setup and permission policy

A project with no recorded preference is different from one whose preference
was explicitly cleared. A cleared preference stays cleared. Older installations
can retain their configured release destination as an unverified setup choice,
including its original deployment name. Conflicting or unavailable environment
information requires an explicit choice; no provider is selected by list order.

Setup records the standing placement, its promotion policy and the preferred
UUID in one transaction. It requires current binding authority and, when adding
a promotion requirement, promotion authority. It does not start a release. A
later refused release leaves the saved preference visible as settings only.

The explicit promotion requirement is separate from data classification.
Changing it in either direction requires both binding and promotion authority,
a current policy revision and an audit receipt. Named releases and redeploy overrides require
deployment permission; protected data/audiences or explicit policy additionally
require promotion permission. Queued execution retains the admitted requirement
and also checks current policy. Relaxing policy cannot erase the gate of an
already admitted request.

The tier map is a runtime view of current deployment placements, not the release
ledger. Main and each customer standing deployment use their own target's runtime
observation and current address. Each customer placement appears with its own
UUID and display label, including multiple placements of the same project.
Unknown or unavailable observations are distinct from stopped deployments;
removed placements no longer appear. A replacement under the same name has its
own observation. Release numbers and historical addresses remain on the
departures board, rather than implying present runtime state in the tier map.

### Explicit release setup from clients

The release page offers setup when no usable default is selected. Its setup form
shows imported choices and customer environment labels, retains the revisions it
started from, and saves the preference as a separate action. Saving setup does
not start a release. If a later release is refused, the saved preference remains
configured and the refusal is shown separately. A cleared preference is never
refilled by reading the release page or requesting a release.

`apptown release-setup <project>` reads the current preference and offered
customer environments. `--target <UUID> --name <deployment> --save` explicitly
saves setup without releasing; `--require-promotion` adds the promotion policy.
An imported choice supplies only its recorded target and deployment name, and
retains its required promotion policy. An unavailable imported target requires an
explicit replacement. The command sends the default and target revisions it
read, and reports a conflict instead of choosing a different environment.

`apptown release <project>` reads the selected default's plan and sends that
revision with the release request. It never saves setup. A custom hostname also
carries the displayed address revision. Both data-copy acknowledgements apply
to default and named releases; customer labels are presentation, not identity.

### Shared container inventory placement

Docker and Portainer inventories capture the verified daemon identity with each
resource observation. Portainer also retains the observed stack name. Cached
observations retain that identity; response shaping never assigns them a newer one.
A compose stack matching a current deployment is attributed to another environment
only after its recorded Portainer connection verifies the same daemon. Customer
environment labels describe that placement. Missing or changed provider scope is
unavailable evidence, not an orphan. Ambiguous generated stack identities refuse.
If placement verification fails while reading the infrastructure view, that target
shows only its authoritative records with an explicit error and unknown provider
inventory; it does not classify unverified observations as missing or unrecorded.
Other targets remain visible. A failed inventory adapter is also shown at its
target. Failure to read the target list or authoritative records still refuses
the fleet reading. Unknown inventory is distinct from an empty provider answer.

Direct Docker and Portainer orphan removal requires a fresh, uniquely matching
observed resource and the same placement check before audit admission or deletion.
Portainer requires both the observed stack name and its ID, and uses one verified
configuration and credential snapshot for the inventory read and deletion. Recorded resources
and verified shared placements must be retired through their deployment. An item
missing from the filtered observation is not eligible for removal through this door.

New Docker standing boots receive the exact owner-produced front-door origin,
using the immutable serving label or reserved direct port. Newly prepared previews
capture their exact configured route before their public digest and worker request
are formed; domainless local previews have no configured origin. Existing admitted
requests keep their captured bytes, and sealed rehearsals keep the source origin.
Portainer apps use their admitted deployment ingress hosts. Fresh preparation
intentionally removes prior wildcard trust of sibling projects; management aliases
do not add application hosts.

### App Platform readiness evidence

App Platform artifacts contain the provider spec and a versioned public
readiness predicate. The canonical hash includes the predicate; only the provider
spec is sent to DigitalOcean. Existing recorded `do-app-spec` artifacts keep their
recorded provider spec and historical successful-response checks.

The driver checkpoints the artifact format and readiness predicate with the exact
provider deployment identity. Recovery refuses malformed new records instead of
falling back to a weaker check. A public explicit HTTP expectation must be a 2xx
status and match without redirects after native readiness succeeds. Private-service
exact predicates are refused because the provider does not express them and the
public ingress does not reach those services. Omitted and historical predicates
retain their redirect behavior. A post-probe provider read must still name the
same active deployment; successful apply or a changed deployment is not proof.

Gated copies remain checked by Data Protection through their access gate. Their
driver observation remains unknown; provider ACTIVE does not replace the gate,
readiness, page or data checks. These release-time samples do not establish
maintained fresh App Platform health for unattended release decisions.

Standing generations created with an immutable connector association retain a
versioned health receipt in their completed handle. It binds the admitted
artifact, generation, provider app and deployment, connector UUID, API base,
public predicate and long-running component names. Recovery carries this receipt
through completion; an older completion without one remains unknown until a new
governed deployment supplies it. Credentials and environment values are not
receipt content; credential rotation within the same connector is supported.

The deployment owner's background sampler checks the current standing-project,
restore, target and attempt state before observation and again before accepting
its result. It observes native component health and replica readiness between
reads of the exact active provider deployment, then asks any admitted public
predicate and release witness. Missing or ambiguous component evidence, a changed
identity or unavailable provider yields unknown. The provider report has no
sample timestamp or deployment ID: this is a bounded bracketed observation,
not an atomic provider snapshot. The health timestamp records local observation
completion. Worker readiness describes provider state, not an undeclared custom
application check. Rate limits and sweep delays may leave health unknown or stale.

Display inventory never starts this sample and ACTIVE alone is not health.
The existing maintained-readiness cache admits only fresh matching generation
and artifact evidence; historical release success cannot substitute for it.
