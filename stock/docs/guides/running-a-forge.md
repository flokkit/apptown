# Running AppTown

<!-- A word like forge:origin in braces is a live link: the Docs page of a
forge shows that forge's own address there. -->

This page is for the person who installs and looks after AppTown: its host,
its workers, where it deploys, who can sign in, its backups and its
upgrades. Each section is short and says where the detail is. Building apps
on a forge is in [Start here](/docs/guides/quickstart); how the parts fit
together is in [How the forge works](/docs/guides/architecture).

`forged -h` lists every start-up flag. Each flag can also be set as an
environment variable: `-db-url` is `FORGE_DB_URL`, `-tier-domain` is
`FORGE_TIER_DOMAIN` (comma-separated), and a flag on the command line wins.

## Install

There are two ways to install AppTown on a server. Both keep the same
records and run the same workflows. They differ in which machines run
your projects' code.

To try AppTown on your own computer instead, the repository's README has a
one-command desktop trial (`docker run --privileged … ghcr.io/flokkit/apptown`);
it is not for a server or for anything other people use.

### Single-server install

AppTown, its agents and its apps all run on one machine, using that
machine's Docker. AppTown starts its own PostgreSQL container there too.
It is the quickest install: the four steps below, on one machine.

It suits one person, or a small team whose projects are all written by
people trusted as much as the machine itself. The usual case is a team
trying AppTown on real work, or running a few internal apps.

Each agent, step and preview runs in its own container with CPU, memory
and process limits. Disk is not limited per container, so one piece of
work can still fill the machine's disk ("Disk space" under "Deployment
targets"). Apps are
published on the machine's loopback address only. None of that protects
AppTown's records from a project's code. On Linux those containers run as
root and can reach the network, and a container is not a strong sandbox.
Code that gets out of one is on the machine that holds AppTown's database,
its secrets file and its keys.

### Split install

AppTown runs on its own machine with no Docker. Agents, steps, checks and
previews run on a separate worker machine (`forge-worker`). Apps run on
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
Docker host. The full list is in "Workers and running without local Docker".

A split install starts with the same four steps, with the differences
given in each. "Workers and running without local Docker" below then says
what to add, in order.

### Which one is for me

Start with a single-server install if you are one person or a small team
that trusts everyone who writes projects. Use a split install if a
misbehaving agent or app must not reach AppTown's records, or once other
people depend on the apps.

Choose before the first project if you can. Moving a single server to a
split install later means moving the database to a PostgreSQL you provide,
draining AppTown's own Docker ("Switching to no local Docker" below), and
moving each deployment on the built-in Docker host to an attached one,
since a split install has no built-in host.

Either way, start AppTown, configure model access, then add a model
provider. Starting the web server alone does not enable model agents. A
production install then adds the pieces the later sections describe:
deployment targets, sign-in, backups and upgrades.

### Prepare the host

You need Docker running, Git, a model-provider account, and the Forge
binaries.

