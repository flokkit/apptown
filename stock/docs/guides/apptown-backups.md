<!-- A word like apptown:origin in braces is a live link: the Docs page of an
AppTown shows that AppTown's own address there. -->

# AppTown's own backups

When AppTown uses its built-in database, it dumps it every night at 02:00
(by the host's clock) into `<data directory>/backups/apptown-<date>.sql.gz`
and keeps the newest 14. Each dump, or failure, is in the audit log
(`db.backup`, `db.backup-failed`). With `-db-url` there is no nightly dump:
back up that database yourself.

The dump is the database only. The rest of AppTown itself is in the data
directory: deployment backup sets under `backups/`, project repositories and change folders under `workspaces/`,
the built-in secrets store `secrets.env`, `db.env` (the built-in database's
password), `instance-id`, the key step credentials are made with
(`runtime-token-key`), the SSH keys of attached hosts, and `layout`, the
version of the directory's own layout (a build refuses a directory written by
a newer one, by name, rather than misread it). Back that
directory up as well, for example with the host's file-system snapshots.
The live data of deployments on the built-in Docker host is not in it: it is
in Docker volumes, covered by [Deployment backups](/docs/guides/deployment-backups).

**AppTown's own id.** Every AppTown has a UUID, the instance setting
`forge.id`, written once at its first start and never changed. It names this
AppTown in its project exports and backup sets. It is in
the database, so an AppTown restored from a dump keeps it; a copy restored
alongside the original has the same id, so run only one of them.

**Restoring the database.** This replaces the current database. If it is
still readable, back it up first ([Upgrading](/docs/guides/upgrading), step 2). Stop `apptownd`, then, with
`<db>` being the built-in database's container, which `db.env` in the data directory names (`APPTOWN_DB_CONTAINER`): `apptown-db`, or `forge-db` on an install made before 1.0, which keeps that name. `<name>` is the database at the end of `APPTOWN_DB_URL` in the same file: `apptown`, or `forge` on an install made before 1.0:

```sh
docker exec <db> dropdb -U postgres <name>
docker exec <db> createdb -U postgres <name>
gunzip -c <file> | docker exec -i <db> psql -v ON_ERROR_STOP=1 -U postgres -d <name>
```

Start once with `-verify-only` to look before anything runs: it goes
through the normal startup, which upgrades the database and the data
directory to this build, then serves reads only, with no recovery, scheduler
or workers. It is not a dry run of an upgrade. Then restart without it.

**Moving an AppTown to another host.** Stop `apptownd` and its database container, copy the
whole data directory (with the built-in database, its files in `pgdata`
come with it), and start `apptownd` on the new host with the same settings,
first with `-verify-only`.

That moves AppTown itself, not the data of deployments on its built-in
Docker host. Their data volumes, and their services' volumes (such as a
Postgres need), are Docker volumes outside the data directory, and the apps
keep running and writing on the old host after `apptownd` stops. On its first
normal start the new host rebuilds those deployments with empty volumes.
Copied backup sets are not restored by themselves.

So, before stopping `apptownd` on the old host, for each such deployment:

1. **Stop every application part, and keep it stopped.** On the host, a
   deployment's containers are named from its project and deployment:
   the public part is `apptown-env-<project>-<deployment>`, and every other
   part and every need is `apptown-svc-env-<project>-<deployment>-<name>`
   (a deployment made before 1.0 keeps `forge-` in place of `apptown-`),
   where `<name>` is the part's name under `app:` or the need's name under
   `needs:` in `.apptown/app.yaml`. `docker stop` the public part and each
   other part (a background worker writes too). Leave the need containers
   running: their copies are taken inside them.
2. **Back up now.** The set is written under the data directory, so it
   moves with it.
3. **After the move,** once the new host has rebuilt the deployment,
   **Restore** the set. Restore stops every application part the running
   release names, puts the data back and starts them again; a part that will
   not stop refuses the restore before anything is written, and one that is
   not there is named as not started.

What each need's copy carries over, and what it leaves out, is in
[Services and data](/docs/guides/services-and-data); the `dataDir` volume of
the public part is copied whole and put back.

Keep the old host until each restore has succeeded. Deployments on
attached Docker hosts, Portainer, App Platform and bucket websites stay
where they are.

