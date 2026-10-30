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
server](server-setup.md), with the differences given in each; this
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
and you issue the certificates. Without a worker, everything runs on AppTown's own host's Docker: every
project step, a script step included, runs in a container there, never as
a plain process on the host. With a worker, agent, review, tests and script steps,
checks and change previews run there, new change chats open there by
default, and each landing's release images are built there. A step the worker cannot run is refused,
not moved back to AppTown. Previews on a worker need a preview domain
(`-tier-domain preview=<domain>`), and release builds need private Docker
(on unless `-private-docker=false`) and a container registry for each project (see the [package
registries](../integrations/registry-registry.admin.md) Admin tab).

A worker does not take all project work off AppTown's host. These still
run there: project chats, chats that continue a step's agent, change chats
opened on AppTown or already running there, and the containers of
deployments on the built-in Docker host. Nor is a worker a strong sandbox: code on it can reach
the network, and the private Docker a step or chat may get is not a
boundary against hostile code. Only `-no-local-docker` keeps every project
process off AppTown's host. The `ghcr.io/flokkit/apptown` image runs this way
when `APPTOWN_NO_LOCAL_DOCKER=true` is in its environment, as the split
install's compose file sets it ("With Docker" below).

With `-no-local-docker` AppTown starts no container and no project process
on its host. Installed from its image, AppTown's host runs Docker for
AppTown's own container only, and AppTown is not given its socket. It needs a worker and an external database (`-db-url`), and
deploys a landing to an attached Docker host instead of its own. This is
the split install.

## Setting up a split install

1. **The database.** A PostgreSQL database AppTown alone uses, named with
   `-db-url`.
2. **The worker.** A machine with Docker, running `apptown-worker` as root,
   from its image ("With Docker" below) or from the release archive: unpack
   the same release archive to `/opt/apptown-worker` ([downloading a
   release](server-setup.md)); its example unit is
   `deploy/systemd/apptown-worker.service`. The unit sets
   `DOCKER_CONFIG=/root/.docker`; override it in
   `/etc/apptown-worker/apptown-worker.env` if the worker uses another
   Docker configuration location.
   You issue its server certificate and AppTown's client certificate; the
   client certificate's Common Name is the worker's `-installation`. On a
   cloud machine, block the metadata service (below).
3. **AppTown.** From its image ("With Docker" below), or from the release
   archive under systemd. Start `apptownd` with `-no-local-docker`, `-db-url`,
   `-worker-url`, `-worker-cert`, `-worker-key` and `-worker-ca`, the
   addresses from "Configure model access" in [Setting up a
   server](server-setup.md), a preview domain
   (`-tier-domain preview=<domain>`). Release images are built on the
   worker in private Docker, which is on unless `-private-docker=false`.
   With no local Docker, a whole-AppTown backup dumps the database on the
   worker, which must reach the `-db-url` address ([AppTown's own
   backups](apptown-backups.md)). The managed database's own backups do
   not replace it: they hold neither the data directory nor the secrets.
4. **An app host.** Attach a Docker host and offer it to the projects that
   may use it ([Deployment targets](deployment-targets.md)). A project's first landing
   creates its `main` deployment there.
5. **A registry for each project.** Each landing publishes its images to
   the project's container registry, and the Docker host pulls them from
   there (see the [package registries](../integrations/registry-registry.admin.md) Admin tab).

The full procedure, with the API calls, is in the repository at
`design/operations/workers.md`, "Running an AppTown without a local Docker
daemon".

## With Docker

Each machine runs one compose file, published with each release at
`https://apptown.dev/split-install/`, with its settings in a `.env` beside
it. The file starts the release it was published with. Both containers run
with an init as process 1 (`init: true`), which reaps the processes AppTown
and the worker leave behind; run without it, the ssh processes left by
commands for attached Docker hosts fill the container's process limit
until AppTown can start no more. Both
containers use the host's network and run as root. You need Docker with
Compose 2.23 or later on AppTown's machine and on the worker.

**The worker**, as root:

```sh
mkdir -p /opt/apptown-worker && cd /opt/apptown-worker
curl -fsSLO https://apptown.dev/split-install/worker/compose.yaml
cat > .env <<'ENV'
APPTOWN_INSTALLATION=apptown.example.com
APPTOWN_LISTEN=10.0.0.5:7790
ENV
docker compose up -d
```

Put the worker's certificate, key and AppTown's CA in `/etc/apptown-worker`
as `worker.pem`, `worker-key.pem` and `ca.pem` first. Its state is in
`/var/lib/apptown-worker`. Any other worker flag goes in `.env` as its
`APPTOWN_` variable, such as `APPTOWN_EXCLUSIVE=true` on a machine that is
the installation's alone. On its first start the worker builds the agent
image, which takes several minutes.

**AppTown**, as root:

```sh
mkdir -p /opt/apptown && cd /opt/apptown
curl -fsSLO https://apptown.dev/split-install/apptown/compose.yaml
chmod 600 .env   # after writing it: it holds the database's password
docker compose up -d
```

`.env` holds every setting step 3 lists, as
`APPTOWN_` variables (`APPTOWN_DB_URL`, `APPTOWN_PUBLIC_HOST`,
`APPTOWN_TIER_DOMAIN`, `APPTOWN_WORKER_URL`, `APPTOWN_CONSOLE_ADDR` and so
on). The worker's client certificate, key and CA go in `/etc/apptown/worker`,
named by `APPTOWN_WORKER_CERT`, `_KEY` and `_CA`. The data directory is
`/var/lib/apptown`, or `APPTOWN_DATA`, at the same path inside and out.
AppTown listens on `127.0.0.1:7777`: put your own proxy in front of it
([Your own proxy](server-setup.md)), serving your domain and every name
under the preview domain.

**Upgrading** either machine: download its new `compose.yaml` (or set
`APPTOWN_VERSION` in `.env` to the new version) and run
`docker compose pull && docker compose up -d`, worker first. To pin the exact
image, set `APPTOWN_IMAGE` to a digest (`ghcr.io/flokkit/apptown@sha256:…`,
or `apptown-worker@…` on the worker); it replaces the version's reference,
and also names a mirror. Run `apptownd` commands such as a backup in the
container: `docker compose exec apptown apptownd backup -store <archive>`
([AppTown's own backups](apptown-backups.md)).

## Switching to no local Docker

An AppTown in this mode cannot see work it once ran on its own Docker. Before
the first start with `-no-local-docker`, stop AppTown, end its chats and let
its runs finish or abandon them, and check that no AppTown container other
than releases and the built-in database is left on its Docker. A fresh
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

What runs where is in [How AppTown works](architecture.md).
Certificates, limits and each kind of work on a worker are in the
repository at `design/operations/workers.md`.
