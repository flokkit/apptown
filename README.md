# Flokkit AppTown

AppTown gives your team a ready-made place to build small web apps with AI
coding agents, in the browser or from a desktop agent via MCP. Each app
follows a way of working you choose, with consistent checks and deployment.
That makes a growing collection of apps easier to maintain and change. You
run AppTown yourself, on your own computer or server.

**1.0 release candidate** · [Tour](https://apptown.dev/tour/) · [Install on a server](#install-on-a-server) · [Release notes](releases/) · [Apache-2.0](LICENSE)

## Why

AppTown started at home, vibe coding games with the kids. Agents could
quickly bring ideas to life, but getting the games online was frustrating.
"Why can't my friend see it?" "Now I want it to be multiplayer!" "What's Git?"
There were deployment credentials, permissions, hosting and DNS to set up.
Once the games were running, keeping them secure and up to date became
another job.

Teams face the same problem with internal tools. An app might take an
afternoon to build, but someone still has to look after it months later,
often after the person who made it has moved on.

Shared conventions and a record of what changed help the next person
understand the app and keep it useful.

## What you get

**For the people using it**

- **People who don't use a coding agent** work entirely in the browser. They
  describe what they want, an agent makes it in a container on your server
  using the model key you provide. They open a preview and try it. Nothing
  to install, no API keys, and no deployment tools to configure.
- **People who already use a coding agent** keep using their own. They
  connect it to AppTown and push their work as a change. AppTown then checks
  that work itself: it runs the tests and checks that the change follows
  your project's rules. They can bring in existing apps too.
- In a team, you decide who approves changes and who can release them.

**For you, keeping it all running**

- You choose the language, framework, checks and agent guidance as a way of
  working. You can have several, such as one for Python web apps and one for
  static sites. Each project adopts one, so people working on similar apps
  share the same conventions.
- AppTown manages HTTPS, databases and object storage, with backups on
  supported deployment targets.
- Library updates and way-of-working updates come to you as changes that are
  already built and tested. Someone on the project accepts them before they
  take effect.
- There's a record of who asked for what, what was checked, who approved it
  and what's running now.
- It's ordinary software underneath: a Git repository and a container image
  you can take anywhere.

## Try it now

On an Apple Silicon Mac with Docker Desktop, or Linux with Docker Engine:

```sh
curl -fsSL https://apptown.dev/try | sh
```

The first start takes a few minutes, then AppTown opens in your browser.
Create the administrator, add an Anthropic or OpenRouter API key on the Home
page, and start a project. Give Docker 4 CPUs, at least 8 GB of memory and
30 GB of free disk. You can [read the script](site/try.sh) first.

The trial is for your own computer: it has full control of your Docker.
[The desktop trial](stock/docs/guides/trial.md) explains what it does, and
how to stop or remove it. To run it for a team, see
[Install on a server](#install-on-a-server).

![A change in AppTown: the checks that ran, the one that failed, and the approval it is waiting for](https://apptown.dev/tour/change-light.png)

More screenshots are in [the tour](https://apptown.dev/tour/).

## What it's good for

Small web apps a team or household runs for itself: internal tools,
dashboards, small APIs, static sites and browser games.

It works best with Go, Django, Rails and plain web front ends, deployed to a
Docker host. Other languages and deploy targets work too, but are less
tested: see [toolchains](stock/docs/guides/toolchains.md) and
[supported platforms](stock/docs/guides/platforms.md).

It isn't for mobile apps, large multi-team systems, or hosting for paying
customers at scale, and it doesn't replace your Git host. Single sign-on and
high availability [aren't in 1.0](stock/docs/guides/compatibility.md#not-in-10).

## Install on a server

One or more admins run AppTown for everyone else. They invite people,
connect the servers or services that will run the apps, add the AI model
key, and choose the way of working new projects start with. Each project
sets its own approval rules. Administrators can also approve changes.

On one Linux server with Docker (Compose 2.23 or later) and a domain name:

```sh
mkdir -p /opt/apptown && cd /opt/apptown
curl -fsSLO https://apptown.dev/compose.yaml
echo APPTOWN_DOMAIN=apptown.example.com > .env
docker compose up -d
```

First point `apptown.example.com`, `*.preview.apptown.example.com` and
`*.main.apptown.example.com` at the server, and open ports 80 and 443.
One server suits a small team that trusts everyone who writes projects.
The agents' code runs in containers on the machine that holds AppTown's
records, and a container is not a strong sandbox. A split install puts the
agents and the apps on other machines,
and takes more setup. [Running AppTown](stock/docs/guides/running-apptown.md)
covers both, and which one to choose.

### The command line

The `apptown` command line works with any AppTown, from your own computer.
On macOS or Linux, with [Homebrew](https://brew.sh):

```sh
brew install flokkit/tap/apptown
```

The tap carries final releases only. The CLI is also `bin/apptown` in each
[release archive](https://github.com/flokkit/apptown/releases).
[Connecting a coding agent](stock/docs/guides/connect-a-coding-agent.md#the-apptown-command-line)
says how to give it a token.

## Read more

- [The tour](https://apptown.dev/tour/): AppTown's main pages, with screenshots.
- [Start here](stock/docs/guides/quickstart.md): making your first app, and
  [connecting your own coding agent](stock/docs/guides/connect-a-coding-agent.md).
- [What it is for](stock/docs/guides/what-apptown-is-for.md) and
  [how it works](stock/docs/guides/architecture.md).
- [Supported platforms and system requirements](stock/docs/guides/platforms.md).
- [What 1.0 promises](stock/docs/guides/compatibility.md).
- [CONTRIBUTING.md](CONTRIBUTING.md): issues are welcome; pull requests by
  arrangement.
- [SECURITY.md](SECURITY.md): how to report a vulnerability privately.

## What is here

This repository contains the Go server, web frontend, command-line tools
and built-in ways of working. PostgreSQL stores AppTown's records, and the
server embeds the web frontend.

For development, start with the [package map](design/architecture/domains.md)
and [code navigation](internal/apptown/README.md). The
[documentation index](design/README.md) covers the product and its design.

## From source

Building AppTown from the repository, for working on it, is in
[CONTRIBUTING.md](CONTRIBUTING.md#building-from-source). To run it, use a
release: [Running AppTown](stock/docs/guides/running-apptown.md).

## Development

```sh
make dev-api      # go run backend on 127.0.0.1:7777
make dev-web      # vite dev server on :5173 with /api proxied
```

Run `make tools` to install the check tools, including the pinned sqlc version,
then `make check`.

Subsystems declare their database schemas. The server embeds their
combined schema from `internal/apptown/dbschema/schema.sql`.
Named queries live under each subsystem's `internal/*sql`. After editing a
query or schema, run `make generate-sql` and commit the generated Go alongside
the SQL. Local checks and CI use `sqlc diff` to reject stale output. See
[schema ownership](design/architecture/schema-ownership.md).

## Layout

- `cmd/apptownd` — entrypoint (`apptownd [flags]`)
- `internal/<x>` — libraries that know nothing of AppTown (`console`, `deploy`, `database`, ...)
- `internal/apptown/<x>` — AppTown's subsystems (`workflow`, `deployment`, `identity`, ...);
  see `design/architecture/domains.md`
- `internal/apptown` — the App: composes the subsystems, serves the HTTP API
- `web` — React/TS frontend (Vite), embedded into the binary from `web/dist`
- `design` — architecture and workflow spec documents

## Licence

Apache License 2.0; see [LICENSE](LICENSE) and [NOTICE](NOTICE). The code is
free to use, change and run, commercially too. The names "Flokkit" and
"AppTown" are not part of the licence: a fork may use the code, not the names.
