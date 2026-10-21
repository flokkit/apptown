# Container and workspace cleanup

Maintenance owns periodic cleanup policy; Docker implements the commands.
Normal operation cleanup handles completed work. The periodic reaper provides
a fallback for resources left behind by missed cleanup or process interruption.

The daemon schedules maintenance about every five minutes. `APPTOWN_REAP=on` enables
deletion; other values select log-only operation. Reaping selects labelled resource
classes and retains persistent `env`/infrastructure resources and unknown classes.
Execution cleanup uses the observed container ID, not a reusable name.

Step, service and DinD cleanup checks recognized execution ownership after the
age threshold. Previews are not reaped by age (see [Previews](#previews)).
Active owners, unknown ownership and failed ownership reads prevent that
execution cleanup. Private console daemons are cleaned up with their session
rather than independently by age.

A step's services network and its private daemon's network carry the same
`forge.class` and `forge.owner` labels as their containers, set when the
network is made. The reaper selects networks by those labels, never by name,
and removes one, by listed ID, once its execution owner has finished, under the
same rule as the containers: an unknown owner or a failed read keeps it. Docker
refuses one that a container is still on, so it goes on a later pass after its
containers. Each network held takes one of Docker's address pools, and a step
that cannot get one fails at `network create`. A network without AppTown's
labels is never selected. Do not clear networks with `docker network prune`:
it would also take a stopped deployment's network.

One AppTown owns one Docker daemon. The labels name a class and an owner but not
AppTown, and step IDs are numbered per forge, so two AppTown installations sharing a daemon
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
  forge's clock, at most once a minute.
- **Stopping** removes the whole stack: the app with its volumes and data
  volume, its services, parts and init leftovers, and its network
  (`docker.RemovePreview`). A change's discard, replacement and restart, a
  failed boot, a release's look, the idle stop and AppTown's start-up
  sweep all remove that way. A run's completion removes every one of its
  preview-step stacks.
- **Preview tends its previews** every five minutes with the other
  maintenance duties (`preview.Service.Tend`, log-only unless
  `APPTOWN_REAP=on`). It lists the previews present on AppTown's Docker
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

- [internal/forge/maintenance/containers.go](../../internal/forge/maintenance/containers.go)
- [internal/forge/maintenance/workspaces.go](../../internal/forge/maintenance/workspaces.go)
- [internal/forge/triggers.go](../../internal/forge/triggers.go)
- [internal/forge/maintenance/README.md](../../internal/forge/maintenance/README.md)
- [internal/forge/preview/worker.go](../../internal/forge/preview/worker.go)
- [internal/forge/preview/looks.go](../../internal/forge/preview/looks.go)
