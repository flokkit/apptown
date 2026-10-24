# Deployment backups

What a deployment's backups copy, when they run, how long sets are kept and
how a restore works is in
[Previews and deployments](container-lifecycle.md); how each need
is copied is in [Services and data](services-and-data.md). This
section is the operator's part: where the sets are kept, and what to set up
for App Platform.

**Where Docker-host backups are kept.** When a backup store is attached
(below), every backup goes to the store; AppTown's disk holds a set only
while it uploads. Postgres is copied by its portable export, so a copy can
also be rehearsed on another host. Without a store, backups
stay on AppTown's own disk, and losing that disk loses them: the
deployment's card reads "Backups kept on this host ⚠" and the Backups page
"Kept on: this host ⚠". A restore rehearsal still works from those
backups. Once a
store is attached,
the nightly sweep moves the sets still on the disk into it, each checked
against its recorded digest; a set that does not match stays on the disk
and says why. If AppTown stops just after it has moved a set and before
it has deleted that set's folder, the folder stays under
`<data directory>/backups/<project>/<deployment>/<time>`; AppTown logs
its name as it moves the set, and the store holds the checked copy. Delete
such a folder by hand. The Backups page's **All sets** table shows each
set's **Kept on** and path: a folder is safe to remove when the set with
the same `<project>/<deployment>/<time>` is kept on "backup store". If the store cannot be reached, backups fail and read
stale; AppTown does not fall back to its disk, a take-down deletes
nothing, and a restore is refused before anything changes.

**What a restore reads.** Each file goes back as its set recorded it: by
the restore command the set carries, after its file is checked against the
set's digest. An older set whose record carries no digest, or no restore
command for a need, is refused item by item ("not restored"), never put
back by today's command or unchecked; restore such a set by hand from its
files.

**Where restore rehearsals run.** A restore rehearsal starts the app from a
backup on a Docker host, checks it answers, and removes it. If a platform
administrator keeps a Docker host for rehearsals (the target's settings,
"kept for restore rehearsals"), every rehearsal runs there, and none runs
while that host does not answer. That host runs nothing else, so the
rehearsal shares it with no running app. If no host is kept, a rehearsal
runs where the app's data already is: on the app's own Docker host, or on
an App Platform target's copy runner. That works on a single server with no
other setup. The rehearsal there has its own containers, network and
volumes, publishes no port and has no address, but it shares the host's
CPU, memory and disk while it runs: it holds the backup once as a file and
once loaded into its own database, so a host that is nearly full can run out
of disk. The rehearsal's line on the Data protection page says "not kept for
rehearsals" when it ran on such a host. What a rehearsal can still do
outside is in [Secrets](secrets.md), "Restore rehearsals".

**App Platform databases go to a backup store, off AppTown's host.** Two
settings turn this on:

1. **Attach a backup store** (Deployments → Backups → Attach a bucket,
   platform administrators): an existing private bucket in an S3-compatible
   store (DigitalOcean Spaces: endpoint `https://syd1.digitaloceanspaces.com`,
   region `syd1`) and the names of the two secrets that hold its key pair
   (`apptown secret set NAME < file`). AppTown writes a private object,
   reads it back and deletes it before it attaches the bucket. Anyone who
   holds a key to that bucket can read the copies in it. Copies are read
   only from the bucket they were written to, so another bucket can be
   attached only when none are left in the current one; do it when no copy,
   check or take-down is running.
2. **Name a copy runner** on the App Platform target (Infrastructure →
   the target → Edit target → copy runner): a Docker host AppTown reaches.
   Copies run there in a throwaway `postgres:<version>-alpine` container,
   as the app's own database user over TLS. The database cluster must let
   that host connect (its trusted sources).
   Until one is named, a deployment's **Copy now**, **Check it comes
   back** and restore buttons show greyed, with the reason, and the
   target's page says backups and restore tests are unavailable there.
   Taking such a deployment down is refused, since no final copy can be
   taken, unless you choose to take it down without one; its database
   then stays at the provider, listed under retained resources.

Then production App Platform deployments are copied every night and checked
once a week (the newest copy is filled into a new database on the same
cluster, checked and removed); any deployment's card has **Copy now** and
**Check it comes back**, and shows how recent its copies are, DigitalOcean's
own backups and the last check. Taking such a deployment down deletes the
app first, takes a final copy, checks it comes back, and only then deletes
the database cluster. If anything falls short the cluster stays and the
deployment reads "app removed, data kept; deploy again to serve". A new
deployment can **start from the final copy** of one taken down: its first
release fills its database from the copy, and checks it, before the app
starts. That needs the restore grant (project owners have it) in both
projects; a production copy goes only into a production deployment. Without
a store, production data on App Platform reads "not protected off-host" and
a take-down keeps the cluster as before. `apptown data -p <project>
<deployment>` prints the same.

**Every release onto data is copied first, and checked after.** When a
landing releases onto a deployment that already runs a release and holds
data, AppTown takes a copy first (trigger `pre-release`). If that copy is
incomplete or cannot be taken, the release is refused before anything is
built or started, unless the data is synthetic. To release anyway, a
person retries the deploy step accepting that
(`apptown retry <change> --accept-uncopied-data`); the attempt records who
and when, and a later retry accepts nothing. An agent's connection or a
chat cannot accept. A first release, a deployment that holds no data and a
target that keeps its own backups need no copy.

After the release boots, AppTown reads each need's data again on a Docker
host: the storage it is on, and its declared measure (Postgres tables,
rows and size; S3 objects and size; Redis keys; MariaDB tables, rows and
size). The step fails, saying the release is running, which copy to
restore from, and that the check changed nothing, when the data is on
other storage than before, when a measure that was at least its floor
before fell by more than half (rows 20, objects 5, keys 5, tables 1, size
1 MiB; tables going from some to none always counts), or when a measure
that was read before cannot be read after. A deliberate reset of
synthetic data is noted, not failed. A need with no measure, or on a
target that is not a Docker host, is noted as not compared.

**A volume no service mounts.** A backup reads partial when the
deployment's records name a need volume that no running service mounts:
"no running postgres container mounts it". Before 21 October 2026 a
service made again could start on new storage and leave its data in such
a volume. Look at it before deleting it.
