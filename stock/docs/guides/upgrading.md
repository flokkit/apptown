# Upgrading

Instance administrators can open **Updates** to check authenticated release
information. The normal daemon checks public release metadata daily;
`-update-channel=prerelease` explicitly includes release candidates, while the
default `stable` channel excludes them. `-update-checks=false` disables periodic
checks, leaving the explicit check available. Verification-only startup does not
run periodic checks.

A notification requires a complete release manifest and notes bound to the
release workflow's Sigstore signature, transparency evidence, source commit and
archive checksums. Missing signatures, failed requests and stale observations
are shown as unavailable or stale, never as a successful no-update check. The
page preserves the last verified candidate and records attempted and successful
checks separately. Opening it reads local status without contacting a publisher.

AppTown tells you a release exists and shows how to install it; it does not
replace itself. Follow the procedure below; a displayed version string alone is
not installed-build provenance. Checks request only
fixed public GitHub and Sigstore metadata, without sending project information
or instance credentials.

1. **Check nothing is executing.** `apptown executing` lists the steps and
   the operations (copies, restores, take-downs) a restart would interrupt;
   it exits 0 when there are none and 3 when there are some. A step cut off
   by a restart may be left in error and need a retry; an operation cut off
   ends unknown with what it had done.
2. **Back up the whole AppTown, and stop if the backup fails.** While
   the old `apptownd` still runs, take a whole-AppTown backup
   ([AppTown's own backups](apptown-backups.md)) with the flags and
   `APPTOWN_*` settings it runs with, `-data` at least, into a file outside
   the data directory (the backup refuses one inside it):

   ```sh
   apptownd -data <data directory> backup <directory>/apptown-pre-upgrade.backup
   ```

   On a one-server compose install run it in the container, writing to its
   `/tmp`, and copy the archive out before the container is replaced:

   ```sh
   docker compose exec apptown apptownd backup /tmp/apptown-pre-upgrade.backup
   docker compose cp apptown:/tmp/apptown-pre-upgrade.backup <directory>/
   ```

   It asks for a passphrase (or reads `APPTOWN_BACKUP_PASSPHRASE_FILE`);
   keep the passphrase apart from the archive, since without it the archive
   cannot be opened. If the backup fails, do not upgrade. Then stop
   `apptownd` (`docker compose stop apptown` on a one-server compose
   install).
3. **Replace the binaries and start.** On a one-server compose install,
   set `APPTOWN_VERSION` in `.env` to the new version (or download the new
   `compose.yaml`), then `docker compose pull && docker compose up -d`: the
   image brings its own agent image's name. Otherwise, unpack the new release over
   `/opt/apptown` ([downloading a release](server-setup.md)), its `share/`
   folder with its `bin/` (AppTown reads its ways of working and tool
   versions from `share/apptown/updates` at start, and does not start
   without them), and set `APPTOWN_AGENT_IMAGE` to the new release's agent
   image (`ghcr.io/flokkit/apptown-agent:<version>`); AppTown pulls it when
   it starts. Workers build their own agent image from the recipe in the
   release. Install the worker first, then start `apptownd`. The new
   `apptownd` upgrades the database when it starts, including with
   `-verify-only`. (A source build rebuilds with `make
   build` and its own agent image: the repository's `CONTRIBUTING.md`.)
4. **Switching to no local Docker** at the same time: follow
   [Split install](split-install.md), "Switching to no local
   Docker", before the first start with `-no-local-docker`.

## From a release candidate

On a native Mac, new built-in databases use Docker-managed storage. Existing
bound databases stay in place. Keep the local `pgdata` storage receipt during
same-installation upgrades and rollback; a missing or replaced recorded volume
requires recovery. The whole-AppTown backup above protects both storage layouts. A directory snapshot alone does not include a managed database.

The oldest supported installation and whole-AppTown backup schema is 167,
from 1.0.0-rc.7. Startup and whole-AppTown restore refuse an older schema;
this build does not perform the earlier release-candidate migrations.

The oldest data directory and whole-AppTown backup this build reads is from
1.0.0-rc.9 (data layout 2, the `layout` file in the data directory). Upgrade
an installation from 1.0.0-rc.7 to a release from 1.0.0-rc.9 to
1.0.0-rc.11 first, which moves its project repositories, then to this one.
Startup refuses an older data directory before it opens the database.

Read the target release's notes for its additional upgrade and recovery
requirements. Keep the whole-AppTown backup described above before starting
the new build; `-verify-only` still performs required schema
changes.

## Rolling back

The way back is the archive from step 2, put back into the same
installation by hand. (`apptownd restore` is for a new host: it refuses one
that already has an AppTown.)

1. Stop `apptownd`.
2. Unpack the archive into an empty folder outside the data directory. It
   is [age](https://age-encryption.org) encryption around a `tar.gz`; `age`
   asks for the passphrase:

   ```sh
   mkdir <staging> && age -d <archive> | tar -xz -C <staging>
   ```

   It holds `database.sql.gz` (the database dump), `data/` (the data
   directory without `pgdata` and `cache`) and `manifest.json`.
3. Load `<staging>/database.sql.gz` into AppTown's database with the
   commands under "Restoring the database" in [AppTown's own
   backups](apptown-backups.md). On `-db-url`, load it into that database
   with its own tools.
4. Put the data directory back from `<staging>/data/`, keeping its `pgdata`
   (the database's files, or the receipt of its Docker volume) and `cache`:

   ```sh
   rsync -a --delete --exclude=/pgdata --exclude=/cache <staging>/data/ <data directory>/
   ```
5. Start the previous release, with its binaries and settings.

That discards everything since the backup: the new build's database upgrade
and any work done on it. The new build's first write is where work starts
being lost, so roll back before people use it, or accept losing what they
did. There is no other downgrade: an older build refuses a database or a
data directory a newer one has upgraded. Each release's test suite upgrades
frozen copies of earlier releases' AppTown and rolls them back this way.

## Versions

`apptownd -version`, `apptown-worker -version` and `apptown version` name the
build: a release name, or `dev-` and the commit it was built from
(`-modified` with local changes). `apptown version` also names AppTown it
talks to, which states its version on every API answer.

Which binaries work together is decided by two protocol numbers, not by the
version names. The CLI states the CLI protocol it speaks on every request;
AppTown accepts a range and refuses another with `426` and a sentence
naming both sides and what to install. An AppTown states its worker protocol to
its worker the same way, and the worker refuses one outside its range. A
worker keeps serving the previous AppTown protocol for one release, so a worker
installed before its AppTown goes on serving the AppTown it replaces.

A request that states no protocol is served: browsers, MCP clients and
`curl` state none, nor do an AppTown or CLI built before protocols were stated.
Being served is not evidence that such a caller is compatible.

A release that raises the CLI protocol says so in its notes. The new CLI is
then refused by the AppTown it replaces, including when an upgrade asks it
`apptown executing`: answer that question with the CLI of the running
release, or establish yourself that nothing is executing and upgrade without
asking.
