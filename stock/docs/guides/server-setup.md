<!-- A word like apptown:origin in braces is a live link: the Docs page of an
AppTown shows that AppTown's own address there. -->

# Setting up a server

This page has the detail behind the one-server install in [Running
AppTown](/docs/guides/running-apptown): what the machine needs, model access,
sign-in, the public address, running under systemd instead of the compose
file, and putting your own proxy in front. A [split
install](/docs/guides/split-install) starts with the same steps, with the
differences given in each.

## What one server protects

AppTown, its agents and its apps all run on one machine, using that
machine's Docker. AppTown starts its own PostgreSQL container there too.
It is the quickest install: the four steps below, on one machine.

It suits one person, or a small team whose projects are all written by
people trusted as much as the machine itself. The usual case is a team
trying AppTown on real work, or running a few internal apps.

Each agent, step and preview runs in its own container with CPU, memory
and process limits. Disk is not limited per container, so one piece of
work can still fill the machine's disk (Disk space in
[Deployment targets](/docs/guides/deployment-targets)). Apps are
published on the machine's loopback address only. None of that protects
AppTown's records from a project's code. On Linux those containers run as
root and can reach the network, and a container is not a strong sandbox.
Code that gets out of one is on the machine that holds AppTown's database,
its secrets file and its keys.

A production install then adds deployment targets, sign-in, backups and
upgrades (each has its guide, listed in [Running
AppTown](/docs/guides/running-apptown)).

## The compose file

`https://apptown.dev/compose.yaml` runs two containers on the host's
network: AppTown, from the `ghcr.io/flokkit/apptown` image of one release,
and Caddy. AppTown uses the host's Docker through its socket and keeps its
data in `/var/lib/apptown` (`APPTOWN_DATA` in `.env` names another folder);
it refuses to start unless it is on the host's network with that folder at
the same path inside and out, because the containers it starts mount its
folders by path. It listens on `127.0.0.1:7777`, signs people in with its
own accounts, and gives its containers model access and the chat API on
Docker's bridge address only. Any `apptownd` setting can go in `.env` as its
`APPTOWN_` variable.

Caddy serves every name on ports 80 and 443 and gets a certificate for each
name the first time it is asked for: your domain, and each preview's and
app's name under `preview.` and `main.`. Before each one it asks AppTown
whether the name is one of its own, so made-up names get none. Each new
preview is a new certificate; Caddy's issuers (Let's Encrypt, then ZeroSSL)
allow far more than a small team's previews.

## Under systemd instead

The release archive runs `apptownd` as a systemd service on the host.
You need Docker running, Git, a model-provider account, and AppTown
binaries. Copy `deploy/systemd/apptownd.service` to `/etc/systemd/system/`
and `deploy/systemd/apptownd.env.example` to `/etc/apptown/apptownd.env`, set
`APPTOWN_PUBLIC_HOST` and `APPTOWN_AGENT_IMAGE` there, `systemctl enable --now
apptownd`, and put a proxy in front of `127.0.0.1:7777` (Your own
proxy, below).

