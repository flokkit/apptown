# Running AppTown

This page is for the person who installs and looks after AppTown. Building
apps on it is in [Start here](/docs/guides/quickstart); how the parts fit
together is in [How AppTown works](/docs/guides/architecture); what it runs
on is in [Supported platforms and system requirements](/docs/guides/platforms).

`apptownd -h` lists every start-up flag. Each flag can also be set as an
environment variable: `-db-url` is `APPTOWN_DB_URL`, `-tier-domain` is
`APPTOWN_TIER_DOMAIN` (comma-separated), and a flag on the command line wins.
These are what AppTown needs to start and what only the machine's owner
should decide: addresses, sign-in, the database, the worker, Docker and
limits. What an administrator changes while AppTown runs is set in AppTown
itself and needs no restart: the default model and model providers (Home and
Integrations), package mirrors and other integrations, and deployment targets
and their settings (Infrastructure).

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

You need:

- A Linux machine with at least 4 CPUs and 8 GB of memory (16 GB is
  better; [Supported platforms](/docs/guides/platforms) has the sizes). On
  a smaller machine AppTown still runs, and each step gets at most the
  CPUs the machine has, so work is slower.
- Docker with Compose 2.23 or later. On a fresh server, Docker's own
  script installs both: `curl -fsSL https://get.docker.com | sh`
  ([Docker's install guide](https://docs.docker.com/engine/install/) has
  the other ways).
- A domain name. For a trial without one, use the server's address under
  `sslip.io`: for a server at `203.0.113.7`, the domain
  `203-0-113-7.sslip.io` and every name under it already point at it,
  and certificates work. Skip step 1 then.
- Ports 80 and 443 open to the internet. AppTown's own port stays on
  `127.0.0.1`. Where a firewall is on, open them, for example with
  `ufw allow 80,443/tcp`.

1. Point your domain, `*.preview.` your domain and `*.main.` your domain at
   the server (three DNS records).
2. As root, or a member of the `docker` group:

   ```sh
   mkdir -p /opt/apptown && cd /opt/apptown
   curl -fsSLO https://apptown.dev/compose.yaml
   echo APPTOWN_DOMAIN=apptown.example.com > .env
   docker compose up -d
   ```

3. Open `https://apptown.example.com`, create the administrator with the
   setup token from `sudo cat /var/lib/apptown/setup-token`, then add an
   Anthropic or OpenRouter key on the Home page. Until a key is added,
   AppTown refuses work that needs a model and says where to add one.
   Without a browser: First administrator without a browser in [Setting
   up a server](/docs/guides/server-setup).

The compose file runs AppTown with Caddy in front, which gets each HTTPS
certificate itself. To run `apptownd` under systemd from the release
archive instead, or behind a proxy you already have, see [Setting up a
server](/docs/guides/server-setup), which also says what one server does and
does not protect.
To check that the image and archives you download are the ones we released,
see Check a release in [Setting up a server](/docs/guides/server-setup).

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
