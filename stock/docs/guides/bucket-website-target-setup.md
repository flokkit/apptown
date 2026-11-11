# Bucket website target setup

A bucket website target serves a static site as files from a bucket in an
S3-compatible store, at the store's website address. Each deployment gets a
bucket of its own. DigitalOcean Spaces is the store AppTown has been built
against. This target is experimental: no real app has been qualified on it
yet.

## What you need

- The **Bucket-site targets** feature turned on under **Setup → Features**
  ([Features](features.md)).
- An S3-compatible store that serves buckets as websites. On DigitalOcean,
  Spaces turned on for the team.
- A container registry attached to each project that deploys here. AppTown
  reads the site from the release image published there by digest.
- An app whose only part is one public `static` part, with no `init` steps and
  no `needs` ([The app contract](contract-reference.md), "Static sites").

## The credential

AppTown creates a bucket for each deployment, so the key must be able to
create buckets. In Spaces that is a full-access key, which can read, write and
configure every bucket in its team. Make a key for AppTown alone, in a team of
its own where other data must stay out of its reach. Store its two halves as
AppTown secrets, under **Integrations › Secrets** or with `apptown secret set`;
the defaults are `SPACES_ACCESS_KEY` and `SPACES_SECRET_KEY`.

## Add the target

Open **Deployments › Targets → Add a deployment target** and choose **Bucket
website**.

| Field | What to enter |
| --- | --- |
| name | A short name for the target, such as `spaces-syd` |
| endpoint | The store's S3 address, such as `https://syd1.digitaloceanspaces.com` |
| region | The store's region, such as `syd1` |
| website address | The address a bucket's site is served at, with `{bucket}` and `{region}` where the names go: `https://{bucket}.{region}-static.digitaloceanspaces.com` for Spaces |
| access key secret | The name of the secret holding the access key |
| secret key secret | The name of the secret holding the secret key |

**Check** lists the store's buckets with the key pair, and says how many the
key can see. It creates nothing. Nothing is recorded until you press **Add
target**. Then set its offer: tick only **public** under **Audiences**.
Anyone with a site's address can read every file in it.

## Check it worked

The target's page shows **Target check:** with the endpoint, the region and
how many buckets the key sees.

## What AppTown creates and deletes

For each deployment AppTown creates one bucket, named `apptown-…`, and records
it before writing to it. The bucket is private; each file AppTown uploads is
public. A release uploads the new files and then checks the site answers with
the new release. Old files are removed one release later.

Taking a deployment down deletes every file and the bucket, and checks the
address stops answering. Keep nothing else in a deployment's bucket. AppTown
never adopts a bucket whose name is taken, and never touches other buckets.

## Costs and limits

- The store bills for storage and traffic.
- Only a deployment whose audience is public is released here.
- A site may be at most 200 MiB.
- There is no custom domain: the site is at the store's website address.
- There is no fallback to `index.html` for other paths.
- **Start**, **Stop** and **Restart** are disabled.
- Nothing is backed up: the site is rebuilt from its release.