**From a release.** Unpack the archive for your platform (its checksum is in
`SHA256SUMS`) to `/opt/forge`. It holds `bin/forged`, `bin/forge`,
`bin/forge-worker`, `bin/forge-linux` (the CLI the forge gives its containers),
`deploy/forge-agent` (the agent image's recipe) and example systemd units in
`deploy/systemd`: copy `forged.service` to `/etc/systemd/system/` and
`forged.env.example` to `/etc/forge/forged.env`, edit it, then
`systemctl enable --now forged`. The unit runs `forged` in `/opt/forge`, where
it finds `bin/forge-linux` and `deploy/forge-agent`. Build the agent image there
once (below). Before publishing a release, `scripts/stranger-install.sh
<archive>` installs it the same way in a fresh container, inside a Docker daemon
of its own on any Linux machine that may run a privileged container, and
reaches a first preview with no model key; it removes that daemon and touches
nothing else. The harness reads the admitted change's exact UUID from its
JSON response; failed admission or an invalid identity fails the check before
previewing. Its disposable host installs its own JSON parser.

**From the repository,** use the Go version in `go.mod` and install Node/npm
for the web build. From the repository root, install the web build's
dependencies once, then build:

```sh
npm ci --prefix web
make build
```

This builds `bin/forged`, the host `bin/forge` CLI and `bin/forge-linux` for
containers. Keep `forge-linux` beside `forged`, or pass its absolute path with
`-console-cli`. Without it, chats and steps run without the Forge CLI.

Agents also need the `forge-agent` Docker image, built from the recipe in the
installation (or repository) directory:

```sh
docker build -t forge-agent deploy/forge-agent
```

The recipe pins every archive it fetches by version and SHA-256; apt packages
and package managers' dependencies resolve when it is built, so two builds can
differ. A forge builds this image only when it is missing, so rebuild it when a
release's notes name a new agent image recipe; workers build their own,
`forge-agent:<recipe hash>`, when they start.

The forge starts that build in the background if the image is missing and
`deploy/forge-agent/Dockerfile` exists relative to its working directory; a
lone binary does not include it. A failed build is recorded in
`<data directory>/agent-image-build.log`.

On Linux, run `forged` as root when it runs containers on its own Docker:
steps and chats run as root in containers that write into the forge's
folders, and the forge must read and remove what they leave. An ordinary
user in the `docker` group cannot, and its workspaces are never cleaned
up. Docker Desktop on a Mac gives such files your ownership instead.

The data directory is `./_data` unless `-data` names another. With no
`-db-url`, Forge starts its own PostgreSQL container, `forge-db`, with its
files under `<data directory>/pgdata`. That container belongs to one
installation: for a second forge on the same machine, give it a separate
PostgreSQL database with `-db-url`; changing only `-data` is refused.

On a split install, AppTown's machine runs no Docker daemon. It needs Git,
an `ssh` client and the `docker` command (only to reach attached Docker
hosts), and a PostgreSQL database named with `-db-url`. Skip the agent
image and running as root there: the worker builds its own agent image,
and runs as root itself.

### Configure model access

Agents reach models through a private listener on the forge, separate from
the web address. Set all three:

| Environment variable | Meaning |
| --- | --- |
| `FORGE_INFERENCE_ADDR` | The private address and port the forge listens on, for example a Docker bridge address on port 7779 |
| `FORGE_INFERENCE_URL` | The address containers use to reach it, including the port |
| `FORGE_INFERENCE_NETWORKS` | Comma-separated CIDRs of the addresses those containers arrive from, as the forge sees them |

The values depend on Docker and host networking. On Linux, inspect the
bridge with `docker network inspect bridge`; chat and step networks may use
other subnets. On Docker Desktop, `host.docker.internal` reaches the host,
but connections may arrive from a shared address. Allow only the networks
your containers use; a `/0` network is refused. Keep this listener off
public ingress. Each call also needs a live credential the forge gave that
step or chat. Provider keys stay on the forge. The forge sets no spending
limit, so set one with your model provider.

Then start:

```sh
: "${FORGE_INFERENCE_ADDR:?set the private listener address}"
: "${FORGE_INFERENCE_URL:?set the container-reachable origin}"
: "${FORGE_INFERENCE_NETWORKS:?set the allowed container CIDRs}"
./bin/forged -addr 127.0.0.1:7777
```

The forge also opens a read-only API for chats on port 7778
(`-console-addr`). Containers must reach it; it is not the public web
address. If the start-up log says `inference disabled`, model agents cannot
run: stop the forge and fix the three settings. For a look at the web
interface only, leave them unset and add `-console-addr ''`; that forge
cannot run agents.

On a split install the containers that use these listeners are on the
worker. Bind both listeners to an address the worker can reach and the
internet cannot, set `-console-url` to the console listener's address as
the worker sees it, and name in `FORGE_INFERENCE_NETWORKS` the addresses
the worker's containers arrive from. The section on addresses in
`design/operations/workers.md` says which addresses those were in testing.

### Add a provider and choose sign-in

Open http://localhost:7777. A fresh forge asks for an Anthropic or
OpenRouter key. Saving it writes `<data directory>/secrets.env` (mode 0600)
and selects a default model. Saving a key does not test it: try a short chat
first. Model choice per job is on the [model providers and
slots](/docs/model/pi) integration's Admin tab.

The default sign-in mode, `-auth open`, trusts whatever name a caller gives,
so in open mode `forged` refuses to start on an address another machine can
reach (the main or the console listener): bind both to `127.0.0.1`, or pass
`-open-on-network` to accept that anyone who reaches the port has full
control. Before sharing the forge, start it with `-auth internal` (Forge's own
accounts and passwords; `-admins` names accounts to make administrators) or
`-auth trusted-header` behind an authenticating proxy.

Under `-auth internal`, the first account is the administrator, and making it
needs the setup token: while no account exists, the forge writes a random
token to `setup-token` in its data directory (readable only by the account
`forged` runs as) and says so in its log. The sign-in page asks for it. Once
the first account exists the file is removed. Reading a file on the host is
the proof of being its operator, so a stranger who finds the sign-in page
first cannot claim the forge.

The doors that take a secret (sign-in, the setup token, invites, OAuth codes
and refresh tokens) allow ten quick attempts from one address, then one every
six seconds; past that they answer 429 with `Retry-After`.

### Give it its public address

Start the forge with `-public-host` set to the origin people reach it by,
scheme included (for example `https://forge.example.com`). Addresses the
forge hands out are built on it, such as the one coding agents sign in to.
On this forge, coding agents connect at {forge:mcp} and the `forge` CLI uses
`FORGE_URL={forge:origin}`. Sign-in, invite and password reset links, and
coding agents' sign-in all use this one address. A bare name
(`forge.example.com`) is still accepted, but it does not say whether the
forge is reached over https: the forge then counts as https only when it
serves TLS itself or a `-tier-domain` is https, and the guides say "your
forge's address" instead of guessing. Behind a proxy that ends TLS, state
the origin, or browsers cannot sign in.

## Workers and running without local Docker

A **worker** is a separate machine, running `forge-worker`, with its own
Docker. Start the forge with `-worker-url`, `-worker-cert`, `-worker-key`
and `-worker-ca`; the two prove who they are to each other with mutual TLS,
and you issue the certificates. Without a worker, everything runs on the
forge's own host's Docker; a script step runs in a container there, and runs
as a plain process on the host only when it says `executor: local` and the
forge was started with `-host-scripts` (for a forge whose projects you trust
as much as its host). `-host-scripts` and `-agent-executor local` are
experimental: they are for developing the forge, not promised in 1.x. With a worker, agent, review, tests and script steps,
checks and change previews run there, new change chats open there by
default, and each landing's release images are built there. A step the worker cannot run is refused,
not moved back to the forge. Previews on a worker need a preview domain
(`-tier-domain preview=<domain>`), and release builds need `-private-docker`
and a container registry for each project (see the [package
registries](/docs/registry/registry) Admin tab).

A worker does not take all project work off the forge's host. These still
run there: project chats, chats that continue a step's agent, change chats
opened on the forge or already running there, the containers of deployments
on the built-in Docker host, and intranet releases of a landing that has no
published images. Nor is a worker a strong sandbox: code on it can reach
the network, and the private Docker a step or chat may get is not a
boundary against hostile code. Only `-no-local-docker` keeps every project
process off the forge's host. The `ghcr.io/flokkit/apptown` image runs this way
when `FORGE_NO_LOCAL_DOCKER=true` is in its environment; without it, the
image starts as the desktop trial (the comment in `deploy/Dockerfile`
shows both).

With `-no-local-docker` the forge's host runs no Docker and no project
process. It needs a worker and an external database (`-db-url`), and
deploys a landing to an attached Docker host instead of its own. This is
the split install.

### Setting up a split install

1. **The database.** A PostgreSQL database AppTown alone uses, named with
   `-db-url`.
2. **The worker.** A machine with Docker, running `forge-worker` as root.
   The release has an example unit, `deploy/systemd/forge-worker.service`.
   You issue its server certificate and AppTown's client certificate; the
   client certificate's Common Name is the worker's `-installation`. On a
   cloud machine, block the metadata service (below).
3. **AppTown.** Start `forged` with `-no-local-docker`, `-db-url`,
   `-worker-url`, `-worker-cert`, `-worker-key` and `-worker-ca`, the
   addresses from "Configure model access", a preview domain
   (`-tier-domain preview=<domain>`) and `-private-docker`, so that
   release images can be built on the worker.
4. **An app host.** Attach a Docker host and offer it to the projects that
   may use it ("Deployment targets" below). A project's first landing
   creates its `main` deployment there.
5. **A registry for each project.** Each landing publishes its images to
   the project's container registry, and the Docker host pulls them from
   there (see the [package registries](/docs/registry/registry) Admin tab).

The full procedure, with the API calls, is in the repository at
`design/operations/workers.md`, "Running a forge without a local Docker
daemon".

### Switching to no local Docker

A forge that last ran with its own Docker first runs once, stopped and with
its normal settings, with `-drain-local-docker`: it checks that no local
work or execution container is left, records that, and exits. A fresh
installation does not need this.

### Limits and the metadata service

Every container the forge starts is bounded. Steps, checks and tasks, here
or on a worker, and a step's private Docker get `-task-cpus` (4),
`-task-memory-mib` (8192) and `-task-pids` (4096); a chat on this host gets
`-chat-cpus` (2), `-chat-memory-mib` (4096) and `-chat-pids` (4096), as a
worker's chats do. A run that hits its memory limit is not a result: the
step errors and says to raise the limit. Each project's steps share a
package cache of their own (`forge-cache-<project>`), and each chat has its
own.

On a cloud host, a container can reach the provider's metadata service
(169.254.169.254) unless the host blocks it. On a cloud worker add a rule that blocks it (see
`design/operations/workers.md`) and check it with
`forge-worker -check-metadata`.

What runs where is in [How the forge works](/docs/guides/architecture).
Certificates, limits and each kind of work on a worker are in the
repository at `design/operations/workers.md`.

## Deployment targets

A deployment target is a place deployments run. Add one under
**Infrastructure → add a deployment target**. Each credential is the name of
a forge secret; set it there, or with `forge secret set <NAME> < file`.

| Kind | What you give it |
| --- | --- |
| Docker host | a machine with Docker, reached over SSH: its host and the secret holding the SSH key. The forge's own Docker is the built-in one |
| Portainer environment | the Portainer address, API key and environment |
| DigitalOcean App Platform | an API token, the region, and the container registry it copies images into. Run the registry's garbage collection occasionally to free space taken-down projects left; it blocks pushes (and so releases) while it runs. An app that needs object storage gets a Spaces bucket, so the account needs Spaces enabled, and the token makes and deletes Spaces keys: for each bucket a key that reaches it alone, and for a moment while it makes or deletes one, a full-access key it deletes again |
| Bucket website | an S3-compatible endpoint, region, website address and keys. Each deployment gets its own bucket, and taking the deployment down deletes it; use a key made for the forge alone |

The intranet target is the Portainer environment the forge is configured
with rather than one added here: it appears when the secrets `PORTAINER_URL`,
`PORTAINER_API_KEY` and `FORGE_APPS_DOMAIN` are set, and releases to the
environment `forged -portainer-environment` names (`FORGE_PORTAINER_ENVIRONMENT`;
default `local`, the name Portainer gives the Docker it is installed on). The
forge records the setting in the `portainer-deploy` integration at every
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
    beside the bar, never drawn as a size. A CPU limit the forge cannot read as
    a number of CPUs makes the sum "at least", and the rest of the bar "not
    read";
  - **disk**: used and free where Docker keeps its data, the folder it is,
    and when it was read. Below a tenth free it says so; below the floor
    new work needs (see "Disk space" below) it says new releases are
    refused, and each deployment's card on that host says deploys are
    refused. A disk it could not read says why.

  The holders are each deployment (by its record: its public container and
  its stack's services), previews, agent work (steps, chats), the forge's own
  containers (its database), forge containers no record names, and other
  containers the forge did not start. `forged` itself is counted once: in the
  host's own use when it runs on the host, in its container when it runs in
  one. Colour follows
  the kind of holder; each deployment is named on hover and in the table, and
  links to it. Small holders of a kind fold into "N more".

  The host's own use, and free memory, are known only on the built-in host,
  and only when the daemon sees `forged`'s files and the same memory total:
  `forged` writes a random marker into a file of its temp folder, the daemon
  reads it back through a read-only mount in the same small pinned image as
  the disk read (at most every five minutes), and both report the same total.
  `forged`'s memory figures are then taken as the daemon machine's. The case
  this cannot tell is two machines of equal memory sharing `forged`'s temp
  folder over a network filesystem. Otherwise the rest of the bar is hatched
  "not read", never free. Attached hosts do not
  report the host's own use in 1.0; per-deployment disk use is not shown.

  **A Portainer endpoint** shows what it can give containers, and the limits
  the forge's own deployments there declare.

  The disk is read by a one-off `df` in a small pinned image (busybox), with no
  network and the folder mounted read-only, at most every five minutes. A host
  that cannot run it shows the disk as unknown. Through Portainer, use and disk
  are not read.

**Disk space.** New work is refused while a disk it would write to has less
free than its floor, the larger of 1 GiB and a twentieth of the disk, or
when the disk cannot be read; the refusal says what is free. Each disk is
read again when the work starts, not taken from the page's reading:
- a release, recovery or preview on a Docker host reads Docker's data
  folder there before it pulls or starts anything; the deployment keeps
  serving what it served;
- a worker reads its state folder and its Docker's data folder once for a
  new task, before it writes anything of it; a release build's image that would leave the state folder under its floor
  is not copied, and the build fails saying so.

Stopping, taking down and removing are never refused. This keeps room for
the next piece of work; it does not reserve it, so two at once or one
larger than its free space can still fill a disk. Infrastructure lists this
forge's data folder and its worker's state folder and Docker data, each
with what is free and when it was read; a target's own disk is on its page.

To free space on a Docker host, remove what you know is unused: stopped
previews (the forge stops and removes them itself after their idle time),
images no container uses that are not a release you may deploy again
(`docker image ls`, then `docker image rm` by name), and released tasks'
leftovers on a worker (releasing a task removes its files). Do not run
`docker system prune --volumes` or `docker volume prune`: a deployment's data
volume belongs to it, and a backup's evidence is not garbage. The forge
does not yet offer an image cleanup of its own.

**Container logs.** Every container the forge starts for a deployment or
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
A crash-loop hold is the forge's own act and is in the deployment's history,
not among these. When the held container belongs to a released generation, a
note is added to that release's log: the release still reads as shipped, and
the later crash loop and stop are separate facts beside it. Limit events
are read from the Docker host's event log every 15 seconds. Docker replays only its recent events, so a burst, or a time when
the forge or the daemon was down, can leave a gap. App Platform does not report
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
deployment; logs can hold secrets, so the forge keeps no copy and does not
redact them. App Platform and Portainer deployments say they do not report
these.

A Docker target's page also lists what the forge recorded creating there
(containers, volumes, networks and images) beside what Docker reports now. A
container is one row per name, however many times it was replaced. **Missing**
means a complete answer from Docker did not include it; if any listing fails,
the page keeps the previous answer with its time and the error rather than
showing a partial one. Images on the host that no record names are not listed
for removal.

A deployment that reads **uncertain** (its last attempt was cut off, or its
records disagree) takes no new deploy until it is settled. Press **Check
again** on its card: the forge asks the host what runs and records the
answer.

## People, invites, roles and grants

Accounts are on the **People** page.

- **Invites.** With `-auth internal`, an administrator invites a person
  from **People → Invite a person** or with
  `forge invite <username> [--role member|admin] [--days N]`. The link is
  shown once and works for 1 to 30 days (7 by default); the person sets
  their own password. Forge sends no email.
- **Password reset links.** For an account that already has a password,
  an administrator uses **People → reset password** or
  `forge password-link <username> [--days N]`. The link is shown once,
  works for 1 to 30 days (3 by default), and you hand it over yourself; a
  newer link for the same person withdraws the older one. Until it is used
  nothing changes. When the person uses it they choose a new password: the
  old password stops working and every browser session of theirs ends,
  while their roles and grants stay exactly as they were. This signs them
  out of browsers. Their personal access tokens and agent connections keep
  working — revoke those on the People page if the account may be
  compromised. A disabled account gets no link; enable it first. With
  single sign-on (`-auth trusted-header`) Forge holds no passwords and
  refuses: reset the password in your identity provider.
- **Roles and grants.** A role is a named set of things a person may do.
  Roles on the whole forge (instance administrator, auditor, project
  creator and others) are granted on **People → Roles on this forge**;
  roles on one project are on its **Settings → Members**. A grant can name
  a person or a group. Nothing is implied: a power nobody was granted,
  nobody holds.
- **Who reads what, by default.** A new forge grants the built-in group
  *all authenticated users* the Auditor role on the whole forge: everyone
  signed in reads every project, change and record. It suits a team that
  works in the open. To keep projects private, revoke that grant on
  **People → Roles on this forge** (the row for *all authenticated users*,
  Auditor); then people read the projects they are members of. The forge
  never puts the grant back. In open mode (`-auth open`, loopback only by
  default) anyone who can reach the forge reads everything as well.
- **Tokens.** `forge token create <name> --purpose cli|agent` makes a
  personal token. A `cli` token acts as its person; an `agent` token, for a
  coding agent, may not approve, land or administer.
- **Lost administrator password.** On the forge's host, with its normal
  settings: `forged -reset-password <username>` prints a new password for
  that existing account, ends its sessions, and exits without serving.

[Connect a coding agent or the CLI](/docs/guides/connect-a-coding-agent)
covers tokens from a developer's side, and the
[glossary](/docs/guides/glossary) defines the roles' terms.

## The forge's own backups

When the forge uses its built-in database, it dumps it every night at 02:00
(by the host's clock) into `<data directory>/backups/forge-<date>.sql.gz`
and keeps the newest 14. Each dump, or failure, is in the audit log
(`db.backup`, `db.backup-failed`). With `-db-url` there is no nightly dump:
back up that database yourself.

The dump is the database only. The rest of the forge itself is in the data
directory: deployment backup sets under `backups/`, project repositories and change folders under `workspaces/`,
the built-in secrets store `secrets.env`, `db.env` (the built-in database's
password), `instance-id`, the key step credentials are made with
(`runtime-token-key`), the SSH keys of attached hosts, and `layout`, the
version of the directory's own layout (a build refuses a directory written by
a newer one, by name, rather than misread it). Back that
directory up as well, for example with the host's file-system snapshots.
The live data of deployments on the built-in Docker host is not in it: it is
in Docker volumes, covered by the deployment backups below.

**The forge's own id.** Every forge has a UUID, the instance setting
`forge.id`, written once at its first start and never changed. It names this
forge in its project exports and backup sets, and selects the identities its
records had before 1.0 (an old link finds its record through it). It is in
the database, so a forge restored from a dump keeps it; a copy restored
alongside the original has the same id, so run only one of them.

**Restoring the database.** This replaces the current database. If it is
still readable, back it up first (see Upgrading). Stop `forged`, then:

```sh
docker exec forge-db dropdb -U postgres forge
docker exec forge-db createdb -U postgres forge
gunzip -c <file> | docker exec -i forge-db psql -v ON_ERROR_STOP=1 -U postgres -d forge
```

Start once with `-verify-only` to look before anything runs: it goes
through the normal startup, which upgrades the database and the data
directory to this build, then serves reads only, with no recovery, scheduler
or workers. It is not a dry run of an upgrade. Then restart without it.

**Moving a forge to another host.** Stop `forged` and `forge-db`, copy the
whole data directory (with the built-in database, its files in `pgdata`
come with it), and start `forged` on the new host with the same settings,
first with `-verify-only`.

That moves the forge itself, not the data of deployments on its built-in
Docker host. Their data volumes, and their services' volumes (such as a
Postgres need), are Docker volumes outside the data directory, and the apps
keep running and writing on the old host after `forged` stops. On its first
normal start the new host rebuilds those deployments with empty volumes.
Copied backup sets are not restored by themselves.

So, before stopping `forged` on the old host, for each such deployment:

1. **Stop every application part, and keep it stopped.** On the host, a
   deployment's containers are named from its project and deployment:
   the public part is `forge-env-<project>-<deployment>`, and every other
   part and every need is `forge-svc-env-<project>-<deployment>-<name>`,
   where `<name>` is the part's name under `app:` or the need's name under
   `needs:` in `.forge/app.yaml`. `docker stop` the public part and each
   other part (a background worker writes too). Leave the need containers
   running: their copies are taken inside them.
2. **Back up now.** The set is written under the data directory, so it
   moves with it.
3. **After the move,** once the new host has rebuilt the deployment,
   **Restore** the set. Restore stops every application part the running
   release names, puts the data back and starts them again; a part that will
   not stop refuses the restore before anything is written, and one that is
   not there is named as not started.

What this carries over depends on the need:

- **The `dataDir` volume** of the public part: copied whole and put back.
- **Postgres:** dumped with `pg_dump` and loaded with `pg_restore`; carried
  over.
- **Redis:** told to save, then its `dump.rdb` is copied; the restore stops
  the Redis container, puts the file in place and starts it, and checks
  that Redis holds as many keys as the copy. Carried over, and the restore
  says "verified". Sets taken before 8 October 2026 hold only an archive of
  Redis's files, which the running Redis does not read: take a new set.
- **S3:** every object is copied through the S3 API with its headers and
  put back the same way, and the restore checks each object's bytes and
  headers, so it says "verified". ACLs, tags and versions are not copied.
  A set taken before 17 October 2026 is a file copy of `/data` without the
  objects' metadata; its restore says "files copied, not verified".

Keep the old host until each restore has succeeded. Deployments on
attached Docker hosts, Portainer, App Platform and bucket websites stay
where they are.

**Recovering the whole forge on a replacement host.** A whole-forge backup is
the database dump and the data directory taken at one moment: a file-system
snapshot of the data directory taken with the dump is the reliable way. What
you must arrange yourself:

- **Copies off the host.** The nightly dump sits on the same disk as the
  database; keep snapshots or copies somewhere else.
- **The data directory, whole.** It holds the forge's keys as files:
  `runtime-token-key` (step credentials), `secrets.env` (the built-in secret
  store and connector credentials), `db.env`, `instance-id`, and `ssh/` (keys
  for attached hosts). The built-in secret store's own key is in the database,
  so the dump and the data directory together hold every secret: protect them
  as you would the secrets.
- **The images a fresh host lacks before anything else starts:** the built-in
  database's `postgres:16-alpine`, and the agent image.
- **Keep the old host** until the new one passes your checks.

The recovery point is the last snapshot and dump. The procedure, with a
replacement host that is cut off from the outside world for a rehearsal, is
`scripts/forge-recovery-rehearsal.sh`; `scripts/forge-restore-check.py` checks the
result through the API with a CLI token made before the backup. Its steps:
dump the database with `scripts/forge-db-backup.sh`, copy the data directory
without `pgdata` and `cache`, install the same `forged` build, start once with
`-verify-only` to make the built-in database, load the dump (the commands under
"Restoring the database" above), check the key files came across unchanged,
check read-only with `-verify-only`, then start normally.
The rehearsal checks this invocation's full target lock before cleanup effects
and again before releasing it. An unreadable or different lock refuses cleanup;
failed lock release is a failed rehearsal, even if resource cleanup already
lifted the network cut. Inspect the target before reusing it after cleanup fails.

Rehearsed on 12 October 2026 by the maintainers from a production forge (28
projects, a 2.5 GB data directory) onto a spare host, three passing runs: 5 to 6 minutes to a forge
serving reads, and a static site rebuilt from its recorded release and
answering 77 to 83 seconds after the normal start (6¼ to 7½ minutes in all).
Copying the data directory took 3½ to 4½ of those minutes (relayed over SSH
through the operator's machine; a direct copy or a restored snapshot is
faster); loading the dump took 16 seconds (it is plain SQL, loaded by one
`psql`). The key files, credentials, the change history,
the evidence, approvals, reports and usage of 80 changes, and the recorded
main of 27 repositories came across unchanged; the one project with no
recorded main on the source is named as unverified, not counted.

**What comes back by itself on a normal start.** Deployments on the built-in
Docker host are stood up again from what each one's last release recorded, not
from the old host's images: a static site with no network, a source build if
the new host can reach the package registries the build uses, a digest bundle
by pulling it. A deployment whose last release recorded no rebuild inputs (the
forge began recording them in early September 2026, before 1.0) cannot be
rebuilt from its record: release it again before you need to, which records
them. A deployment fed by landing takes by hand only the bundle it is known to
run, and only while it is settled (no deploy under way), so this changes
nothing a landing would have chosen: find the bundle of the release that put it
there (`GET /api/v1/projects/<slug>/releases`, the newest release to that
deployment, its `bundleId`) and `POST
/api/v1/projects/<slug>/deployments/<name>/deploy` with `{"bundleId": <that
bundle>}`. Any other bundle, even one at the same revision, is refused: landing
deploys it.
Deployments on attached hosts, Portainer, App Platform and
bucket websites stay where they are. Their data comes back through deployment
backups (below), not by itself.

**A rehearsal is cut off from the outside.** A restored forge holds the
credentials of everything it manages: upstream Git, cloud accounts, attached
hosts, model providers. As soon as it starts normally it reconciles
deployments and fires schedules. The rehearsal script cuts the replacement
host off before the forge starts (an nftables table allowing only loopback,
DNS, Docker's own networks and one registry, and `/etc/hosts` entries for the
names that share that registry's address). The cut is selective: DNS, Docker's own
networks and that registry stay reachable, and reaching the registry is not
read-only. When the rehearsal ends it stops the forge and removes every
container, network and volume created since it started (it knows them from
what Docker held before), and lifts the cut only once none remain. It refuses
a target that already holds a forge's containers.

## Deployment backups

The forge backs up deployments on Docker hosts: every data volume the
deployment holds, which is each one recorded when the forge made it plus
each one the release it runs declares (the public part's `dataDir` and
each need that keeps data), whatever the project's latest commit says. A
volume nothing can copy (no running container mounts it) is in the set as
not copied, so the set reads partial. A deployment with no recorded
release reads unknown and its nightly backup fails until it is released
again. A need that declares a
copy (Postgres's dump, Redis's save and snapshot) is copied and restored
with it; any other (S3) has its data folder archived and unpacked as files,
badged "file copy — may be inconsistent", and its restore says "files
copied, not verified". A restore reports "restored" only when every item
was verified or loaded by its service. A set is
taken after each release goes live there and every night at 03:00, and
on **Back up now**. The newest 14 sets of a production deployment are kept,
and the newest seven of any other, wherever they are. Taking a deployment
down takes a final set first, kept for 30 days. Portainer deployments are
backed up the same way, through Portainer's Docker proxy (an edge
environment in async mode has none, and is refused). Bucket websites hold
no data.

**What a deployment's backups cover.** Each deployment card (and `forge
data`) says what its backups cover, item by item ("covers: postgres ·
volume …"), and marks the rest **not assessed**: an item nothing could
copy, and always data the app keeps outside the needs it declares, such as
a bucket or a database it reaches with its own settings (a bucket from the
app's `s3` need is one of the items covered). The forge
cannot tell from an app's settings which outside stores hold its data, so a
fresh backup, a passed check, rehearsal or restore test is evidence for the
items it covers, not for the whole application. Protect an outside store
with its own provider's mechanism (versioning, its own backups) and
rehearse its recovery yourself. The backup store below holds the forge's
copies; it is not a place for an app's uploads.

**Where Docker-host backups are kept.** When a backup store is attached
(below), every backup goes to the store; the forge's disk holds a set only
while it uploads. Postgres is copied by its portable export, so a copy in
the store can also be rehearsed on another host. Without a store, backups
stay on the forge's own disk, and losing that disk loses them: the
deployment's card reads "Backups kept on this host ⚠", the Backups page
"Kept on: this host ⚠", and a restore rehearsal is unavailable. Once a
store is attached,
the nightly sweep moves the sets still on the disk into it, each checked
against its recorded digest; a set that does not match stays on the disk
and says why. If the forge stops just after it has moved a set and before
it has deleted that set's folder, the folder stays under
`<data directory>/backups/<project>/<deployment>/<time>`; the forge logs
its name as it moves the set, and the store holds the checked copy. Delete
such a folder by hand. The Backups page's **All sets** table shows each
set's **Kept on** and path: a folder is safe to remove when the set with
the same `<project>/<deployment>/<time>` is kept on "backup store". If the store cannot be reached, backups fail and read
stale; the forge does not fall back to its disk, a take-down deletes
nothing, and a restore is refused before anything changes.

**App Platform databases go to a backup store, off the forge's host.** Two
settings turn this on:

1. **Attach a backup store** (Deployments → Backups → Attach a bucket,
   platform administrators): an existing private bucket in an S3-compatible
   store (DigitalOcean Spaces: endpoint `https://syd1.digitaloceanspaces.com`,
   region `syd1`) and the names of the two secrets that hold its key pair
   (`forge secret set NAME < file`). The forge writes a private object,
   reads it back and deletes it before it attaches the bucket. Anyone who
   holds a key to that bucket can read the copies in it. Copies are read
   only from the bucket they were written to, so another bucket can be
   attached only when none are left in the current one; do it when no copy,
   check or take-down is running.
