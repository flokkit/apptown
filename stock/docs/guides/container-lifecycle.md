# Previews and deployments

A **preview** is a temporary copy of your app, booted to try a change. A
**deployment** is a named, standing copy of your app on a deployment
target: `main`, fed by every landing, and any others the project adds. This
guide covers where each runs, how long it lasts, taking a deployment down,
and backups. The containers behind chats and workflow steps have their own
rules, in [What chats and steps can use](/docs/guides/runtime-environments).

## Previews

A preview boots the app **cold** from the `.forge/app.yaml` of the commit
being previewed: nothing carries over from a chat's warm session. If it
comes up, it comes up from the declared contract alone. Use **Try it** on a
change, `forge preview <change>`, or a workflow's preview step.

**Where it runs.** On a forge with a worker (a separate machine for project
code), every preview of a change or a run boots on the worker, with no
fallback to the forge's own host. It is reached by name, at
`change-<number>.<preview domain>` or `run-<number>.<preview domain>`.
Without a worker, previews boot on the forge's own Docker.

**How long it lasts.** A preview is removed when its change closes or its
run finishes, and every preview is removed when the forge restarts. On a
forge whose operator has switched on clean-up of its own Docker, a preview
older than about two hours is also removed. In each case the answer is the
same: press **Try it** again and it boots again from the contract.

A preview's services and data are disposable. It is not a copy of
production data; see [Services and data](/docs/guides/services-and-data).

## Deployments

A deployment stays up until a new release replaces it or someone takes it
down. It restarts on its own, and the forge's clean-up never removes it.
Its backing services (its database, its cache) belong to it.

`main` is fed by every landing. A project adds other deployments under
**Settings › Deployments**, choosing a name, one of the targets offered to
the project, who it is for, and whether its data is production data. Those
are fed by promotion: **Deploy latest** on the deployment's card ships the
latest release there; `forge release <project> <deployment>` does the same
from the command line and follows its log. `forge release <project>` with no
deployment named promotes what landed on `main` to the intranet.

A landing's deploy step names each phase in its log as it starts: building
and publishing images, starting services, running init, starting parts,
checking readiness and recording the release. The log stays on the step
afterwards, including after a failure. The step's outcome, not the last
line, says whether the deploy worked.

**Deployment procedure.** Standing deployments use `compatible-update`, the
name for Forge's existing update behavior. It does not certify that your changes
are backwards compatible or promise automatic restoration of application/data.
The configuration API accepts `deploymentPolicy`; omitted, null or empty means
`compatible-update`, and existing deployments report that same default.

`protected-maintenance` is a known, unavailable procedure: quiescence, verified
complete recovery, private validation and reopening. Selecting it is refused
before configuration or provider effects (422, code
`deployment_policy_not_implemented`); use `compatible-update`. Unknown values
are invalid. The configuration and named deploy APIs reject unknown request
keys. A deployment procedure cannot be overridden on a single deploy request.
Only one procedure executes, so no alternative selection is saved. The setting
belongs to Forge's standing placement, not to your app's contract or database.

**Kinds of target.** An administrator attaches targets and offers them to
projects; you choose among those offered.

| Target | What it runs |
|---|---|
| Built-in Docker host | The forge's own Docker |
| Docker host | Another machine's Docker, attached over SSH |
| Portainer environment | A stack in an existing Portainer |
| App Platform | A DigitalOcean App Platform app; a declared Postgres becomes a managed database |
| Bucket website | Static sites only, served as files from an S3-compatible bucket (DigitalOcean Spaces first) |

A bucket website takes an app with exactly one public `static` part, no
`init` steps and no `needs`; anything else is refused, with the reason.
Each such deployment owns one bucket the forge creates. Don't keep anything
else in it: taking the deployment down deletes it. Custom domains are not
supported yet. See the [contract reference](/docs/guides/contract-reference)
for declaring a static site.

**Where the release is built.** On a forge with a worker, a landing builds
its release images on the worker and stores them in the project's
container registry; the target pulls them by digest. Nothing is built on
the target. If the project has no container registry yet, the landing
attaches the one the forge offers; with none offered, or several, it stops
and says which choice to make.

## Taking a deployment down

A deployment other than `main` has **Tear down** on its card under
**Settings › Deployments**. `main` goes only when its project is deleted.
A take-down removes exactly what the forge recorded creating for the
deployment. What it does depends on the target:

