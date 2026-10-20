# Flokkit AppTown

A software factory for the rest of us.

AppTown helps small teams keep the apps they make with AI coding agents. You
run it yourself, on your own machine or server.

Coding agents have dropped the cost of small custom apps to almost nothing,
but owning an app that people can rely on is still a lot of work. Someone has
to deploy it, back it up, keep it patched and still understand it a year
later, usually after the person who made it has moved on.

AppTown makes that handover boring and repeatable. You can bring in an app you
made with a coding agent on your own computer, or have AppTown's own agents
build it, and AppTown looks after it from there. Every app gets the habits of
a careful engineering team: AppTown runs the tests itself instead of taking
the agent's word for it, a person looks at each change before it goes live,
you can try it on a preview first, and there's a record of who asked for what
and what's running now.

Those habits come from a way of working. It's a team's engineering
conventions written down, such as the language and framework, the linter, the
checks that run on every change, and the guidance the agents follow. A team
can have several, say one for Python web apps and one for static sites. Each
project adopts one, and takes up newer versions when someone on the project
accepts them.

One or more admins run AppTown for everyone else. They set it up and keep it
going. They invite people, connect the places apps get deployed to (a Docker
host, a Portainer environment or DigitalOcean), plug in the AI model provider,
and choose the way of working new projects start with. They don't approve
other people's changes. Each project's own people do that.

What comes out is ordinary software: a git repository and a container image
you can take somewhere else. It's open source (Apache-2.0).

It's a 1.0 release candidate. Release notes are in [releases/](releases/).

## What it's good for

Primarily, web applications: the ordinary software a team runs for itself,
such as an internal tool, a form, a dashboard, a small API or a static site. The ways of
working it ships are proven for Go, Django, Rails and plain front-end
projects. Java, Node, .NET, Rust, WordPress and games work, but are less
proven.

Apps run on a Docker host. Roughly, if you can express it in a Compose stack,
you can ship it in AppTown. The difference is that you don't run your own
database or storage containers: the app says it needs Postgres, MariaDB or S3,
and AppTown provides them and backs them up. Portainer, DigitalOcean App
Platform and static bucket websites also work, but haven't yet released a
real app in our testing.

It isn't built for mobile apps, for large systems with many services and
teams, or for hosting software for paying customers at scale. It isn't
low-code, and it doesn't replace your Git host or ticket tracker.

## Reading about it

- [What it is for](stock/docs/guides/what-apptown-is-for.md): the problems it
  addresses and what it is not.
