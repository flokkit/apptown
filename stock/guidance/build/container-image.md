---
name: build/container-image
tags: build,container,security
checks: [{"name": "dockerfile-lint", "expects": "hadolint finds nothing at warning or above in the Dockerfile: a pinned base-image tag (never latest), COPY not ADD, an absolute WORKDIR, JSON-form CMD/ENTRYPOINT, and no package cache left in a layer — the image AppTown builds is what runs", "severity": "medium", "evaluator": "tool", "tool": "hadolint", "title": "Resolve Dockerfile warnings", "appliesTo": ["build"]}, {"name": "runs-as-non-root", "expects": "the final stage of the Dockerfile switches to an unprivileged USER, so the app process does not run as root inside the container. A final stage FROM scratch that holds nothing but COPY lines (a static site's build output) runs nothing and passes; a Dockerfile with line continuations, heredocs or an escape directive gets no such exception, since this check reads lines, not Docker's instructions", "severity": "medium", "evaluator": "sh", "sh": "[ -f Dockerfile ] || { w=\"part $APPTOWN_PARTS_HERE is\"; case \"$APPTOWN_PARTS_HERE\" in *\" \"*) w=\"parts $APPTOWN_PARTS_HERE are\";; esac; d=$APPTOWN_FOLDER; [ \"$d\" = . ] && d=\"the repository root\"; echo \"$w built from the Dockerfile in $d (build: in .apptown/app.yaml) and there is no Dockerfile there: add it, or correct the part's build folder\"; exit 1; }; if ! grep -q '[\\\\`][[:space:]]*$' Dockerfile && ! grep -q '<<' Dockerfile && ! grep -qi '^#[[:space:]]*escape' Dockerfile; then r=$(awk 'toupper($1)==\"FROM\"{b=tolower($2);ok=1;next} /^[[:space:]]*(#|$)/{next} toupper($1)!=\"COPY\"{ok=0} END{print b \" \" ok}' Dockerfile); set -- $r; [ \"$1\" = scratch ] && [ \"$2\" = 1 ] && exit 0; fi; u=$(awk 'toupper($1)==\"FROM\"{u=\"\"} toupper($1)==\"USER\"{u=$2} END{print u}' Dockerfile); case \"$u\" in ''|root|0|root:*|0:*) echo \"the final stage's USER is '${u:-unset}': the app runs as root\"; exit 1;; esac", "title": "Run the application without root privileges", "appliesTo": ["build"]}, {"name": "no-volume", "expects": "the Dockerfile's final stage declares no VOLUME. AppTown mounts the app's data folder itself (the public part's dataDir in .apptown/app.yaml), and any VOLUME, even at that path, makes Docker create a new unnamed volume in every container started from the image without that mount (checks, previews, image checks, a worker built from the same folder), which AppTown does not back up or move. Judged from the instructions as Docker reads them (continued lines, comments and a heredoc's body; a stage named in FROM brings its VOLUMEs); a VOLUME inherited from the base image, and Dockerfiles in other build contexts, are not judged; an instruction with two heredocs, or a heredoc whose end is never found, is reported as not judged", "severity": "medium", "evaluator": "sh", "sh": "[ -f Dockerfile ] || { w=\"part $APPTOWN_PARTS_HERE is\"; case \"$APPTOWN_PARTS_HERE\" in *\" \"*) w=\"parts $APPTOWN_PARTS_HERE are\";; esac; d=$APPTOWN_FOLDER; [ \"$d\" = . ] && d=\"the repository root\"; echo \"$w built from the Dockerfile in $d (build: in .apptown/app.yaml) and there is no Dockerfile there: add it, or correct the part's build folder\"; exit 1; }; vols=$(awk 'function inst(i, s, a, d, c, n, k, v, p) { if (toupper($1) == \"FROM\") { if (name != \"\") sv[name] = cur; i = 2; while ($i ~ /^--/) i++; a = tolower($i); cur = (a in sv) ? sv[a] : \"\"; name = (toupper($(i+1)) == \"AS\") ? tolower($(i+2)) : \"\" } else if (toupper($1) == \"VOLUME\") { v = $0; sub(/^[ \\t]*[^ \\t]+[ \\t]*/, \"\", v); sub(/[ \\t]+$/, \"\", v); if (v ~ /^\\[/) { gsub(/^\\[|\\]$/, \"\", v); n = split(v, p, \",\"); for (k = 1; k <= n; k++) { gsub(/^[ \\t]*\"|\"[ \\t]*$/, \"\", p[k]); add(p[k]) } } else { n = split(v, p, /[ \\t]+/); for (k = 1; k <= n; k++) add(p[k]) } } else if (toupper($1) ~ /^(RUN|COPY|ADD)$/ && match($0, /(^|[^<])<<-?[ \\t]*[\"\\047]?[A-Za-z_][^ \\t\"\\047<>|;&]*/)) { d = substr($0, RSTART, RLENGTH); c = $0; if (gsub(/(^|[^<])<<-?[ \\t]*[\"\\047]?[A-Za-z_]/, \"\", c) > 1) many = 1; sub(/^[^<]?<</, \"\", d); hs = (d ~ /^-/); sub(/^-?[ \\t]*[\"\\047]?/, \"\", d); hd = d } } function add(x) { if (x == \"\") return; if (x != \"/\") sub(/\\/+$/, \"\", x); cur = cur x \"\\n\" } { sub(/\\r$/, \"\") } hd != \"\" { t = $0; if (hs) sub(/^\\t+/, \"\", t); if (t == hd) hd = \"\"; next } /^[ \\t]*#/ { next } b != \"\" && /^[ \\t]*$/ { next } /\\\\[ \\t]*$/ { sub(/\\\\[ \\t]*$/, \"\"); b = b $0 \" \"; next } { $0 = b $0; b = \"\"; inst() } END { if (b != \"\") { $0 = b; inst() } if (many || hd != \"\") print \"<<\"; else printf \"%s\", cur }' Dockerfile); [ \"$vols\" = \"<<\" ] && { echo 'not judged: a heredoc whose end this check cannot find (heredoc-like text inside a quoted string reads as one), or an instruction with more than one heredoc'; exit 2; }; [ -n \"$vols\" ] || exit 0; echo \"$vols\" | while IFS= read -r v; do echo \"the Dockerfile declares VOLUME $v. Remove the VOLUME line. AppTown mounts the app's data folder itself; if the app keeps data at that path, declare it as dataDir in .apptown/app.yaml.\"; done; exit 1", "occurrences": "lines", "title": "Leave VOLUME out of the Dockerfile", "appliesTo": ["build"]}, {"name": "runtime-applies-os-updates", "expects": "the Dockerfile's final (runtime) stage applies the distribution's available package updates when the image is built (apt-get upgrade, apk upgrade, dnf or microdnf upgrade), or starts from a base with no package manager (scratch, distroless, chiselled), or the Dockerfile or README says why it does neither; an upgrade only in a build stage does not count", "severity": "medium", "evaluator": "llm", "scope": "tree", "files": ["**/Dockerfile", "README.md", ".apptown/app.yaml"], "rubric": "Unknown evidence first, before any pass: when the evidence says files were omitted or capped, or a Dockerfile is truncated before its final stage, or its final stage cannot be told, answer unclear, never pass. Otherwise judge each Dockerfile in the evidence by its FINAL stage: the instructions after its last FROM line. When that FROM names an earlier stage (FROM debian:bookworm-slim AS base ... FROM base), that stage's RUN lines are part of the final stage too; a stage the final one only copies from (COPY --from) is not. A base given by a build argument is judged by the ARG's default; with no default, answer unclear. PASS when any of these holds: (1) a RUN in the final stage upgrades the installed OS packages: apt-get update then apt-get upgrade (or apt upgrade, apt-get dist-upgrade), apk upgrade, dnf upgrade, microdnf upgrade, or yum update. Installing named packages (apt-get install, apk add) is not an upgrade. (2) The final base ships no package manager: FROM scratch, a distroless image (gcr.io/distroless/...), or a chiselled image (a tag containing chiseled). (3) A comment in the Dockerfile or a line in README.md gives a concrete reason the final stage does not upgrade, such as a base pinned by digest and raised by a named automated updater; a bare \"no upgrade\" without a reason does not count. FAIL when the final stage starts from a base that has a package manager (debian, ubuntu, alpine, a language's slim or alpine image, nginx, caddy, eclipse-temurin, the .NET aspnet image, and the like) and none of the three holds. An upgrade only in a build stage does not count: COPY --from copies files and leaves the runtime base's packages as they were. Removing the package cache is judged by dockerfile-lint, not here. When the evidence is complete and a part that .apptown/app.yaml builds (build:, or static: with build:) has no Dockerfile in its folder, fail: the contract builds an image from a Dockerfile that is not there; name the part and the folder. The note is one sentence giving the reason; the evidence cites file:line of the final FROM and, for a pass, of the upgrade or the stated reason.", "title": "Apply OS package updates in the runtime image", "appliesTo": ["build"]}]
---
For a `build: .` part, write the Dockerfile so the app runs as a **non-root
user** in the final stage:

```dockerfile
RUN useradd --uid 10001 --create-home app   # or: adduser -D -u 10001 app (alpine)
USER app
```

- `USER` comes after installs and copies and before `CMD`/`ENTRYPOINT`. Use
  the base image's own non-root user where it has one (`node`, `nginx`).
- Paths the user must write inside the image (a cache, generated files) are
  `chown`ed while still root, then `USER`. A `dataDir` in the contract is
  mounted already owned by your `USER`: never keep a root entrypoint to
  `chown` it. A port below 1024 needs no capability.
- Only the final stage needs `USER`; build stages can stay root.

**Build in one stage, run from another.** A build stage `FROM` the toolchain
image (`rust`, `golang`, `maven`, the .NET SDK, the full `node` or `python`)
makes the output; the final stage starts from a runtime image and gets only
that output with `COPY --from=build`. Interpreted stacks use the language's
slim image in both stages and keep compilers, pip and npm in the build stage.
Each stack's selection guidance names its images; its seeded Dockerfile shows
the recipe.

**No `VOLUME`.** Files the app must keep go in the public part's `dataDir` or
in a need, which AppTown names, backs up and moves. AppTown mounts the
`dataDir` itself. A `VOLUME` line, even at that path, makes Docker create a
new unnamed volume in every container started without that mount, so leave
it out.

