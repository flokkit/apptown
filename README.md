# Flokkit AppTown

AppTown is where a small team builds and keeps the apps it makes with AI
coding agents. You run it yourself, on your own machine or server.

Coding agents have dropped the cost of small custom apps to nearly zero, but
owning an app that people can rely on is still a lot of work. Someone has to
deploy it, back it up, keep it patched and still understand it a year later,
usually after the person who made it has lost interest.

AppTown makes that handover boring and repeatable. You bring an app, or have
the agents build it (that part is optional), and AppTown looks after it from
there. Every change goes through the same steps, so you can see who asked for
what, what was checked and what's actually running.

What comes out is ordinary software: a git repository and a container image
you can take somewhere else. It's open source (Apache-2.0).

It's a 1.0 release candidate. Release notes are in [releases/](releases/).

## Reading about it

- [What it is for](stock/docs/guides/what-forge-is-for.md): the problems it
  addresses and what it is not.
- [How it works](stock/docs/guides/architecture.md): the pieces, as built.
- [What 1.0 promises](stock/docs/guides/compatibility.md) and
  [what is next](stock/docs/guides/roadmap.md).
- [Running one](stock/docs/guides/running-a-forge.md): installing and
  operating a forge, including model-backed agents.

## What is here

Project repositories, Changes and versioned workflow runs, with reviews,
approvals, conformance checks, deployments and persistent agent consoles.
The web UI, HTTP API, CLI and MCP tools call the same application owners.
PostgreSQL stores the records; the Go daemon embeds the web frontend.

For development, start with the [current package map](design/architecture/domains.md)
and [code navigation](internal/forge/README.md). The [documentation index](design/README.md)
distinguishes current guides from accepted intent. Authentication and resource
access are checked separately; an actor name is not permission.

## Quickstart: a local trial

You need Go (the version in [go.mod](go.mod)), Node.js 24 with npm, Git, and a
running Docker (the forge starts its own PostgreSQL in a container).

```sh
git clone https://github.com/flokkit/apptown.git
cd apptown
npm ci --prefix web   # the web interface's dependencies (make build also does this when they are missing)
make build            # web/dist, then bin/forged, bin/forge, bin/forge-worker and bin/forge-linux
./bin/forged -addr 127.0.0.1:7777 -console-addr '' -admins trial
```

Then open <http://127.0.0.1:7777>. The data directory is `./_data` unless
`-data` names another.

This is a web-only trial in the default `open` authentication mode: the browser
says who it is (`trial`, until you change the name), and `-admins trial` makes
that name an administrator. Open mode trusts that name, so keep it on
`127.0.0.1` and never expose it; use `-auth internal` for anything shared.
Chats and agent workflows also need a model provider, a private inference
listener, container network settings and an agent image: follow Install in
[Running a forge](stock/docs/guides/running-a-forge.md). It also covers running
work on a separate worker machine and running a forge with no local Docker
(`-no-local-docker`).

The daemon mounts `bin/forge-linux` into agent and console containers. `make build`
and `make go` build this artifact; runtime source compilation is not required.
It defaults to `forge-linux` beside the daemon executable. For another location,
set `-console-cli /absolute/path/forge-linux` (or `FORGE_CONSOLE_CLI`). A missing
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

## Names in the code

Inside the code the product is still called Forge: the daemon is `forged`,
the command-line tool `forge`, the worker `forge-worker`, the Go module
`flokkit.io/forge`, request headers `X-Forge-*`, environment variables
`FORGE_*`, workflow files `apiVersion: forge.flokkit.io/v1` and project
folders `.forge/`. Those names stay as they are.

## Layout

- `cmd/forged` — entrypoint (`forged [flags]`)
- `internal/<x>` — libraries that know nothing of forge (`console`, `deploy`, `database`, ...)
- `internal/forge/<x>` — forge's subsystems (`workflow`, `deployment`, `identity`, ...);
  see `design/architecture/architecture.md`
- `internal/forge` — the App: composes the subsystems, serves the HTTP API
- `web` — React/TS frontend (Vite), embedded into the binary from `web/dist`
- `design` — architecture and workflow spec documents

## Licence

Apache License 2.0; see [LICENSE](LICENSE) and [NOTICE](NOTICE). The code is
free to use, change and run, commercially too. The name "Flokkit" is not part
of the licence: a fork may use the code, not the name.
