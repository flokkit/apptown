# Worker

`apptown-worker` (`cmd/apptown-worker`) runs an AppTown's tasks on a machine that
shares no files with it. This package is its server: an HTTPS API over mutual
TLS that accepts only the client certificate naming its one installation,
workspaces cloned from Git bundles, tasks run through an
`execution.TaskDriver` (the worker's own Docker in production), and results
returned as Git bundles of the commits a task made.

Before acting on a task the worker writes one fsynced record of the request
(`state/records/<key>.json`): a restarted worker refuses the same key for a
different request, and finds its tasks again through its driver. It knows
nothing about projects, workflows or AppTown's database; AppTown's side
is `execution.WorkerTasks`. `workertest` starts a worker in a test: in process,
as a process on this machine driving its Docker (`StartOnHost`), or in its own
Docker-in-Docker container.

Every task's, check's and chat's container mounts the worker's tool cache
(`tool_cache.go`, `Server.ToolCache`, `<state>/tool-cache/mise`) writable at
`execution.ToolCacheMount`, where AppTown points mise's download cache: one
cache of release listings and archives per worker, never a tool's
installation, which stays in the container.

It also runs chats (`sessions.go`, `session_io.go`): AppTown's own chat
runtime (a `console.Agent`, pi under tmux in production) on the worker's
Docker, with the chat's files under the state directory. A session's record
is marked removed before its runtime is taken away and is kept, so a late
start cannot bring an ended chat back; a removal succeeds only once Docker
shows nothing of it. Stopping or rebuilding keeps the session; a chat is
rebuilt only while it is stopped, by Docker's answer, so a new image, model or
forge CLI (a new content-named blob, so a new mount)
reaches a running chat when it next stops. On a worker the chat runtime gives
each session its own cache volume, removed with its runtime, the worker's
tool cache (`tool_cache.go`) in its container, and the
worker's chat limits on the chat's container, its daemon and a save's
container; the daemon uses the worker's resolvers and the mirror AppTown
sends in the session's spec.

On AppTown, `RemoteAgent` is that runtime for the chat service: the same
interface, answered by the worker (`SessionClient`), so the service is
unchanged. What AppTown mounts into a chat travels as a blob named by its
content (the `apptown` binary, generated registry files); the chat's other
files are sent when they change; the work tree is made once from a bundle
of the change's branch; messages, interrupts and the terminal go through;
the chat's record is copied back with the time of the last good copy, and a
turn handed back in that copy is the moment to save. A save is a bundle of
the chat's commits since the branch's tip, made by the chat's own git in a
short-lived container of the chat's image with only its work tree mounted
(`Save`); AppTown imports it through its checked import and publishes it
(`repositoryedit.SaveChat`). Ending a chat stops it first, then makes its last
save with what it left uncommitted (`SaveLast`, through the `Ending` hook),
copies its record, and removes it. AppTown calls the worker; the worker
never calls AppTown. On AppTown, `console.Placed` gives each chat the
runtime its record places it on. An answer
AppTown cannot get is `execution.ErrWorkerUnavailable`, never absence, and
the chat shows as unavailable. The agent image is built by the worker from
its own `deploy/apptown-agent` and named by that context's content
(`AgentImage`), so an AppTown and a worker of different versions refuse rather
than mix; `GET /v1/agent-image` reports its name and labels. `Reach` reports
which registries the worker can reach.

A task's inputs (`task_io.go`) are blobs AppTown sent, checked against
their bounds by the blobs' own sizes before anything is created (AppTown
asks, `POST /v1/inputs/check`, before it uploads a source, so a refusal
leaves no workspace), copied into
the task's folder (`state/task-inputs/<key>`) and mounted read-only at their
paths. Each workspace has a state folder (`state/workspace-state/<key>`)
mounted writable at `/apptown/state`, beside the tree, so nothing in it is
collected; it goes with the workspace. A task's declared result files are
read once its container has exited, confined as a check's report is, and
kept durably with the task (`state/task-results/<key>`) until it is released,
so a restarted worker answers the same, and a later change to the tree does
not reach them. A release build's declared images (`task_images.go`) are
captured at the same moment, under the task's lock: each OCI layout in the
state folder, a folder of regular files reached and holding no link, within
its admitted size, is copied into `state/task-results/<key>/images`, and only
the record written after every copy makes it visible. The index and blobs
are served file by file (`GET /v1/tasks/{key}/images/{name}/{file}`) with
their length. A file is opened under the task's lock, so after a release is
decided a new open is gone (410); a read already open finishes.

A task's private Docker (`private.go`, from the private-Docker plan) lives
from its start to its exit. Its record names its network, services and
daemon before any exists; the network is made when it is ensured, the
services (each with the worker's service limits, by default 1 CPU, 1 GiB
and 256 processes, waited on until healthy)
and the daemon (`internal/dind`: the task's own limits, its tree at the same
path, its client state in `/apptown/state/docker`, no cache or socket of the
worker's, the privileged mark first) when it first starts, and never again.
A failed start removes what it made and is refused with the reason. When
the task exits its daemon and services are stopped, confirmed by Docker,
before it reads as exited and its results are captured; until then it reads
as running. One running without them is reported absent (lost). A stop
stops the whole invocation, the task and then its daemon and services, and
fails unless Docker confirms them stopped; a workspace is not collected
while any task in it has private Docker not confirmed stopped. Ensure,
start, stop and release of a task are serialized by its lock. Release
first writes its decision (`state/released/<key>`), so from then on, after
a restart too, a request for the key is refused as stale; it then removes
everything, and confirms only when nothing of the key is left; one
interrupted is asked again. The worker's sweep stops a task whose admission
has expired and removes its private Docker, with or without AppTown.

New work is refused while a disk it writes to is short of space
(`diskspace.Admit`, `admitNew`): a new task is admitted once, before the
first thing of it is written (its own workspace's upload, or else its
ensure before its record), reading the state folder and the task driver's
Docker data folder; the driver does not read it again
(`execution.DockerTasks.CallerAdmitsDisk`). A disk filling after that is
the accepted race. An image capture that would leave the state folder
under its floor is recorded refused, not copied.
`GET /v1/disks` reports both readings.

An agent worker measures its profile when it starts (`profile.go`): what
`docker info` says of its Docker (OS, architecture, kernel, cgroup mode,
version, storage driver), the private daemon's image (pulled first if
absent): its tag, Docker's local id of it, its repository digest and the
version its own `dockerd --version` reports, whether a privileged container
runs (probed, after the privileged mark), and the operator's `-exclusive`
declaration. `GET /v1/profile` reports it; AppTown records it with each
attempt admitted with private Docker and sends it in the request. A new
task's ensure, and its first start, measure Docker again and refuse private
Docker admitted on another profile, or on none: the OS, architecture,
kernel, Docker version, storage driver, cgroup mode or (for a request with
a daemon) the daemon's image, digest or version changed. The daemon's
version is measured again only when the image's id is not the one measured
at start.
Operating guide:
[design/operations/workers.md](../../design/operations/workers.md).