Pin the base image to a tag (never `latest`), `COPY` not `ADD`, an absolute
`WORKDIR`, `CMD`/`ENTRYPOINT` in JSON form, and no package-manager cache left
in a layer.

**OS updates.** The landing's image scan refuses any vulnerability with a fixed
release, and base images lag. In the final stage, as root and before `USER`,
take the distribution's fixes in one `RUN` that leaves no cache:

```dockerfile
# Debian, Ubuntu (debian:*-slim, python/node/ruby:*-slim, eclipse-temurin, dotnet/aspnet)
RUN apt-get update && apt-get upgrade -y && rm -rf /var/lib/apt/lists/*
# Alpine (alpine, *-alpine, caddy:*-alpine, nginx:*-alpine)
RUN apk upgrade --no-cache
# Fedora, RHEL, UBI (ubi-minimal has microdnf)
RUN dnf upgrade -y && dnf clean all
RUN microdnf upgrade -y && microdnf clean all
```

An upgrade in a build stage does not reach the final image. A base with no
package manager (`scratch`, distroless, chiselled Ubuntu) gets its fixes from a
newer tag; a project kept fresh by an automated digest updater says so in a
comment beside the final `FROM`. A language library is fixed by raising its
version in the app's manifest.

**Extra certificate authorities.** Behind a company's TLS-inspecting proxy,
AppTown gives a build the authorities it must trust as the build secret
`apptown-trust`, and the running app as the variable `APPTOWN_TRUST`. Each is
a script that adds them beside the image's own roots; with nothing
configured it does nothing, so the Dockerfile builds and runs the same
anywhere. Every `RUN` that downloads (a package manager, `curl`) mounts the
secret and runs it first, and the final command runs the variable first:

```dockerfile
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; npm ci
CMD ["sh", "-c", "eval \"${APPTOWN_TRUST:-}\"; exec \"$@\"", "sh", "node", "server.js"]
```

A `RUN` as a user other than root adds `,mode=0444` to the mount. A
`command:` in `.apptown/app.yaml` replaces `CMD` and runs the line itself if
the app calls out.

A final stage with no shell (distroless, `scratch`) cannot run the script, so
the build stage writes the certificates into the image instead: after the
script, `APPTOWN_TRUST_CERTS` names a file of them alone. Copy it in the same
`RUN` (the file goes when that line ends) to where the runtime reads extra
roots; Go reads every file in `/etc/ssl/certs`. Declare
`ARG APPTOWN_TRUST_SUM` before that line: AppTown sets it to the
certificates' checksum, and a secret is not part of the build cache's key, so
without it a rebuild keeps the certificates an earlier build cached. The
go-small starter does this:

```dockerfile
ARG APPTOWN_TRUST_SUM
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; mkdir -p /trust && if [ -n "${APPTOWN_TRUST_CERTS:-}" ]; then install -m 644 "$APPTOWN_TRUST_CERTS" /trust/apptown-trust.pem; fi
# in the final stage
COPY --from=build /trust/ /etc/ssl/certs/
```

With nothing configured `/trust` is empty and the image keeps only its own
roots. The image holds the certificates it was built with: a change reaches
it at its next build.

**A finding that cannot be fixed** (no fixed release, or not applicable) is
recorded as an exception by a person on the project's Security tab. Report it
and the fix it needs; never record the exception yourself or work around the
scan.
