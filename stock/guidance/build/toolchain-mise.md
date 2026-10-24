---
name: build/toolchain-mise
tags: build,toolchain,registries,security
checks: [{"name": "toolchain-runtime-drift", "expects": "mise.toml's runtime version and the Dockerfile's runtime base image agree (dev and deploy run the same JVM/Node/Python major)", "severity": "note", "evaluator": "llm", "scope": "tree", "files": ["mise.toml", "Dockerfile*", "**/Dockerfile", ".apptown/app.yaml"], "rubric": "mise.toml is the dev/build toolchain; the Dockerfile's final (runtime-stage) FROM line is the runtime the app deploys on. For each versioned runtime — a JVM (java), Node, Python — the major version in mise.toml must match the major version of the Dockerfile's runtime base image (java 21 with a temurin-21 / eclipse-temurin:21 base; node 22 with node:22). A mismatch (java 22 vs temurin-21, node 22 vs node:20) means the app is tested on one version and deployed on another: cite both lines. Go and Rust compile to static binaries with no separate runtime version — never flag them. A part that .apptown/app.yaml builds (build:, or static: with build:) with no Dockerfile in its folder is a violation: name the part and the folder. With no mise.toml at the repository root, the toolchain is not declared: that is a violation, citing the Dockerfile's runtime base. When both exist and the Dockerfile's runtime base has no versioned runtime (Go, Rust, a static site's files), there is nothing to match: pass.", "title": "Align the runtime with the declared toolchain", "appliesTo": ["build"]}]
---
Toolchains for the stock workflows are declared via mise, not installed by
hand. (Registry norms — mirrors, nested environments, refused downloads —
live in build/registries; this block is the mise layer on top.)

- Declare tools and versions in `mise.toml` at the repo root (stock
  defaults live in stock/toolchains/). Steps install the declared
  toolchain automatically; resolved versions are recorded in run evidence.
- Inside AppTown (steps, chats, previews) the declared tools are already on
  PATH: the workspace's mise shims come first and the toolchain is installed
  on entry. Run plain commands — `go test ./...`, `bundle exec rails test`,
  `npm test` — and write plain commands in READMEs and BUILD.md; people
  reading them are not inside a step. `mise exec -- <cmd>` is only for a
  shell outside AppTown that has not activated mise.
- Never `curl | sh` installers, never `apt-get` toolchains inside steps.
- mise's `ubi` backend is disabled (it cannot be mirrored) — use `aqua:`
  or `github:` backends. For Java, pin Temurin: other vendors download
  from CDNs the mirror does not cover.
- Tool downloads flow through the toolchain mirrors listed in your
  steering's ambient section.
- `mise.toml` is your DEV/BUILD toolchain, not the deploy runtime. For a
  `build:` app the Dockerfile is the deploy artifact and carries its OWN
  runtime — a build stage with the toolchain (e.g. `maven:…-temurin-21`) and,
  for a versioned runtime (JVM/Node/Python), a runtime-stage base image
  (e.g. `eclipse-temurin:21-jre`) — because mise is NOT present at deploy. Keep
  the Dockerfile's runtime version matched to `mise.toml`, or you test on one
  version and ship on another. (Go/Rust compile to a static binary — no runtime
  version to match.)