The release archive for your platform unpacks to `/opt/apptown`. Each
release on [GitHub](https://github.com/flokkit/apptown/releases) has
`apptown-<version>-<os>-<arch>.tar.gz` for `linux-amd64`, `linux-arm64`,
`darwin-arm64` and `darwin-amd64`, and `SHA256SUMS`. For 1.0.0 on an x86
Linux server:

```sh
v=1.0.0 a=apptown-1.0.0-linux-amd64.tar.gz
curl -fLO https://github.com/flokkit/apptown/releases/download/v$v/$a
curl -fLO https://github.com/flokkit/apptown/releases/download/v$v/SHA256SUMS
sha256sum --ignore-missing -c SHA256SUMS
mkdir -p /opt/apptown && tar -xzf $a --strip-components=1 -C /opt/apptown
```

`SHA256SUMS.sigstore.json` beside it is a keyless signature of the sums by
the release workflow, for that tag:

```sh
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity "https://github.com/flokkit/apptown/.github/workflows/images.yml@refs/tags/v$v" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
```

The archive holds `bin/apptownd`, `bin/apptown`,
`bin/apptown-worker`, `bin/apptown-linux` (the CLI AppTown gives its
containers), `deploy/apptown-agent` (the agent image's recipe), example
systemd units in `deploy/systemd` and `scripts/apptown-db-backup.sh` (the
checked backup [upgrading](/docs/guides/upgrading) runs). The unit runs `apptownd` in `/opt/apptown`,
where it finds `bin/apptown-linux`; without it, chats and steps run without
the `apptown` CLI (`-console-cli` names another path).

**The agent image is pulled, not built.** `apptownd.env.example` sets
`APPTOWN_AGENT_IMAGE=ghcr.io/flokkit/apptown-agent:<version>`; with an image
named in a registry, AppTown pulls it when it is missing. A failed pull is
recorded in `<data directory>/agent-image-build.log`. Workers build their own,
`apptown-agent:<recipe hash>`, when they start. Building from the repository
is in the repository's `CONTRIBUTING.md`.

On Linux, run `apptownd` as root when it runs containers on its own Docker:
steps and chats run as root in containers that write into AppTown's
folders, and AppTown must read and remove what they leave. An ordinary
user in the `docker` group cannot, and its workspaces are never cleaned
up. Docker Desktop on a Mac gives such files your ownership instead.

The data directory is `./_data` unless `-data` names another. With no
`-db-url`, AppTown starts its own PostgreSQL container, `apptown-db` (`db.env`
records its name; an install made before 1.0 keeps `forge-db`), with its
files under `<data directory>/pgdata`. That container belongs to one
installation: for a second AppTown on the same machine, give it a separate
PostgreSQL database with `-db-url`; changing only `-data` is refused.

On a split install, AppTown's machine runs no Docker daemon. It needs Git,
an `ssh` client and the `docker` command (only to reach attached Docker
hosts), and a PostgreSQL database named with `-db-url`. Skip the agent
image and running as root there: the worker builds its own agent image,
and runs as root itself.

## Configure model access

Agents reach models through a private listener on AppTown, separate from
the web address. On one Linux server it needs no settings: AppTown listens
on Docker's bridge address (docker0) port 7779, which only this machine and
its containers reach, and allows the networks Docker gives containers here
(its bridges and its default address pools, less any that hold this
machine's own LAN). Its start-up log says which address it chose.

Set all three yourself when the defaults do not fit: on a split install,
when Docker's `daemon.json` sets its own `default-address-pools` or
`host-gateway-ip`, or when AppTown runs on a Mac. Setting any one of them
turns the defaults off.

| Environment variable | Meaning |
| --- | --- |
| `APPTOWN_INFERENCE_ADDR` | The private address and port AppTown listens on, for example a Docker bridge address on port 7779 |
| `APPTOWN_INFERENCE_URL` | The address containers use to reach it, including the port |
| `APPTOWN_INFERENCE_NETWORKS` | Comma-separated CIDRs of the addresses those containers arrive from, as AppTown sees them |

On Linux, `docker network inspect bridge` shows the bridge; chat and step
networks may use other subnets. On Docker Desktop, `host.docker.internal`
reaches the host, but connections may arrive from a shared address. Allow
only the networks your containers use; a `/0` network is refused. Keep this
listener off public ingress. Each call also needs a live credential AppTown
gave that step or chat. Provider keys stay on AppTown. AppTown sets no
spending limit, so set one with your model provider.

AppTown also opens a read-only API for chats on port 7778
(`-console-addr`). Containers must reach it; it is not the public web
address. If the start-up log says `inference disabled`, model agents cannot
run: stop AppTown and fix the three settings. For a look at the web
interface only, add `-console-addr ''`; that AppTown cannot run agents
unless model access is configured.

On a split install the containers that use these listeners are on the
worker. Bind both listeners to an address the worker can reach and the
internet cannot, set `-console-url` to the console listener's address as
the worker sees it, and name in `APPTOWN_INFERENCE_NETWORKS` the addresses
the worker's containers arrive from. The section on addresses in
`design/operations/workers.md` says which addresses those were in testing.

## Add a provider and choose sign-in

Open AppTown's address. A fresh AppTown asks for an Anthropic or
OpenRouter key. Saving it writes `<data directory>/secrets.env` (mode 0600)
and selects a default model. Saving a key does not test it: try a short chat
first. Model choice per job is on the [model providers and
slots](/docs/model/pi) integration's Admin tab.

The default sign-in mode, `-auth open`, trusts whatever name a caller gives,
so in open mode `apptownd` refuses to start on an address another machine can
reach (the main or the console listener): bind both to `127.0.0.1`, or pass
`-open-on-network` to accept that anyone who reaches the port has full
control. Before sharing AppTown, start it with `-auth internal` (AppTown's own
accounts and passwords; `-admins` names accounts to make administrators) or
`-auth trusted-header` behind an authenticating proxy.

Under `-auth internal`, the first account is the administrator, and making it
needs the setup token: while no account exists, AppTown writes a random
token to `setup-token` in its data directory (readable only by the account
`apptownd` runs as) and says so in its log. The sign-in page asks for it. Once
the first account exists the file is removed. Reading a file on the host is
the proof of being its operator, so a stranger who finds the sign-in page
first cannot claim AppTown.

### First administrator without a browser

An install script or a coding agent can make the first administrator, a
command-line token and the model key through the API, with the setup token
read on the host (`DOMAIN` is AppTown's address):

```sh
curl -fsS -c cookies -H 'Content-Type: application/json' https://DOMAIN/api/v1/auth/bootstrap \
  -d '{"username":"you","password":"at least 8 characters","setupToken":"<from setup-token>"}'
curl -fsS -b cookies -H 'Content-Type: application/json' https://DOMAIN/api/v1/tokens \
  -d '{"name":"laptop-cli","purpose":"cli"}'
curl -fsS -b cookies -H 'Content-Type: application/json' https://DOMAIN/api/v1/setup/model \
  -d '{"provider":"anthropic","key":"<your API key>"}'
```

The first call signs you in (the cookie file holds the session); the
second prints a command-line token with your role, shown once, for
`APPTOWN_TOKEN`; the third is the Home page's key form (`openrouter` is the
other provider). Remove the cookie file afterwards.

The doors that take a secret (sign-in, the setup token, invites, OAuth codes
and refresh tokens) allow ten quick attempts from one address, then one every
six seconds; past that they answer 429 with `Retry-After`.

## Give it its public address

Start AppTown with `-public-host` set to the origin people reach it by,
scheme included (for example `https://forge.example.com`). Addresses AppTown hands out are built on it, such as the one coding agents sign in to.
On this AppTown, coding agents connect at {apptown:mcp} and the `apptown` CLI uses
`APPTOWN_URL={apptown:origin}`. Sign-in, invite and password reset links, and
coding agents' sign-in all use this one address. A bare name
(`forge.example.com`) is still accepted, but it does not say whether AppTown is reached over https: AppTown then counts as https only when it
serves TLS itself or a `-tier-domain` is https, and the guides say "your
AppTown's address" instead of guessing. Behind a proxy that ends TLS, state
the origin, or browsers cannot sign in.

## Your own proxy

Behind a proxy you already run instead of the compose file's Caddy, the
proxy must:

- serve your domain and every name under `preview.` and `main.` of it, over
  HTTPS, and send them all to AppTown (`127.0.0.1:7777`): AppTown tells
  apps apart by name;
- pass the `Host` header unchanged;
- set `X-Forwarded-For` and `X-Forwarded-Proto: https`;
- allow WebSocket upgrades and responses that stream for many minutes
  (agents' chats and logs);
- if it runs on another machine, be named in `-trusted-proxies`, so AppTown
  believes its `X-Forwarded-For`.

A wildcard certificate for the two `*.` names needs DNS validation. Without
one, a proxy that can get certificates on demand can ask AppTown first:
`GET http://127.0.0.1:7777/tls/ask?domain=<name>` answers 200 for
AppTown's own names and 404 for any other, and only to a caller on the same
machine. Keep `/tls/ask` off the public address, as the compose file's
Caddy does.
