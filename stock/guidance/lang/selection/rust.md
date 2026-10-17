---
name: lang/selection/rust
tags: lang,rust
checks: [{"name": "gitignore-names-build-output", "expects": "git would ignore what Cargo writes (target/), so build output can never be committed by accident. If the project commits that output deliberately, this check does not fit it: say so and stop rather than add the line, since an ignore rule over tracked files makes the repository contradict itself", "severity": "medium", "evaluator": "sh", "sh": "t=$(mktemp -d) && git -c init.defaultBranch=main init -q \"$t\" || exit 2; bad=0; for p in target/probe; do git --git-dir=\"$t/.git\" --work-tree=. -c core.excludesFile=/dev/null check-ignore -q \"$p\" || { echo \".gitignore does not cover ${p%/*}/\"; bad=1; }; done; exit $bad", "title": "Declare generated files in the repository’s ignore rules"}, {"name": "runtime-stage-not-build-image", "expects": "the Dockerfile's final stage is not FROM rust (any tag), the image this pack's seed builds with: a build stage makes the artifact and the final stage starts from debian:bookworm-slim or gcr.io/distroless/cc-debian12:nonroot with only that artifact copied in. Judged from the FROM instructions as Docker reads them (continued lines, comments and a heredoc's body; a stage name resolves to the image it names); a base image given by a build argument is not judged, an instruction with two heredocs, or heredoc-like text inside a quoted shell string whose end is never found, is reported as not judged", "severity": "high", "evaluator": "sh", "sh": "[ -f Dockerfile ] || exit 0; img=$(awk 'function inst(i, s, a, d, c) { if (toupper($1) == \"FROM\") { i = 2; while ($i ~ /^--/) i++; s = $i; a = tolower(s); if (a in st) s = st[a]; if (toupper($(i+1)) == \"AS\") st[tolower($(i+2))] = s; last = s } else if (toupper($1) ~ /^(RUN|COPY|ADD)$/ && match($0, /(^|[^<])<<-?[ \\t]*[\"\\047]?[A-Za-z_][^ \\t\"\\047<>|;&]*/)) { d = substr($0, RSTART, RLENGTH); c = $0; if (gsub(/(^|[^<])<<-?[ \\t]*[\"\\047]?[A-Za-z_]/, \"\", c) > 1) many = 1; sub(/^[^<]?<</, \"\", d); hs = (d ~ /^-/); sub(/^-?[ \\t]*[\"\\047]?/, \"\", d); hd = d } } { sub(/\\r$/, \"\") } hd != \"\" { t = $0; if (hs) sub(/^\\t+/, \"\", t); if (t == hd) hd = \"\"; next } /^[ \\t]*#/ { next } b != \"\" && /^[ \\t]*$/ { next } /\\\\[ \\t]*$/ { sub(/\\\\[ \\t]*$/, \"\"); b = b $0 \" \"; next } { $0 = b $0; b = \"\"; inst() } END { if (b != \"\") { $0 = b; inst() } if (many || hd != \"\") print \"<<\"; else print last }' Dockerfile); [ \"$img\" = \"<<\" ] && { echo 'not judged: a heredoc whose end this check cannot find (heredoc-like text inside a quoted string reads as one), or an instruction with more than one heredoc'; exit 2; }; r=${img%@*}; case ${r##*/} in *:*) r=${r%:*};; esac; r=${r#docker.io/}; r=${r#library/}; [ \"$r\" = rust ] || exit 0; echo \"the final stage runs FROM $img, the image your pack's seed builds with: its compiler, toolchain and their known vulnerabilities ship with the app. Build in a stage FROM rust, then copy only the release binary into a final stage FROM debian:bookworm-slim or gcr.io/distroless/cc-debian12:nonroot (lang/selection/rust shows the recipe)\"; exit 1", "title": "Run the application from a runtime image, not the build image"}]
---
Rust services and tools: stable toolchain pinned in mise.toml
(installed through the rust-dist mirror; crates through the cargo mirror).

- Std-first: a small HTTP service is `std::net::TcpListener` and a loop —
  no tokio, no framework, until concurrency needs prove otherwise.
- Dependencies earn their place: `anyhow` for binaries, `thiserror` for
  libraries, `clap` only when arguments are real. Every crate is a
  dependency decision.
- Errors are values: `Result` everywhere, `?` liberally, no `.unwrap()`
  outside tests and `main`'s final expression.
- Tests inline (`#[cfg(test)] mod tests`) next to what they test;
  integration tests only when a binary boundary exists. The landing runs
  `make test`, and the seeded Makefile runs `cargo test --locked`: keep
  that target running the whole suite.
- The release binary is the deployable: the seeded `.forge/app.yaml`
  builds it with `build: .` (the seeded Dockerfile runs `cargo install
  --locked`, so `Cargo.lock` is committed, and starts the binary as an
  unprivileged user), or runs a pushed `image:`. The app listens on
  $PORT when set (8080 otherwise) and answers `GET /healthz`, the
  contract's readiness path.
- Build in a stage `FROM rust` and ship only the binary. The runtime
  stage is `debian:bookworm-slim` (the seed: a shell, and the
  distribution's security updates at build) or
  `gcr.io/distroless/cc-debian12:nonroot` (glibc and nothing else: no
  shell, no package manager). Never run from the `rust` image: it is the
  compiler on a full Debian. Measured on hello-rust, 2026-10-11: `rust`
  1.47 GB with 1,248 high or critical advisories; bookworm-slim 110 MB
  with 13; distroless/cc 35 MB with 1. A static musl binary on `scratch`
  needs the musl target through the mirror; take it only with a reason.

Build output stays out of git: `.gitignore` names `target/`, which is
everything Cargo writes.

**Fits a container limit.** A Rust service needs nothing set; a small one
uses a few megabytes, so declare `resources: { memory: 48M }` in
`.forge/app.yaml` and raise it when the deployment's card shows it near the
limit. deploy/memory-declared says how to choose and check it.
