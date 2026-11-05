---
name: build/toolchain-mise
tags: build,toolchain,registries,security
checks: [{"name": "toolchain-runtime-drift", "expects": "mise.toml's runtime version and the Dockerfile's runtime base image agree (dev and deploy run the same JVM/Node/Python major)", "severity": "note", "evaluator": "llm", "scope": "tree", "files": ["mise.toml", "Dockerfile*", "**/Dockerfile", ".apptown/app.yaml"], "rubric": "mise.toml is the dev/build toolchain; the Dockerfile's final (runtime-stage) FROM line is the runtime the app deploys on. For each versioned runtime — a JVM (java), Node, Python — the major version in mise.toml must match the major version of the Dockerfile's runtime base image (java 21 with a temurin-21 / eclipse-temurin:21 base; node 22 with node:22). A mismatch (java 22 vs temurin-21, node 22 vs node:20) means the app is tested on one version and deployed on another: cite both lines. Go and Rust compile to static binaries with no separate runtime version — never flag them. A part that .apptown/app.yaml builds from a Dockerfile (build:) with no Dockerfile in its folder is a violation: name the part and the folder. A static: part deploys files, not a runtime: judge only its mise.toml. With no mise.toml at the repository root, the toolchain is not declared: that is a violation, citing the Dockerfile's runtime base. When both exist and the Dockerfile's runtime base has no versioned runtime (Go, Rust, a static site's files), there is nothing to match: pass.", "title": "Align the runtime with the declared toolchain", "appliesTo": ["tool:dotnet", "tool:go", "tool:java", "tool:node", "tool:python", "tool:ruby", "tool:rust"]}]
---
Declare stock workflow tools and versions in root `mise.toml`, never install them by
hand. Registry rules are in build/registries; stock defaults in stock/toolchains/.

- A tool without a stock toolchain (PHP, built and tested in Docker by the
  WordPress way of working) needs no declaration. Steps install declared tools
  automatically and record resolved versions in run evidence.
- In AppTown steps, chats and previews, tools are installed on entry and the
  workspace's mise shims lead PATH. Use plain commands there and in READMEs and
  BUILD.md. `mise exec -- <cmd>` is only for outside shells without mise activated.
- Never `curl | sh` installers or `apt-get` toolchains inside steps.
- mise's unmirrorable `ubi` backend is disabled; use `aqua:` or `github:`.
  Pin Java to Temurin; other vendors use CDNs outside the mirror.
- Downloads use the toolchain mirrors in steering's ambient section.
- mise is absent at deploy. For a `build:` app, the Dockerfile carries the build
  toolchain and deploy runtime. Match its runtime-stage JVM/Node/Python major to
  `mise.toml`. Go/Rust static binaries have no runtime version to match.
