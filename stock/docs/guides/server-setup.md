<!-- A word like apptown:origin in braces is a live link: the Docs page of an
AppTown shows that AppTown's own address there. -->

# Setting up a server

This page has the detail behind the one-server install in [Running
AppTown](/docs/guides/running-apptown): what the machine needs, model access,
sign-in and the public address. A [split install](/docs/guides/split-install)
starts with the same steps, with the differences given in each.

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

Either way, start AppTown, configure model access, then add a model
provider. Starting the web server alone does not enable model agents. A
production install then adds deployment targets, sign-in, backups and
upgrades (each has its guide, listed in [Running
AppTown](/docs/guides/running-apptown)).

## Prepare the host

You need Docker running, Git, a model-provider account, and AppTown
binaries.

The release archive for your platform (its checksum is in `SHA256SUMS`)
unpacks to `/opt/apptown`. It holds `bin/apptownd`, `bin/apptown`,
`bin/apptown-worker`, `bin/apptown-linux` (the CLI AppTown gives its
containers), `deploy/apptown-agent` (the agent image's recipe) and example
systemd units in `deploy/systemd`. The unit runs `apptownd` in `/opt/apptown`,
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
`-db-url`, AppTown starts its own PostgreSQL container, `forge-db`, with its
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
the web address. Set all three:

| Environment variable | Meaning |
| --- | --- |
| `APPTOWN_INFERENCE_ADDR` | The private address and port AppTown listens on, for example a Docker bridge address on port 7779 |
| `APPTOWN_INFERENCE_URL` | The address containers use to reach it, including the port |
| `APPTOWN_INFERENCE_NETWORKS` | Comma-separated CIDRs of the addresses those containers arrive from, as AppTown sees them |

The values depend on Docker and host networking. On Linux, inspect the
bridge with `docker network inspect bridge`; chat and step networks may use
other subnets. On Docker Desktop, `host.docker.internal` reaches the host,
but connections may arrive from a shared address. Allow only the networks
your containers use; a `/0` network is refused. Keep this listener off
public ingress. Each call also needs a live credential AppTown gave that
step or chat. Provider keys stay on AppTown. AppTown sets no spending
limit, so set one with your model provider.

Then start:

```sh
: "${APPTOWN_INFERENCE_ADDR:?set the private listener address}"
: "${APPTOWN_INFERENCE_URL:?set the container-reachable origin}"
: "${APPTOWN_INFERENCE_NETWORKS:?set the allowed container CIDRs}"
./bin/apptownd -addr 127.0.0.1:7777
```

AppTown also opens a read-only API for chats on port 7778
(`-console-addr`). Containers must reach it; it is not the public web
address. If the start-up log says `inference disabled`, model agents cannot
run: stop AppTown and fix the three settings. For a look at the web
interface only, leave them unset and add `-console-addr ''`; that AppTown
cannot run agents.

On a split install the containers that use these listeners are on the
worker. Bind both listeners to an address the worker can reach and the
internet cannot, set `-console-url` to the console listener's address as
the worker sees it, and name in `APPTOWN_INFERENCE_NETWORKS` the addresses
the worker's containers arrive from. The section on addresses in
`design/operations/workers.md` says which addresses those were in testing.

## Add a provider and choose sign-in

Open http://localhost:7777. A fresh AppTown asks for an Anthropic or
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
