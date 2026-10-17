---
name: build/container-image
tags: build,container,security
checks: [{"name": "dockerfile-lint", "expects": "hadolint finds nothing at warning or above in the Dockerfile: a pinned base-image tag (never latest), COPY not ADD, an absolute WORKDIR, JSON-form CMD/ENTRYPOINT, and no package cache left in a layer — the image the forge builds is what runs", "severity": "medium", "evaluator": "tool", "tool": "hadolint", "title": "Resolve Dockerfile warnings"}, {"name": "runs-as-non-root", "expects": "the final stage of the Dockerfile switches to an unprivileged USER, so the app process does not run as root inside the container. A final stage FROM scratch that holds nothing but COPY lines (a static site's build output) runs nothing and passes; a Dockerfile with line continuations, heredocs or an escape directive gets no such exception, since this check reads lines, not Docker's instructions", "severity": "medium", "evaluator": "sh", "sh": "[ -f Dockerfile ] || exit 0; if ! grep -q '[\\\\`][[:space:]]*$' Dockerfile && ! grep -q '<<' Dockerfile && ! grep -qi '^#[[:space:]]*escape' Dockerfile; then r=$(awk 'toupper($1)==\"FROM\"{b=tolower($2);ok=1;next} /^[[:space:]]*(#|$)/{next} toupper($1)!=\"COPY\"{ok=0} END{print b \" \" ok}' Dockerfile); set -- $r; [ \"$1\" = scratch ] && [ \"$2\" = 1 ] && exit 0; fi; u=$(awk 'toupper($1)==\"FROM\"{u=\"\"} toupper($1)==\"USER\"{u=$2} END{print u}' Dockerfile); case \"$u\" in ''|root|0|root:*|0:*) echo \"the final stage's USER is '${u:-unset}': the app runs as root\"; exit 1;; esac", "title": "Run the application without root privileges"}, {"name": "runtime-applies-os-updates", "expects": "the Dockerfile's final (runtime) stage applies the distribution's available package updates when the image is built (apt-get upgrade, apk upgrade, dnf or microdnf upgrade), or starts from a base with no package manager (scratch, distroless, chiselled), or the Dockerfile or README says why it does neither; an upgrade only in a build stage does not count", "severity": "medium", "evaluator": "llm", "scope": "tree", "files": ["**/Dockerfile", "README.md"], "rubric": "Unknown evidence first, before any pass: when the evidence says files were omitted or capped, or a Dockerfile is truncated before its final stage, or its final stage cannot be told, answer unclear, never pass. Otherwise judge each Dockerfile in the evidence by its FINAL stage: the instructions after its last FROM line. When that FROM names an earlier stage (FROM debian:bookworm-slim AS base ... FROM base), that stage's RUN lines are part of the final stage too; a stage the final one only copies from (COPY --from) is not. A base given by a build argument is judged by the ARG's default; with no default, answer unclear. PASS when any of these holds: (1) a RUN in the final stage upgrades the installed OS packages: apt-get update then apt-get upgrade (or apt upgrade, apt-get dist-upgrade), apk upgrade, dnf upgrade, microdnf upgrade, or yum update. Installing named packages (apt-get install, apk add) is not an upgrade. (2) The final base ships no package manager: FROM scratch, a distroless image (gcr.io/distroless/...), or a chiselled image (a tag containing chiseled). (3) A comment in the Dockerfile or a line in README.md gives a concrete reason the final stage does not upgrade, such as a base pinned by digest and raised by a named automated updater; a bare \"no upgrade\" without a reason does not count. FAIL when the final stage starts from a base that has a package manager (debian, ubuntu, alpine, a language's slim or alpine image, nginx, caddy, eclipse-temurin, the .NET aspnet image, and the like) and none of the three holds. An upgrade only in a build stage does not count: COPY --from copies files and leaves the runtime base's packages as they were. Removing the package cache is judged by dockerfile-lint, not here. When the evidence is complete and holds no Dockerfile, pass: there is no image to judge. The note is one sentence giving the reason; the evidence cites file:line of the final FROM and, for a pass, of the upgrade or the stated reason.", "title": "Apply OS package updates in the runtime image"}]
---
When your app ships as a container image (`build: .` in the contract, so the
forge builds your repo Dockerfile), write the Dockerfile to **run as a
non-root user**. A container with no `USER` runs as root, which the source
scan flags (`dockerfile.security.missing-user`) and which is a real hazard —
a compromised process then has root inside the container.

- Create an unprivileged user and switch to it before the entrypoint:
  ```dockerfile
  RUN useradd --uid 10001 --create-home app   # or: adduser -D -u 10001 app (alpine)
  USER app
  ```
  Put the `USER` line after you've installed dependencies and copied files
  (those steps need write access) and before `CMD`/`ENTRYPOINT`, so the app
  itself runs unprivileged. Many official base images ship a ready-made
  non-root user (`node`, `nginx`); use it rather than inventing one.

