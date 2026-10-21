# Split install

AppTown runs on its own machine with no Docker. Agents, steps, checks and
previews run on a separate worker machine (`apptown-worker`). Apps run on
separate Docker hosts, and can also go to Portainer or DigitalOcean App
Platform. The database is a PostgreSQL you provide, such
as a managed one.

The reason to do it is that the agents' code and your apps don't run where
AppTown keeps its records. Code that gets out of a container is on the
worker or on an app host, and neither holds the database or AppTown's
data directory. It suits a team that shares AppTown, or one whose apps
other people rely on.

It does not make a project's code harmless. Every project's work shares
the worker, can reach the network from it, and can use the credentials
AppTown gives each step. Apps on one Docker host share that host. It also
takes more to set up: certificates for the worker, a preview domain, a
container registry for each project, and an SSH key for each Docker host.

A worker on its own, without `-no-local-docker`, is a halfway point.
Agents, steps, checks and previews move to the worker, but some work stays
on AppTown's machine, such as project chats and the apps on the built-in
Docker host. The full list is in "Workers and running without local Docker" below.

A split install starts with the steps in [Setting up a
server](/docs/guides/server-setup), with the differences given in each; this
page then says what to add, in order.

**Moving a single server to a split install later** means moving the
database to a PostgreSQL you provide, draining AppTown's own Docker
("Switching to no local Docker" below), and moving each deployment on the
built-in Docker host to an attached one, since a split install has no
built-in host. Choose before the first project if you can.

## Workers and running without local Docker

A **worker** is a separate machine, running `apptown-worker`, with its own
Docker. Start AppTown with `-worker-url`, `-worker-cert`, `-worker-key`
and `-worker-ca`; the two prove who they are to each other with mutual TLS,
and you issue the certificates. Without a worker, everything runs on AppTown's own host's Docker; a script step runs in a container there, and runs
as a plain process on the host only when it says `executor: local` and AppTown was started with `-host-scripts` (for an AppTown whose projects you trust
as much as its host). `-host-scripts` and `-agent-executor local` are
experimental: they are for developing AppTown, not promised in 1.x. With a worker, agent, review, tests and script steps,
checks and change previews run there, new change chats open there by
default, and each landing's release images are built there. A step the worker cannot run is refused,
not moved back to AppTown. Previews on a worker need a preview domain
(`-tier-domain preview=<domain>`), and release builds need `-private-docker`
and a container registry for each project (see the [package
registries](/docs/registry/registry) Admin tab).

A worker does not take all project work off AppTown's host. These still
run there: project chats, chats that continue a step's agent, change chats
opened on AppTown or already running there, the containers of deployments
on the built-in Docker host, and intranet releases of a landing that has no
published images. Nor is a worker a strong sandbox: code on it can reach
the network, and the private Docker a step or chat may get is not a
boundary against hostile code. Only `-no-local-docker` keeps every project
process off AppTown's host. The `ghcr.io/flokkit/apptown` image runs this way
when `APPTOWN_NO_LOCAL_DOCKER=true` is in its environment; without it, the
image starts as the desktop trial (the comment in `deploy/Dockerfile`
shows both).

With `-no-local-docker` AppTown's host runs no Docker and no project
process. It needs a worker and an external database (`-db-url`), and
deploys a landing to an attached Docker host instead of its own. This is
the split install.

## Setting up a split install

1. **The database.** A PostgreSQL database AppTown alone uses, named with
   `-db-url`.
2. **The worker.** A machine with Docker, running `apptown-worker` as root.
   Unpack the same release archive to `/opt/apptown-worker` ([downloading a
   release](/docs/guides/server-setup)); its example unit is
   `deploy/systemd/apptown-worker.service`.
   You issue its server certificate and AppTown's client certificate; the
   client certificate's Common Name is the worker's `-installation`. On a
   cloud machine, block the metadata service (below).
3. **AppTown.** Start `apptownd` with `-no-local-docker`, `-db-url`,
   `-worker-url`, `-worker-cert`, `-worker-key` and `-worker-ca`, the
   addresses from "Configure model access" in [Setting up a
   server](/docs/guides/server-setup), a preview domain
   (`-tier-domain preview=<domain>`) and `-private-docker`, so that
   release images can be built on the worker.
4. **An app host.** Attach a Docker host and offer it to the projects that
   may use it ([Deployment targets](/docs/guides/deployment-targets)). A project's first landing
   creates its `main` deployment there.
5. **A registry for each project.** Each landing publishes its images to
   the project's container registry, and the Docker host pulls them from
   there (see the [package registries](/docs/registry/registry) Admin tab).

The full procedure, with the API calls, is in the repository at
`design/operations/workers.md`, "Running an AppTown without a local Docker
daemon".

## Switching to no local Docker

An AppTown that last ran with its own Docker first runs once, stopped and with
its normal settings, with `-drain-local-docker`: it checks that no local
work or execution container is left, records that, and exits. A fresh
installation does not need this.

## Limits and the metadata service

Every container AppTown starts is bounded. Steps, checks and tasks, here
or on a worker, and a step's private Docker get `-task-cpus` (4),
`-task-memory-mib` (8192) and `-task-pids` (4096); a chat on this host gets
`-chat-cpus` (2), `-chat-memory-mib` (4096) and `-chat-pids` (4096), as a
worker's chats do. A run that hits its memory limit is not a result: the
step errors and says to raise the limit. Each project's steps share a
package cache of their own (`apptown-cache-<project>`), and each chat has its
own.

On a cloud host, a container can reach the provider's metadata service
(169.254.169.254) unless the host blocks it. On a cloud worker add a rule that blocks it (see
`design/operations/workers.md`) and check it with
`apptown-worker -check-metadata`.

What runs where is in [How AppTown works](/docs/guides/architecture).
Certificates, limits and each kind of work on a worker are in the
repository at `design/operations/workers.md`.
