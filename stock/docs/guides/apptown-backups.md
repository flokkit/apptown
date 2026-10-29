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

An AppTown on `-db-url` is backed up the same way: the host's own
`pg_dump` dumps that database, so install the database server's major
version of it (`pg_dump` 16 for PostgreSQL 16; the backup refuses another
major before it holds anything). A restore always loads the archive into a
new built-in database on the new host, so the restored AppTown runs on the
built-in database; the server backed up must be PostgreSQL 16, as the
built-in one is. Restore it, and start it afterwards, without `-db-url`
(`APPTOWN_DB_URL`) and without `-no-local-docker`
(`APPTOWN_NO_LOCAL_DOCKER`), which a restore refuses: the new host's
AppTown runs on its own built-in database and Docker, with the new data
directory.

## Backing up

`apptownd backup /root/apptown-2026-10-23.backup` asks the AppTown running
on that data directory to write one archive. It does this, in order:

1. **AppTown holds new work for a moment.** No new job or step starts while
   the database is dumped and the data directory is copied. Work already
   running carries on.
2. **The archive is written.** It holds the database dump, the data
   directory without `pgdata` (bound database files or the local Docker-volume
   receipt; the dump supplies the database) and `cache`, and a manifest that lists what is inside.
   The data directory includes the deployment backups kept on AppTown's
   disk.
3. With `-store`, the archive is also copied to the backup store that
   deployment backups use (Settings, Backup store). It goes up in parts, so
   its size has no single-upload limit.

Nothing is stopped: the backup is AppTown's own. Apps keep their data in
their own deployment backups (the Backups page), and a restore brings
each app back from its newest one. The command prints what it did.

**One moment.** The dump is taken before the files are copied, and nothing
new starts in between, so every file the database names is in the archive.
Work that was already running can leave a file newer than the dump. A
restored AppTown treats that work as interrupted, as it would after any
stop. An app's data is from its own newest deployment backup, which is
older than the dump: anything the app wrote after that backup is not in
the restored AppTown.

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

This build restores archives from 1.0.0-rc.7 (schema 167) or newer, up to
its own schema version. Archives below that floor, including an archive
without a schema version, are refused before files are extracted or the
built-in database is created. A newer schema needs a build that supports it.
The archive must also be in this build's data layout, which archives from
1.0.0-rc.9 onwards are: an older one is refused after its files are extracted
and before the built-in database is created. Restore it with a release from
1.0.0-rc.9 to 1.0.0-rc.11, then take a new backup.

1. It refuses a host that already has an AppTown: a data directory that is
   not empty, AppTown's database container, or any container AppTown made.
2. It unpacks the archive into the data directory and checks that the key
   files and the dump are exactly what the manifest says, and that the
   archive's data layout is this build's.
3. It makes the built-in database and loads the dump. It counts each table's
   rows on the way in and compares them with the loaded database.
4. It opens the database as `-verify-only` would, bringing it up to this
   build. When the destination differs from the archive's original data path,
   it rewrites the active local references that name that path: repository
   paths of unfinished branch publications, chat checkouts, and local Git
   remotes of workspaces. A newer build is fine; an unsupported older build
   refuses.
5. It records what the restored AppTown does when it first starts (below)
   and stops.

Then start AppTown normally, with the same flags as the restore. Start it once with
`-verify-only` first if you want to look before anything runs. The restored
AppTown has the same id as the one backed up: run only one of them.

If extraction or database loading fails part way, ordinary startup refuses the
incomplete restore. Restore the original archive into a fresh empty destination
and database. Preserve the failed restore's files, database container and volume
until you have checked what they contain. Removing only the container and data
directory does not remove a Docker-managed database volume. If you choose to
discard a failed fresh-host restore, identify and remove only its database
container, volume and directory before retrying; deleting a managed volume
permanently deletes its database bytes.

Once loading succeeds, AppTown keeps the original manifest until relocation and restore
bookkeeping finish. Starting with the same flags retries that remaining work and
exits; it does not reload the dump or begin normal recovery. Stop surviving writers
or resolve the named ownership/configuration problem if relocation refuses. An
admitted task that retains a local path being moved must first be settled and
released under its original path; do not edit its saved request or digest.

If a deployment rebuild has already created database volumes, preserve those
volumes and the restore diagnostics for recovery. A later rebuild refuses
preexisting engine-data volumes; it does not assume they are empty or automatically
import into them again.


**What the first start does.**

