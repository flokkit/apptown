# What chats and steps can use

Forge gives its helpers a workspace, development tools and access to the
services their task needs. The provision differs between a project chat, a
change chat and a workflow step. This guide describes the helper's
environment; `.forge/app.yaml` describes how the application itself runs
([contract reference](/docs/guides/contract-reference)).

## Which environment am I in?

| Environment | Workspace and tools | Private Docker |
|---|---|---|
| Project chat | Read-only snapshot at `/work`; writable scratch under `/console`; standard agent image | No |
| Change chat | The change's writable worktree at `/work`; standard agent image | One daemon per session, unless disabled |
| Agent step using Docker that declares `docker: true` | The run's workspace at `/work`; configured agent image | One daemon per attempt; refused when disabled |
| Other agent step using Docker | The run's workspace at `/work`; configured agent image | No |
| Other workflow step | Defined by the step: for example a test container, scanner, approval or engine operation | No blanket grant |
| Local model-agent execution | Refused: a local process cannot isolate the daemon’s provider credentials | Not provisioned |
| Your own agent connected over MCP | Your agent's own computer or runtime and its tools | Connecting to Forge does not supply one |

A step asks for private Docker with `docker: true` in its `with:`; its skills
and name do not. forged's `-no-private-docker` disables it: a step that
declares it is refused with that reason, and change chats get no daemon.
Existing consoles change their Docker access when stopped or after the current
agent turn finishes. A step of a run started before steps declared private
Docker, which uses `skill/implementation` and says nothing of `docker`, is
refused when dispatched or retried; start a new run.

## Where it runs

Without a worker, all of this runs on the forge's own host: chats, agent
steps, checks and previews in containers on its Docker, and a script step
as a plain shell process on the host, with the forge's environment, unless
the step says `executor: docker`.

A forge can have a **worker**: a separate machine with its own Docker that
takes most of that work off the forge's host. With a worker:

| What | Where |
|---|---|
| New change chat | On the worker by default; the person opening it can choose the forge. A chat keeps the place it opened in. |
| Project chat, and a chat that continues a step's agent | On the forge |
| Agent, review and tests steps, and script steps with `executor: docker` | On the worker. A script step with `executor: local` is refused. |
| Checks and scans | On the worker |
| Previews of a change or a run | On the worker, with no fallback to the forge |
| A landing's release images | Built on the worker and pushed to the project's container registry |
| Deployments on the built-in Docker host | On the forge |

A forge run with no Docker of its own (`-no-local-docker`) refuses any
chat that would open on the forge, with the reason, and deploys only to
attached targets. A chat on the worker has its private Docker daemon on the
worker too. A worker that cannot be reached shows its chats as unavailable,
not ended, and its previews' state as not known.

A worker keeps project code off the forge's host for the work listed
above. It is not a strong sandbox: code on it can reach the network, and a
private Docker daemon is not a boundary against hostile code. The forge
addresses a container is given for model access and the `forge` CLI are
configuration, not a network restriction; what else it can reach is up to
the operator's network.

## Who can use a chat

A chat is not private to the person who opened it. Anyone who can read the
project can **look**: the conversation, the terminal's screen and who
opened it. Looking changes nothing and never wakes a stopped chat.

**Acting** needs the right to work on changes in the project (the Project
contributor role): sending a message, pasting a picture, stopping a turn,
typing into the terminal, or ending the chat. A viewer sees the
conversation without those controls, and an attempt is refused with what
it needs. The chat's agent keeps acting with the authority of the person
who opened it, and each act is recorded under the person who did it, with
the opener beside them. The conversation itself does not say which person
typed each message; the record does.

## Tools and project toolchains

The standard `forge-agent` image contains these tool families:

| Purpose | Included tools |
|---|---|
| Shell and source work | Shell, Git, curl, ripgrep, jq, native build tools |
| Native extension headers | libpq (pg, psycopg), libyaml, libffi, OpenSSL, zlib, pkg-config |
| Language development | Node/npm, Python/uv; every other toolchain (Go, Ruby, Java, …) from the project's `mise.toml` |
| Project tool installation | mise |
| Looking at pages | Chromium's headless shell (`chromium-headless-shell`) and fonts, to render and screenshot a running preview; no guarantee of every browser or automation library |
| Containers | Docker CLI, Buildx and Compose plugins; daemon availability follows the table above |
| Interactive sessions and process groups | tmux and hivemind |
| Agent and integrations | pi, approved agent extensions, GitLab CLI; credentials are separate grants |
| Code feedback | Biome, Ruff and gopls; agent language-server integration is opt-in |

Exact versions are listed in `deploy/tool-pins.txt` (the agent image's tools and
the scanners), recorded in the image's build labels, and declared for a project's
own toolchain in its `mise.toml`. The image has no Go: a Go project declares its
Go in `mise.toml`, and gopls uses that Go. A custom step image can provide a different
inventory. Forge mounts a packaged Linux CLI into standard containerized agent sessions.
Build it with `make console-cli`; by default Forge reads `forge-linux` beside
`forged`, or the path set by `-console-cli` / `FORGE_CONSOLE_CLI`. It does not
build source at runtime. A missing/non-executable artifact is logged and leaves
the helper without it; availability is checked once per daemon process.

Pin the tools the project needs in `mise.toml` (or `.mise.toml`). Forge attempts
toolchain installation before starting the agent and uses shared caches, but
this preparation is currently best-effort. Verify that the required command
actually works; an agent starting successfully is not proof that installation
succeeded. On a writable change, for example:

```toml
[tools]
godot = "4.4.1-stable"
```

```sh
mise install
mise exec -- godot --headless --version
```

Match the version to the project's build image. Godot is not baked into the
standard agent image. You can instead run the repository's pinned Godot image
through private Docker. Keep installation declarations and build recipes in the
repository: manually installed tools outside the mounted directories disappear
when a container is replaced. A project chat cannot edit its read-only snapshot
to install dependencies into the tree; use a change chat for that work.

## Private Docker: builds, files and ports

When granted, `DOCKER_HOST=tcp://dind:2375` points to a daemon belonging to the
session or step. It is a separate Docker engine, not the host Docker socket.
The daemon sees the same checkout at `/work`, so this tests the actual files:

```sh
docker info
docker run --rm -v /work:/work -w /work <test-image> <test-command>
docker buildx build --load -t my-app .
docker run -d --name my-app -p 8080:8080 my-app
curl http://dind:8080/
```

Substitute the project's image, test command and container port. Published
ports are reached at `dind`, not the helper's `localhost`, and are not published
on the Forge host for the person's browser. Use `forge preview` for a served
preview URL.

Forge configures a `forge` Buildx builder with DNS and the bound Docker Hub
mirror. Change consoles load local builds into their daemon automatically;
explicit `--load` makes the intent clear across both chat and step builders.
Step setup attempts to preload the Forge agent image in the background; change
console daemons start without that preload. Inspect before assuming any image,
container or build cache exists.

Readiness differs today: a new or upgraded change console reports an error if
its daemon or builder cannot be prepared. A step may continue with a progress
message saying private Docker or its builder is unavailable. Check `docker info`
and the step's progress when a build cannot connect; do not infer availability
from the presence of the CLI alone.

Package and image mirrors are routing configuration, not an outbound firewall.
Nested Docker builds and containers do not inherit the helper's environment:
pass required proxy build arguments and service connection variables explicitly.
The DinD sidecar is privileged; a private engine is not a hardened boundary
against hostile code on the host.

## Services, identity and credentials

Chats do not automatically receive a running database, cache or the production
app's environment. A change chat can ask `forge preview` to boot the declared
application and its needs as a preview (on the worker when the forge has
one, otherwise on the forge's host), or start development services using
its private Docker. `forge dev` uses that private Docker to bring up the
declared needs and write their connection settings; see
[How apps run](/docs/guides/how-apps-run). Docker agent steps receive explicitly configured sidecar
services; steps with `needs: true` also request the app contract's needs as
emulators. Other executors do not automatically provide those Docker services.

The permissions follow the task, independently of which CLIs are installed:

- Chats receive a session identity and generated model configuration. Pi and
  delegates authenticate to Forge's private inference proxy with the chat
  token; upstream provider credentials remain on the Forge server. The same
  chat token survives idle periods and restarts and ends with the chat.
  The Forge API permits reads and the chat-specific actions through normal gates.
- Agent steps receive an attempt-scoped Forge token and model access through
  the same proxy. The token is revoked at completion and does not survive a
  daemon restart. A continued chat receives its own token. Generated config
  carries a token environment reference, never an upstream provider key.
- Inference requires both a live runtime token and an explicitly allowed worker
  network. It is attributable and concurrency-limited, but has no hard monetary
  budget in this first version. Local model execution is refused because it
  cannot isolate the daemon's credentials.
- The `STEP_` secret namespace supplies explicit step environment values, with
  that prefix removed. Chats do not receive this namespace automatically.
  SCM access is an explicit step configuration. Chats can receive a scoped,
  read-only main-branch Git remote; it does not grant push access.
- Eligible implementation steps receive the project's integration environment
  and, when a registry is bound, staged Docker registry authentication.
  Change-console DinD does not add registry push credentials or app integration
  secrets. Installing `glab` or Docker does not itself authorize either service.

Use only the credentials actually granted. Do not dump the environment or
credential files to diagnose availability; probe the specific tool or service.

## What survives?

| Event | Chat records and change files | Console runtime | Private console Docker |
|---|---|---|---|
| Forge restart | Kept | Container remains; Forge reconnects | Kept |
| Unattached and inactive for 30 minutes | Kept | Stopped; shell processes end | Stopped; images and container records remain |
| About two hours idle, with reaping enabled | Kept for resume | Container can be removed | Daemon, nested state and private network removed |
| End session | Conversation retained; change worktree retained | Removed | Removed |
| Container removed or refreshed | Mounted session files and change worktree remain | Recreated; manual container-only installs are lost | Reused if valid, rebuilt if missing |

Project-chat snapshots are removed when their session ends. Runtime caches
are disposable, not backups. Nested containers may need restarting after an
idle stop according to their own restart policy. Step containers and daemons
are cleaned up after execution; their workspace and recorded evidence have
separate retention rules.

Forge shutdown cancels console Docker control operations within its bounded
shutdown policy. It preserves console containers instead of waiting for every
job running inside them. A resumed conversation must inspect current state
rather than assuming that an earlier process, image or service still exists.

Keep the application's durable recipe in its Dockerfile, toolchain declaration
and app contract. Previews and deployments must work from those inputs
independently of the helper's warm session; their lifetimes are in
[Previews and deployments](/docs/guides/container-lifecycle).