- Anything the running user must write inside the image (a cache, a
  build output) is set up while still root — `chown` the paths to the
  user — then drop to `USER`. A `dataDir` in the contract is different:
  the forge mounts it and hands it to your image's `USER` before the app
  starts, so it is already yours. Never keep a root entrypoint to `chown`
  it (waypoint did, 2026-09-08); declare the `USER` and write to it.
  A port below 1024 is fine inside a container; no capability is needed.

- Multi-stage builds: the non-root `USER` belongs in the FINAL runtime stage.
  Earlier build stages can stay root — they don't ship.

This is a stock convention; a project with a genuine need to run as root
(rare) forks this guidance in its own pack and says why.

**Build in one stage, run from another.** The image that compiles the app
is not the image that runs it. A build stage `FROM` the toolchain image
makes the binary, jar or published output; the final stage starts from a
runtime image and gets only that output, with `COPY --from=build`. A
toolchain image (`rust`, `golang`, `maven`, the .NET SDK, the full `node`
or `python` image) carries a compiler, headers, a package manager and a
whole Debian the app never uses:

- size: hello-rust ran from `rust:1.84.1`, 1.47 GB; the same binary on
  `debian:bookworm-slim` is 110 MB, on distroless/cc 35 MB;
- attack surface: a compiler and a package manager are tools for anyone
  who gets into the process;
- scan noise: each of those packages brings its own advisories to the
  image scan (hello-rust: 1,248 high or critical, against 13 and 1).

Interpreted stacks run from the language's slim image in both stages and
leave compilers, pip and npm in the build stage. Each stack's selection
guidance names its build and runtime images, and its seeded Dockerfile
shows the recipe. On the compiled stacks (Rust, Go, Java, .NET, Godot)
the check `runtime-stage-not-build-image` fails a final stage `FROM` the
image the pack's seed builds with; it reads the `FROM` lines, so a base
given by a build argument is not judged.

Two checks hold this. `runs-as-non-root` reads the last `USER` line of the
Dockerfile. `dockerfile-lint` runs hadolint at the warning threshold: a
base image pinned to a tag (never `latest`), `COPY` rather than `ADD`, an
absolute `WORKDIR`, `CMD`/`ENTRYPOINT` in JSON form so signals reach the
process, and no package-manager cache left in a layer. Pinning every
distro package to a version is not asked; the base tag is. A repository
with no Dockerfile (a pushed `image:` or a static site) passes both, and
so does a final stage `FROM scratch` made only of `COPY` lines: it holds
only files, such as a static site's build output, and runs nothing. The
check reads lines, not Docker's parsed instructions, so a Dockerfile with a
line continuation, a heredoc or an escape directive gets no such
exception and must declare its `USER` as any other.

**Base images and the image scan.** The landing scans the built image and
refuses any vulnerability that has a fixed release. A base image is rebuilt
on its publisher's schedule, so its packages lag the distribution's fixes.
The final stage takes those fixes when the image is built: one `RUN`, as
root and before `USER`, that leaves no package cache behind:

```dockerfile
# Debian, Ubuntu (debian:*-slim, python/node/ruby:*-slim, eclipse-temurin, dotnet/aspnet)
RUN apt-get update && apt-get upgrade -y && rm -rf /var/lib/apt/lists/*
# Alpine (alpine, *-alpine, caddy:*-alpine, nginx:*-alpine)
RUN apk upgrade --no-cache
# Fedora, RHEL, UBI (ubi-minimal has microdnf)
RUN dnf upgrade -y && dnf clean all
RUN microdnf upgrade -y && microdnf clean all
```

An upgrade in a build stage does not reach the final image: `COPY --from`
copies files, not the build stage's packages. A base with no package
manager (`scratch`, `gcr.io/distroless/…`, a chiselled Ubuntu image) has
nothing to upgrade; its fixes come with a newer base tag. A project that
keeps its base fresh another way, such as a digest raised by an automated
updater, says so in a comment beside the final `FROM`. The rubric
`runtime-applies-os-updates` reads the final stage and passes on any of
these three; a model judges it, so it flags for a person to confirm and
never blocks a landing (the image scan does). A language library is fixed
by raising its version in the app's own manifest.

**When a finding cannot be fixed.** A finding with no fixed release, or
one that does not apply to how the app runs, is recorded as an exception
by a person, not worked around: on the project's Security tab, open the
finding and record a disposition — accepted risk or false positive — with
a justification and an expiry. The scan gate honours it, and the finding
shows again when it expires. (The same record can be made with
`POST /api/v1/projects/<slug>/dispositions` and a body of `identifier`
(the CVE), `package`, `status` (`accepted` or `false_positive`), `justification` and
`expiresAt`; there is
no CLI command for it.) An agent reports the finding and the fix it needs;
it never records the exception itself.
