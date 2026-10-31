# AppTown setups

| Setup | Who it suits | Machines it needs | Where apps get ingress | Guide |
| --- | --- | --- | --- | --- |
| Desktop trial | One person trying AppTown | Your computer with Docker | AppTown's local HTTPS listener at `…localhost:7777` | [The desktop trial](trial.md), then [Start here](quickstart.md) |
| One server with Caddy (`deploy/one-server`) | One person or a small team that trusts everyone writing projects | One Linux server with Docker | Public Caddy → AppTown → apps on the same server | [Running AppTown](running-apptown.md), [Setting up a server](server-setup.md) |
| Split install with Docker | Teams keeping project code off AppTown's machine | AppTown container, separate Docker worker, external PostgreSQL and an app host; a container registry for releases | App host's own ingress; previews through AppTown to the worker | [Split install](split-install.md) |
| Native install | Operators running the release binaries directly | Linux server with Docker, or the split install's machines; systemd units are supplied | Your proxy → AppTown for built-in apps and previews; attached targets have their own ingress | [Setting up a server](server-setup.md) |
| AppTown with a worker | Teams moving steps, checks, change chats and previews to another machine | AppTown and a Docker worker; a container registry for releases | AppTown proxies worker previews; each deployment uses its target's ingress | [Split install](split-install.md), “Workers and running without local Docker” |
| Built-in Docker target | Apps sharing AppTown's host | AppTown's Docker host | AppTown's hostname routing; published ports bind to loopback | [Deployment targets](deployment-targets.md) |
| Attached Docker target with Traefik | Production apps with their own public ingress | Separate Docker host reached over SSH, with Traefik and app DNS | Host's Traefik for `main`; every deployment, `main` included, also publishes its port on all interfaces | [Deployment targets](deployment-targets.md) |
| Attached Docker target without a domain | Apps reached by address and port | Separate Docker host reached over SSH | Host's published ports, on all interfaces; its firewall controls access | [Deployment targets](deployment-targets.md) |
| Portainer target (experimental) | Teams with a standalone Docker endpoint in Portainer | Portainer and its verified Docker endpoint | Target's Traefik, using each deployment's admitted hostname | [Deployment targets](deployment-targets.md) |
| DigitalOcean App Platform target (experimental) | Apps using the platform's supported services | AppTown plus the cloud account and container registry | App Platform's own app address | [Deployment targets](deployment-targets.md) |
| Bucket website target (experimental) | Static sites with no runtime or services | AppTown plus an S3-compatible website store | Store's website address | [Deployment targets](deployment-targets.md) |

Choose an install first, then the targets where its apps run. The
[platforms guide](platforms.md) lists supported systems and machine sizes.
Use the trial to try AppTown on your computer. For production, use the
setup below.

## Recommended setup

Keep AppTown on the tailnet and put production apps on a separate Docker
host with its own public ingress, or on a cloud target. App traffic then
never passes through `apptownd`, and people using the apps do not need
access to AppTown. Previews stay on AppTown's built-in host and are
reachable only through the tailnet.

```text
Team on tailnet ── tailscale serve ── 127.0.0.1:7777 AppTown
Team on tailnet ── private preview proxy (Tailscale IP:443) ── 127.0.0.1:7777 AppTown ── built-in previews

Public visitors ── production host's ingress ── production apps
                                             (separate Docker or cloud target)
```

Install the native release as in [Setting up a server](server-setup.md).
Keep local Docker enabled and leave `-worker-url` unset: a worker moves
previews to the worker. This setup keeps agents on AppTown's host too.
For code you do not trust, use the [split install](split-install.md).
This recommendation combines the native install with the same attached
app host used by the split install.

Use these settings, replacing the example names and Docker bridge address
with yours. Each flag also has its `APPTOWN_` environment variable.

| Setting | Value |
| --- | --- |
| Web listener | `-addr 127.0.0.1:7777` |
| AppTown address | `-public-host https://apptown.<tailnet-name>.ts.net` |
| Sign-in | `-auth internal` |
| Console listener | `-console-addr 172.17.0.1:7778` |
| Console address for containers | `-console-url http://172.17.0.1:7778` |
| Inference | Leave the three inference settings unset; on one server their defaults use the Docker bridge ([Setting up a server](server-setup.md)) |
| Preview hostnames | `-tier-domain preview=https://preview.example.com` |
| Private preview DNS | `*.preview.example.com` → AppTown's Tailscale IP |
| Preview proxy | Bind to AppTown's Tailscale IP on port 443; forward to `127.0.0.1:7777`, preserving `Host` |
| Preview certificate | Wildcard certificate for `*.preview.example.com`, obtained through DNS validation |
| Built-in production routing | Leave `-tier-domain main=…` unset |