2. **Name a copy runner** on the App Platform target (Infrastructure →
   the target → Edit target → copy runner): a Docker host the forge reaches.
   Copies run there in a throwaway `postgres:<version>-alpine` container,
   as the app's own database user over TLS. The database cluster must let
   that host connect (its trusted sources).
   Until one is named, a deployment's **Copy now**, **Check it comes
   back** and restore buttons show greyed, with the reason, and the
   target's page says backups and restore tests are unavailable there.
   Taking such a deployment down is refused, since no final copy can be
   taken, unless you choose to take it down without one; its database
   then stays at the provider, listed under retained resources.

Then production App Platform deployments are copied every night and checked
once a week (the newest copy is filled into a new database on the same
cluster, checked and removed); any deployment's card has **Copy now** and
**Check it comes back**, and shows how recent its copies are, DigitalOcean's
own backups and the last check. Taking such a deployment down deletes the
app first, takes a final copy, checks it comes back, and only then deletes
the database cluster. If anything falls short the cluster stays and the
deployment reads "app removed, data kept; deploy again to serve". A new
deployment can **start from the final copy** of one taken down: its first
release fills its database from the copy, and checks it, before the app
starts. That needs the restore grant (project owners have it) in both
projects; a production copy goes only into a production deployment. Without
a store, production data on App Platform reads "not protected off-host" and
a take-down keeps the cluster as before. `forge data -p <project>
<deployment>` prints the same.

