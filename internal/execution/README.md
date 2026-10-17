# Execution

`Runner` runs a bounded process locally or in Docker and returns its outcome;
it knows nothing about projects, workflows or persistence.

`TaskDriver` is the contract for a task whose identity must survive its
caller (the first-slice plan, step 3): ensure a task by operation key (the
same key finds the same task), start it, observe it, wait for it, read its
output by byte cursor, stop it, release it. Ensure never starts the task, so a
caller can record the task's identity before it runs. Every request carries an
`Admission`: the profile that admitted it, its limits (zero means none), its
network policy (`none` or `default`) and its expiry. A driver refuses an
expired or unsupported request before creating anything.

`DockerTasks` is the local Docker driver: containers named from the key,
limits as `--cpus`, `--memory` and `--pids-limit`, `--network none` for that
policy, the expiry as a label. It claims no capability beyond the lifecycle.
`SelectDocker` resolves the daemon once, as the CLI would select it
(`DOCKER_HOST`, else `DOCKER_CONTEXT`, else the saved current context, else
the default context: the local socket, or `tcp://localhost:2376` when
`DOCKER_TLS_VERIFY` or `DOCKER_TLS` asks for TLS), to its address; every call
passes that address with `--host`, so rewriting a context later cannot move
a call to another daemon. The TLS variables reach a call only where the CLI
would apply them (`DOCKER_HOST` and the default context), never a named
context's daemon.
A selection that cannot be pinned (an unreadable context, or one that needs
TLS files) refuses every task until the forge restarts with one that can;
such a daemon is selected with `DOCKER_HOST` and `DOCKER_TLS_VERIFY` /
`DOCKER_CERT_PATH` instead.

`executiontest` is the conformance suite. Its lifecycle group applies to
every driver; the private Docker, terminal and persistence groups run only for
a driver that claims them (a worker claims private Docker; its group checks
the refusals, and the worker's integration tests what it runs). `executionfake` is an in-memory
driver for callers' tests. The fake passing the suite establishes the
contract; the Docker driver passing it (`-tags integration`) establishes that
backend.

`WorkerTasks` is the driver for a `forge-worker` on another machine
(`internal/worker`, the plan's step 4). It speaks HTTPS with the
installation's client certificate. A task's source goes to the worker as a Git
bundle of one commit (`TaskSpec.Source`), never as a host path: host paths,
mounts and host routes are refused. `Collect` returns the task's commits as a
bundle, which the caller imports with `gitexec.Quarantine` before publishing
anything. A wait is repeated in bounded slices, so no request outlives a
proxy's idle timeout. An unreachable worker is `ErrWorkerUnavailable`
(observation: `ErrTaskUnobservable`), never absence.
A refusal the forge acts on carries a code from a fixed set
(`worker_refusals.go`: expired, start refused, unsupported, no agent image,
missing blob, conflict) in a JSON body of its own content type, with the
words for people beside it; the client maps the code to its error once and
never reads the words or the status. A refusal without a known code, of any
status, is no class, and a 5xx or a body that cannot be read is the worker
unavailable: neither is ever "refused with nothing created".

A worker also claims `Inputs` and `Results` (`inputs.go`), which the local
drivers refuse. `TaskSpec.Inputs` are files a task reads read-only under
`/forge/in/`, prepared on the forge as bytes and sent ahead of the request
as content-addressed blobs; the request names each by digest, path and mode
(0644 or 0755), within 256 inputs, 64 MiB each and 512 MiB in all.
`TaskSpec.Results` are files a task declares it will leave, in its tree
(`/work/...`) or its workspace's state (`/forge/state/...`), each with a
maximum size and `Fresh` (removed before the task first starts). The worker
captures them when the task exits; `WorkerTasks.Results` reads each as
present (its bytes), absent, refused (a link on the way, not a regular file,
an escape) or too large, never one as another. `WorkerTasks.AgentImage`
reads the worker's agent image name and labels, or `ErrNoAgentImage`. A
request without inputs or results keeps the identity it had before they
existed.

A worker also claims `Images` (`image_results.go`): `TaskSpec.Images` are
the OCI image layouts a release build declares it will leave under
`/forge/state/`, each with its admitted size. A request declaring them is a
release build: it carries no environment, inputs or services, so no
credential or secret travels with it, and works in a workspace of its own
made from its source. `WorkerTasks.ImageResults` reads each as present,
absent, refused (a link at, on the way to or inside the folder, not a
folder, not a regular file) or too large; `WorkerTasks.ImageFile` opens its
index or one of its blobs with its stated length, so a reply cut short is a
transport error, never a shorter file. Once the task is released both
answer `ErrImageGone`, and a task whose images are not captured yet is said
to be so. `ImageLayout` reads one captured image as an OCI layout, file by
file, for the forge's publisher. The forge checks every byte against its
digest.

A worker also claims `PrivateDocker` (`private.go`): `TaskSpec.Private` is a
task's own network, the services on it (resolved when the task is admitted,
so a request sent again runs the same ones) and, when asked, a private
daemon, with the Docker Hub mirror its daemon and builder use. It needs the
default network and explicit CPU, memory and PID limits: zero is never
unlimited there. Two services of one name, or one named `dind`, are
refused. `TaskSpec.Network` is the network a backend joins the task to,
named by the backend itself; the forge never sends it. A start the backend
refuses with its reason is `ErrStartRefused`.

`WorkerProfile` is what a worker measured of the Docker its private Docker
rests on; `Changed` says how a later measurement differs. `qualified.go`
reads the qualified fact sets from `qualified_profiles.json`, each naming the
record of the qualification run behind it: `WorkerProfile.Qualification`
names the record a profile's measured facts match, or "" (unqualified). It
compares facts; it does not run or read a qualification. The shipped facts
are the maintainers' attestation: the records (`design/qualification/` in
their development repository) are not published with the code. Qualifying
another set of facts is a data change with its record, not a code change.
