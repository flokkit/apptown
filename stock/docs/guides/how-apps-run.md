# How apps run: process-mode, container-mode, and the gates

The forge has an opinion about how you run an app, and it's deliberate: **the
simple thing is simple, the hard thing is possible.** You should be able to just
`npm run dev`, you should be able to launch and poke containers, and you
shouldn't have to think about which unless you want to. This guide is that
opinion written down. (It's a *default*, not a law — see "Overriding" at the
end.)

## The one idea: how the app runs is separate from how its needs are met

A **need** — a database, a cache, object storage, AI models — is always
fulfilled the same way: the forge provides it (an emulator locally, a managed
resource in cloud, a key for a service) and injects the connection env
(`DATABASE_URL`, `PG*`, `REDIS_URL`, `AWS_*`). That's constant. The catalog
is in [Services and data](/docs/guides/services-and-data). What changes is whether the **app itself** is a *process* or a
*container*:

| | App runs as | When |
|---|---|---|
| **Process-mode** | processes (`npm run dev`) | while you build and iterate |
| **Container-mode** | containers, booted cold | at the gates: preview and deploy |

Containers are the medium of **proof and shipping**, not of development.

## Process-mode — the default, the fast loop

You (or the agent) run the app with its own tooling. The needs are already up
and their env is set, so the app connects and you iterate — edit, reload, repeat
— with none of the build-an-image / compose-up weight.

- One process: just run it. `forge dev` brings up the needs and writes a `.env`;
  `set -a && . ./.env && set +a && npm run dev`.
- Several processes (web + worker + watcher): put them in a **`Procfile`** and
  run **`hivemind`**, which starts them together, merges their logs, and stops
  them cleanly on Ctrl-C. `forge dev` generates the Procfile from your app parts
  (each part's command becomes a line); edit it if your dev commands differ from
  the run commands. Don't hand-background each process with `&` — that's how
  you get orphans and tangled logs.
- In an agent step there is no Ctrl-C: a server left in the foreground holds
  the step until its timeout. Start the one command (`npm run dev`,
  `hivemind`) with its output to a log file and `&`, or a container with
  `docker run -d`; poll it with a bounded wait, and stop it by its pid (or
  `docker stop`) when done.

Reach services by the injected host from the env, never a hard-coded
`localhost:5432` — that's what lets the same code work unchanged in a container
and in cloud.

## Container-mode — the gates

At **preview** (the try-it / boot gate) and **deploy**, the forge renders the
contract to containers and boots it **cold** — nothing from your warm session
carries over. This is the point: it proves the app comes up from its declared
contract alone (the class of bug that ships when something only worked because
your dev shell had a stray env var or a running process). "It worked in my
process loop" is a different claim from "it boots cold"; the preview gate is
where the two are reconciled. `forge dev up` runs the whole thing in containers
locally if you want to rehearse that yourself.

## Hard things stay possible — raw containers

Process-mode is the default, not a cage. An eligible implementation step or
writable change chat gets its own private Docker daemon (`DOCKER_HOST` is
already set): build images, launch containers, `exec` into them, tail logs,
inspect a misbehaving Postgres. When the work is genuinely container-native,
or you're debugging the runtime itself, use containers directly. Who gets a
daemon, how to reach its ports, and what survives cleanup are in
[What chats and steps can use](/docs/guides/runtime-environments). Without
one, the forge's own preview (▶ Try it, or `forge preview`) still builds and
boots the app independently: on the worker when the forge has one,
otherwise on the forge's host.

## `forge dev` — the two gears

- `forge dev` — process-mode: bring up the needs, write `.env` (and a `Procfile`
  if there's more than one part), then you run your processes.
- `forge dev up` — container-mode: render and run the whole app via compose (the
  local rehearsal of the preview/deploy path).
- `forge dev generate` — write the rendered `.forge/docker-compose.yml` and stop.
- `forge dev down` — tear either down.

## Overriding the opinion

This stance ships as a stock way of working (the `dev/process-and-container-mode`
guidance block). If your project genuinely wants a different one, don't edit the
stock block — author your own and swap it in via a pack that excludes the stock
one. The forge is opinionated by default and overridable on purpose.
