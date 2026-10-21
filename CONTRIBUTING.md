# Contributing

Thank you for your interest. Please read this before you open anything.

## Issues are welcome

Bug reports, questions and suggestions are welcome as
[issues](https://github.com/flokkit/apptown/issues). Use the bug report
template, and never paste secrets, tokens, passwords or private host names
into an issue. A security problem goes through private reporting instead
([SECURITY.md](SECURITY.md)).

## Pull requests by arrangement

Pull requests here are how the maintainers land their own changes, one per
change. Opening one is limited to the repository's collaborators. If you
would like to change something:

1. **Open an issue first** and describe the change. Wait for a maintainer to
   agree before you put work into it.
2. For a small fix, a maintainer may make the change themselves, with credit
   to you in its pull request.
3. For anything larger, agree the approach in the issue first; a maintainer
   may then invite your pull request.

## Working on the code

- Build: [Building from source](#building-from-source) below.
- Check: `make tools` once (the pinned analyzers), then `make check`, which runs
  the same checks as CI ([.github/workflows/ci.yml](.github/workflows/ci.yml))
  plus the web tests. It needs Docker: the database tests start their own
  PostgreSQL.
- Read [CLAUDE.md](CLAUDE.md) for the design rules the code follows and
  [design/README.md](design/README.md) for where each part is described.

## Building from source

You need Go (the version in [go.mod](go.mod)), Node.js 24 with npm, Git, and a
running Docker (AppTown starts its own PostgreSQL in a container).

```sh
git clone https://github.com/flokkit/apptown.git
cd apptown
npm ci --prefix web   # the web interface's dependencies (make build also does this when they are missing)
make build            # web/dist, then bin/apptownd, bin/apptown, bin/apptown-worker and bin/apptown-linux
./bin/apptownd -addr 127.0.0.1:7777 -console-addr '' -admins trial
```

`sh scripts/release-archives.sh <version> <commit> <out-dir>` makes the
release archives and `SHA256SUMS` as the release workflow does, after `make
web`.

Then open <http://127.0.0.1:7777>. The data directory is `./_data` unless
`-data` names another.

This is a web-only trial in the default `open` authentication mode: the browser
says who it is (`trial`, until you change the name), and `-admins trial` makes
that name an administrator. Open mode trusts that name, so keep it on
`127.0.0.1` and never expose it; use `-auth internal` for anything shared.
Chats and agent workflows also need a model provider, a private inference
listener, container network settings and an agent image: see [Setting up a server](stock/docs/guides/server-setup.md), and
[Split install](stock/docs/guides/split-install.md) for a separate worker and
running AppTown with no local Docker (`-no-local-docker`).

The daemon mounts `bin/apptown-linux` into agent and console containers. `make build`
and `make go` build this artifact; runtime source compilation is not required.
It defaults to `apptown-linux` beside the daemon executable. For another location,
set `-console-cli /absolute/path/apptown-linux` (or `APPTOWN_CONSOLE_CLI`). A missing
artifact is reported and sessions continue without the CLI. `make dev-api` builds
and supplies the artifact explicitly for `go run`.

**The agent image, from source.** Agents need an agent image. A source build
has no published image to name, so build `apptown-agent` from the recipe in the
repository directory:

```sh
docker build -t apptown-agent deploy/apptown-agent
```

The recipe pins every archive it fetches by version and SHA-256; apt packages
and package managers' dependencies resolve when it is built, so two builds can
differ. AppTown builds this image only when it is missing, so rebuild it when a
release's notes name a new agent image recipe; workers build their own,
`apptown-agent:<recipe hash>`, when they start. AppTown starts that build in the
background if the image is missing and `deploy/apptown-agent/Dockerfile`
exists relative to its working directory; a lone binary does not include it.
A failed build is recorded in `<data directory>/agent-image-build.log`.

By contributing you agree that your contribution is licensed under the Apache
License 2.0 ([LICENSE](LICENSE)).