All sets are on **Deployments → Backups**. **Restore** first copies what is
there now to a safety set, then stops every part of the deployment, puts the
set back and starts them all again. A part that will not stop refuses the
restore before anything is written; a part the running release names that is
not there is reported as not started. The restore runs as an operation,
which `forge executing` lists while it runs, so a deploy waits for it. If the
forge restarts in the middle, the restore ends `unknown` and names what it
may have left stopped; check the host for a restore still running, then
release the deployment again. Each copy goes
back into the volume it was taken from. A set from another deployment (a
final set), or from before October 2026, goes by role: the app's data into
the app's volume, each need into that need's. If the deployment holds two
volumes of a role, that restore is refused before anything changes. Details are in
[Previews and deployments](/docs/guides/container-lifecycle).

**The S3 emulator changed in October 2026.** MinIO's images can no longer
be pulled from Docker Hub or quay.io, so the stock `s3` need now runs the
Versity S3 Gateway (`versity/versitygw`, pinned). A deployment whose S3
service was made with MinIO keeps running, but its next release sees the
service's image has changed: with synthetic data the service is recreated
empty; otherwise the release is refused and says so. MinIO's files are in
its own format, which the gateway does not read. Copy the objects out
with an S3 client first if they matter, then retire the deployment's
services under Deployments so they are made again from the contract.