**Recovering the whole AppTown on a replacement host.** A whole-AppTown backup is
the database dump and the data directory taken at one moment: a file-system
snapshot of the data directory taken with the dump is the reliable way. What
you must arrange yourself:

- **Copies off the host.** The nightly dump sits on the same disk as the
  database; keep snapshots or copies somewhere else.
- **The data directory, whole.** It holds AppTown's keys as files:
  `runtime-token-key` (step credentials), `secrets.env` (the built-in secret
  store and connector credentials), `db.env`, `instance-id`, and `ssh/` (keys
  for attached hosts). The built-in secret store's own key is in the database,
  so the dump and the data directory together hold every secret: protect them
  as you would the secrets.
- **The images a fresh host lacks before anything else starts:** the built-in
  database's `postgres:16-alpine`, and the agent image.
- **Keep the old host** until the new one passes your checks.

The recovery point is the last snapshot and dump. The procedure, with a
replacement host that is cut off from the outside world for a rehearsal, is
`scripts/apptown-recovery-rehearsal.sh`; `scripts/apptown-restore-check.py` checks the
result through the API with a CLI token made before the backup. Its steps:
dump the database with `scripts/apptown-db-backup.sh`, copy the data directory
without `pgdata` and `cache`, install the same `apptownd` build, start once with
`-verify-only` to make the built-in database, load the dump (the commands under
"Restoring the database" above), check the key files came across unchanged,
check read-only with `-verify-only`, then start normally.
The rehearsal checks this invocation's full target lock before cleanup effects
and again before releasing it. An unreadable or different lock refuses cleanup;
failed lock release is a failed rehearsal, even if resource cleanup already
lifted the network cut. Inspect the target before reusing it after cleanup fails.

Rehearsed by the maintainers from a production AppTown (28
projects, a 2.5 GB data directory) onto a spare host, three passing runs: 5 to 6 minutes to an AppTown
serving reads, and a static site rebuilt from its recorded release and
answering 77 to 83 seconds after the normal start (6¼ to 7½ minutes in all).
Copying the data directory took 3½ to 4½ of those minutes (relayed over SSH
through the operator's machine; a direct copy or a restored snapshot is
faster); loading the dump took 16 seconds (it is plain SQL, loaded by one
`psql`). The key files, credentials, the change history,
the evidence, approvals, reports and usage of 80 changes, and the recorded
main of 27 repositories came across unchanged; the one project with no
recorded main on the source is named as unverified, not counted.

**What comes back by itself on a normal start.** Deployments on the built-in
Docker host are stood up again from what each one's last release recorded, not
from the old host's images: a static site with no network, a source build if
the new host can reach the package registries the build uses, a digest bundle
by pulling it. A deployment whose last release recorded no rebuild inputs cannot be
rebuilt from its record: release it again before you need to, which records
them. A deployment fed by landing takes by hand only the bundle it is known to
run, and only while it is settled (no deploy under way), so this changes
nothing a landing would have chosen: find the bundle of the release that put it
there (`GET /api/v1/projects/<slug>/releases`, the newest release to that
deployment, its `bundleId`) and `POST
/api/v1/projects/<slug>/deployments/<name>/deploy` with `{"bundleId": <that
bundle>}`. Any other bundle, even one at the same revision, is refused: landing
deploys it.
Deployments on attached hosts, Portainer, App Platform and
bucket websites stay where they are. Their data comes back through deployment
backups ([Deployment backups](/docs/guides/deployment-backups)), not by itself.

**A rehearsal is cut off from the outside.** A restored AppTown holds the
credentials of everything it manages: upstream Git, cloud accounts, attached
hosts, model providers. As soon as it starts normally it reconciles
deployments and fires schedules. The rehearsal script cuts the replacement
host off before AppTown starts (an nftables table allowing only loopback,
DNS, Docker's own networks and one registry, and `/etc/hosts` entries for the
names that share that registry's address). The cut is selective: DNS, Docker's own
networks and that registry stay reachable, and reaching the registry is not
read-only. When the rehearsal ends it stops AppTown and removes every
container, network and volume created since it started (it knows them from
what Docker held before), and lifts the cut only once none remain. It refuses
a target that already holds an AppTown's containers.
