# Deployment targets

A deployment target is a place deployments run. Add one under
**Deployments › Targets → Add a deployment target**. Each credential is the name of
an AppTown secret; set it there, or with `apptown secret set <NAME> < file`.

| Kind | What you give it |
| --- | --- |
| Docker host | a machine with Docker, reached over SSH: its host and the secret holding the SSH key. AppTown's own Docker is the built-in one |
| Portainer environment | the Portainer address, API key and environment |
| DigitalOcean App Platform | an API token, the region, and the container registry it copies images into. Run the registry's garbage collection occasionally to free space taken-down projects left; it blocks pushes (and so releases) while it runs. An app that needs object storage gets a Spaces bucket, so the account needs Spaces enabled, and the token makes and deletes Spaces keys: for each bucket a key that reaches it alone, and for a moment while it makes or deletes one, a full-access key it deletes again |
| Fly.io Machines | the secret holding a Fly org token (`fly tokens create org`; `FLY_API_TOKEN` by default) and a Machines region. The org is read from the token; name it only when the token reaches several. Each deployment is a Fly app of its own, named `at-…` and reached at its `fly.dev` address, with one Machine per part. An org token can manage every app in its org, so prefer an org for your team's apps alone; AppTown touches only the apps it made and recorded, and leaves the rest alone. Postgres runs as a stock container on its own volume, and a data folder gets a volume. Other needs are refused, and a part's liveness probe, a readiness threshold or exact status, and parts given different secrets are refused before anything is made. A Fly app answers anyone at its `fly.dev` address with no AppTown sign-in, so only a deployment whose audience is public is released there |
| Bucket website | an S3-compatible endpoint, region, website address and keys. Each deployment gets its own bucket, and taking the deployment down deletes it; use a key made for AppTown alone |

AppTown keeps one SSH connection per attached Docker host and reuses it for
Docker commands, including event streams. Up to three Docker calls run at once
per target. This bounds calls, rather than SSH sessions: Docker opens an SSH
session for each HTTP connection, and run, attach and BuildKit can hold two or
three at once. The limit leaves room within sshd's default of ten sessions per
connection. OpenSSH keeps the connection for three minutes after the last call
finishes, then removes its socket. The sockets are private to AppTown's data
folder; different installations have separate sockets and SSH aliases.

Attached hosts require OpenSSH connection sharing. If AppTown cannot establish
and check the shared connection, the call fails with an SSH connection sharing
error.
Keep AppTown's data folder path short enough for a Unix socket; an overlong path
is reported when the host is checked. A dead master is replaced on the next
call. Keepalive checks close an unresponsive connection after a network drop;
a call already using that connection can fail and is not replayed automatically.
AppTown's local Docker does not use SSH.

A Portainer target uses a saved connection and a verified endpoint. You can
attach several connections and targets. After verification, changing an
environment name cannot redirect the target: attach a new target for a different
endpoint. The intranet target configured for AppTown verifies its endpoint
when AppTown starts; until it has, releases to it are refused and the target
page says the endpoint is not observed.

A project's deployments are made on the project's **Deployments**.
What each kind runs, and what a deployment owns and deletes when taken
down, is in [Previews and deployments](container-lifecycle.md).

Each target opens with its status, a capacity strip, deployments and their
resource counts, resources not accounted for, settings and offers. Its
**Resources** page lists resources and filters by deployment; opening a resource
shows its full reference, ownership and generation. **History** keeps checks
and settings changes. A deployment's **Resources** link opens the same filtered
resource table.

The capacity strip has one thin bar for CPU, memory and disk. Each shows used
and total, or **unknown** with an empty bar when the complete current reading
is unavailable. Usage above 80% colours the bar. Limits do not stand in for use.

Previews run on the executor. **Integrations › Executor** shows where work runs,
the worker's address, connection check and version, running work and previews,
and its disks. A different worker version is marked beside AppTown's version.
Without a worker, work runs on AppTown's own server.