## Upgrading

1. **Check nothing is executing.** `forge executing` lists the steps and
   the operations (copies, restores, take-downs) a restart would interrupt;
   it exits 0 when there are none and 3 when there are some. A step cut off
   by a restart may be left in error and need a retry; an operation cut off
   ends unknown with what it had done. Asked of a forge from before rc.6,
   which does not report its operations, it says so and fails: look at the
   forge's operations yourself, then upgrade without asking.
2. **Stop the forge, then copy it, and stop if a copy fails.** Stop
   `forged` (leave `forge-db` running), so nobody writes between the two
   copies and they describe one moment. Take the database dump with the
   checked backup script from the repository (the nightly dump may be a day
   old):

   ```sh
   bash scripts/forge-db-backup.sh forge-db postgres forge <directory> pre-upgrade
   ```

   It refuses, and leaves no file, unless `pg_dump` and `gzip` both
   succeed, the file is valid gzip, the dump ends with its "dump complete"
   line, and it holds the forge's tables. A bare `pg_dump | gzip` can
   produce a file that looks fine and holds nothing. With `-db-url`, use that
   database's own backup and check that it completed. Then copy the data
   directory without `pgdata` (the dump holds the database) and `cache`:

   ```sh
   tar -C <data directory> --exclude=./pgdata --exclude=./cache -czf <directory>/forge-data-pre-upgrade.tar.gz .
   ```
