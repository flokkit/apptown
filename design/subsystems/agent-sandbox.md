# Agent isolation limits

## Implemented

Agents run through the configured executor. Docker model execution uses prepared
workspaces and generated configuration, with AppTown runtime credentials for the
private inference gateway. Local model execution is refused because filtering its
environment cannot isolate a local process from daemon credentials.

**A separate worker machine.** With `-worker-url`, project code runs on
`apptown-worker`, not on AppTown's host: Docker script steps, tests, agent and
review steps, checks and scans, change and run previews, release builds, and new
change chats by default. A script step without `executor: docker` is refused
(without a worker it runs as a local process on the AppTown host). AppTown and
worker share no files; AppTown sends one commit as a Git bundle over mutual
TLS and receives the task's commits back. A task the worker cannot run or reach
is refused or an error, never moved to the host. A step's or chat's container is
configured with AppTown's console and inference listeners and a credential
minted for it; that is not a network restriction, and what else it can reach on
AppTown host or elsewhere depends on the operator's network and firewall.
Checks run on the same worker as agents, so a gate's evidence may have run
beside the agent it judges; each result records where it ran. With `-no-local-docker` the
AppTown host has no Docker and runs no project process at all. Otherwise project
chats, chats continuing a step's agent, and change chats placed on AppTown or
already running there still run on the AppTown host. The operating guide
([workers](../operations/workers.md#what-a-worker-isolates-and-what-it-does-not))
lists exactly what moved and the verified limits; `internal/execution` and
`internal/worker` own the task contract.

**Private Docker.** Agent steps that declare `docker: true` and writable change
chats can use a private Docker daemon. `-no-private-docker` turns it off for
both. On a worker, agent steps get it, and release builds run, only when the
operator opts in with `-private-docker`; change chats on a worker get it unless
`-no-private-docker` is set.
That avoids handing them the host Docker socket, but the privileged sidecar
is not a strong boundary against hostile code. Registry mirrors provide routing,
not an enforced egress allowlist.

## Not implemented

A worker moves project code off AppTown's host; it does not contain that code
on the worker. Containers there use Docker's default bridge network (or `none`
where a task's admission says so), and the worker profile sets no resource
limits by default. Several projects' work can share one worker. A worker does not
make AppTown safe for untrusted projects.

Explicit network and execution isolation, nested build and container traffic
included, is not built. It needs a design of its own: where policy is enforced,
which destinations are allowed, how blocked traffic is reported, and how it
interacts with declared services and provider access. MicroVMs, alternative
container runtimes and an enforced egress proxy are options, not installed
controls. The [threat register](../architecture/threat-register.md)
tracks the risk. The [runtime guide](../../stock/docs/guides/runtime-environments.md)
describes actual mounts, tools and grants.

## Implementation references

- [internal/apptown/agentenv/inference.go](../../internal/apptown/agentenv/inference.go)
- [internal/apptown/agentenv/model_process.go](../../internal/apptown/agentenv/model_process.go)
- [internal/apptown/agentenv/workshop.go](../../internal/apptown/agentenv/workshop.go)
- [internal/execution/README.md](../../internal/execution/README.md)
- [internal/worker/README.md](../../internal/worker/README.md)
- [internal/apptown/no_local_docker.go](../../internal/apptown/no_local_docker.go)
