<!-- A word like apptown:origin in braces is a live link: the Docs page of an
AppTown shows that AppTown's own address there. -->

# AppTown's own backups

A whole AppTown is backed up with one command and restored with another:

```sh
apptownd backup [-store] <archive>   # on the AppTown's host, while it runs
apptownd restore <archive>           # on a new host, before AppTown starts there
```

Give each the same flags and `APPTOWN_*` settings `apptownd` runs with, at
least `-data` (the data directory). Put the flags first, then the command.
In a container install, run them in the container (`docker exec`).

Both need the built-in database. An AppTown on `-db-url` keeps its database
elsewhere: back that database up yourself, as "By hand" below says.

## Backing up

`apptownd backup /root/apptown-2026-10-23.backup` asks the AppTown running
on that data directory to write one archive. It does this, in order:

1. **Each app on AppTown's own Docker host is backed up with its app
   stopped.** Every part of the app stops, its data is copied, and the parts
   start again. Its services (a database, say) keep running, because the copy
   is taken through them. One app stops at a time, for as long as its copy
   takes. This copy is an ordinary deployment backup, kept with that
   deployment's other backups.
2. **AppTown holds new work for a moment.** No new job or step starts while
   the database is dumped and the data directory is copied. Work already
   running carries on.
3. **The archive is written.** It holds the database dump, the data
   directory without `pgdata` (the database's own files, which the dump
   replaces) and `cache`, and a manifest that lists what is inside.
4. With `-store`, the archive is also copied to the backup store that
   deployment backups use (Settings, Backup store). The store takes one
   object of up to 5 GB.

The command prints what it did. It fails, with the reason, if an app's
data could not be copied whole. The archive is still written in that
case, and the manifest names that app.

**One moment.** The dump is taken before the files are copied, and nothing
new starts in between, so every file the database names is in the archive.
Work that was already running can leave a file newer than the dump. A
restored AppTown treats that work as interrupted, as it would after any
stop. Each app's data is from its own copy in step 1, a little earlier than
the dump.

