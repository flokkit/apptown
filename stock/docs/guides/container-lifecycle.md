# Previews and deployments

A **preview** is a temporary copy of your app, booted to try a change. A
**deployment** is a named, standing copy of your app on a deployment
target. A project chooses which deployments follow main and which take manual releases. This
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

A new project gets a `main` deployment that follows main on the default target
when its creator has permission to deploy. With local Docker enabled, this is
the built-in Docker host; without it, this is the single eligible attached
Docker host. The setting records the creating person and credential. An agent
credential leaves it unconfirmed: a person saves **Follows main** in the
deployment’s **Release feed** setting before automatic releases run. Without a default target or deploy permission,
nothing deploys yet.

A project adds deployments under **Deployments**, choosing a name,
a target offered to the project, an audience and a data class. A follows-main
deployment requests the runtime bundle from every completed landing. Its
setting records who authorized those automatic releases and with which
credential. Create that setting through
`POST /api/v1/projects/<project>/deployments`, for example:

```json
{"name":"main","target":"<target>","feed":"main","audience":"team","data":"synthetic"}
```

This requires permission to bind and deploy to that target, including
protected-release permission where needed. Posting an existing deployment
again does not change its feed, audience or data.

A deployment's audience and data class change in its **Settings**, under
**Audience and data** (`PUT /api/v1/projects/<project>/deployments/<name>/classification`
with the new values and the values you edited from). The page lists, before
you save, what the change adds or removes: release permission for deploys,
how many backup sets are kept, AppTown's daily copies on App Platform and AWS
ECS, and whether a Docker host's release may discard test data. Values the
target or its offer refuse are greyed with the reason: Fly.io and AWS ECS keep
only a public audience. Lowering either one asks you to confirm a sentence
saying what it now holds and serves; that needs a person signed in, the
permission to configure deployments and, for a protected deployment, release
permission, and is recorded as its own act. A deployment that started from a
copy of production data keeps production data. A deploy already waiting when
the class changes is refused and asked for again. The audience is AppTown's
rule for who may deploy and what is refused; it puts no sign-in in front of
the app. A deployment that has run stays on its target: to run it elsewhere,
make a new deployment there and move its data with a backup. The card's creation form adds a
manual-release deployment. Migrated follows-main settings need **Confirm follows
main** before automatic releases can run.

Each deployment's **Settings** section chooses how it updates:
**Follows main**, **Promoted from** another deployment of the same project,
or **By hand**. Promotion sources cannot form a cycle. Saving a feed requires
deploy permission, plus protected-release permission for a protected deployment.
The same settings edit chooses **Needs approval**: none or a project role.
Settings refuse an edit if someone has changed the feed or approval rule since it was read.

**Promote release N** deploys the release the source last deployed successfully,
even when a newer bundle exists. It is disabled with a reason when the source
has been taken down, has never deployed, or is currently observed stopped.
The web Promote button selects the release it shows. If the source has since
deployed a different bundle, AppTown refuses and asks you to promote again.
The CLI selects the source’s last successful release when you request it and
prints which release it promoted in the log.

Unknown or stale health does not block promotion. The release records
“promoted release N from <source>”.
**Deploy latest** on a by-hand deployment requests the latest runtime bundle.
`apptown release <project> <deployment>` performs the action its feed selects.
Set the feed from the command line with:

```sh
apptown deployment set-feed <project> <deployment> main
apptown deployment set-feed <project> <deployment> manual
apptown deployment set-feed <project> <deployment> promotion --from <source-deployment>
```

New deployments default to **By hand**. Choosing **Follows main** records you
and your credential as its standing authorization. AppTown checks your current
permissions again before each automatic release. `apptown release <project>` uses the project's chosen release
preference. Each release has its own log and outcome.

A release with an approval rule waits for a person holding that role on the
project and `release:approve` on the deployment. The requester cannot approve
their own release, including through another credential. Agent tokens cannot
approve. On the deployment page, **Approve** accepts the displayed sentence:
“I accept release N running on <deployment>”. The release and deployment
history retain the person's principal, how they assented, the time and meaning.
Home's **Waiting on you** and Attention link eligible approvers to that page.
The requester or an approver can **Cancel** a waiting release. A newer request
to the same deployment supersedes a waiting one and records that fact.

Follows-main releases wait in the same way. The landing is complete while its
release waits. Before any provider effect or backup, AppTown checks the
requester's authority and the approver's current role, permission and credential
again. Losing that authority stops the release. Changing the approval rule
requires a new request.

```sh
apptown deployment set-feed <project> production promotion --from staging --approval-role release-manager
apptown release approve <project> <release-number> -y
apptown release cancel <project> <release-number>
```

The approve command displays the meaning before submitting it. Without `-y`, it
shows the question and asks you to run it again to accept. Use
`--approval-role none` to remove an approval rule. A feed edit without that flag
keeps the current rule. MCP has no release approval tool.

A release checks its pre-release copy before starting services, running init
steps and starting app parts. It then checks readiness and records the outcome.
The release keeps its log and copy evidence, including after failure.

**How a deploy replaces what runs.** A deploy starts the new release in place
of the old one. It does not check
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

A deployment that does not follow main has **Tear down** on its card under
**Deployments**. Follows-main deployments go when their project is deleted.
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
target page under Deployments › Targets, and while it is, a new deployment of that name on that
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

- **When.** Before and after every release, every night, and on **Back up now**. The
  newest 14 sets of a production deployment are kept, and the newest seven
  of any other.
- **Partial sets.** A set says which items it could not copy and why (a need
  whose definition AppTown cannot read, a volume nothing mounts). A restore
  into a deployment with an item that cannot be put back is refused before
  anything changes.
- **Where.** Back up now and **Restore from a backup…** are on the
  deployment's ⋯ menu. A restore offers the deployment's own sets and the
  final sets kept from the project's taken-down deployments. A new
  deployment starts from a set in the project's new-deployment form. The
  Backups page lists every set; a row opens its deployment.
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