3. **Replace the binaries and start.** Unpack the new release over
   `/opt/forge` (or rebuild with `make build`, which builds `forged`, `forge`
   and `forge-worker`); when the release notes name a new agent image recipe,
   rebuild `forge-agent` where you run it; install the worker first, then
   start `forged`. The new `forged` upgrades the database and the data
   directory's layout when it starts, including with `-verify-only`.
4. **Switching to no local Docker** at the same time: run once with
   `-drain-local-docker` before the first start with `-no-local-docker`
   (see above).
5. **Deployments made before ownership was recorded.** The forge deletes
   only what it recorded creating for a deployment. For older deployments,
   an administrator runs the one-time inspection,
   `GET /api/v1/ownership/inspection`, and approves what it proposes with
   `POST /api/v1/ownership/approvals`. Exactly what is approved is
   recorded.

### Rolling back

The way back is the two copies from step 2: stop `forged`, load the dump
(the commands under "Restoring the database"), replace the data directory's
contents except `pgdata` with the copy, and start the previous release. That
discards everything since the copies: the new build's database and layout
upgrades and any work done on it. The new build's first write is where work
starts being lost, so roll back before people use it, or accept losing what
they did. There is no other downgrade: an older build refuses a database or a
data directory a newer one has upgraded. Each release's test suite upgrades
every earlier release's forge and rolls it back this way
(`make upgrade-test`).

