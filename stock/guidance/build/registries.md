---
name: build/registries
tags: build,registries,security
---
Package registries are AMBIENT: bound at the instance, injected into your
environment at step start. The repo declares WHAT it depends on; the
instance declares WHERE it comes from.

- **What is mirrored on THIS instance is listed in your steering under
  "Ambient registry environment"** — generated from the live bindings, so
  it is always true. `env | grep -E 'GOPROXY|REGISTRY|INDEX|RUSTUP|MAVEN'`
  shows the actual values. Generated configs are read-only.
- Never write registry or mirror URLs into repo files, and never edit the
  generated configs.
- **Nested environments you create do NOT inherit this.** A Dockerfile's
  `RUN npm install`, a CI config, a docker-compose build — none see the
  ambient env. Propagate it explicitly (`ARG`/`ENV`, build args from the
  environment) and note it in your report.
- **When a download is refused (connection refused / unreachable to a
  public registry): STOP. Do not work around it** — no curl-piping
  installers, no vendoring tarballs, no editing registry config. Report
  "registry format <X> is not bound for this instance" and continue with
  what you CAN do. A blocked download is a finding to surface, not an
  obstacle to be clever about.
