# Docker host target setup

A Docker host target runs each deployment as containers on a machine with
Docker. AppTown reaches an attached host over SSH. The built-in Docker host is
AppTown's own Docker; it needs no setup.

## The built-in Docker host

AppTown adds the target `built-in-docker` when it starts, unless `apptownd`
runs with `-no-local-docker`. Its apps are reached through AppTown's own
address, and their ports stay on the machine's loopback address. It cannot be
retired. The rest of this guide is about attaching another machine.

## What you need

- A Linux machine with Docker, reached over SSH by a user who can run
  `docker`. That user need not be root.
- The machine's SSH server with its default of ten sessions per connection,
  or more. AppTown shares one SSH connection per host and runs up to three
  Docker calls on it at once.
- For apps at a domain: a wildcard DNS record such as `*.apps.example.com`
  pointing at the host, and Traefik running there. AppTown labels each
  deployment's `main` container for Traefik; it does not install or run
  Traefik. [AppTown setups](setups.md) lists the network, entrypoints and
  certificate resolver Traefik needs, and the firewall rules.
- Without a domain, apps are reached at the host's address and each
  deployment's port.

## The credential

Make an SSH key for AppTown alone, and add its public key to the host user's
`~/.ssh/authorized_keys`. Store the private key as an AppTown secret, under
**Integrations › Secrets** or with:

```sh
apptown secret set DROPLET_SSH_KEY < id_ed25519
```

The secret must hold the whole private key, from its `BEGIN … PRIVATE KEY`
line. AppTown accepts the host's key the first time it connects and refuses a
different key after that.

## Add the target

Open **Deployments › Targets → Add a deployment target** and choose **Docker
host**.

| Field | What to enter |
| --- | --- |
| name | A short name for the target, such as `droplet-syd` |
| address | `ssh://user@host`, with `:port` when SSH is not on 22 |
| SSH key secret | The name of the secret holding the private key |

**Check** connects over SSH and runs `docker info`. Nothing is recorded until
you press **Add target**. Then set its offer and facts:

- **Apps domain**: the domain apps are reached under; `main` is served at
  `<project>.<domain>`. Leave it empty to reach apps by address and port.
- **Disks**: tick it when deployments may keep a data folder here.
- **Needs**: **emulators on the host** runs each declared service, such as
  Postgres, as a container beside the app.

## Check it worked

The target's page shows **Target check:** with the Docker version, the
operating system and how many containers run. Its **Capacity** page shows
memory, CPU and disk. An attached host does not report its own system's memory
use, so that part of the bar reads "not read". A failed SSH connection is
shown at the top of the page with the reason.

## What AppTown creates and deletes

For each deployment AppTown creates containers named `apptown-…`, a network
`apptown-net-…`, a volume for the app's data folder and one for each service
that keeps data, and it pulls the release's images. Each carries AppTown's
labels, and each is recorded.

Taking a deployment down takes a final backup, then deletes exactly what
AppTown recorded creating for it. If the final backup is incomplete, the app
is started again and nothing is removed. AppTown never deletes containers,
volumes or images it did not create. Its own image clean-up removes only
unused, untagged images it built itself. A small `busybox` container reads the
disk's free space.

## The SSH connection

AppTown keeps one shared SSH connection per attached host and reuses it for
Docker commands, including event streams. Up to three Docker calls run at once
per host. This limits calls, not SSH sessions: Docker opens an SSH session for
each HTTP connection, and run, attach and BuildKit can hold two or three at
once. Three calls stay within sshd's default of ten sessions per connection.
OpenSSH keeps the connection for three minutes after the last call, then
removes its socket. The sockets are private to AppTown's data folder, so
different installations do not share them.

AppTown writes the host's SSH settings to the `~/.ssh/config` of the user
`apptownd` runs as ([Setting up a server](server-setup.md)). If AppTown cannot
open and check the shared connection, the call fails with an SSH connection
sharing error. Keep AppTown's data folder path short enough for a Unix socket;
a path that is too long is reported when the host is checked. A dead
connection is replaced on the next call. After a network drop, keepalive
checks close an unresponsive connection; a call already using it can fail,
and is not run again by itself.

## Costs and limits

You pay your provider for the machine. AppTown shows no cost for it.

- Every deployment also publishes its port on all of the host's network
  addresses. Docker's published ports bypass the host's own firewall rules,
  so put a provider firewall in front of the host ([AppTown
  setups](setups.md)).
- **Stop** stops only the app's containers; its services keep running.
- Backups are kept on AppTown's disk, or in the backup store when one is
  attached ([Deployment backups](deployment-backups.md)).
- New releases are refused while Docker's disk is nearly full ([Deployment
  targets](deployment-targets.md), "Disk space").