The console and inference addresses must be private and reachable from
AppTown's containers. The console default, `:7778`, listens on all
interfaces; bind it to a private address as above or disable it with
`-console-addr ''` (and omit `-console-url`). Do not forward either listener
through an internet proxy or publish their ports on a router.

Join AppTown's machine to Tailscale, enable tailnet HTTPS, and run:

```sh
sudo tailscale serve --bg http://127.0.0.1:7777
tailscale serve status
```

`tailscale serve` carries AppTown's setup, token use and Git over the tailnet name.
The private preview proxy works over HTTP. Preview HTTPS with a wildcard
certificate and the restore-test route have not been tried with AppTown.
Attaching the production host by its tailnet name and the firewall
arrangement below have not been tried either.

[Tailscale Serve](https://tailscale.com/docs/reference/tailscale-cli/serve)
provides HTTPS at the machine's tailnet name. Keep it restricted to the
team with tailnet access rules. Do not enable Funnel or put a public proxy
in front of AppTown.

Previews require their own private hostname routing. A landing's runtime
checks reach the change's preview at its preview address. Without a preview
domain and proxy in this setup, every landing stops at the
guidance-conformance gate. Point
`*.preview.example.com` at AppTown's Tailscale IP in private DNS. Configure
your preview proxy with a wildcard certificate obtained through DNS
validation, bind it only to that IP on port 443, and send requests to `127.0.0.1:7777`, preserving
`Host`. The other proxy requirements are in [Setting up a
server](server-setup.md), “Your own proxy”. Serve at the single tailnet
name does not create wildcard preview DNS or certificates. Built-in app
ports stay on loopback. If you use a certificate-on-demand proxy for
previews, keep `/tls/ask` on loopback: it answers only callers on the same
machine, never on the tailnet or public address.

Attach the production Docker host in **Infrastructure** and offer it to
the project. Configure its domain and Traefik for `main`: the adapter uses
the `traefik` network, the `websecure` entrypoint and the `letsencrypt`
certificate resolver. Before attaching the host, create the network
(`docker network create traefik`) and run Traefik 3.6 or later on it (Docker 29 refuses the
Docker client in Traefik 3.5),
publishing ports 80 and 443. Configure:

- `--providers.docker=true` and `--providers.docker.exposedbydefault=false`.
- `--providers.docker.network=traefik`.
- An entrypoint `web` on `:80` that redirects to `websecure`.
- An entrypoint `websecure` on `:443`.
- A certificate resolver `letsencrypt` with an ACME email, storage and the
  HTTP challenge on `web`.

Give the target its domain when attaching it (`"domain": "apps.example.com"`).
`main` is served at `<project>.apps.example.com`; point `*.apps.example.com`
at the production host's public address.

Every deployment, `main` included, also publishes its container port on
all of the host's interfaces; AppTown checks the app through that port.
Join the production host to the tailnet and attach it by its tailnet name,
so AppTown's SSH connection and app checks travel over the tailnet. Then
give the host a provider firewall (for example a DigitalOcean cloud
firewall) that admits only TCP 80 and 443 from the internet; Docker's
published ports bypass the host's own `ufw`/`INPUT` rules. Tailnet traffic
arrives inside Tailscale's tunnel and is unaffected. Without that, each app
is also reachable over plain HTTP at
the host's public address and port. Other
deployment names have no Traefik hostname route; give them their own ingress
to the published port.

Create the project's `main` deployment on the attached target before its
first landing; with local Docker enabled, a landing otherwise creates
the missing `main` on the built-in host. Cloud targets receive promotions
from a Docker `main`; Portainer and App Platform remain experimental. Target setup and
credentials are in [Deployment targets](deployment-targets.md).

Restore tests need `-restore-gate-image` pinned by digest and a stated
`-public-host` origin. The target's gate container must reach `/gate/redeem`
at that origin, which here uses the tailnet name. Put that target on the
tailnet, including the route and DNS its gate container uses, or give it
one narrow route to that endpoint.
An isolated target with neither cannot run the restore test. See
[Deployment backups](deployment-backups.md) for the gate image and other
restore-test settings.
