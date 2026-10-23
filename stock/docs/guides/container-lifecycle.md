# Previews and deployments

A **preview** is a temporary copy of your app, booted to try a change. A
**deployment** is a named, standing copy of your app on a deployment
target: `main`, fed by every landing, and any others the project adds. This
guide covers where each runs, how long it lasts, taking a deployment down,
and backups. The containers behind chats and workflow steps have their own
rules, in [What chats and steps can use](runtime-environments.md).

## Previews

A preview boots the app **cold** from the `.apptown/app.yaml` of the commit
being previewed: nothing carries over from a chat's warm session. If it
comes up, it comes up from the declared contract alone. Use **Try it** on a
change, `apptown preview <change>`, or a workflow's preview step.

**Where it runs.** On an AppTown with a worker (a separate machine for
project code), every preview of a change or a run boots on the worker, with
no fallback to AppTown's own host. Without a worker, previews boot on
AppTown's own Docker. A preview is reached by name, at
`change-<number>.<preview domain>` or `run-<number>.<preview domain>`.

**How long it lasts.** A preview stops eight hours after anyone last looked
at it (opened a page of it, or booted it), and is removed when its change
closes or its run finishes. A change's preview keeps its built images and
its snapshot for thirty days after the last look, so booting it again is
quick. Either way, **Try it** boots it again from the contract.

A preview's services and data are disposable. It is not a copy of
production data; see [Services and data](services-and-data.md).

## Deployments

A deployment stays up until a new release replaces it or someone takes it
down. It restarts on its own, and AppTown's clean-up never removes it.
Its backing services (its database, its cache) belong to it.

`main` is fed by every landing. A project adds other deployments under
**Settings › Deployments**, choosing a name, one of the targets offered to
the project, who it is for, and whether its data is production data. Those
are fed by promotion: **Deploy latest** on the deployment's card ships the
latest release there; `apptown release <project> <deployment>` does the same
from the command line and follows its log. `apptown release <project>` with no
deployment named promotes what landed on `main` to the intranet.

A landing's deploy step names each phase in its log as it starts: building
and publishing images, starting services, running init, starting parts,
checking readiness and recording the release. The log stays on the step
afterwards, including after a failure. The step's outcome, not the last
line, says whether the deploy worked.

**How a deploy replaces what runs.** A deploy starts the new release in place
of the old one (the procedure called `compatible-update`). It does not check
that the new code can read the old data, and it does not put data back if the
new release fails. A change that rewrites data incompatibly needs its own
plan: stop writes, take and test a backup, then deploy.

**Kinds of target.** An administrator attaches targets and offers them to
projects; you choose among those offered.

| Target | What it runs |
|---|---|
| Built-in Docker host | AppTown's own Docker |
| Docker host | Another machine's Docker, attached over SSH |
| Portainer environment | A stack in an existing Portainer |
| App Platform | A DigitalOcean App Platform app; a declared Postgres becomes a managed database |
| Bucket website | Static sites only, served as files from an S3-compatible bucket (DigitalOcean Spaces first) |

A bucket website takes an app with exactly one public `static` part, no
`init` steps and no `needs`; anything else is refused, with the reason.
Each such deployment owns one bucket AppTown creates. Don't keep anything
else in it: taking the deployment down deletes it. A bucket website has no custom domain. See
[the app contract](contract-reference.md) for declaring a static
site.

**Where the release is built.** On an AppTown with a worker, a landing builds
its release images on the worker and stores them in the project's
container registry; the target pulls them by digest. Nothing is built on
the target. If the project has no container registry yet, the landing
attaches the one AppTown offers; with none offered, or several, it stops
and says which choice to make.

## Taking a deployment down

A deployment other than `main` has **Tear down** on its card under
**Settings › Deployments**. `main` goes only when its project is deleted.
A take-down removes exactly what AppTown recorded creating for the
deployment. What it does depends on the target:

| Target | On take-down |
|---|---|
| Docker host (either kind) | Takes a final backup of its data, then deletes its containers, network, data volumes and images. If the final backup is incomplete, the parts are started again and nothing is removed. Stop anything outside the deployment that writes to its data first. |
| App Platform | Deletes the app, takes a final copy of its database (and its bucket) when a copy runner and backup store are set, checks the copy comes back, and only then deletes the database and the bucket. Without a copy runner the take-down is refused unless you choose to go without a final copy; then, or when the copy falls short, the database stays at DigitalOcean, listed as retained. Its images go when no other deployment of the project on that account uses them. |
| Portainer environment | Takes a final set through Portainer first, then deletes the stack AppTown made and the volumes its deploys made. A volume that was there before is copied and kept. |
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

AppTown backs up a deployment's data: its public part's `dataDir` and each
need that keeps data. How each need is copied, and what its copy leaves out,
is in [Services and data](services-and-data.md). Setting up a
backup store and a copy runner is in
[Deployment backups](deployment-backups.md).

| Target | What is copied |
| --- | --- |
| Docker host (either kind) | the data volumes and needs, each its own way; a set is kept on AppTown's disk, or in the backup store when one is attached |
| Portainer environment | the same, through Portainer (an edge environment in async mode has no way in, and is refused) |
| App Platform | the managed Postgres and the deployment's bucket, from the target's copy runner into the backup store, when both are set; DigitalOcean's own daily backups are shown beside them |
| Bucket website | nothing: it holds no data |

- **When.** After every release, every night, and on **Back up now**. The
  newest 14 sets of a production deployment are kept, and the newest seven
  of any other.
- **Partial sets.** A set says which items it could not copy and why (a need
  whose definition AppTown cannot read, a volume nothing mounts). A restore
  into a deployment with an item that cannot be put back is refused before
  anything changes.
- **Restore.** A restore replaces what is in the deployment, so AppTown
  first takes a safety set of what is there. It then stops every part, so
  nothing writes while the data goes back, and starts them again
  afterwards. A part that will not stop refuses the restore before anything
  is written. Each item goes back into the volume or need of the same
  name; a set holding something with nowhere to go (data but no
  `dataDir`, a need the contract no longer declares) is refused and
  nothing changes. A restore runs as an operation: `apptown wait operation
  <n>` and its page show each step. If AppTown restarts in the middle, the
  restore ends `unknown` and names the containers it may have left
  stopped; AppTown does not start them, because the restore may still be
  writing. Check the host, then release the deployment again.
- **What a restore claims.** Each item says what witnessed it: *verified*
  (the need's own check confirmed the service holds the copy), *loaded* (the
  need's restore ran in the running service), or *files copied, not
  verified* (the `dataDir` volume, or a need with no declared restore). The
  restore as a whole is "restored" only when no item is merely copied;
  otherwise "restored, not verified", or "partly restored" when an item
  failed.
- **What a set covers.** A deployment's card (and `apptown data`) says, item
  by item, what its backups cover, and marks the rest *not assessed*: an
  item nothing could copy, and always data the app keeps outside its
  declared needs (a bucket or database it reaches with its own settings).
  AppTown cannot tell from an app's settings which outside stores hold its
  data, so a backup, a rehearsal or a restore test is evidence for the
  items it covers, not for the whole app. Protect such a store with its
  provider's own backups, and rehearse its recovery yourself; AppTown's
  backup store is not a place for an app's uploads.
- **Final backup.** A take-down's final set is kept with its project until
  the project is purged. It can be restored into another standing
  deployment of the same project on a Docker host, under the rules above.

The **Backups** tab of the Deployments page lists every set, with **Back up
now** and **Restore**. Restoring a deployment's own set asks you to confirm;
restoring a final set asks you to type the name of the deployment to
restore into. The tab is shown to people who can see AppTown's
infrastructure. Taking and restoring a backup need the right to deploy to
the project, and are also on the API:
`POST /api/v1/projects/<project>/deployments/<name>/backups` takes one now,
and `POST .../backups/<id>/restore` with `{"confirm": "<name>"}`, the
destination deployment's name, restores one into it.
