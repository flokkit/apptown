---
name: dev/process-and-container-mode
tags: dev,runtime,workflow
---
**Develop as processes; prove and ship as containers.**

- Run the app with its own tooling while you build it (`npm run dev`,
  `go run .`, `manage.py runserver`). Its `needs:` are already running and
  their connection env is set ($DATABASE_URL, $PG*, $REDIS_URL, …): read it and
  reach services by the injected host, never localhost. Do not build an image
  or run compose to develop.
- Several long-running processes (web, worker, asset watcher) go in one
  `Procfile` run with `hivemind`, never hand-backgrounded with `&`. On a
  person's computer, `apptown dev` starts the needs and writes
  `.apptown/dev.env` (and `.apptown/Procfile` for several parts): run
  `set -a && . ./.apptown/dev.env && set +a`, then
  `hivemind -f .apptown/Procfile`.
- Never run a server in the foreground of your shell: start it detached with
  its output to a log, poll with a bounded wait, stop it by the pid you
  started (`docker stop` for a container).
- Previews and deploys boot the contract cold in containers; that is the proof,
  not your warm session.
- When your session has its own Docker daemon (`DOCKER_HOST` is set and
  `docker info` answers), use containers for container-native work or for
  debugging the runtime.
- `lsp_diagnostics`/`lsp_fix`, where present, are quick feedback; the
  project's own build, lint and tests are the gate.
