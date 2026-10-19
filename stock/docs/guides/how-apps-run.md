# Running your app while you work

While you work on an app, run it the quick way: as ordinary processes,
against its services. When it matters (a preview, a deploy), AppTown boots it
the strict way: in containers, cold, from its contract. You can also run it
the strict way yourself.

## Services are always provided

A **need** (a database, a cache, object storage, AI models) is always
provided for you and its connection variables are set (`DATABASE_URL`,
`PG*`, `REDIS_URL`, `AWS_*`): a local container while you work, a service
of the deployment's own once deployed. The variables are listed in
[Services and data](/docs/guides/services-and-data). What changes is how
the app itself runs:

| | The app runs as | When |
|---|---|---|
| **As processes** | your own dev command (`npm run dev`) | while you build and try things |
| **In containers, cold** | the containers its contract declares | at a preview and a deploy |

## As processes: the quick loop

`forge dev` starts the app's needs in containers and writes their connection
variables to `.forge/dev.env`. You then run the app with its own tooling:

```sh
forge dev
set -a && . ./.forge/dev.env && set +a && npm run dev
```

An app with several parts (web, worker, a watcher) also gets
`.forge/Procfile`, one line per part with a command. Run them together with
`hivemind -f .forge/Procfile`: it starts them, merges their logs, and stops
them all on Ctrl-C. Edit the lines if your dev commands differ from the run
commands. Do not start each with `&` by hand: that leaves orphans and
tangled logs.

Everything `forge dev` writes is under `.forge/`, which never lands.

In an agent step there is no Ctrl-C: a server left in the foreground holds
the step until it times out. Start the one command (`npm run dev`,
`hivemind`) with its output to a log file and `&`, or a container with
`docker run -d`; wait for it with a bounded check, and stop it by its pid
(or `docker stop`) when done.

Reach services by the host their variables give, never a hard-coded
`localhost:5432`, so the same code works in a container and once deployed.

## In containers: the preview and the deploy

At a **preview** (**Try it**, `forge preview`) and a **deploy**, AppTown
builds the app from its [contract](/docs/guides/contract-reference) and
boots it cold. Nothing from your working session carries over: no stray
variable, no process you left running, no file you did not commit. "It
worked as I worked" and "it boots cold" are different claims; the preview
is where they meet.

To rehearse that yourself, `forge dev up` runs the whole app in containers
on your Docker. A static site built by a `Dockerfile` is the exception: use
its own dev server.

## Your own containers

When the work is about containers themselves, or a misbehaving service
needs a closer look, a change chat or a step that asks for it gets its own
Docker daemon (`DOCKER_HOST` is set): build images, start containers,
`exec` into them, read their logs. Who gets one, how to reach its ports, and
what survives are in
[What chats and steps can use](/docs/guides/runtime-environments). Without
one, a preview still builds and boots the app on its own: on the worker
when AppTown has one, otherwise on AppTown's host.

## `forge dev` in full

- `forge dev`: start the needs, write `.forge/dev.env` (and
  `.forge/Procfile` when the app has more than one part); you run the app.
- `forge dev up`: run the whole app in containers with Docker Compose.
- `forge dev generate`: write `.forge/docker-compose.yml` and stop.
- `forge dev down`: stop either, and remove its volumes.

`forge dev` asks your AppTown to render the files, so it needs the address
and token the command line uses.

## Changing the advice

This advice ships as the guidance block `dev/process-and-container-mode`.
A project that wants different advice adopts a pack that leaves the stock
block out and adds its own.
