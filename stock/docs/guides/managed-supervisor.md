# Managed supervisor

The Linux `apptown-update` program keeps installation registration and startup
control outside the daemon's release and data directories. Enrollment does not
enable replacement: matched recovery, candidate verification and worker
compatibility must also be available. The Updates page continues to show signed
release notifications and the separate supervisor state.

An empty `APPTOWN_UPDATE_SUPERVISOR_SOCKET` means unmanaged. A configured socket
that cannot be contacted means unavailable, including while its service is
restarting. Neither state permits replacement. Use the normal
[upgrade procedure](upgrading.md) when managed recovery is unavailable.

## Installation ownership

Only a host administrator enrolls an installation. The supervisor supports a
root-run Linux systemd daemon or a root-run container on that same host's Docker.
It does not enroll a desktop trial's private Docker, an arbitrary remote Docker
endpoint, a non-root daemon or an unmanaged source process.

Keep the stable executable outside the versioned release and the data directory,
for example `/usr/local/libexec/apptown-update`. Keep its root-owned state directory
separate, for example `/var/lib/apptown-update`, with mode `0700`. Control records
and the socket are private to root. Do not replace the stable executable during
an unresolved transaction.

A registration JSON file names the installation's `dataDir`, `stateDir`,
`supervisor`, `supervisorSHA256`, `environmentFile`, `environmentSHA256`,
`service`, `unitSHA256`, `adapter` and `daemonUID` (zero). `databaseIdentity` is a
SHA-256 digest of the host's secret-free database target descriptor. It records
which target was named; enrollment does not prove database recovery readiness.
For `systemd`, also supply the immutable `releaseDir` and `executableSHA256` of
its `bin/apptownd`. For `docker`, supply the full `container` ID, immutable
`image` ID (`sha256:…`), observed `dockerEngine` ID and local `dockerSocket` path.
Enrollment creates the installation UUID and records the host administrator and
time. It never invents a product user's assent.

The environment file uses literal `KEY=value` assignments, one per line, with
optional blank lines and `#` comments. Quoting, expansion, backslash escapes,
duplicate keys and surrounding whitespace are refused. It must explicitly bind
`APPTOWN_DATA` and `APPTOWN_UPDATE_SUPERVISOR_SOCKET` to the registered data path
and `<stateDir>/control.sock`. Include every effective `APPTOWN_` and `DOCKER_`
setting. Protect secrets in this file with root ownership and mode `0600`.

Prepare the exact managed unit with:

```sh
apptown-update unit registration.json > apptown-managed.service
```

Its stable guard launches the registered daemon, retains startup exclusion until
the child settles, and checks the effective configuration. Record the generated
unit's SHA-256 in `unitSHA256`, install it under the registered service name, and
reload systemd. Drop-ins, an unreloaded unit or changed artifacts refuse managed
startup. Stop the old unmanaged service before enrollment; enrollment refuses a
running installation and does not alter a live service or container.

```sh
apptown-update -state /var/lib/apptown-update enroll registration.json
```

Run `apptown-update -state /var/lib/apptown-update serve` in a separate root-owned
systemd service, outside the daemon it supervises. The registered daemon service
uses the generated unit. Both services may be enabled for host boot: the daemon's
stable guard reads the external journal before launch, even when the supervisor
has not yet bound its socket.

For host-Docker, prepare the exact immutable container with restart policy `no`,
host networking, and data/control bind mounts at their identical registered paths.
The environment must match the registration. The generated systemd unit runs the
independent host controller; Docker's own restart policy must not bypass it.
Caddy, PostgreSQL and application containers are outside this controller. It never
uses `docker compose up`, recreates a container or chooses another container by
name when the registered ID is missing.

## Status and interrupted work

```sh
apptown-update -state /var/lib/apptown-update status
apptown-update -state /var/lib/apptown-update recover
```

Status is available from the local supervisor or, for host root when it is
absent, from its external registration and journal. Offline status is explicitly
not a current process observation. The recovery command reports the retained
state and refuses when matched recovery is unavailable; it does not clear a fence,
restore files or declare success.

Staging a request records an intention, not permission to stop or replace the
daemon. Reusing its UUID with changed input refuses. Missing or corrupt progress,
a maintenance phase, changed installation identity, or an unconfirmed container
shutdown blocks ordinary startup. Cancelling an unstopped intention cannot clear
a maintenance or process-settlement fence. Preserve those records for host
recovery; deleting them does not establish that the installed state is safe.

Release archives include the separate supervisor executable and bind its bytes
in signed member evidence. Its control protocol is independent of a release's
replacement compatibility declaration. A version string, registration record or
healthy HTTP response does not establish replacement or rollback readiness.