### Versions

`forged -version`, `forge-worker -version` and `forge version` name the
build: a release name, or `dev-` and the commit it was built from
(`-modified` with local changes). `forge version` also names the forge it
talks to, which states its version on every API answer.

Which binaries work together is decided by two protocol numbers, not by the
version names. The CLI states the CLI protocol it speaks on every request;
the forge accepts a range and refuses another with `426` and a sentence
naming both sides and what to install. A forge states its worker protocol to
its worker the same way, and the worker refuses one outside its range. A
worker keeps serving the previous forge protocol for one release, so a worker
installed before its forge goes on serving the forge it replaces.

A request that states no protocol is served: browsers, MCP clients and
`curl` state none, nor do a forge or CLI built before protocols were stated.
Being served is not evidence that such a caller is compatible.

A release that raises the CLI protocol says so in its notes. The new CLI is
then refused by the forge it replaces, including when an upgrade asks that
forge `forge executing`: answer that question with the CLI of the running
release, or establish yourself that nothing is executing and upgrade without
asking.

## Packs, versions and what-if

A pack is a way of working: workflows, guidance, policies and checks that
projects adopt. They are under **Ways of working → Packs**.

- **Stop offering** a pack and new projects cannot pick it; projects on it
  keep it. Stock packs cannot be deleted. To change a stock pack, **Include
  in a new pack** and change your copy.
