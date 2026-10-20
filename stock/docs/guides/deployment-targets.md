# Deployment targets

A deployment target is a place deployments run. Add one under
**Infrastructure → add a deployment target**. Each credential is the name of
an AppTown secret; set it there, or with `apptown secret set <NAME> < file`.

| Kind | What you give it |
| --- | --- |
| Docker host | a machine with Docker, reached over SSH: its host and the secret holding the SSH key. AppTown's own Docker is the built-in one |
| Portainer environment | the Portainer address, API key and environment |
| DigitalOcean App Platform | an API token, the region, and the container registry it copies images into. Run the registry's garbage collection occasionally to free space taken-down projects left; it blocks pushes (and so releases) while it runs. An app that needs object storage gets a Spaces bucket, so the account needs Spaces enabled, and the token makes and deletes Spaces keys: for each bucket a key that reaches it alone, and for a moment while it makes or deletes one, a full-access key it deletes again |
| Bucket website | an S3-compatible endpoint, region, website address and keys. Each deployment gets its own bucket, and taking the deployment down deletes it; use a key made for AppTown alone |

The intranet target is the Portainer environment AppTown is configured
with rather than one added here: it appears when the secrets `PORTAINER_URL`,
`PORTAINER_API_KEY` and `FORGE_APPS_DOMAIN` are set, and releases to the
environment `apptownd -portainer-environment` names (`APPTOWN_PORTAINER_ENVIRONMENT`;
default `local`, the name Portainer gives the Docker it is installed on). AppTown records the setting in the `portainer-deploy` integration at every
start, so starting without it sends intranet releases to the environment
named `local`, or has them refused when that Portainer has none.

A project's deployments are made on the project's **Settings → Deployments**.
What each kind runs, and what a deployment owns and deletes when taken
down, is in [Previews and deployments](/docs/guides/container-lifecycle).

**How full a target is** is on its page, under Capacity:
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
    and when it was read. Below a tenth free it says so; below the floor
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
  and only when the daemon sees `apptownd`'s files and the same memory total:
  `apptownd` writes a random marker into a file of its temp folder, the daemon
  reads it back through a read-only mount in the same small pinned image as
  the disk read (at most every five minutes), and both report the same total.
  `apptownd`'s memory figures are then taken as the daemon machine's. The case
  this cannot tell is two machines of equal memory sharing `apptownd`'s temp
  folder over a network filesystem. Otherwise the rest of the bar is hatched
  "not read", never free. Attached hosts do not
  report the host's own use in 1.0; per-deployment disk use is not shown.

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
larger than its free space can still fill a disk. Infrastructure lists this
AppTown's data folder and its worker's state folder and Docker data, each
with what is free and when it was read; a target's own disk is on its page.

To free space on a Docker host, remove what you know is unused: stopped
previews (AppTown stops and removes them itself after their idle time),
images no container uses that are not a release you may deploy again
(`docker image ls`, then `docker image rm` by name), and released tasks'
leftovers on a worker (releasing a task removes its files). Do not run
`docker system prune --volumes` or `docker volume prune`: a deployment's data
volume belongs to it, and a backup's evidence is not garbage. AppTown
has no image clean-up of its own.

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