**How full a target is** is in detail on its **Capacity** page:
- **A Docker host** shows stacked bars, with the same numbers in a table
  beneath ("the numbers"):
  - **memory used**: what each holder's containers use, then the host
    itself (its system, caches and processes outside containers), then free;
  - **memory promised** and **CPUs promised**: the limits the running
    containers hold, against what the host has. A promise past the total runs
    past a mark at the total, striped; containers holding no limit are counted
    beside the bar, never drawn as a size. A CPU limit AppTown cannot read as
    a number of CPUs makes the sum "at least", and the rest of the bar "not
    read";
  - **disk**: used and free where Docker keeps its data, the folder it is,
    and when it was read. Below the warning threshold (a tenth free, capped at 40 GiB and never below
    the refusal floor) it shows the threshold; below the floor
    new work needs (see "Disk space" below) it says new releases are
    refused, and each deployment's card on that host says deploys are
    refused. A disk it could not read says why.

  The holders are each deployment (by its record: its public container and
  its stack's services), previews, agent work (steps, chats), AppTown's own
  containers (its database), AppTown containers no record names, and other
  containers AppTown did not start. `apptownd` itself is counted once: in the
  host's own use when it runs on the host, in its container when it runs in
  one. Colour follows
  the kind of holder; each deployment is named on hover and in the table, and
  links to it. Small holders of a kind fold into "N more".

  The host's own use, and free memory, are known only on the built-in host,
  and only when `apptownd` and the daemon report the same memory total.
  `apptownd`'s memory figures are then taken as the daemon machine's. A
  remote daemon on a machine with exactly the same memory as `apptownd`'s
  would be read wrongly. Otherwise the rest of the bar is hatched "not read",
  never free. Attached hosts do not
  report the host's own use in 1.0; that optional figure is absent from the
  capacity response and does not make a successful check unknown. Per-deployment
  disk use is not shown.

  **A Portainer endpoint** shows what it can give containers, and the limits
  AppTown's own deployments there declare.

  The disk is read by a one-off `df` in a small pinned image (busybox), with no
  network and the folder mounted read-only, at most every five minutes. A host
  that cannot run it shows the disk as unknown. Through Portainer, use and disk
  are not read.

**Disk space.** New work is refused while a disk it would write to has less
free than its floor, a twentieth of the disk but at least 1 GiB and at most
20 GiB, or when the disk cannot be read; the refusal says what is free. Each
disk is read again when the work starts, not taken from the page's reading:
- a release, recovery or preview on a Docker host reads Docker's data
  folder there before it pulls or starts anything; the deployment keeps
  serving what it served;
- a worker reads its state folder and its Docker's data folder once for a
  new task, before it writes anything of it; a release build's image that would leave the state folder under its floor
  is not copied, and the build fails saying so.

Stopping, taking down and removing are never refused. This keeps room for
the next piece of work; it does not reserve it, so two at once or one
larger than its free space can still fill a disk. The Executor page shows the disks where work runs; a target’s own disk is on its Capacity page.

To free space on a Docker host, remove what you know is unused: stopped
previews (AppTown stops and removes them itself after their idle time),
images no container uses that are not a release you may deploy again
(`docker image ls`, then `docker image rm` by name), and released tasks'
leftovers on a worker (releasing a task removes its files). Do not run
`docker system prune --volumes` or `docker volume prune`: a deployment's data
volume belongs to it, and a backup's evidence is not garbage. AppTown's own
image clean-up removes only the unused, untagged images it built itself.

**Container logs.** Every container AppTown starts for a deployment or
preview keeps its log in Docker's `json-file` files, rotated at 10 MiB with
three kept: about 30 MiB at most, and the oldest output is discarded. The
same holds for a worker's private services and daemons; a task's own
container is not rotated (its whole output is read while it runs, and it
goes when the task is released). Containers started before this keep their
logs until they are made again: a deployment's app parts at its next
deploy, a standing database only when it is next made again, never just to
change its logging. Portainer endpoints use their daemon's own default.
- **App Platform grows.** Its page lists the size each part runs on, with its
  listed price, and an estimated monthly cost. Managed databases are not
  priced by the API, and are left out of the estimate.
- **Fly grows too.** Its page lists the Machine size each part runs on
  (shared CPUs and memory, rounded up from what the part declares; 512 MB
  when it declares none). A need uses its declared resources or its catalogue
  default. Fly's prices are not read, so no cost is shown. A part with a
  data folder stops briefly during releases and backups. Postgres keeps its
  Machine and volume across releases; an incompatible image version is refused.

Fly backups require an attached backup store. AppTown exports Postgres live
as the app's user and copies data folders while their parts are stopped.
The copies are unencrypted. Restore takes a safety backup, stops all app
writers, restores and verifies Postgres in place, and fills a new volume for
a data folder. Take-down stops the app, takes a final backup, then deletes
the Fly app and volumes. Without a store, when a need cannot be copied, or
after the final backup failed, choose to keep the data: the app
and volumes stay under retained resources with every Machine stopped. A
deployment that was never released holds nothing at Fly and is removed
without a final backup.

A deployment's card shows its share of its host, and over the last day each
part's **limit events**: killed out of memory, or exited without anyone
stopping it. An exit is what Docker saw, not proof that a limit was reached.
A crash-loop hold is AppTown's own act and is in the deployment's history,
not among these. When the held container belongs to a released generation, a
note is added to that release's log: the release still reads as shipped, and
the later crash loop and stop are separate facts beside it. Limit events
are read from the Docker host's event log every 15 seconds. Docker replays only its recent events, so a burst, or a time when
AppTown or the daemon was down, can leave a gap. App Platform does not report
them.

The card's **diagnostics** line says which part is failing and what to do
first, for the generation the deployment settled on. Its details show each
part's and need's container as its Docker host reports it when you look:
running, stopped or gone, the last exit code, an out-of-memory kill, Docker's
restart count for that container, when it started and finished, and its health
check ("not configured" when it has none). A host that does not answer shows
**unknown** with the facts last read and when, never stopped. Each limit event
names the generation it happened under where that is known. **Read log** shows a
part's newest 200 lines (at most 64 KiB) once, to someone who may operate the
deployment; logs can hold secrets, so AppTown keeps no copy and does not
redact them. App Platform and Portainer deployments say they do not report
these.

A Docker target's page also lists what AppTown recorded creating there
(containers, volumes, networks and images) beside what Docker reports now. A
container is one row per name, however many times it was replaced. **Missing**
means a complete answer from Docker did not include it; if any listing fails,
the page keeps the previous answer with its time and the error rather than
showing a partial one. Images on the host that no record names are not listed
for removal.

A deployment that reads **uncertain** (its last attempt was cut off, or its
records disagree) takes no new deploy until it is settled. Press **Check
again** on its card: AppTown asks the host what runs and records the
answer.

## Start, stop and restart

Open a deployment to use **Start**, **Stop** or **Restart** beside its name and
status. Stop asks once before acting. It keeps the deployment's data, address,
secrets and settings. Restart runs the current release again without a rebuild
or a pre-deploy backup. These actions need deployment operate permission and
record who acted and when.

Docker hosts and Portainer stop only the app's containers; databases and other
needs stay up. App Platform supports Restart; Start and Stop are disabled because
it cannot keep an app stopped. Fly.io targets are the same: Restart restarts
each part's Machine, and Start and Stop are disabled. Bucket websites show all
three disabled with a reason.

Deploying new code to a stopped deployment leaves it stopped. Its page says which
release will run when someone starts it. This applies to automatic deploys,
promotions and rollbacks too.

The command line has the same actions:

```sh
apptown deployment start <project> <deployment>
apptown deployment stop <project> <deployment>
apptown deployment restart <project> <deployment>
```

## AWS ECS on Fargate

An administrator can enable the AWS ECS beta feature and attach an existing AWS
estate using the [AWS setup guide](aws-target-setup.md). The target runs single-part
public apps with an optional dedicated PostgreSQL RDS instance.
It refuses multiple parts, object storage, data directories, shared databases,
restored-copy placements and audiences that require AppTown sign-in before any
provider effect. Images must be published by digest for Linux/AMD64.

Attach names the expected account, region, cluster, task subnets, HTTPS listener,
execution role and apps domain, plus optional database subnets. Access keys are
instance secrets, read at use time. AppTown checks these explicit values and
manages only resources it creates for each deployment. The existing AWS estate,
DNS and prerequisite templates remain the administrator's responsibility.

A backup store enables database copies through one-off ECS tasks; no copy runner
host is needed. A backup set includes the database list, dumps of databases with
user tables, and evidence for databases checked as empty. Each dump is restored
and checked in a scratch database before a final take-down may delete the RDS
instance. Only RDS's own `rdsadmin` database is excluded. Failed checks keep data.
RDS's automatic snapshots are shown separately and are never AppTown backup sets.

A take-down without a final copy keeps the RDS instance and its database network,
listed as retained resources with continuing costs. Deleting the instance also
deletes its automatic snapshots. Costs that cannot be read are shown as unknown.
