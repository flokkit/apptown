# App Platform target setup

A DigitalOcean App Platform target runs each deployment as an App Platform
app. A declared Postgres becomes a managed database, and declared object
storage becomes a Spaces bucket. This target is experimental: no real app has
been qualified on it yet.

## What you need

- The **App Platform targets** feature turned on under **Setup → Features**
  ([Features](features.md)).
- A DigitalOcean account, and one of its regions: `syd`, `sgp`, `blr`, `nyc`,
  `sfo`, `tor`, `lon`, `fra` or `ams`. Add one target per account and region.
- The account's container registry. App Platform pulls images only from it.
  If the account has none, AppTown creates one on the first release, on the
  Starter plan. Starter holds one repository and Basic five. Each project
  released here takes one, and a restored copy takes one more for AppTown's
  gate image. A release that would not fit is refused before anything is
  made, so choose Basic or larger for more than one project.
- Spaces turned on for the account, if any app declares object storage.
- For backups, a backup store and a copy runner ([Deployment
  backups](deployment-backups.md)).

## The credential

Create a personal access token under **API → Tokens** in DigitalOcean's
control panel, for a team that holds only AppTown's apps. A custom-scoped
token needs create, read, update and delete on apps, databases, the container
registry, Spaces keys and tags, and read on the account.

Store it as an AppTown secret, under **Integrations › Secrets** or with
`apptown secret set DIGITALOCEAN_ACCESS_TOKEN < file`.

The token makes and deletes Spaces keys. For each bucket it makes a key that
reaches that bucket alone. While it makes or deletes a bucket it also makes a
full-access key named `apptown-maker-…`, and deletes it again straight after.
A maker key left behind by an interruption is listed under **Not accounted
for** on the target's page as a live full-access key.

## Add the target

Open **Deployments › Targets → Add a deployment target** and choose
**DigitalOcean App Platform**.

| Field | What to enter |
| --- | --- |
| name | A short name for the target, such as `do-syd` |
| token secret | The name of the secret holding the token |
| region | The region apps run in |

**Check** reads the account and its container registry, and says how many of
the registry's repositories are used. When the account has no registry, the
form asks for **container registry to create**: a name, unique across
DigitalOcean. Nothing is recorded until you press **Add target**. Then set its
offer and facts. Apps get App Platform's own address, so there is no apps
domain.

## Check it worked

The target's page shows **Target check:** with the account, and the
**Container registry** line with its plan and the repositories used. With
**Edit target** you choose the copy runner; until one is chosen the page says
backups and restore tests are unavailable. The **Capacity** page lists the
size each part runs on with its listed price, and an estimated monthly cost.

## What AppTown creates and deletes

For each deployment AppTown creates:

- an App Platform app named `apptown-…`;
- for Postgres, a managed database cluster `apptown-db-…`, tagged `apptown`
  and with the project, deployment and installation;
- for object storage, a Spaces bucket `apptown-<project>-<deployment>-…`, and a
  key for it alone;
- one container registry repository per project, named after the project.

Taking a deployment down deletes the app, takes a final copy of its database
and bucket and checks it comes back, then deletes them. Without a copy runner
the take-down is refused unless you choose to go without a final copy; the
database then stays, listed as retained, and keeps costing money. When the
project's last deployment on the account goes, its repository's images are
deleted.

AppTown never adopts an app, cluster or repository it did not create, even
one with the same name; the release is refused instead. It never deletes
them.

The registry frees space, and an emptied repository's place on the plan, only
in a garbage collection. AppTown requests one after its image clean-up removes
old images, when no App Platform release is running. You can also start one in
DigitalOcean's control panel. While a collection runs, the registry refuses
pushes, so a release started then fails and says so.

## Costs and limits

- Apps, managed databases, the registry plan and Spaces are billed by
  DigitalOcean. The estimate covers app sizes only: managed databases are
  not priced by DigitalOcean's API, and are left out.
- Postgres and object storage are the only services offered. Every part and
  `init` step must use the same image.
- **Restart** works. **Start** and **Stop** are disabled, because App Platform
  cannot keep an app stopped.
- Container logs, diagnostics and limit events are not reported.
- A Spaces key can make an object public with a `public-read` ACL. Apps must
  not; it publishes the object at Spaces' address.
