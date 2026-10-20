# Running AppTown

This page is for the person who installs and looks after AppTown. Building
apps on it is in [Start here](/docs/guides/quickstart); how the parts fit
together is in [How AppTown works](/docs/guides/architecture); what it runs
on is in [Supported platforms and system requirements](/docs/guides/platforms).

`apptownd -h` lists every start-up flag. Each flag can also be set as an
environment variable: `-db-url` is `APPTOWN_DB_URL`, `-tier-domain` is
`APPTOWN_TIER_DOMAIN` (comma-separated), and a flag on the command line wins.

## Install

### 1. Try it on your computer

```sh
curl -fsSL https://apptown.dev/try | sh
```

This starts AppTown in one Docker container called `apptown`, with its data
in a Docker volume of the same name, and opens it in your browser. It runs
its own Docker inside that container, so it is for your own computer only,
not for a server or anything other people use. AppTown serves itself and your
apps at `https://…localhost:7777` addresses from its own certificate
authority; on macOS the script explains this and asks before trusting it, and
on Linux it links the steps. `sh -s -- stop` stops it with its data kept;
`sh -s -- uninstall` removes it, its data and the trust. Details: [The desktop
trial](/docs/guides/trial).

### 2. One server

AppTown, its agents and its apps on one Linux machine with Docker. It suits
one person or a small team whose projects are written by people trusted as
much as the machine.

1. Unpack the release archive for your platform to `/opt/apptown`.
2. Copy `deploy/systemd/apptownd.service` to `/etc/systemd/system/` and
   `deploy/systemd/apptownd.env.example` to `/etc/apptown/apptownd.env`.
3. In `apptownd.env`, set `APPTOWN_PUBLIC_HOST` to the address people will
   use and `APPTOWN_AGENT_IMAGE` to this release's agent image; check the
   model-access addresses against your Docker bridge.
4. `systemctl enable --now apptownd`, put a TLS proxy in front of
   `127.0.0.1:7777`, and open the public address.
5. Create the administrator with the setup token from
   `/var/lib/apptown/setup-token`, then add an Anthropic or OpenRouter key.

What each setting means, what one server does and does not protect, and
sign-in modes: [Setting up a server](/docs/guides/server-setup).

### 3. Split install

AppTown on its own machine, agents on a worker, apps on separate Docker
hosts or a cloud, and a PostgreSQL you provide. The agents' code and your
apps don't run where AppTown keeps its records. It takes more setup:
[Split install](/docs/guides/split-install).

### Which one

Both server installs keep the same records and run the same workflows;
they differ in which machines run your projects' code. Start with one server if you are one person or a small team that trusts
everyone who writes projects. Use a split install if a misbehaving agent or
app must not reach AppTown's records, or once other people depend on the
apps. Choose before the first project if you can: moving later is in [Split
install](/docs/guides/split-install).

## Looking after it

- [Deployment targets](/docs/guides/deployment-targets): where apps run, how
  full each target is, disk space, logs and diagnostics.
- [People, invites, roles and grants](/docs/guides/people-and-roles).
- [AppTown's own backups](/docs/guides/apptown-backups): its database and
  data directory, restoring, moving and recovering AppTown.
- [Deployment backups](/docs/guides/deployment-backups): where apps' backups
  are kept, and App Platform databases.
- [Upgrading](/docs/guides/upgrading), rolling back, and which versions work
  together.
- [Packs, versions and what-if](/docs/guides/packs-and-what-if).
- [Monitoring and integrations](/docs/guides/monitoring).