- **Apps on AppTown's own Docker host** are rebuilt from their last release,
  and their data goes back in during that rebuild from the app's newest
  whole deployment backup: after their services start and before any part of
  the app starts. The restore prints which backup each app gets and when it
  was taken. Until the rebuild ends, nothing else can deploy them. An app
  whose newest whole backup is in the backup store, not on AppTown's disk,
  or whose data could not be put back, is not started on empty data, now or
  at a later start: its card on the Deployments page says why. Releasing it
  starts it without that data; then restore its data from its backups. An
  app with no whole backup at all is not held: if it is rebuilt, it has no
  restored data. An app whose last release did not
  record how to rebuild it is named by the restore. A promotion-fed deployment
  can take a new release through its normal checks, followed by restoring its
  copy from backup. A landing-fed deployment needs recorded bundle provenance
  for a manual redeploy; unknown historical software cannot be inferred from
  its name or revision. A failure with one app does not stop the others.
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
writing until you stop them. Each app comes back with its newest
deployment backup: take one of each app (the Backups page) just before
the move if its newest is old. Keep the old host until the new one passes
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

These steps are for loading one nightly dump, and for backing up an
AppTown on `-db-url` with that database's own tools instead.

**What the data directory holds.** Deployment backups under `backups/`,
project repositories and change folders under `workspaces/`, the built-in
secret store `secrets.env`, `db.env` (the built-in database's password),
`instance-id`, the key step credentials are made with (`runtime-token-key`),
the SSH keys of attached hosts, and `layout`, the version of the directory's
own layout (a build refuses a directory written by an older or a newer one,
by name; a new data directory is marked when AppTown first starts in it).

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

**An AppTown on `-db-url`, by hand.** Back up that database with its own tools, and
the data directory with it, taken at the same moment (a file-system
snapshot of the data directory taken with the dump is the reliable way).
The data directory holds every secret as files, and the database holds the
built-in secret store's key: protect both as you would the secrets. A manual
paired restore keeps the original absolute data path. Use the whole-AppTown
restore command when moving to a different path, so its saved manifest supplies
the original location.

## Rehearsing a recovery

`scripts/apptown-recovery-rehearsal.sh` backs up an AppTown with `apptownd
backup` (nothing stops), carries the archive to a spare host without
writing it on the machine that runs the script, restores it there with
`apptownd restore`, and checks the result: each key file the source has
came across unchanged, reads through the API match (a sample of activity
history and recent changes per project, with a CLI token made before the
backup), and after a normal start AppTown answers and names the expected
version. Given a release archive (`CANDIDATE`), the restore and the start
use that release's `apptownd`: the rehearsal is then that release's upgrade
of the source's data. Apps are not checked: their data comes back from
their own deployment backups. The archive is deleted from the source and
the spare host, and the restored data directory from the spare host, when
the run ends, however it ends.

**The spare host is cut off from the outside.** A restored AppTown holds the
credentials of everything it manages: upstream Git, cloud accounts, attached
hosts, model providers. As soon as it starts normally it fires schedules and
checks its deployments. The script cuts the spare host off before AppTown
starts (an nftables table allowing only loopback, DNS and Docker's own
networks, and `/etc/hosts` entries for names it is given). When the
rehearsal ends it stops AppTown and removes every container, network and
volume created since it started (it knows them from what Docker held
before) and the restored data, and lifts the cut only once none remain. The
lock it takes on the spare host must still be its own before it cleans up
and before it releases it: an unreadable or different lock refuses cleanup,
and a failed release is a failed rehearsal, even if the cut was already
lifted. Inspect the host before using it again after a failed cleanup.

New whole-AppTown archives record the observed PostgreSQL server and `pg_dump`
client versions. This build refuses archives that record a server major other
than the built-in PostgreSQL 16 before extracting their data. These observations
do not certify cross-major restore compatibility. RC7 archives without this
metadata remain readable through the existing restore path; their engine
provenance is unknown. Older AppTown binaries ignore this additive metadata and
do not enforce this refusal.


**Deployment database engines.** A restored database needs evidence of the engine
that may open its files. New physical backup items record the actual immutable
source engine, observed before export from the exact source container and volume.
A physical restore (including Redis RDB) requires that observation to match the
source storage binding carried in the archive and requires the exact image to be
available. The restore creates a new target volume and records its incarnation
before starting the engine. A logical copy uses its declared supported engine and
also requires a newly created target. No existing engine-data volume is overwritten
by this first-start path.

Older physical items, including Redis copies in RC7 archives, do not carry this
immutable engine evidence. Their files remain usable for independently verified
recovery, but automatic first-start database fill refuses them with a missing
source-engine-provenance diagnosis. Preserve the archive and original host's
container/image/volume evidence, establish the exact engine that produced the
copy, and use a separately verified recovery procedure. Do not try engine tags
against retained data. RC7 whole-instance automatic restore parity is not promised
for this physical-copy subset; logical database copies retain their supported
restore path.
