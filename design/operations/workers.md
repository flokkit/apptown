# Workers

A worker is a separate machine that runs an AppTown's script steps, tests
steps, agent and review steps, checks and change previews, and its change
chats, so project code in those does
not run on AppTown's own host. This guide says how to run one and what it
does and does not isolate.

## What a worker isolates, and what it does not

With a worker configured, AppTown is **isolated for script steps, tests
steps, agent and review steps, checks and change previews**, and for change
chats (see the sections on each below):

- A script step with `executor: docker` runs on the worker's own Docker
  daemon, in the image the step names.
- A script step with `executor: local` (the default for scripts) is refused
  with "this apptown runs scripts only on its worker". Give such steps
  `executor: docker` and an image.
- A script step on a project with registry bindings runs on the worker with
  its registry configuration. AppTown generates the configuration files
  (mise's system configuration, the cargo configuration, maven's and
  NuGet's settings, the gemrc) as bytes, and they travel with the task.
  The worker keeps them in the task's own folder, `<state>/task-files/<key>`,
  outside its tree, from before the task is created until it is released,
  and presents each read-only at its path. The generated files are part of
  the task's identity: if the bindings change while AppTown is down, the
  recorded task is stopped and its step is an error, never run under other
  configuration. The worker shares one cache between its containers, the
  tool cache for mise's downloads (see "The tool cache"); package-manager
  downloads (npm, Go modules, Maven) still repeat.
- Checks carry generated files the same way, written in the check's own
  folder beside its tree. On AppTown's own host, the files are written
  under `<data>/generated` for a run and `<data>/task-files` for a task,
  never into a project's tree.

- Every check runs on the worker: `apptown check`, guidance checks (shell and
  tool checks, exec-image checks), source and image scans with their
  baselines, a landing's history scan, and the landing's compile check. The
  images they need are built or pulled there. `cmd/apptown-check`, a local
  operator tool, stays on AppTown's host.
- Every preview of a change or a run boots on the worker, and runtime checks
  probe it there. A preview of a release whose images were published to a
  registry boots there too, by digest; only a release recorded before then
  boots on AppTown's host (see "Previews on the worker").
- Every tests step runs on the worker as a check:
  the change's commit, and the base commit when the change's tests fail.
  A tests check may reach the network (installs need it), writes only its
  scratch tree, and gets the generated registry files. It shares only the
  worker's tool cache (see "The tool cache"); other downloads repeat. The
  app's services run beside it under the worker's service limits (see
  "Limits on services"). Only a witnessed exit answers:
  exit 0 passes and any other exit fails. A run that could not start, timed
  out, was stopped, left its container behind, or whose answer was lost is
  an error, and never makes a failure carried debt. A worker that cannot be
  reached is an error; nothing falls back to AppTown's host.
- A tests step may repeat what it does outside the worker. Its network
  access is not limited to reads: a test that writes to another service
  writes again when the step is run again, after a lost answer, a restart
  or a retry. Nothing makes a test run exactly once. Work that must not
  repeat belongs in another admitted step, not in a test.
- A tests step and guidance checks read the same one commit: a landing's
  pinned candidate from the project's repository; for a change, its saved
  branch when the folder is behind it or absent, otherwise the folder's
  `HEAD`; for a run without a change, its folder's `HEAD`. The evidence
  names the commit. Source that cannot be read is an error, never a pass.

- Agent steps (`agent/pi`) and review steps (`agent/review`) run on the
  worker, in the worker's agent image, and nowhere else. A step that needs
  what the worker cannot provide yet (source-control access, another
  image) is refused, never run on AppTown's host instead. Private Docker
  (`docker: true`) runs there only when AppTown's operator opts in;
  services and the app's needs run beside the step. See "Agent and review
  steps on the worker" and "Private Docker on the worker".

Everything else still runs on AppTown's host exactly as before: project
chats, chats continuing a step's agent, chats the person opens on AppTown,
the containers of deployments on the built-in Docker host, previews of
releases recorded before releases were built on the worker, and intranet
releases of a landing that has no published images. A landing's release
images are built on the worker (see "Releases built on the worker"). A worker
does not make AppTown safe to run untrusted projects.

Workflow drafting ("AI assist") stays on AppTown and runs no container or
process: it is one tool-free model call, made in-process through AppTown's
gateway as the person who asked, on the `draft` slot's model. A worker
isolates project code, and there is none here to isolate.

## How work travels

The worker shares no files with AppTown.

1. Before a script task starts, AppTown publishes the change's committed
   branch (the same guarded publication a step's commit uses) and records the
   published commit on the task attempt. Uncommitted files in AppTown's
   workspace are not part of it.
2. AppTown sends that one commit to the worker as a Git bundle. The worker
   clones it into a workspace of its own and runs the task with the files at
   `/work`. The workspace's Git directory is kept apart from `/work`, so
   nothing a task writes can configure or hook the worker's own git. A
   script task's `/work` has no `.git`, and a script that needs Git history
   does not work on a worker yet. (Agent and review steps are given a
   repository of their own; see "Agent and review steps on the worker".)
3. When the task exits, the worker commits what it changed in `/work` as
   git takes it (the project's `.gitignore` applies, and the workspace's own
   Git directory also ignores untracked files under `.apptown`, so a step's
   report there stays out of a repository whose rules do not mention
   `.apptown`; the native app contract `.apptown/app.yaml` is taken even where
   ignored) and returns the
   commits made after the given commit as a Git bundle. Collecting again
   (after a lost reply) returns the same commits. The worker decides
   nothing about what may enter the branch; AppTown does, below.
4. AppTown checks the bundle before trusting it: it must be a valid bundle
   of one branch, every object in it must pass `git index-pack --strict`
   (which refuses a tree naming `.git` or `..`; deltas against the given
   commit's objects are completed from them), its size is bounded
   (256 MiB), its tip must be the commit the worker named, and it must
   build on the commit the task was given. The commits are imported into a
   scratch repository first. A bundle that fails any of these is kept
   nowhere.
5. The checked tip is kept in the project's repository as the attempt's
   candidate, `refs/apptown/candidates/step-<step>-a<attempt>`, beside the
   commit the attempt was given (recorded on its task attempt). A task that
   changed nothing has a candidate too: the commit it was given. Collection
   is done only once that ref exists; an AppTown that restarts after it finds
   the candidate and does not collect again.
6. AppTown moves the change's branch to the candidate by compare-and-swap
   in the project's repository, under the authority the step was admitted
   with, and only if the branch is still at the commit the task was given.
   Only source may enter the branch: a result any of whose commits changes
   `.apptown` itself or anything under it other than `.apptown/app.yaml`
   (runtime files, which never travel) is refused, naming the files, as is
   a file over the publication's size limits. Every returned commit is
   checked, not only the last one. A branch handed to someone else, or
   moved by other work, while the task ran refuses the result too. A
   landing's run publishes nothing: its candidate is kept and the branch is
   not moved. Only a refusal the branch owner decided refuses the result.
   A publication whose branch moved but whose record could not be written,
   or whose outcome is unknown, is not a refusal: the candidate stays, the
   AppTown asks again (reading the branch first, so nothing is published
   twice), and if it is still undecided the step and task stay recorded
   for a restarted AppTown to finish from the branch.
7. A refused result is not lost: the candidate is renamed to
   `refs/apptown/refused/step-<step>-a<attempt>`, the run's
   `commit.unpublished` notice names that ref, the commit and the reason,
   the branch is unchanged and the step fails with the reason. The step's
   evidence (`result`) names the ref that keeps the result, its commit and
   whether it was refused. Anyone who
   can read the run downloads it as a Git bundle from
   `GET /api/v1/runs/{run}/steps/{step}/attempts/{attempt}/refused` and
   fetches the ref from it to repair the work.
8. AppTown's workspace is fast-forwarded to the published commits. If it
   cannot be (it has its own commits, or its Git metadata is not the
   forge's own clone), the branch still holds them, the workspace is left
   alone and readers read the branch.

The worker's workspace is released only after the candidate or its refused
ref exists. If the result could not be collected, failed the checks, or its
refusal could not be kept, the attempt is recorded as unknown ("result not
kept") and then the step fails; the task and its workspace are held on the
worker, also by a restarted AppTown. If that record cannot be written, the
step is left running for a restarted AppTown to collect again. A restarted
AppTown releases an attempt whose step ended while its result was being
collected only if the candidate or refused ref exists; otherwise it holds
it.

Git runs hardened in the project's repository on this path: no system or
global configuration, nothing from AppTown's environment but `PATH` and
an empty `HOME`, hooks and `core.fsmonitor` off, only local transports.

A checked landing's script runs on its pinned candidate; its result is
kept as its candidate and nothing is published.

## Recovery

Each task is recorded by AppTown before it is created and again before it
starts; the worker records each task too, in an fsynced
file under its state directory, before acting on it. An AppTown restarted while
a task runs finds it on the worker by its key and does not start it again. A
worker restarted while a task runs finds the task in its Docker daemon; its
records let it refuse the same key for a different request.

A worker that cannot be reached has not said the task is gone. While a task
runs, AppTown keeps asking until the step's time limit (its recorded
deadline). Reading the task's output and collecting its commits are retried
for up to five minutes. If the worker is still silent after that, the step
fails with the reason. When the output could not be read, the attempt's
outcome is recorded as unknown, not as failed; when the commits could not
be collected, the attempt is recorded as unknown and its workspace held
(above). Either way the task is not started again and nothing it made is
published. A reply that is malformed (a result that names no
commit) is treated like silence: asked for again, never taken as "no
changes".

An AppTown restarted while a task runs recovers its attempt in the same way,
bounded by the same recorded deadline, not by the restart. Before the
deadline it keeps asking an unreachable worker. After it, AppTown takes
one brief look; if the worker still does not answer, the step fails (timed
out, or its outcome unknown) and a stop is recorded for the task. The
recorded stop is durable: a later start, once the worker answers, stops and
removes the task. Nothing
needs another manual restart to make progress, but a task on a worker that
never comes back stays recorded, with its stop, until it does.

An agent or review step's invocation is recovered the same way. Its token
survives a restart, and its workspace is held when its outcome is unknown;
see "Agent and review steps on the worker".

## Running a worker

`apptown-worker` is built from `cmd/apptown-worker`. It needs:

- a Docker daemon of its own (`DOCKER_HOST`, or the default socket) and the
  `docker` and `git` binaries. For previews, that daemon runs on the
  worker's own machine: the worker reaches each preview at the port it
  publishes on its own loopback. With `DOCKER_HOST` set to anything but a
  local socket, the worker runs no previews, and says so;
- to run as root on Linux, when its daemon's containers run as root (the
  default): steps, checks and chats write into the folders the worker
  bind-mounts, as root, and the worker reads and removes what they leave.
  Run as an ordinary user in the `docker` group, it cannot remove a
  workspace once a step has written there, and the workspace is never
  released. Docker Desktop hides this by mapping ownership to the user;
- a state directory it alone uses;
- a TLS server certificate and key;
- the CA certificate that signs the installation's client certificate.

```sh
apptown-worker -installation <name> -state /var/lib/apptown-worker \
  -cert worker.pem -key worker-key.pem -client-ca forge-client-ca.pem \
  -listen :8443
```

`-installation` is the one installation the worker serves: the Common Name of
the client certificate it accepts. Another installation's certificate is
refused (403) before any request is read; a client without a certificate
cannot connect. The worker speaks TLS 1.3 only.

Configure AppTown with:

```sh
apptownd ... -worker-url https://worker.example:8443 \
  -worker-cert forge-client.pem -worker-key forge-client-key.pem \
  -worker-ca worker-ca.pem
```

or `APPTOWN_WORKER_URL`, `APPTOWN_WORKER_CERT`, `APPTOWN_WORKER_KEY` and
`APPTOWN_WORKER_CA`. All four are required together; a missing or unreadable
file stops startup. The client certificate's Common Name must be the
worker's `-installation`. Task attempts then record the backend `worker`.

Issuing and rotating these certificates is the operator's job; AppTown
does not manage a CA.

**Versions.** `apptown-worker -version` names its build and AppTown protocols
it serves; `GET /v1/version` answers the same. AppTown states the protocol
it speaks on every request (`X-AppTown-Worker-Protocol`), and the worker refuses
one outside its range with `426` and a sentence naming both and what to install,
before any route runs; a check worker's refusal reaches AppTown's startup
error with those words. A request stating none (an AppTown built before
protocols were stated) is served. A worker keeps serving the previous
protocol for one release, so install the worker first, then AppTown
(`internal/worker/protocol.go`). Serving a protocol means keeping its
behaviour, not only admitting its number: a break that cannot keep the old
behaviour for a release is a stop-both-and-upgrade change, said in its notes. Protocol 2: a task's
observation says whether Docker killed it at its memory limit, so an AppTown
speaking 2 never hears a worker that cannot say (which would read such a
kill as an ordinary exit); a worker serves 1 and 2.

### The tool cache

Every script, agent, review and tests step, and every chat, installs the
toolchain its project declares in `mise.toml` before it starts
(`internal/console/toolchain.go` for agents and chats). Without a cache shared between them, each
install asked GitHub's API again for the same release listings, and a busy
worker ran into GitHub's limit on anonymous requests (60 an hour for one
address).

The worker keeps one folder, `<state>/tool-cache/mise`, and mounts it
writable at `/apptown-cache/mise-cache` in every task's, check's and chat's
container. AppTown's environment points mise's download cache
(`MISE_CACHE_DIR`) there, as it does at the `apptown-cache` volume on its own
host. What it holds is what mise fetches: release listings (kept an hour,
mise's `fetch_remote_versions_cache`), some tools' archives, and the lock
files mise takes, so concurrent installs of the same tool wait for one
another rather than write over each other. Several tools installed at once on an empty cache all succeed, and a
second install within the hour reads the listings from the cache and asks
GitHub's API nothing.

What it is not:
- **not the installs.** Each container installs the tools into its own
  folder (`MISE_DATA_DIR`), so a package one project installs into its
  Python is never another project's. An install still extracts, and still
  downloads the archives mise does not keep in its cache; GitHub's release
  downloads are not limited as its API is.
- **not a channel for the worker's own files.** No tree, record, credential
  or generated file is written there. It is writable by every container that
  mounts it, so one project's container can leave files there that another
  project's can read. mise checks the archives it installs against the
  checksums or attestations a tool publishes, where it publishes them.

When it starts, the worker logs the folder and its size:
`apptown-worker: tool cache /var/lib/apptown-worker/tool-cache/mise holds <n> MiB`.
The size is of the files it could read.
mise removes cache entries older than 30 days itself (`cache_prune_age`).
To clear it, stop the worker (or wait until nothing runs) and remove what is
in the folder: `rm -rf <state>/tool-cache/mise/*`. Nothing is lost but
downloads; the next installs fetch again.

There is no GitHub token for these downloads. An agent or review step can be
given one as any other value: a runtime secret `STEP_GITHUB_TOKEN` reaches
every step of every project as `GITHUB_TOKEN`, which mise uses (see
"Credentials a step carries"). Tests steps and chats do not get it. Give it
only a token that may read public repositories and nothing else.

### Limits on services

A service the worker runs beside a tests step or a check (a database, a
cache, an emulator the app's needs resolve to), and a service beside an
agent step's private Docker, gets each:

| Flag | Default | |
|---|---|---|
| `-service-cpus` | 1 | CPUs |
| `-service-memory-mib` | 1024 | memory in MiB, swap included |
| `-service-pids` | 256 | processes |

so one project's test database cannot starve the worker. A zero is refused
when the worker starts; it never means "unlimited". A service past its
memory is killed by the kernel, and the tests then fail or are an error as
they would with any service that stops. There is no per-service declaration
of limits; raise the defaults on a worker whose projects need more. A
preview's services, on the worker or AppTown's host, are not limited by
these.

### Limits on tasks and checks, and the metadata service

A task or check AppTown sends a worker carries AppTown's task limits
(`apptownd -task-cpus`, `-task-memory-mib`, `-task-pids`; 4, 8192, 4096 by
default), and the worker applies them to its container. A check request
with none, from an older forge, runs under the same defaults. A run killed at
its memory limit comes back as `out_of_memory`, never as an exit code: the
step errors and names the limit.

A container on a cloud host's Docker bridge reaches the provider's metadata
service (169.254.169.254) unless the host blocks it. On a cloud worker, add
a drop rule to Docker's `DOCKER-USER` chain:

```sh
iptables -C DOCKER-USER -d 169.254.169.254 -j DROP 2>/dev/null \
  || iptables -I DOCKER-USER -d 169.254.169.254 -j DROP
```

and make it persist the way the host persists its firewall. The rule is the
host's configuration; the worker does not attest it.
`apptown-worker -check-metadata` runs a container against the address and
says "reachable", "not reachable", or "could not tell" when the probe itself
did not run; check after installing and after a reboot.

### Disk space on the worker

The worker refuses new work while a disk it writes to is short of space:
less free than its **floor**, a twentieth of the filesystem but at least
1 GiB and at most 20 GiB, or a disk it could not read. A new task is admitted once,
before the first thing of it is written: when its own workspace is
uploaded, or, for a task without one, at its ensure before its record. Both
disks are read then, not from a cache:
- **its state folder** (`-state`: workspaces, inputs, generated files,
  captured results), read with `statfs`;
- **its Docker's data folder** (where task containers, images, build cache
  and each private daemon's storage land), read through the task driver
  with a throwaway `df` container of the pinned busybox image.

The refusal says which disk, what is free, the floor, and to free space and
try again; it carries the refusal code `disk`, and AppTown settles the
step's attempt as refused with nothing created. Nothing of the task exists
to clean up. A task already recorded under its key is ensured again whatever
the disks say. Copying a release build's image into
its results doubles it on the state folder: a copy that would leave less
than the floor free is not made, and the image is recorded refused with the
same words, so the release build fails saying why instead of filling the
disk. `GET /v1/disks` reports each reading (free, total, floor, when, or
why it could not be read); AppTown shows them on Infrastructure. An AppTown
without a worker reads its own task Docker the same way, once, before
anything of the task is written; a container Docker cannot say is absent or
present is unknown, never refused.

This is a headroom guard, not a reservation: two tasks admitted together,
or a build larger than its free space, can still fill the disk, and what
fails then fails as before (see "no disk limit per invocation"). Stopping,
releasing and removing never read the disk.

**Logs.** A private service's container and each private daemon's container
log through Docker's `json-file` driver rotated at 10 MiB, three files kept.
A task's own container is not rotated: AppTown reads its whole output by
byte offset while it runs, and the container goes when the task is released.

### Checks on the worker


Each check is one request to the worker (`POST /v1/checks`), not a recorded
task:
- AppTown resolves the commit the check reads once, stages its tree, and
  sends the tree. A landing's history scan also sends a bundle of the
  landing's commits.
- The worker runs one container on a scratch copy, labelled
  `forge.class=check` with its deadline, and with no network unless the
  scanner binding asks for egress.
- It answers with the exit code, the output and the one report the check
  declares. The report is read only as a regular file inside the tree.
- AppTown judges the answer. An answer that is incomplete, a report that is
  missing or not of its format, or a worker that cannot be reached is an
  error, never a pass. AppTown does not fall back to its own host.

Images are prepared on the worker (`POST /v1/check-images`):
- found by tag, pulled with a login made for that one request, or built from
  the staged tree when the caller allows builds;
- named by their ID in the evidence;
- image scans export the image into the scan's tree on the worker;
- reusing a release image, and the serving deployment's image as a baseline,
  do not apply on a worker, since both are on AppTown's host. A baseline
  that cannot be prepared there says so.

What a check leaves on the worker:
- **Scratch folders.** A check's scratch folder, and an image request's
  folder with any registry login in it, go with the answer.
- **Leftovers.** Every minute the worker sweeps its check containers and
  folders past their deadline, and all of its own when it starts. A check
  interrupted by a restart is discarded; asked again, it runs again. A
  worker's check containers carry its own label (`forge.worker=`, from its
  installation and state folder), and its sweep removes only those: another
  worker's checks on the same Docker are left alone.
- **Built images.** Images checks built are removed a day after their last
  use: preparing one, running a check in it, or exporting it counts as use.
  An image the sweep sees for the first time gets its day from then.
  Removal takes every name the image goes by. An image a container still
  uses is kept, and so is one a preparation is working with: preparations
  and the sweep exclude each other.
- **Pulled images** (a contract's registry image, a scanner's image) are not
  labelled as the checks', and are not removed by the sweep.
- **While the worker service is down,** a container already running keeps
  running until the worker starts again.

The worker's agent image (for the compile check, which builds a Go tree with
the Go its `mise.toml` declares) is named from the agent build context (`Config.AgentContext`, by default
`deploy/apptown-agent`, relative to AppTown's working directory), as for
chats.

### A check worker


A worker has a role, `-role agent` (the default: chats, tasks, previews,
checks) or `-role check` (checks and nothing else). The role is written to
`<state>/role`, with the time, the first time the worker starts on an empty
state directory, and is never changed:
- a start with another role on the same state directory is refused;
- a state directory from before roles, without `role`, is an agent worker's,
  and can never become a check worker's. A check worker is provisioned
  fresh, with a new state directory, from an image that never ran agents.

A check worker answers `GET /v1/role`, `POST /v1/checks` and
`POST /v1/check-images`, and refuses everything else (403) with its reason.
An agent worker writes `<state>/privileged-agents` durably before it creates
or starts any privileged container (a chat's private Docker); if the write
fails, nothing is created. The mark is a guard, not an attestation.

Configure AppTown with `-check-worker https://check.example:8443` (or
`APPTOWN_CHECK_WORKER`), reached with the same `-worker-cert`, `-worker-key`
and `-worker-ca`. At start AppTown records, under `<data>/workers-used`,
when it first used each worker address, reads the check worker's role, and
refuses to start unless it is `check`, fixed before that first use. Checks,
tests steps and base reruns then run on the check worker.

Every check result (tests, scan and guidance steps) records its placement:
`{"worker": <address>, "role": "check"}` for the check worker (the role it
reported at AppTown's start), `{"worker": <address>}` for the worker,
shared with agents (its role is not read), or `{"local": true}` for the
forge's own host. Script results record where scripts ran, the worker or
AppTown's host, never the check worker. Results recorded before this have
none. With a check worker, a gate counts a passing check result only if the
check worker produced it; any other is "not independently checked; rerun
required" when advancing or landing, and in the run's activity, and running
the stage's checks again fixes it. A script's result counts for no gate:
scripts run beside agents, so they are not independent checks; express such
a check as a tests or guidance step. Running the stage's checks again
cannot change that, so the run's activity says to revise the workflow and
start a new run, and the change page offers no rerun for it. The earlier
result is kept as it is.
Approvals and a person's override keep their own meaning. Without a check
worker, gates behave as before: the worker's checks and scripts satisfy
them, and their placement shows they ran on a machine shared with agents.
A check worker is an option, not a requirement for private Docker
on a worker.

### Previews on the worker


With a worker configured, every preview of a change or a run boots on the
worker, and none on AppTown's host. That covers:
- the try-it button, and console and MCP previews;
- a workflow's preview step;
- a chat's boot rehearsal.

There is no fallback to AppTown's host. A release preview boots on the
worker too when the release's images were published to a registry, pulling
them by digest; a release recorded before then boots the image it recorded
on AppTown's Docker.

**A preview domain is required.** Worker previews are reached only by name,
at `change-N.<preview domain>` or `run-N.<preview domain>`, through the
forge's preview proxy (`-tier-domain preview=<domain>`). An AppTown with a
worker and no preview domain refuses previews, with that reason.

**How a boot travels.**
- **AppTown prepares.** It prepares the tree as before (the change's
  folder, or a snapshot of the commit shown), and settles everything the
  boot reads: the contract, the secrets per consumer, the needs, a
  workflow's services, the integrations' values, the routes and the
  project's registry login.
- **The worker boots.** It receives them in one request
  (`PUT /v1/previews/{name}`), and boots with AppTown's own boot code on
  its Docker. The registry login lives only in the boot's own Docker
  configuration, in the preview's folder, while the boot runs.
- **Reuse.** The worker keeps a preview that is running the same
  incarnation (tree and prepared values) and whose boot completed.
  Anything else is removed first, wholly.
- **Lifetime.** Every boot, on the worker or here, has a lifetime of 30
  minutes, and every command it runs ends with it.

**How long a preview lives.** The same rule as on AppTown's own Docker
([container lifecycle](../subsystems/container-lifecycle.md#previews)): a
preview stops about eight hours after anyone last looked at it, and a
change's preview keeps its build images thirty days after that. Every
preview on a worker is reached through AppTown's preview proxy, so every
request to one is a look. AppTown decides; the worker removes what it is
asked to. The snapshot a change's preview was booted from stays on the
forge; the tree uploaded to the worker goes with the preview, and a reopen
uploads it again.

**A chat on the worker** has its change previewed as saved. Once the
preview is authorized, the chat saves first, and the preview boots the saved
commit. After each save that succeeds, AppTown rehearses the latest saved
commit, once per incarnation (commit and prepared values). It tells the chat
when the app itself fails to come up; an unreachable worker is only logged,
and the rehearsal is tried again after the next save.

**A workflow's preview step** boots the run's commit (the run folder's
`HEAD`), and the gate's runtime checks probe a preview only when it is the
commit they check, judging it by what AppTown recorded it was booted
with.

**Visitors.** AppTown's proxy forwards a visitor's request over the
worker's connection. The worker forwards it to that preview's port on its
own loopback. The app sees:
- the visitor's Host, path and query, as sent;
- only AppTown's forwarding headers. Any a visitor sent are removed,
  here and on AppTown's own proxy.

Runtime checks probe the same front door. Their naked door is a relay on
AppTown's loopback to the preview on the worker.

**What a preview leaves on the worker.**
- A preview's folder (`previews/<name>`) holds:
  - its spec;
  - its tree, which a static site mounts;
  - a `booted` mark once its boot completed;
  - a `removing` mark if a removal failed.
- Removal takes the app, its stack's services and init containers, its
  network, their volumes and the folder. A change's preview keeps the
  images its boot built: a reopen builds from them. A run's or a release's
  images go with it.
- A preview goes when:
  - nobody has looked at it for eight hours (AppTown's own rule, below);
  - its change closes;
  - its run finishes;
  - its project is removed (with its kept images);
  - AppTown starts (every preview goes, and each boots again when asked;
    until the worker confirms that, no preview boots there).
- A change's preview's kept images go thirty days after the last look at
  it, when AppTown asks (`DELETE /v1/previews/{name}/images`, fenced like
  any request that changes a preview). An image a container uses stays.

  A removal the worker does not confirm is an error to the job that asked.
  Beyond that, AppTown keeps only the previews on the worker it has a
  record of. Every minute it removes any other that is not booting, so a
  removal lost while the worker was away is made once it answers.
- Every request that changes a preview carries a token: AppTown's
  session number (greater at each apptown start, kept in AppTown's data
  folder as `worker-session`) and its number within the session. The
  worker keeps, on its disk (`previews/fence.json`), the last token per
  preview and the latest start-up claim's, and refuses any request older
  than either. A request or claim delayed past a later one never undoes
  it, across worker restarts too.
- Every minute the worker sweeps previews whose removal failed, and those
  whose boot never completed. A preview a request is booting or removing is
  left to that request.
- When the worker starts, it also removes previews that are not running,
  and the boot configurations its stop left.
- Every minute the worker trims its build cache to AppTown's budget
  (4 GB of unused cache), as AppTown trims its own.
- **A worker that cannot be reached** makes previews unavailable. The
  change page says the preview's state is not known, and runtime checks
  cannot assess.

### Agent and review steps on the worker


With a worker configured, every agent step (`agent/pi`) and review step
(`agent/review`) runs on the worker, in the worker's agent image (built from
`-agent-context`), and none on AppTown's host. There is no fallback.

**How a step runs.**
- A step attempt has one workspace on the worker, `step-<id>-a<attempt>`,
  made from the attempt's source commit.
- **AppTown catches the change up with main first.** Before an agent
  step of a change's run starts (not a review, not a landing's step), when
  the change's branch is behind `main`, AppTown merges `main` into it on
  its own side: an ordinary merge commit (the branch, then `main`), made by
  AppTown with the message "Merge main into <branch>", only when the
  merge has no conflicts and the run still holds the branch under the
  attempt. It is published after the step's own source and before the
  worker is asked to run anything; the attempt's source commit is that
  merge, the run records it (`main.merged`: by AppTown, `main`'s tip, the
  merge), and the change's history shows it as a sentence (the record is
  narration: one that fails to be written is logged, and the merge stands).
  A merge with conflicts is not made: the step works on the branch as it
  is, and the run records `main.conflict` with the files before each step
  that meets them. Nobody is sent it, since no chat can be open while the
  run holds the change. The record is what a person reads: the change
  page's state and `apptown wait -c` say which files conflict and give the
  catch-up command for the change's chat, until the branch contains that
  `main`. The steering tells the agent it need not catch up itself. The
  same rule holds before every such step, so a rework after a landing that
  stopped behind `main` starts from `main`; the landing still checks on
  AppTown that the branch contains `main`. A step is never given
  `main`'s tip to merge itself: what it commits is collected as one
  commit (below), which would carry `main`'s files as the change's own.
- Each run of Pi in it is a recorded invocation. An agent step runs `agent`,
  then at most one `agent-retry` after an empty turn. A review runs
  `review`, then at most one `review-wrapup` (no report, or it timed out) or
  `review-fixup` (an invalid report). The next invocation is started only
  after the worker has said the earlier one exited.
- What Pi reads is sent as bytes under `/apptown/in`: steering, resources,
  attachments, a long review brief, Pi's settings, the `apptown` CLI,
  registry files, a continued session's transcript, and the declared
  output files of the run's earlier steps (`.apptown/PLAN.md`,
  `.apptown/REPRO.md`), which the entry copies into the tree at their
  declared paths where the tree has nothing there. Pi's home and
  sessions are in `/apptown/state`, beside the tree, so nothing Pi writes for
  itself is committed.
- `/work/.git` is the agent's own repository, made by the worker with the
  workspace: the source commit's history on the change's branch, the base
  branch (usually `main`) at the newest main commit the source contains
  (where the change started, or the main it has since caught up with) when
  the change's start is in the source's history, no remote, and `apptown` as the
  committer. The agent can read history, `git diff main` and commit. The
  worker's own Git directory stays apart and its git never reads the
  agent's, so no configuration, hook, filter or monitor the agent writes
  there runs on the worker, and nothing under `.git` is collected. When the
  last invocation exits, the worker commits the tree the agent left, the
  AppTown collects that once as the attempt's candidate and publishes it, as
  for a script task. **The agent's own commits are squashed:** an
  attempt that changed anything is collected as one commit, made by the
  worker after the attempt's last invocation (a retry, wrap-up or fixup
  included), with AppTown's message. The agent's commits and their
  messages are not kept, and their SHAs never reach AppTown. The step's output and written files
  are read from the candidate. Its artifacts are recorded with its pass, in
  one transaction, so a later step never sees a passing step without them.
- An AppTown restarted while an invocation runs finds it on the worker, waits
  for it and settles the attempt once. Pi is not started again.
- An invocation the worker says it no longer has is an error, "the worker
  lost this step's work"; nothing retries by itself, and a person's retry
  is a new attempt. A worker that stops answering is waited for until the
  invocation's deadline, then asked to stop (see "Held workspaces").
- Each invocation's usage is recorded once. It is marked incomplete when
  the output could not be read whole, and "usage unknown" (nothing
  recorded) when it could not be read at all. A delegate's (subagent's)
  calls are not in it, and the record says so ("delegated calls not
  included").

**`apptown check` inside a step** checks the agent's committed HEAD. The
AppTown does not have that commit until the step's work is collected, so the
CLI first pushes it to the attempt's scratch ref (`APPTOWN_SCRATCH_REF`,
`refs/apptown/scratch/change-<change>/step-<step>-a<attempt>` in the
project's repository) through the read-only listener, with the step's
runtime token, which may push that ref and no other. AppTown then checks
it as it checks any commit and says the result is feedback only: nothing
is published or recorded, and the landing checks the published commit
again. Uncommitted work is not checked. If the push fails the CLI says so
and asks anyway; a commit AppTown does not have is refused, naming it
("not on AppTown yet"). `apptown check --published` checks the last
published commit, and names it. The scratch ref is removed when the
attempt settles; the scheduler's sweep (every five minutes, and at start)
removes any left by a crash.

**Reviews.** The verdict is `.apptown/review.json`. The worker removes any
such file before each invocation and reads it when the invocation exits; a
link, a path outside the tree or a file over 256 KiB is refused. A verdict
counts only from an invocation that exited 0, with no model-provider error
and a valid report. Anything else is an error, never a pass. The scope
check compares the change's base with the candidate in the project's
repository, so an implementer's out-of-scope commit is caught even when the
reviewer commits nothing. A scope that cannot be read is an error ("could
not be assessed").

**What is refused, and why.** A step that needs any of these is refused
before anything is published or sent, with a reason naming the need. What
it needs is read from its admitted workflow and from the commit it would be
given, never from AppTown's folder; a declaration that cannot be read
refuses the step too.
- private Docker (the step declares `docker: true`) on an AppTown started
  without `-private-docker`: "private Docker on a worker is the operator's
  opt-in: start AppTown with -private-docker" (see "Private Docker on the
  worker"). Services and the app's needs need no opt-in. An AppTown started
  with both `-private-docker` and `-no-private-docker` refuses to start;
- source-control access (`scm:`): today's token covers the whole project,
  lasts 72 hours and, with the write profile, can push outside AppTown's
  publication. A worker would hold it after the step ended;
- an image other than the worker's, or an executor other than `docker`: the
  worker runs only its own agent image;
- a project without a repository.


**Credentials a step carries.**
- **Runtime token** (`APPTOWN_SESSION_TOKEN`, also its key for the model
  gateway). It is derived from the step attempt under a key AppTown keeps
  in its data folder as `runtime-token-key` (created once, mode 0600). The
  same attempt has the same token after an AppTown restart, so a step still
  running keeps its access. The gateway and the API each check that the run
  is running, the attempt is the live one and its owner's account is
  enabled, so a token stops working when the attempt settles, is cancelled
  or is superseded. AppTown does not start if the key cannot be read or
  created. Keep the file with the data folder's other secrets: replacing it
  invalidates every running step's token, and anyone holding it can make
  tokens. Besides the API and the gateway, the token opens one Git push:
  to its attempt's own scratch ref, for `apptown check`.
- **`STEP_*` secrets reach every project.** Every runtime secret named
  `STEP_<NAME>` on the instance is given, as `<NAME>`, to every agent and
  review step of every project, on AppTown's Docker or a worker. It is an
  instance-wide grant by whoever sets it; there is no per-project scope.
  On a worker the values travel in the task's environment, and stay in the
  worker's task record and in AppTown's copy of the request
  (`<data>/tasks/<operation>/request.json`, mode 0600) until the attempt is
  released. Set a `STEP_*` secret only for a value every project may see.
- **Not sent:** model provider keys, source-control tokens, integration
  values, registry logins for private Docker, AppTown's own credentials
  and the database address.

**Transcripts.** Pi is pointed at one transcript file AppTown named
(`--session`). When an invocation exits, the worker returns it, and the
AppTown keeps it by its digest in `<data>/transcripts/<sha256>.jsonl`, named by
a receipt for that invocation. A continued session (`retry --continue`, a
review's wrap-up) resumes exactly the bytes its receipt names; a session
whose history is not available is refused, never started fresh. A
transcript that could not be captured is recorded as "history unavailable"
and does not fail the step. Kept files no receipt names are removed a day
after they were written; the others go with their run.

At each start, before recorded work resumes, AppTown imports the history
of agent attempts that recorded none (runs from before transcripts were
kept): the one file in the run's folder whose header names the session, or
"unavailable" with the reason. Each attempt is imported once. The start log
says how many: `startup repair: agent histories read from folders=… recorded
unavailable=… failed=…`. Nothing reads a folder's file after that.

**Refused work.** When the branch owner's rules refuse a candidate, it is
kept under `refs/apptown/refused/step-<id>-a<attempt>` in the project's
repository, and the branch does not move. The run's `commit.unpublished`
notice and the step's evidence (`result`) name the ref, the commit and the
reason. Anyone who can read the run can download it as a Git bundle and
fetch it:

```sh
curl -H "Authorization: Bearer $TOKEN" -o refused.bundle \
  https://forge.example/api/v1/runs/<run>/steps/<step>/attempts/<attempt>/refused
git fetch refused.bundle refs/apptown/refused/step-<step>-a<attempt>:refused-work
```

**Held workspaces.** When AppTown cannot know how an invocation ended (the
worker did not confirm a stop) or could not keep its result, the step is an
error and its invocation and workspace are held on the worker, not released,
even by a restarted AppTown. To find them:
- the step's reason says "its outcome is unknown", or starts "running the
  step's task: result not kept:";
- at each start AppTown logs `task <operation>: result not kept: …; its
  workspace on the worker is held`;
- its task attempt's record reads `observed = 'held'`;
- on the worker, the tree is `<state>/workspaces/step-<id>-a<attempt>` and
  its Git directory `<state>/git/step-<id>-a<attempt>.git`. Read the work
  with `git --git-dir <state>/git/<key>.git --work-tree <state>/workspaces/<key> status`.

AppTown has no command yet to list or release held workspaces.

### Private Docker on the worker


An agent step that declares `docker: true` gets a private Docker daemon on
the worker, for one invocation of the agent: from its start to its exit.
Services the step declares, and those the app's needs resolve to, run
beside it on a network of its own, reached by name; the daemon is `dind`
on that network (`DOCKER_HOST=tcp://dind:2375`), with a buildx builder,
`apptown`, and the worker's resolvers and AppTown's Docker Hub mirror.

**It is an opt-in.** Start AppTown with `-private-docker` (or
`APPTOWN_PRIVATE_DOCKER=true`). Without it, a step declaring `docker: true`
is refused on the worker with "private Docker on a worker is the
operator's opt-in: start AppTown with -private-docker".

**Qualified, or not.** The `worker-vm` profile is qualified on the facts a
separate Linux host measured; the facts each qualified set rests on ship in
`internal/execution/qualified_profiles.json`, and the maintainers keep the
qualification records.
Every attempt admitted with private Docker or services
records the worker's profile (`workerProfile`) and what it rests on
(`privateDocker`):
- `qualified: worker-vm (private/design/reviews/qualification/worker-vm-2026-10-01.md)`
  when the worker's measured facts match a qualified set: Debian 13,
  `x86_64`, cgroup `v2 (systemd)`, `overlay2`, Docker 26, the daemon image
  `docker@sha256:aa3df78e…` whose dockerd is `27.5.1, build 4c9b3b0`,
  with `-exclusive` declared and a privileged container that ran;
- `unqualified: ...` otherwise: the attempt is still admitted (the opt-in
  decides that), but nothing qualified it.

The qualified sets are data in `internal/execution/qualified_profiles.json`,
each naming its record. To qualify another set of facts, run the
qualification tests against a worker on that machine
(`go test -tags integration ./internal/worker -run '^TestIntegrationQualification'`,
with the `APPTOWN_QUAL_*` settings those tests read naming the worker) and add
its entry with its record.

**The `worker-vm` profile.** A machine, physical or virtual, that runs one
installation's worker and its Docker. What it is:
- one installation per machine. Privileged daemons can reach the machine's
  kernel; nothing separates one installation from another on it. Start the
  worker with `-exclusive` to declare that the machine is the
  installation's alone; the declaration is recorded, not measured;
- private Docker per invocation, never shared: each has its own daemon,
  network and services, removed when the invocation is released. It does
  not separate one session (or project) of the same installation from
  another against a compromised privileged container: they share the
  worker's kernel and Docker;
- checks on the same worker are labelled. Without a check worker, tests
  steps and checks run on the same worker and still satisfy gates; their
  placement (`{"worker": <address>}`) shows they ran on a machine shared
  with privileged agents. A check worker is optional; with one, gates
  accept only its results (see "A check worker").

What it is not:
- **egress is not contained.** The agent, its daemon, nested containers
  and services reach whatever the worker's network reaches;
- not host separation from AppTown when the worker runs on AppTown's
  machine (as in the local tests below): that is dispatch confinement only;
- **no disk limit per invocation.** Nested work can fill the worker's disk;
  the qualification showed new work refused with the reason ("Disk quota
  exceeded" on the test host's ZFS) and recovery by freeing space and releasing
  the full task, not prevention. New work is refused below a floor of free
  space (see "Disk space on the worker"); work already admitted is not
  stopped.

**Limits.** Each container has its own: the agent's container the step's
limits; the daemon the same CPU, memory and process limits (nested
containers run inside its cgroup); each service the worker's service
limits (by default 1 CPU, 1 GiB and 256 processes; see "Limits on
services"). An invocation's stated aggregate is the agent's limits plus the
daemon's plus each service's budget. A limit of zero is refused, never
"unlimited". Disk is not limited per invocation: nested images, build
cache and volumes share the worker's disk.

**Caches.** None for a task's daemon: every invocation starts with an empty
daemon, and its downloads and builds repeat. The task's own container
shares the worker's tool cache (see "The tool cache"). A chat on the worker
has its own cache, removed with it. No registry credential is sent; pulls from a private
registry fail with the registry's own error.

**Profile facts, and a changed profile.** An agent worker measures, when it
starts, and reports at `GET /v1/profile`:
- OS and architecture, kernel, cgroup mode, Docker server version and
  storage driver, from `docker info`;
- the daemon image (`-dind-image`), pulled first if absent: its tag,
  Docker's local id of it (`dindImageId`), its repository digest
  (`dindDigest`, from the registry it was pulled from) and the version its
  own `dockerd --version` reports (`dindVersion`), never read from the tag;
- whether a privileged container runs (one is run, after the
  privileged-agents mark is written);
- the `-exclusive` declaration, and "egress: not contained".

A fact that cannot be measured is `unknown`, with the reason. AppTown
reads the profile when it admits each invocation with private Docker or
services, records it in the attempt's evidence, and sends it in the request.
The worker measures its Docker again when a new task is ensured and when it
first starts, and refuses the request when the OS, architecture, kernel,
Docker version, storage driver, cgroup mode or, for a request with a
daemon, the daemon image's id, digest or version differs from the recorded
one (a reboot into another kernel is a changed profile)
("this worker's profile is not the one the task was admitted on"). The step
fails with that reason; run it again to admit it on the current profile.
After upgrading Docker or the daemon image, or rebooting into another
kernel, restart the worker so that it reports the new profile.

### Chats on the worker

The worker can also run chats
([requirement RE11](../requirements/runtime-environments.md#re11-work-on-a-separate-machine),
[chat guide](../subsystems/console.md#chats-on-a-worker)). On an AppTown with a
worker configured, a change's chat opens on the worker unless the person
chooses AppTown; a chat keeps the placement it opened with. An AppTown
started with `-no-local-docker` refuses every chat the worker cannot take. A chat on
the worker is the chat's container and its private Docker daemon, on the
worker's Docker. For that daemon the worker must be able to run privileged
containers, and it uses the image `-dind-image` names (`docker:27-dind` by
default).

A chat on the worker runs under the same rules as a task's private Docker:
- **Limits.** The chat's container, its private daemon and a save's
  container each get `-chat-cpus` (default 2), `-chat-memory-mib` (default
  4096, swap included) and `-chat-pids` (default 4096). What the daemon runs
  is held inside the daemon's limits. A chat's ordinary aggregate is twice the
  three values (its container and its daemon); while a save runs, its
  container is a third, so the peak is three times them (at the defaults,
  6 CPUs, 12 GiB and 12,288 PIDs). A zero is refused when the
  worker starts; it never means "unlimited". Disk is not limited per chat:
  the daemon's images and volumes share the worker's disk.
- **Caches.** Each chat has its own cache volume
  (`apptown-cache-console-<session>`, labelled `forge.console=<session>`),
  mounted at `/apptown-cache` in its container and its daemon. It is removed
  with the chat's runtime: when the chat ends, when it is rebuilt, and by the
  worker's clean-up at start for a session it has no live record of. The
  chat's container (not its daemon) also mounts the worker's tool cache at
  `/apptown-cache/mise-cache` (see "The tool cache"), the one cache chats on
  the worker share. Chats on AppTown still share `apptown-cache`, as before.
- **Resolvers and mirror.** The daemon and its builder use the worker's own
  resolvers and search domains (its `/etc/resolv.conf`, loopback entries
  skipped). AppTown sends its Docker Hub mirror (the `docker-hub`
  registry binding) with each chat that has private Docker, and the daemon
  and its builder pull through it. No registry credential is sent.

**Upgrading a worker to these rules.** A chat started before the upgrade
keeps its container: it still mounts the shared cache volume and has no
limits. Drain the worker before upgrading it, as for any upgrade that
changes what a chat's container is: end the chats on it (or let the people
using them end them). There is no conversion of a running chat. After the
last such chat has ended, the old shared cache volume on the worker is
unused and can be removed.

The chat's image is built by the worker when it starts, from its own copy
of the agent image's build context (`-agent-context`, `deploy/apptown-agent`
by default), and named by that context's content (`apptown-agent:<hash>`).
AppTown asks for the name its own copy gives: a worker and an AppTown of
different versions do not share a name, and the worker refuses the chat,
saying it does not have the image. Upgrade the two together. The first
start builds the image and takes minutes.

A chat on the worker reaches AppTown at the addresses chats use:

- the console listener, at `-console-url` on AppTown (by default
  `http://host.docker.internal:<port>`, which only this host's containers
  can reach); set it to an address both AppTown's containers and the
  worker can reach, normally https through a proxy;
- inference, at `-inference-url`, with the worker's network in
  `-inference-networks`.

AppTown sends the change's branch to the worker as a Git bundle, and
the worker makes the chat's work tree from it. A chat's work reaches the
change when AppTown saves it: after a turn, AppTown asks the worker for
a bundle of the chat's new commits, made by the chat's own git in a
short-lived container of the chat's image, and publishes them with the usual
checks. Ending a chat stops it first (its turn, its terminal, anything it
started, its private Docker), then saves what it left uncommitted as its
leftovers, then removes it. A last save refused by the checks is kept on the
AppTown for its download and the chat still ends; a last save the worker
cannot confirm keeps the chat, and ending it is asked again. A chat whose
project was deleted is ended by the project's cleanup with its unsaved work
discarded, and the ledger records `chat.save-discarded`. Nothing connects from
the worker to AppTown except the chat's own `apptown` command and its model.

The worker keeps a record per session under `<state>/sessions`, the chat's
files under `<state>/chats`, and content chats mount (AppTown's `apptown`
binary, registry configuration) under `<state>/blobs`, named by content and
removed when no live chat uses it. Ending a chat removes the session: its
record is kept, marked removed, so the session is never started again, and
the removal succeeds only once the worker's Docker shows neither container.
Stopping or rebuilding a session's runtime keeps the session. When it
starts, the worker finishes any removal that did not complete and removes
private daemons of sessions it has no live record of.

## Running AppTown without a local Docker daemon


An AppTown started with `-no-local-docker` (`APPTOWN_NO_LOCAL_DOCKER=true`) has no
Docker daemon of its own and runs no project command on its host. It can
be a plain process on a droplet or a container with no Docker socket.

**What it needs.**
- A worker (`-worker-url`, `-worker-cert`, `-worker-key`, `-worker-ca`)
  and an external database (`-db-url`). Without either it does not start.
- `git` and an `ssh` client, and the `docker` CLI binary for attached
  Docker hosts only. No daemon. `DOCKER_HOST` and the CLI's current
  context are ignored: only an attached target's recorded host is used.
- A persistent data directory (`-data`). It holds AppTown's
  repositories, folders, secrets file, runtime token key and the keys of
  attached hosts. AppTown writes its ssh configuration to
  `~/.ssh/config` of the user it runs as, so that file must be writable.
- An init process when `apptownd` is PID 1, as in a container
  (`docker run --init`, or `tini`). `apptownd` does not reap orphaned
  processes; the Docker CLI's ssh helpers and git's children are
  reparented to it. Without an init they stay as zombies (89 after one
  journey on the test host).

**What runs where.**
- On the worker: script, tests, agent and review steps; checks and scans;
  change and run previews; change chats.
- In AppTown's own process: drafting, as a model call through the
  inference owner.
- On the worker, in private Docker: a landing's release images, built
  once from the landed commit (see [releases built on the
  worker](#releases-built-on-the-worker)).
- On attached Docker hosts, App Platform and Portainer: the releases
  themselves, each target pulling the release's images by digest from the
  project's registry. Nothing is built on a target.

**Addresses.** Three connections cross machines:
- **Forge to worker:** the worker URL, over mutual TLS.
- **Worker to AppTown:** a step's container reaches AppTown's console
  listener at `-console-url` and its inference listener at
  `-inference-url`. `-inference-networks` must name the addresses those
  containers arrive from, as AppTown sees them. In the no-local-Docker
  proof those were Docker's default address pools only, without the test
  host's own LAN: name the pools step containers use, not the whole
  network the host sits on.
- **Forge to attached host:** ssh, and the app's published port at the
  host's address, for the readiness probe. An attached host publishes an
  app's port on every interface; the host's firewall decides who else may
  reach it. On AppTown's own daemon, apps are published on loopback
  only.

**Integrations.** `docker-deploy` reads the attached Docker hosts, each
asked to answer when the page is read: working when one answers, naming
those that do not; not working when none is attached or none answers. `portainer-deploy` is not read as Docker and shows as not
checked.

Previews on the worker are reached through AppTown's preview domain:
start AppTown with `-tier-domain preview=<domain>`. Without it a
preview step fails with "previews run on the worker, and are reached
through AppTown's preview domain, which this AppTown does not have".

**Releasing.** There is no built-in Docker host in the mode. Before a
project's first landing:
1. Put the SSH private key for the host in AppTown's secrets store,
   as an administrator. It is one line, with its newlines written as
   `\n`: `awk '{printf "%s%s", $0, "\\n"}' <key file> | apptown secret set
   DOCKER_HOST_KEY`. Integrations → Secrets, or "set this secret now"
   beside the key's name in the target wizard, does the same from the
   browser. The value is never shown again; AppTown refuses a name
   Infisical already holds (change it there).
2. Attach the host: `POST /api/v1/targets` with
   `{"kind":"docker-host","name":…,"host":"ssh://user@host","keyFrom":<secret name>}`.
   AppTown asks the daemon to answer before it records the target.
3. Offer it: `POST /api/v1/targets/<name>/offers` with `{"group":…}`, or
   `"*"` for everyone. Retiring it later is
   `POST /api/v1/targets/<name>/retire` with `{"confirm":"<name>"}`.
4. Attach a container registry to the project (see [releases built on
   the worker](#releases-built-on-the-worker)). Every landing publishes
   there.

A project's first landing then creates its `main` deployment (feed
`landing`, audience `team`, synthetic data) on the one attached Docker
host offered to the project for those settings, as it creates `main` on
the built-in host outside the mode. With no such host the landing stops
and says to offer one. With several it stops, names them, and says to
create `main` on the one it should run on:
`POST /api/v1/projects/<slug>/deployments` with
`{"name":"main","target":<name>,"feed":"landing"}`, then land again. A
`main` that exists already is used wherever it is.

`main` must be on a Docker host. App Platform and Portainer receive a
release only by promotion:
- a Docker host or App Platform deployment: `POST /api/v1/projects/<slug>/deployments/<name>/deploy`,
  with `{"bundleId":…}` or nothing for the latest bundle. The answer
  names the release's number; `apptown wait -p <slug> -r <number>` follows
  its log until it is released (exit 0) or failed (exit 1);
- Portainer: the intranet release, `POST /api/v1/projects/<slug>/release`,
  which ships the project's latest published bundle (it takes no
  `bundleId`).

Refused in the mode, each with its reason:
- the built-in Docker host;
- for a release recorded before releases were built on the worker (its
  images are daemon image IDs, not digests): promotion to a target other
  than the one its images live on, App Platform, Portainer and release
  previews; and, for one recorded before releases named their target,
  manual promotion and missing-container recovery;
- project chats, continuations and `placement=forge`.

**Read views.** Whatever would have asked AppTown's own Docker says
"unavailable" (the built-in host) or "unknown" (what AppTown cannot
observe), never "not running". With the worker stopped, a change's preview
reads "unavailable" with the worker's error.

**Switching an existing forge.** An AppTown that ran with its own Docker
needs a drain before it starts in the mode; a fresh installation does not.
See [switching an AppTown to no local Docker](#switching-an-apptown-to-no-local-docker).

**Deleting a project.** A deleted project's cleanup tears down its
deployments on attached Docker hosts over ssh, as on the built-in host. A
host it cannot reach keeps the cleanup pending, then needing attention,
with the deployment named; a deployment on a Portainer or App Platform
target still has to be torn down first
([deleting a project](../subsystems/project-deletion.md)).

The main tier, `get_project` and the conformance runtime check observe a
`main` on its own target and probe it at that host.

### Releases built on the worker


On an AppTown with a worker, in the no-local-Docker mode or not, **every
landing builds its release on the worker**. The deploy step fixes the
commit and runs one `release-build` task on an agent worker in private
Docker (`buildx`, one platform, no provenance or SBOM attestations). The
task carries the commit and the generated build files only: no secrets,
no environment, no registry credential and no build cache. A `FROM`
naming a private image fails with that registry's error.

AppTown then reads each image the worker left, checks every blob
against its digest and the platform, writes it to the project's
container registry with the attachment's credential, and reads it back
by digest. Only the registry's answer is recorded. The bundle names every
part and init image as `<repository>@sha256:<digest>`, with the
platform, the worker that built it and its profile qualification. It is
committed once, by the step attempt that still owns the step. The step
then deploys to `main`, a Docker host, which pulls those digests.

Readable tags are added for people, never used as evidence:
`change-<change>-<image>` (or `run-<run>-<image>`) at landing, and
`release-<number>-<image>` when a promotion or an intranet release of the
bundle goes live. The landing's own release carries only the change tag;
DOCR copies carry a commit-based tag. Images are named `part-<part>` and
`init-<step>`.

**What it needs.**
- AppTown started with `-private-docker`, and an agent worker. A check
  worker does not build releases.
- A container registry for each shipping project: the one
  container-registry connector AppTown has, which a project's first
  landing attaches when it has none, or a registry attached to the project
  (below). AppTown needs to reach it, and so does every target: Docker
  hosts pull with a per-operation login, the worker pulls for release
  previews.
- The worker's platform (its measured architecture, as `linux/amd64` or
  `linux/arm64`) must be the target's: a Docker host's `docker info`, App
  Platform's `linux/amd64`, Portainer's endpoint. Unknown or different is
  refused before anything runs.

**Refused before anything is built**, each naming the next action: no
registry attached and no single connector to attach ("this project has no
container registry for its releases, and no container-registry connector
is offered …" or "… several container-registry connectors are offered (a,
b); attach the one <slug> publishes to …"), a connector credential the
secrets store does not hold, no `-private-docker`, `main` not on a Docker host
("a landing deploys to a Docker host … promote to <kind>"), and an
unknown or mismatched platform.

**Consumers.** Promotion to another Docker host, recovery after a host
lost its images or container (the rebuild a apptown start asks for), App
Platform (each digest copied into DOCR by AppTown, then named in the
app spec), Portainer (the stack names every digest) and release previews
(booted by digest where change previews boot) all pull. None builds. A
registry that refuses or cannot be reached is an error; a digest the
registry confirms missing is refused: land the change again.

**Accepted failures.** An AppTown restart during a deploy step leaves the
step in error: restart only while `apptown executing` lists nothing, or
retry the step, which builds again. An interrupted publication can leave an unused tagged image;
AppTown never deletes from a registry, and the registry's retention
decides how long a release can be redeployed. Every image byte passes
through AppTown, and builds are cold.

An AppTown without a worker builds on its targets.

#### Offering a registry before the rollout

A landing on an AppTown with a worker publishes to the project's container
registry. A project with none is given AppTown's one container-registry
connector at its first landing, recorded as an attachment (provenance
`provisioned`, by `apptown-system`, with the run and step in its receipt)
that shows under the project's integrations like one made by hand. With
no connector, or several, the landing is refused before any effect, naming
the choice. A project that has a registry keeps it. Before upgrading a
AppTown with a worker to this version, or before giving an AppTown its first
worker:

1. Define the `container-registry` service kind, if AppTown has none:
   `POST /api/v1/services` with its YAML (the kind is runtime-defined). The
   definition in use has connector fields `url`, `group`, `provider`,
   `username` and `tokenFrom`, and resource fields `repo`, `username` and
   `token`:

   ```yaml
   capability: container-registry
   title: Container registry
   description: OCI images per project — a pushed image is the unit of release.
   connector:
     fields:
       - key: url
         label: Registry host
         placeholder: registry.example.com
       - key: group
         label: Namespace / group
       - key: provider
         label: Provider
         optional: true
       - key: username
         label: Push username
         optional: true
       - key: tokenFrom
         label: Push password secret
         type: secretRef
         optional: true
   resource:
     fields:
       - key: repo
         label: Image repository
         optional: true
       - key: username
         label: Push username
         optional: true
       - key: token
         label: Push token
         optional: true
   env:
     - key: REGISTRY
       value: "{{connector.url}}"
     - key: IMAGE_REPO
       value: "{{resource.repo}}"
   ```
2. Offer one registry: add one `container-registry` connector
   (`POST /api/v1/integrations`, provider `service`). Either
   - `provider: gitlab` with `url` and `group`: a project's first landing
     provisions a per-project deploy token, as the linked attach does; or
   - any other registry: `url`, `group`, `username` and `tokenFrom`, the
     name of a secret in AppTown's secrets store that holds the
     password. Projects are attached as `<url>/<group>/<slug>` and hold no
     credential; AppTown reads the secret each time it pushes or logs a
     daemon in, so rotating it is a change to the secret only. The
     credential must be able to push and pull every project's repository.

   With several registries, attach each project yourself:
   `POST /api/v1/projects/<slug>/integrations` with
   `{"capability":"container-registry","connector":<name>,"resource":{"repo":"<url>/<group>/<slug>"}}`
   uses that connector's credential (add `username` and `token` to the
   resource only for a connector that names none; a GitLab connector
   provisions when no resource is given). Attachments made before this
   version, holding their own `token`, keep working unchanged.
3. Check that every target can pull from that registry: its TLS
   certificate is trusted by each Docker host's daemon, and the hosts can
   reach it.
4. Upgrade or start AppTown with `-private-docker` and the worker. The
   first landing of each project is its check: a missing connector, an
   unheld secret or an unreachable registry fails the deploy step before
   any effect on `main`.

Releases recorded before the upgrade keep their image IDs and their old
rules (same-target reuse in the mode, a rebuild outside it).

### Switching an AppTown to no local Docker

An AppTown started with `-no-local-docker` has no Docker of its own to look at,
so it cannot tell whether work it once ran there is still writing. Switching
into the mode is therefore a separate step, taken by AppTown while it still
has its Docker.
Stop AppTown, then run it once with its normal settings plus
`-drain-local-docker` (or `APPTOWN_DRAIN_LOCAL_DOCKER=true`), without
`-no-local-docker`. It serves nothing: it repairs interrupted work as a
normal start does, refuses while its records hold local work (a local task
not settled and released, a chat on AppTown not ended, a cleanup not
done), then asks its Docker for containers carrying AppTown's execution
labels (agents, reviews, scripts, tasks, chats, previews, services) and
refuses while any is listed, naming them. Release containers of deployments
on the built-in target and the built-in database are not counted. When
nothing is left it records the drain and exits 0; then start AppTown with
`-no-local-docker`. An AppTown that cannot ask its Docker records nothing.

A start in the mode is admitted after a drain, after an earlier start in the
mode (a restart with a merge or deploy step running is fine), or on a
fresh database. An upgraded database always needs a drain, even one that
ran nothing: the upgrade cannot tell. Any start without the mode needs a new drain
before the next start in it. The drain sees containers only: an agent run as
a host process (`-agent-executor local`) is not observed, so stop such a
forge's agents before draining.

## What the tests show, and what is not verified

Integration tests (`-tags integration`) run real Docker through the worker:

- the task lifecycle conformance suite; a worker process killed and
  restarted mid-task, the task found again by key and not started twice; a
  duplicate request with a different body refused after the restart; another
  installation's certificate refused before any request is read;
- a script step end to end from an AppTown through the worker, its commit
  published and AppTown's workspace brought forward
  (`TestIntegrationAScriptStepRunsOnAWorkerInItsOwnContainer`);
- an agent and a review step on the worker and nowhere else, against a
  controlled model behind AppTown's gateway, settled once across an AppTown
  rebuild, with a settled attempt's token then refused
  (`TestIntegrationAnAgentAndAReviewRunOnTheWorkerAndNowhereElse`);
- an agent step with private Docker and a catalog service, then a tests step,
  with and without a check worker
  (`TestIntegrationAnImplementationStepBuildsWithPrivateDockerOnAWorker`);
- a chat on the worker from opening to its end, across AppTown and worker
  restarts (`TestIntegrationAChatOnAWorkerFromOpeningToItsEnd`), and a chat
  session's limits held by the kernel and its nested containers refused forks
  past the daemon's limit;
- tasks and checks reading their generated files from a read-only mount
  (`TestIntegrationAWorkerTaskAndCheckReadTheirGeneratedFiles`).

`TestNoScriptOrTestsStepRunsOnThisMachineWhenAWorkerIsConfigured` shows,
without real Docker, that nothing runs on AppTown's machine when a worker is
configured, and that an unreachable worker is an error.

The maintainers also run the whole topology (an AppTown with no local Docker, a
worker, attached Docker hosts, a registry, App Platform against a fake API)
through one journey before a release that changes it.

Not verified: a worker on a second physical machine in the test suites, a
daemon-free VM, a real App Platform round trip in the suites, an agent's own
repository on a Linux worker whose user is not the agent image's, a worker
behind a proxy or load balancer, certificate rotation, a worker losing its
state directory, a network partition longer than AppTown's retry, the
worker's Docker daemon restarting, network policy beyond Docker's default
bridge and `none`, and resource limits (the worker profile imposes none; zero
limits are recorded as none). The worker does not clean up workspaces of tasks
AppTown never released.
