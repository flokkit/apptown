# Container and workspace cleanup

Maintenance owns periodic cleanup policy; Docker implements the commands.
Normal operation cleanup handles completed work. The periodic reaper provides
a fallback for resources left behind by missed cleanup or process interruption.

The daemon schedules maintenance about every five minutes, and it removes
what it finds. `apptownd -disable-reap` (`APPTOWN_DISABLE_REAP=true`) turns
deletion off, so maintenance only logs what it would remove. Set it when
another AppTown shares the Docker daemon: reaping does not ask other AppTowns
what they still use. Reaping selects labelled resource
classes and retains persistent `env`/infrastructure resources and unknown classes.
Execution cleanup uses the observed container ID, not a reusable name.

Step, service and DinD cleanup checks recognized execution ownership after the
age threshold. Previews are not reaped by age (see [Previews](#previews)).
Active owners, unknown ownership and failed ownership reads prevent that
execution cleanup. Private console daemons are cleaned up with their session
rather than independently by age.

A step's services network and its private daemon's network carry the same
`apptown.class` and `apptown.owner` labels as their containers, set when the
network is made. The reaper selects networks by those labels, never by name,
and removes one, by listed ID, once its execution owner has finished, under the
same rule as the containers: an unknown owner or a failed read keeps it. Docker
refuses one that a container is still on, so it goes on a later pass after its
containers. Each network held takes one of Docker's address pools, and a step
that cannot get one fails at `network create`. A network without AppTown's
labels is never selected. Do not clear networks with `docker network prune`:
it would also take a stopped deployment's network.

A throwaway container's image may declare an anonymous volume: the shell
checks' image declares `/git`, a database image its data folder, an app image
whatever its Dockerfile declares. `--rm` does not delete that volume when the
container is removed with `rm -f`. So when its work ends, a throwaway
container (a check, a test, a task, an agent, a step's service, a job) is
removed with its anonymous volumes (`docker rm -f -v`), and the reaper
removes leftovers the same way. Named volumes are never deleted this way. One
case still leaves a volume: before a service or init container starts, a
container of the same name left from an earlier attempt is replaced without
`-v`.

Disk readings (`diskspace.ReadThroughDocker`: a target's capacity, a
worker's disks, and before a task or a boot is admitted, on every daemon
AppTown reads) run in the daemon's standing disk reader
(`containerresult.RunStanding`), not a container of their own. They recur
for as long as a page is open, and each container start costs the host's
init a scope, a network interface and overlay mounts; a CT whose systemd
kept a little of each was killed for memory after weeks of them. An exec
into a running container makes none of these (on a Proxmox LXC, 1,000
reads through one reader made no mount units and no scopes; 1,000
`docker run --rm` reads made 1,000 scopes).

The reader is one long-lived container per daemon, `apptown.class=disk-reader`,
named `apptown-reader-disk-reader-` and a hash of its image and options: the
tool versions' `df` image with the data root bound read-only at `/d`, no
network, `--restart unless-stopped`, running `sleep`. A reading lists the
class's containers, removes the class's other readers (an older image's)
that no read in the process is using, starts the reader if it stopped, makes
it only when it is gone, then runs `df -Pk /d` in it with `docker exec`, its
output written to a file in the reader's `/tmp` and copied out with
`docker cp`; the answer is accepted only when it parses. A process lists,
makes, starts and removes a class's readers one call at a time, because
`docker run` reserves a name before the container can be exec'd into; a
reader found gone when the exec runs is made again once and read again. Anything else is an unknown reading, which refuses new work;
nothing falls back to a throwaway container. No reaper or sweep selects the
reader, and the capacity view counts it as AppTown's own.

Portainer readiness samples (one per sample period for each deployment under
observation) run the same way. The probe joins the public container's network
namespace, so the standing reader is per deployment: class
`readiness-<deployment id>`, `--network container:<public container id>`, and a
`apptown.ready-for` label holding the container's id and start time. A restart
of the public container changes that label and so makes a new reader (a reader
joined to the old namespace would probe a dead one); making one removes the
deployment's older one. Taking the deployment down removes its readers through
the endpoint's Docker API. The diagnostic's run and exec are gated by the same
current-authority check as its other effects.

A standing deployment's containers are removed without `-v`: one made before
its data moved to named volumes may still hold its data in an anonymous one.
For the same reason, do not clear volumes with `docker volume prune`.

One AppTown owns one Docker daemon. The labels name a class and an owner but not
AppTown, and step IDs are numbered per AppTown, so two AppTown installations sharing a daemon
would each read the other's resources as their own and judge them against the
wrong steps. That arrangement is not supported.

An idle console may stop or lose its disposable container while retaining the
conversation and checkout ownership. An ended session has different cleanup rules.
A saved conversation can retain a closed Change's workspace. Workspace cleanup
preserves transcripts before removal and retains the source if preservation fails.

The reaper acts on the AppTown host's own Docker only; it never reaches a worker.
On a worker, cleanup has other owners. The
AppTown releases each task by key, and the worker removes everything of a released
key; the worker's own sweep stops a task whose admission expired and removes its
private Docker. AppTown keeps only previews it has a record of and, every
minute, removes any other preview on the worker that is not booting; the worker
sweeps previews whose removal or boot failed. The worker does not collect
workspaces of tasks AppTown never released. See
[workers](../operations/workers.md#previews-on-the-worker) and
[`internal/worker`](../../internal/worker/README.md).

Deployments are not reaped. Their resources are retained by class here and
leave only by the deployment's own take-down, which deletes what AppTown
recorded creating for it (on a Docker host, data only after a complete final
backup; other targets keep their data), or by project deletion, which uses that
take-down ([deployment](deployment.md#removing-a-deployment)).

The shared daemon shutdown deadline bounds waiting. It does not prove every
legacy Docker command is cancellable. Resource caps and LRU eviction remain
proposals. See [console](console.md) and
[daemon lifecycle](daemon-lifecycle.md) for the related lifetimes.

## Previews

A preview stops about eight hours after anyone last looked at it. A change's
preview keeps its build images and its snapshot for thirty days after the
last look; then they go. The same rule holds on AppTown's Docker and on a
worker.

- **A look** is a request to the preview's address through AppTown's
  preview (or eval) domain, or a boot of it: the open or restart button,
  `apptown preview`, a chat's rehearsal, a run's preview step, a release's
  look. Preview records the last one per preview (`preview_views`), on the
  AppTown's clock, at most once a minute.
- **Stopping** removes the whole stack: the app with its volumes and data
  volume, its services, parts and init leftovers, and its network
  (`docker.RemovePreview`). A change's discard, replacement and restart, a
  failed boot, a release's look, the idle stop and AppTown's start-up
  sweep all remove that way. A run's completion removes every one of its
  preview-step stacks.
- **Preview tends its previews** every five minutes with the other
  maintenance duties (`preview.Service.Tend`, log-only when
  apptownd runs with `-disable-reap`). It lists the previews present on AppTown's Docker
  (running or exited) and on the worker, and stops each whose last look is
  over eight hours old, or that has none. Each decision is read again under
  the preview's boot lock, so a boot or a look since keeps it. Looks that
  cannot be read, or a place that cannot be asked, stop nothing that pass.
- **Kept builds.** Stopping keeps a change's preview's build images
  (`apptown-preview-build-forge-preview-change-N`, and its parts') and its
  snapshot folder (`workspaces/asof-change-N`, always on AppTown). Each
  preview build carries its cache inline and builds from the kept image when
  its tag is there, so a reopen builds quickly though BuildKit's own cache
  was trimmed; BuildKit decides what matches. A build that moves its tag
  removes the image it replaced once nothing else names it. Thirty days
  after the last look, Preview removes the kept images (on a worker, by
  asking it) and the snapshot, then the look. Snapshots and local build
  images of a change's preview with no look and nothing present go too.
- **Project deletion** removes everything at once, kept or not.

Accepted limits: an AppTown without a preview domain sees no request to a
local preview at its published port, so such a preview stops eight hours
after its last boot even in use. A WebSocket or a long streaming response is
one look when it opens. Any request counts, a polling tab's or a bot's.
Times are approximate to a few minutes. A reopen is a build: quick when the
kept image or BuildKit's cache match, not guaranteed. Kept images are not
bounded in size.

## Implementation references

- [internal/apptown/maintenance/containers.go](../../internal/apptown/maintenance/containers.go)
- [internal/apptown/maintenance/workspaces.go](../../internal/apptown/maintenance/workspaces.go)
- [internal/apptown/triggers.go](../../internal/apptown/triggers.go)
- [internal/apptown/maintenance/README.md](../../internal/apptown/maintenance/README.md)
- [internal/apptown/preview/worker.go](../../internal/apptown/preview/worker.go)
- [internal/apptown/preview/looks.go](../../internal/apptown/preview/looks.go)

## Deleted projects' previews

Project lifecycle retains run, change and preview-step IDs, and the complete
historical release inventory (project UUID, immutable serving label and release
numbers), before deleting source records. Preview retires the disposable containers, stacks, build images,
looks and snapshots named by that inventory, on the worker and local Docker as
applicable. These operations require no source-row lookup. Application volumes
are outside this preview retirement.

Cleanup attempts independent effects even when another fails. The retained
inventory and failure detail drive retries; a missing snapshot does not mean
that a container was removed. Ordinary workflow workspaces and shared transcripts
remain separate lifecycle phases. Images still used by another container stay;
image removal never forces them.

Historical release retirement serializes with release boots and uses the retained
label even after an address change. It retires the exact local release stack and
worker preview, kept images and look, then observes absence before releasing its
keys. Shared images and standing application data remain protected. Missing
historical inventory is unknown ownership, requiring inspection rather than an
inferred successful cleanup.