| Target | On take-down |
|---|---|
| Docker host (either kind) | Takes a final backup of its data, then deletes its containers, network, data volumes and images. If the final backup is incomplete, the parts are started again and nothing is removed. Stop anything outside the deployment that writes to its data first. |
| App Platform | Deletes the app, and its images when no other deployment of the project on that account uses them. The managed database stays at DigitalOcean; the forge takes no backups there. |
| Portainer environment | Takes a final set through Portainer first, then deletes the stack the forge made and the volumes its deploys made. A volume that was there before is copied and kept. |
| Bucket website | Deletes every file and the bucket, and checks the address stops answering. |

A deployment of an app that needs AI models also has its key revoked. The
result names what was deleted, the final backup, and anything left at the
target, with why. Data left at the target (a Portainer
volume, an App Platform database) is listed as retained on the
Infrastructure page, and while it is, a new deployment of that name on that
target is refused.

Deleting a project stops its new work at once. For ten minutes the
deletion can be cancelled; then its deployments are taken down, one at a
time, with `main` last. If one is refused (its final backup falls short,
its target cannot be reached), the deletion stops there and says why: the
deployments already taken down stay taken down, each with its receipt and
final backup, and nothing is put back. Because `main` goes last, a refusal
of any other deployment leaves `main` serving. Deleting again carries on
with the deployments that are left.

Once none is left the project is deleted and kept, read-only, with all its
backups, for 30 days. Until its purge starts, **Restore project** brings it
back, with no deployments. Then it is purged: its backups, its repository
and its records are removed, and its GitLab copy is renamed and archived,
never deleted. An administrator can purge sooner.

## Backups and restore

The forge backs up deployments on Docker hosts, the built-in one and
attached ones: the app's `dataDir` volume and each need that keeps data,
copied the way the need declares (Postgres and MariaDB are dumped as SQL;
Redis is told to save, then its snapshot is copied). A need that declares no copy (the S3
emulator) and the app's volume are copied as files while they run;
each such item is marked "file copy, may be inconsistent", because a copy
taken mid-write can be torn. An app that declares neither has nothing to
back up. A
Portainer environment, App Platform or a bucket keeps its own; the forge
takes none there.

- **A need the forge cannot read.** If the contract declares a need whose
  definition the forge cannot read (not in the catalog, or a definition
  that does not parse), each set says that need's data was not copied, and
  why, and is partial; a restore into that deployment is refused.
- **When.** Every day, after every release, and on demand. The newest 14
  sets of a production deployment are kept, and the newest seven of any
  other.
- **Restore.** A restore replaces what is in the deployment, so the forge
  takes a safety set of what is there first. It then stops every part of the
  deployment, so nothing writes while the data goes back, and starts them
  again afterwards. A part that will not stop refuses the restore before
  anything is written; a part the running release names that is not there is
  reported as not started. The
  set's data volume goes into the app's `dataDir` volume and each need into
  the need of the same name, as the project's contract declares them. If
  the set holds something with nowhere to go (data but no `dataDir`, or a
  need the contract no longer declares), the restore is refused and nothing
  changes. Something the destination has that the set lacks is left as it
  is.
- **What a restore claims.** Each item says what witnessed it: *verified*
  (the need's declared check confirmed the service holds the copy, as for
  Redis, which is restored with its container stopped and loads the copy
  when it starts), *loaded* (the need's declared restore ran in the running
  service, as `pg_restore` does), or *files copied, not verified* (the
  app's volume, a need with no declared restore, or a set from before
  8 October 2026). The restore as a whole is "restored" only when no item
  is merely copied; otherwise "restored, not verified", or "partly
  restored" when an item failed.
- **What a copy leaves out.** A need whose copy is known to leave
  something out says so with the set, in the question before a restore
  and in its result. S3's copy holds each object's bytes and headers but
  not its ACLs, tags or versions, and an object uploaded in parts comes
  back with a new ETag; a set taken before 17 October 2026 is a file copy
  without object metadata.
- **Final backup.** A take-down's final set is kept with its project, until
  the project is purged. It can be restored into another standing deployment of the same project on a
  Docker host, under the rule above; not into App Platform, Portainer or a
  bucket.

The **Backups** tab of the Deployments page lists every set, with **Back up
now** and **Restore**. Restoring a deployment's own set asks you to confirm;
restoring a final set asks you to type the name of the deployment to
restore into. The tab is shown to people who can see the forge's
infrastructure. Taking and restoring a backup need the right to deploy to
the project, and are also on the API:
`POST /api/v1/projects/<project>/deployments/<name>/backups` takes one now,
and `POST .../backups/<id>/restore` with `{"confirm": "<name>"}`, the
destination deployment's name, restores one into it.
