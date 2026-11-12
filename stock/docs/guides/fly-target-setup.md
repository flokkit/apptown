# Fly.io target setup

A Fly.io Machines target runs each deployment as a Fly app of its own, with
one Machine per part. The app is reached at its `fly.dev` address. This target
is experimental: no real app has been qualified on it yet.

## What you need

- The **Fly.io targets** feature turned on under **Setup → Features**
  ([Features](features.md)).
- A Fly.io organization, preferably one that holds only your team's apps.
- A container registry attached to each project that deploys here. Fly runs
  only images published there by digest, for `linux/amd64`. AppTown copies
  each image into Fly's own registry for the app.
- For backups, a backup store ([Deployment backups](deployment-backups.md)).

## The credential

Create an organization token with Fly's command line:

```sh
fly tokens create org
```

A deploy token, limited to named apps, is refused: it cannot create apps. An
organization token can manage every app in its organization; AppTown touches
only the apps it made and recorded. Store the token as an AppTown secret, under
**Integrations › Secrets** or with `apptown secret set FLY_API_TOKEN < file`.

## Add the target

Open **Deployments › Targets → Add a deployment target** and choose **Fly.io
Machines**.

| Field | What to enter |
| --- | --- |
| name | A short name for the target, such as `fly-syd` |
| token secret | The name of the secret holding the token; `FLY_API_TOKEN` by default |
| region | A Fly region code, such as `syd` |
| org (if the token reaches several) | The organization's slug. Leave it empty when the token reaches one organization |

**Check** reads the token, chooses the organization and checks that Fly has the
region. It answers with the organization and region, and reminds you what the
token can reach. Nothing is recorded until you press **Add target**. Then set
its offer and facts. Offer it with the **public** audience only (see below).

## Check it worked

The target's page shows **Target check:** with the organization and region.
Its **Capacity** page lists the Machine size each part runs on, such as
`shared-cpu-1x (512 MB)`. Fly's prices are not read, so no cost is shown.
A deployment's **Status** tab shows each part's Machine: its state, image,
when it started, and its CPU and memory use against its size, read from the
organization's metrics with the same token. Out-of-memory kills come from
Fly's Machine events.

## What AppTown creates and deletes

For each deployment AppTown creates:

- a Fly app named `at-…`, with a shared IPv4 and an IPv6 address;
- one Machine per part, carrying the installation and the deployment in its
  metadata;
- a 1 GB volume for each data folder, and one for Postgres;
- the deployment's secrets, as the app's secrets.

Postgres runs as a stock container on its own Machine and volume. Its data
lives in a `pgdata` folder inside the volume, because a Fly volume has a
`lost+found` folder at its root and Postgres refuses to start in a folder that
is not empty. Postgres keeps its Machine and volume across releases; a release
with an incompatible Postgres image is refused.

Taking a deployment down stops the app, takes a final backup, then deletes the
Fly app with its Machines and addresses, and its volumes. Without a backup
store, or when the final backup fails, you can choose to keep the data: the
app and its volumes stay, every Machine stopped, listed as retained. A
deployment that was never released holds nothing at Fly.

AppTown never adopts an app it did not create, even one with the same name;
the release is refused instead. Before it deletes an app it checks that every
Machine carries this deployment's mark and every volume is one it recorded,
and stops if anything else is there.

## Costs and limits

- Fly bills the organization for Machines, volumes and addresses.
- A Fly app answers anyone at its `fly.dev` address, with no AppTown sign-in in
  front of it. Only a deployment whose audience is public is released here.
- Postgres is the only service offered, and data folders get volumes. Other
  needs are refused before anything is made.
- Refused before anything is made: a liveness check, a readiness check that is
  not HTTP, a readiness threshold or exact status, and parts given different
  secrets (Fly gives every Machine of an app the same secrets).
- A part with a data folder stops briefly during releases and backups.
- **Restart** restarts each part's Machine. **Start** and **Stop** are
  disabled.
- Backups export Postgres as the app's user and copy data folders while their
  part is stopped. The copies are not encrypted. A restore takes a safety
  backup, stops the app, restores Postgres in place and fills a new volume for
  a data folder.
