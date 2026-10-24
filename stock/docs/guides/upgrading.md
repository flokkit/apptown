# Upgrading

1. **Check nothing is executing.** `apptown executing` lists the steps and
   the operations (copies, restores, take-downs) a restart would interrupt;
   it exits 0 when there are none and 3 when there are some. A step cut off
   by a restart may be left in error and need a retry; an operation cut off
   ends unknown with what it had done.
2. **Stop AppTown, then copy it, and stop if a copy fails.** Stop
   `apptownd` (`docker compose stop apptown` on a one-server compose
   install; leave the database container running), so nobody writes between the two
   copies and they describe one moment. Take the database dump with the
   checked backup script, in `/opt/apptown/scripts` (the nightly dump may
   be a day old). `<db>` is the built-in database's container, which `db.env` in the data directory names (`APPTOWN_DB_CONTAINER`): `apptown-db`. `<name>` is the database at the end of `APPTOWN_DB_URL` in the same file: `apptown`, or `forge` on an install made before 1.0:

   ```sh
   bash /opt/apptown/scripts/apptown-db-backup.sh <db> postgres <name> <directory> pre-upgrade
   ```

   On a one-server compose install the script is in the image; run it from
   the folder holding `compose.yaml`, with a directory under the data
   folder:

   ```sh
   docker compose run --rm --no-deps --entrypoint bash apptown \
     /opt/apptown/scripts/apptown-db-backup.sh <db> postgres <name> /var/lib/apptown/backups pre-upgrade
   ```

   It refuses, and leaves no file, unless `pg_dump` and `gzip` both
   succeed, the file is valid gzip, the dump ends with its "dump complete"
   line, and it holds AppTown's tables. A bare `pg_dump | gzip` can
   produce a file that looks fine and holds nothing. With `-db-url`, use that
   database's own backup and check that it completed. Then copy the data
   directory without `pgdata` (the dump holds the database) and `cache`:

   ```sh
   tar -C <data directory> --exclude=./pgdata --exclude=./cache -czf <directory>/forge-data-pre-upgrade.tar.gz .
   ```
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
   `apptownd` upgrades the database and the data directory's layout when it
   starts, including with `-verify-only`. (A source build rebuilds with `make
   build` and its own agent image: the repository's `CONTRIBUTING.md`.)
4. **Switching to no local Docker** at the same time: follow
   [Split install](split-install.md), "Switching to no local
   Docker", before the first start with `-no-local-docker`.

## From a release candidate

An upgrade from 1.0.0-rc.6 needs every deployment named by its key first.
The upgrade stops, changing nothing, while a deployment still holds a
container, a Portainer stack or anything else on a host under the names it
was made with before 1.0, and names each one. Under rc.6, move one on a
Docker host with `apptown move-names <project> <deployment>`; take one on
Portainer down and release it again (its data is not carried over: copy it
first, and a stack AppTown did not record creating is taken down in
Portainer by its owner). Upgrading from a release before rc.6 is not
supported.

## Rolling back

The way back is the two copies from step 2: stop `apptownd`, load the dump
(the commands under "Restoring the database" in [AppTown's own
backups](apptown-backups.md)), replace the data directory's
contents except `pgdata` with the copy, and start the previous release. That
discards everything since the copies: the new build's database and layout
upgrades and any work done on it. The new build's first write is where work
starts being lost, so roll back before people use it, or accept losing what
they did. There is no other downgrade: an older build refuses a database or a
data directory a newer one has upgraded. Each release's test suite upgrades
frozen copies of earlier releases' AppTown and rolls them back this way
(`make upgrade-test`).

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