**The passphrase.** Every archive is encrypted. It holds every secret
AppTown has: the built-in secret store and its key, connector and model
keys, target credentials, the SSH keys of attached hosts, and the key step
credentials are made with. The command asks for a passphrase on the
terminal, or reads it from the file named in
`APPTOWN_BACKUP_PASSPHRASE_FILE`. It must be at least 12 characters.
Without it the archive cannot be restored, by AppTown or anyone else. Keep
the passphrase somewhere other than the archive. The archive is standard
[age](https://age-encryption.org) encryption around a `tar.gz`, so
`age -d archive | tar tz` lists it without AppTown.

**An external secret store** such as Infisical keeps its own values. The
archive holds AppTown's connection to it, not the values. The manifest and
the command's output say so when one is set up.

**Copies off the host.** An archive on the AppTown's own disk is lost with
that disk. Use `-store`, or copy it somewhere else.

## Restoring

On a host with Docker and no AppTown:

```sh
apptownd -data /srv/apptown/data restore /root/apptown-2026-10-23.backup
```

1. It refuses a host that already has an AppTown: a data directory that is
   not empty, AppTown's database container, or any container AppTown made.
2. It unpacks the archive into the data directory and checks that the key
   files and the dump are exactly what the manifest says.
3. It makes the built-in database and loads the dump. It counts each table's
   rows on the way in and compares them with the loaded database.
4. It opens the database as `-verify-only` would, bringing it up to this
   build (a newer build than the one that took the archive is fine; an older
   one is refused).
5. It records what the restored AppTown does when it first starts (below)
   and stops.

Then start AppTown normally, with the same flags. Start it once with
`-verify-only` first if you want to look before anything runs. The restored
AppTown has the same id as the one backed up: run only one of them.

If a restore fails part way, remove the data directory and the database
container (`apptown-db`) and start again.

**What the first start does.**

- **Apps on AppTown's own Docker host** are rebuilt from their last release,
  and their data goes back in during that rebuild: after their services
  start and before any part of the app starts. Nothing of the app runs on
  empty data. Until the rebuild ends, nothing else can deploy them. An app
  whose copy in the archive is incomplete, or missing, is not started; its
  card on the Deployments page says why. An app whose last release did not
  record how to rebuild it is named by the restore: release it again, then
  restore its copy from its backups. A failure with one app does not stop
  the others.
- **App Platform and Portainer deployments** stay where they are. AppTown
  checks each one with its provider before it acts on it:
  - The provider runs what AppTown last recorded: nothing changes.
  - The provider runs something else (it was released to after the backup):
    AppTown records it as a release marked "observed after restore". It is
    not a release AppTown shipped. You can deploy the release AppTown last
    shipped there again.
  - The provider cannot be reached: the card says "not yet confirmed since
    restore", and AppTown asks again every minute. Until then nothing
    releases, restores into or takes down that deployment, including work
    that was waiting when the backup was taken.
  - The app is gone from the provider: the card says "gone since restore".
    You can take it down. Rebuilding it from its record is not available
    yet.
- **Deployments on attached Docker hosts and bucket websites** are not
  checked: they keep running as they are.

## Moving AppTown to another host

Back up on the old host, restore on the new one, then stop `apptownd` on
the old host before you start it on the new one: both have the same id and
the same credentials. Apps on the old host's Docker keep running and
writing until you stop them. Anything they write after their copy in the
backup is not on the new host. Keep the old host until the new one passes
your checks.

## The nightly database dump

When AppTown uses its built-in database, it dumps it every night at 02:00
(by the host's clock) into `<data directory>/backups/apptown-<date>.sql.gz`
and keeps the newest 14. Each dump, or failure, is in the audit log
(`db.backup`, `db.backup-failed`). With `-db-url` there is no nightly dump.

The nightly dump is the database only. A whole backup is the archive above.

**AppTown's own id.** Every AppTown has a UUID, the instance setting
`forge.id`, written once at its first start and never changed. It names this
AppTown in its backups. It is in the database, so a restored AppTown keeps
it.

## By hand

These steps are for an AppTown on `-db-url`, and for loading one nightly
dump.

**What the data directory holds.** Deployment backups under `backups/`,
project repositories and change folders under `workspaces/`, the built-in
secret store `secrets.env`, `db.env` (the built-in database's password),
`instance-id`, the key step credentials are made with (`runtime-token-key`),
the SSH keys of attached hosts, and `layout`, the version of the directory's
own layout (a build refuses a directory written by a newer one, by name).

**Restoring the database.** This replaces the current database. If it is
still readable, back it up first ([Upgrading](upgrading.md),
step 2). Stop `apptownd`, then, with `<db>` being the built-in database's
container, which `db.env` in the data directory names
(`APPTOWN_DB_CONTAINER`): `apptown-db`. `<name>` is the database at the end of `APPTOWN_DB_URL` in the
same file: `apptown`, or `forge` on an install made before 1.0:

```sh
docker exec <db> dropdb -U postgres <name>
docker exec <db> createdb -U postgres <name>
gunzip -c <file> | docker exec -i <db> psql -v ON_ERROR_STOP=1 -U postgres -d <name>
```

Start once with `-verify-only` to look before anything runs: it goes
through the normal startup, which upgrades the database and the data
directory to this build, then serves reads only, with no recovery, scheduler
or workers. It is not a dry run of an upgrade. Then restart without it.

**An AppTown on `-db-url`.** Back up that database with its own tools, and
the data directory with it, taken at the same moment (a file-system
snapshot of the data directory taken with the dump is the reliable way).
The data directory holds every secret as files, and the database holds the
built-in secret store's key: protect both as you would the secrets.

## Rehearsing a recovery

`scripts/apptown-recovery-rehearsal.sh` backs up an AppTown with `apptownd
backup`, restores the archive onto a spare host with `apptownd restore`, and
checks the result: the key files match, reads through the API match (with a
CLI token made before the backup), and an app chosen for the check was
rebuilt with its data and answers the same query as on the source. Backing
up writes on the source: each app on its own host stops for its copy.

Rehearsed by the maintainers on a small AppTown (one project, an app with a
Postgres need holding marker rows) onto a fresh host: the backup held new
work for about a second; the restore, its read-only check and the rebuild of
the app with its data took 74 seconds, and the rows came back unchanged.

**The spare host is cut off from the outside.** A restored AppTown holds the
credentials of everything it manages: upstream Git, cloud accounts, attached
hosts, model providers. As soon as it starts normally it fires schedules and
checks its deployments. The script cuts the spare host off before AppTown
starts (an nftables table allowing only loopback, DNS, Docker's own networks
and one registry, and `/etc/hosts` entries for the names that share that
registry's address). DNS, Docker's own networks and that registry stay
reachable, and reaching the registry is not read-only. When the rehearsal
ends it stops AppTown and removes every container, network and volume
created since it started (it knows them from what Docker held before), and
lifts the cut only once none remain. The lock it takes on the spare host
must still be its own before it cleans up and before it releases it: an
unreadable or different lock refuses cleanup, and a failed release is a
failed rehearsal, even if the cut was already lifted. Inspect the host
before using it again after a failed cleanup.

New whole-AppTown archives record the observed PostgreSQL server and `pg_dump`
client versions. This build refuses archives that record a server major other
than the built-in PostgreSQL 16 before extracting their data. These observations
do not certify cross-major restore compatibility. Older archives without this
metadata remain readable through the existing restore path; their engine
provenance is unknown. Older AppTown binaries ignore this additive metadata and
do not enforce this refusal.