- [How it works](stock/docs/guides/architecture.md): the pieces, as built.
- [What 1.0 promises](stock/docs/guides/compatibility.md), and
  [what is not in 1.0](stock/docs/guides/compatibility.md#not-in-10).
- [Running one](stock/docs/guides/running-apptown.md): installing and
  operating AppTown, including model-backed agents.
- [Supported platforms and system requirements](stock/docs/guides/platforms.md).
- [The tour](https://apptown.dev/tour/): AppTown's main pages, with
  screenshots (the pictures are in [site/tour](site/tour)).

## What is here

Project repositories, Changes and versioned workflow runs, with reviews,
approvals, conformance checks, deployments and persistent agent consoles.
The web UI, HTTP API, CLI and MCP tools call the same application owners.
PostgreSQL stores the records; the Go daemon embeds the web frontend.

For development, start with the [current package map](design/architecture/domains.md)
and [code navigation](internal/forge/README.md). The [documentation index](design/README.md)
distinguishes current guides from accepted intent. Authentication and resource
access are checked separately; an actor name is not permission.

## Desktop trial

To try AppTown on your own computer you need Docker (Docker Desktop on a
Mac, or Docker Engine on Linux) and a model-provider key (Anthropic or
OpenRouter). Run:

```sh
docker run --name apptown --privileged -p 127.0.0.1:7777:7777 -v apptown:/var/lib/apptown ghcr.io/flokkit/apptown:<version>
```

and open the link it prints when it says `AppTown is ready`. The first start
takes a few minutes. Create the administrator (the link fills in the
one-time setup token), give your model key, and start a project from the
dashboard. AppTown and your apps are served over HTTPS at
`https://localhost:7777` and `https://*.preview.localhost:7777`, from a
certificate authority the trial makes for itself that can vouch only for
`localhost` names. Your browser warns about these addresses until you trust
it once: [Running AppTown](stock/docs/guides/running-apptown.md) says how
("Trust the trial's certificate"). What it runs on is in
[Supported platforms and system requirements](stock/docs/guides/platforms.md).

**This is for trying AppTown on your own computer.** It has full control of
your Docker (`--privileged` is root on a Linux machine, and root in Docker
Desktop's VM on a Mac), and the agents and apps it runs are containers on
this machine. Only port 7777 is published, on this computer's own address.
The link it prints carries the setup token, so that token is in the
container's log until you create the administrator. For a server, or
anything other people use, follow
[Running AppTown](stock/docs/guides/running-apptown.md).

Stop it with Ctrl-C (or `docker stop apptown`); `docker start -a apptown`
starts it again with everything kept. To remove it and everything it made:

```sh
docker rm -f apptown
docker volume rm apptown
```

If something looks stuck:

- `docker logs apptown` is AppTown's own log.
- `docker exec apptown docker ps` lists what it started. AppTown runs its
  own Docker inside its container, so these containers do not show in
  Docker Desktop or in your own `docker ps`.

A hosted demo is available by invitation from the maintainers.

## Installing on a server

There are two ways to install AppTown on a server.

A single-server install puts AppTown, its agents and its apps on one
machine. It is quick to set up and suits a small team that trusts everyone
who writes projects. The agents' code runs in containers on the machine
that holds AppTown's records, and a container is not a strong sandbox.

A split install puts AppTown on its own machine, the agents on a
separate worker, and the apps on separate Docker hosts or a cloud. The
agents' code and your apps don't run where AppTown keeps its records. It
takes more setup: a worker with its own certificates, a PostgreSQL
database, a preview domain and a container registry.

[Running AppTown](stock/docs/guides/running-apptown.md) covers both, and
which one to choose.

## From source

You need Go (the version in [go.mod](go.mod)), Node.js 24 with npm, Git, and a
running Docker (AppTown starts its own PostgreSQL in a container).

```sh
git clone https://github.com/flokkit/apptown.git
cd apptown
npm ci --prefix web   # the web interface's dependencies (make build also does this when they are missing)
make build            # web/dist, then bin/apptownd, bin/apptown, bin/apptown-worker and bin/apptown-linux
./bin/apptownd -addr 127.0.0.1:7777 -console-addr '' -admins trial
```

Then open <http://127.0.0.1:7777>. The data directory is `./_data` unless
`-data` names another.

This is a web-only trial in the default `open` authentication mode: the browser
says who it is (`trial`, until you change the name), and `-admins trial` makes
that name an administrator. Open mode trusts that name, so keep it on
`127.0.0.1` and never expose it; use `-auth internal` for anything shared.
Chats and agent workflows also need a model provider, a private inference
listener, container network settings and an agent image: follow Install in
[Running AppTown](stock/docs/guides/running-apptown.md). It also covers running
work on a separate worker machine and running AppTown with no local Docker
(`-no-local-docker`).

The daemon mounts `bin/apptown-linux` into agent and console containers. `make build`
and `make go` build this artifact; runtime source compilation is not required.
It defaults to `apptown-linux` beside the daemon executable. For another location,
set `-console-cli /absolute/path/apptown-linux` (or `APPTOWN_CONSOLE_CLI`). A missing
artifact is reported and sessions continue without the CLI. `make dev-api` builds
and supplies the artifact explicitly for `go run`.

## Security and contributing

- [SECURITY.md](SECURITY.md): how to report a vulnerability privately, and the
  trust model in short.
- [CONTRIBUTING.md](CONTRIBUTING.md): issues are welcome; pull requests by
  arrangement.

## Development

```sh
make dev-api      # go run backend on 127.0.0.1:7777
make dev-web      # vite dev server on :5173 with /api proxied
```

Run `make tools` to install the check tools, including the pinned sqlc version,
then `make check`. Each owner declares its schema fragment; `internal/forge/dbschema/schema.sql`
is the generated bundle embedded by the daemon. See [schema ownership](design/architecture/schema-ownership.md). Named queries live under each subsystem's `internal/*sql`.
After editing a query or schema, run `make generate-sql` and commit the generated
Go alongside the SQL. Local checks and CI use `sqlc diff` to reject stale output.
See [schema ownership](design/architecture/schema-ownership.md).

## Layout

- `cmd/apptownd` — entrypoint (`apptownd [flags]`)
- `internal/<x>` — libraries that know nothing of forge (`console`, `deploy`, `database`, ...)
- `internal/forge/<x>` — forge's subsystems (`workflow`, `deployment`, `identity`, ...);
  see `design/architecture/domains.md`
- `internal/forge` — the App: composes the subsystems, serves the HTTP API
- `web` — React/TS frontend (Vite), embedded into the binary from `web/dist`
- `design` — architecture and workflow spec documents

## Licence

Apache License 2.0; see [LICENSE](LICENSE) and [NOTICE](NOTICE). The code is
free to use, change and run, commercially too. The names "Flokkit" and
"AppTown" are not part of the licence: a fork may use the code, not the names.
