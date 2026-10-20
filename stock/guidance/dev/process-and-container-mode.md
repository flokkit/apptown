---
name: dev/process-and-container-mode
tags: dev,runtime,workflow
---
How to run an app while you build it, and how it runs at the gates. AppTown is
opinionated here so the simple case stays simple and the hard case stays
possible. (This is a stock way of working; a project that wants a different
stance forks it in its own pack.)

**Develop as processes. Prove and ship as containers.**

- **Iterate in process-mode.** Run the app with its own tooling — `npm run dev`,
  `go run .`, `manage.py runserver`. Its declared needs (`needs:` in
  .forge/app.yaml) are already running as emulators and their connection env is
  in your environment ($DATABASE_URL, $PG*, $REDIS_URL, …). Read that env; reach
  services by the injected host, never localhost-assumed or hard-coded. Do NOT
  build an image or run compose to develop — that's slow and it's not where the
  work is.

- **Multiple processes → one Procfile, run with `hivemind`.** If the app is more
  than one long-running process (a web server + a worker + an asset watcher),
  put them in a `Procfile` (`web: …`, `worker: …`) and run `hivemind` (baked
  into your environment) — it starts them together, aggregates logs, and stops
  them cleanly. Do NOT hand-background each process with `&` — that orphans
  processes and interleaves output. On your own computer, `apptown dev` starts
  the needs, writes their connection env to `.forge/dev.env` and, for an app
  with several parts, a Procfile at `.forge/Procfile`; run it with
  `hivemind -f .forge/Procfile` after `set -a && . ./.forge/dev.env && set +a`.
  A single-process app needs no Procfile — just run it.

- **Never in the foreground of your shell.** A server (or `hivemind`, or
  `docker run` without `-d`) does not return, so it holds your step until its
  timeout. Start the one command detached, its output to a log file and `&`
  (`docker run -d` for a container), poll it with a bounded wait, and stop it
  by the pid you started (`docker stop` for a container) when you are done.

- **The gates are container-mode.** Preview and deploy run the contract COLD in
  containers — that's the proof (it boots from nothing, not from your warm
  session) and the shipping boundary. "It ran in my process loop" is not
  "it boots cold"; the preview boot gate is what proves the difference. You
  don't develop there.

- **Raw containers are yours when you need them.** When your session has its
  own Docker daemon (`DOCKER_HOST` is set and `docker info` answers) —
  launch containers, `exec` in, read logs, inspect and fix a misbehaving
  service, run a one-off image. Process-mode is the default, not a fence. Reach
  for containers when the task is genuinely container-native or when you're
  debugging the runtime itself.

- **Fast diagnostics, if available.** If `lsp_diagnostics` / `lsp_fix` tools are
  present (the lsp extension, enabled on some projects), use them for exact,
  in-the-loop type/lint feedback while editing — then run the project's
  authoritative build/lint/test before declaring done. They're intermediate
  feedback, not the gate.

In one line: **develop as processes against provided needs; prove and ship as
containers; drop to raw containers whenever the work calls for it.**