- Saving a pack makes a new version. On the pack's page, **Recommend** a
  version to make it the one new projects get and the one others are
  offered (or `forge pack recommend <pack> <version>`; `forge pack versions
  <pack>` lists them). No project moves until its owner adopts the version.
  A new forge release publishes its stock packs' new versions without
  recommending them; the forge's start-up log names each one waiting
  (`stock pack <name>: vN published, vM recommended`).
- Before recommending, run **What-if** on that version (or `forge whatif
  <pack> <version>`): by default it runs on every eligible project, one on
  an earlier version of the pack, or on projects you choose (on the pack,
  or not yet on it, with `-p <project>[,<project>…]`). Its checks run on
  each project's main branch beside what it adopted. Its page has one row
  per project: **new failures** (by check, including an exception that
  would no longer cover one), **could not assess** (with why), **no new
  failures**, or **not evaluated**. Recommend the version from there, or tick projects and
  **Adopt** to open one adoption Change per project with exactly what was
  assessed (or `forge whatif adopt <id> -p <project>[,<project>…]`); each
  Change says which what-if it came from, and a project whose adoption moved
  since is refused (run the what-if again). Recommending from the what-if
  page, or with `forge pack recommend <pack> <version> --what-if <id>`,
  records the what-if on the recommendation it makes; naming a what-if for
  the version already recommended is refused, as nothing would change. Model
  rubrics cost money, so they run only if you include them with a spend
  limit (on the page, or `--rubrics <dollars>`); spend is read before each
  project, and once it reaches the limit no more are sent, so one project's
  calls may pass it. It never changes a project's standing. `forge whatif show <id>`
  prints one again.
- To see whether work done under the version comes out better, start an
  **Eval** of it (or `forge eval start --pack <name@version>`): finished
  Changes are re-run in throwaway copies as their projects run now and with
  the version, and the eval page compares them side by side, check by check.
  Evals are read by standards administrators and auditors and started by
  people who manage packs.

How projects measure up against what they adopted is in
[Conformance](/docs/guides/conformance).

## Monitoring

- **Up or down.** `GET /healthz` answers `ok` (200) while the forge serves and
  its database answers, and 503 otherwise; it needs no sign-in and says nothing
  else, for a load balancer or a service manager.
- **Usage** shows model tokens and estimated cost, and "Where the time goes" shows time per
  step (also `forge timing`).
- **Telemetry.** With `-otlp-endpoint` (and `-otlp-protocol http|grpc`) the
  forge sends its own traces, metrics and logs to an OpenTelemetry
  receiver. A Grafana dashboard for them is in the repository at
  `stock/observability/forge-dashboard.json`, with a README on loading it.
- **Errors.** Attach the `forge` project to the error tracker and the
  forge reports its own errors there; see the [error
  reporting](/docs/errors/glitchtip) Admin tab.
- **Complaints** collects what got in agents' way.

## Integrations

Each integration's page under **Integrations** has an Admin tab here in the
Docs:

- [Model providers and slots](/docs/model/pi): which models agents use, per job.
- [AI models](/docs/llm/openrouter): model access for the apps you deploy.
- [Package registries and mirrors](/docs/registry/registry), and the container registries releases are published to.
- [Error reporting](/docs/errors/glitchtip).
- [Scanners](/docs/scanner/tool).
