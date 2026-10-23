---
name: build/container-image
tags: build,container,security
checks: [{"name": "dockerfile-lint", "expects": "hadolint finds nothing at warning or above in the Dockerfile: a pinned base-image tag (never latest), COPY not ADD, an absolute WORKDIR, JSON-form CMD/ENTRYPOINT, and no package cache left in a layer — the image AppTown builds is what runs", "severity": "medium", "evaluator": "tool", "tool": "hadolint", "title": "Resolve Dockerfile warnings"}, {"name": "runs-as-non-root", "expects": "the final stage of the Dockerfile switches to an unprivileged USER, so the app process does not run as root inside the container. A final stage FROM scratch that holds nothing but COPY lines (a static site's build output) runs nothing and passes; a Dockerfile with line continuations, heredocs or an escape directive gets no such exception, since this check reads lines, not Docker's instructions", "severity": "medium", "evaluator": "sh", "sh": "[ -f Dockerfile ] || exit 0; if ! grep -q '[\\\\`][[:space:]]*$' Dockerfile && ! grep -q '<<' Dockerfile && ! grep -qi '^#[[:space:]]*escape' Dockerfile; then r=$(awk 'toupper($1)==\"FROM\"{b=tolower($2);ok=1;next} /^[[:space:]]*(#|$)/{next} toupper($1)!=\"COPY\"{ok=0} END{print b \" \" ok}' Dockerfile); set -- $r; [ \"$1\" = scratch ] && [ \"$2\" = 1 ] && exit 0; fi; u=$(awk 'toupper($1)==\"FROM\"{u=\"\"} toupper($1)==\"USER\"{u=$2} END{print u}' Dockerfile); case \"$u\" in ''|root|0|root:*|0:*) echo \"the final stage's USER is '${u:-unset}': the app runs as root\"; exit 1;; esac", "title": "Run the application without root privileges"}, {"name": "no-volume-outside-data-dir", "expects": "the Dockerfile's final stage declares no VOLUME, except at the dataDir a part declares in .apptown/app.yaml (only the public part may), where AppTown mounts the app's named data volume. A VOLUME anywhere else makes Docker create a new unnamed volume for every container started from the image (checks, previews, releases), which AppTown does not back up or move. A VOLUME at the dataDir still gives an unnamed volume to containers without that mount (an image check, a worker built from the same context); a pass means the rule is met, not that none can appear. Judged from the instructions as Docker reads them (continued lines, comments and a heredoc's body; a stage named in FROM brings its VOLUMEs); a VOLUME inherited from the base image, and Dockerfiles in other build contexts, are not judged; a VOLUME path given by a variable, an instruction with two heredocs, a heredoc whose end is never found, or an app: section written in braces, or a part written in braces that names dataDir or spans lines, is reported as not judged (the dataDir is read in block form only)", "severity": "medium", "evaluator": "sh", "sh": "[ -f Dockerfile ] || exit 0; vols=$(awk 'function inst(i, s, a, d, c, n, k, v, p) { if (toupper($1) == \"FROM\") { if (name != \"\") sv[name] = cur; i = 2; while ($i ~ /^--/) i++; a = tolower($i); cur = (a in sv) ? sv[a] : \"\"; name = (toupper($(i+1)) == \"AS\") ? tolower($(i+2)) : \"\" } else if (toupper($1) == \"VOLUME\") { v = $0; sub(/^[ \\t]*[^ \\t]+[ \\t]*/, \"\", v); sub(/[ \\t]+$/, \"\", v); if (v ~ /^\\[/) { gsub(/^\\[|\\]$/, \"\", v); n = split(v, p, \",\"); for (k = 1; k <= n; k++) { gsub(/^[ \\t]*\"|\"[ \\t]*$/, \"\", p[k]); add(p[k]) } } else { n = split(v, p, /[ \\t]+/); for (k = 1; k <= n; k++) add(p[k]) } } else if (toupper($1) ~ /^(RUN|COPY|ADD)$/ && match($0, /(^|[^<])<<-?[ \\t]*[\"\\047]?[A-Za-z_][^ \\t\"\\047<>|;&]*/)) { d = substr($0, RSTART, RLENGTH); c = $0; if (gsub(/(^|[^<])<<-?[ \\t]*[\"\\047]?[A-Za-z_]/, \"\", c) > 1) many = 1; sub(/^[^<]?<</, \"\", d); hs = (d ~ /^-/); sub(/^-?[ \\t]*[\"\\047]?/, \"\", d); hd = d } } function add(x) { if (x == \"\") return; if (x != \"/\") sub(/\\/+$/, \"\", x); cur = cur x \"\\n\" } { sub(/\\r$/, \"\") } hd != \"\" { t = $0; if (hs) sub(/^\\t+/, \"\", t); if (t == hd) hd = \"\"; next } /^[ \\t]*#/ { next } b != \"\" && /^[ \\t]*$/ { next } /\\\\[ \\t]*$/ { sub(/\\\\[ \\t]*$/, \"\"); b = b $0 \" \"; next } { $0 = b $0; b = \"\"; inst() } END { if (b != \"\") { $0 = b; inst() } if (many || hd != \"\") print \"<<\"; else printf \"%s\", cur }' Dockerfile); [ \"$vols\" = \"<<\" ] && { echo 'not judged: a heredoc whose end this check cannot find (heredoc-like text inside a quoted string reads as one), or an instruction with more than one heredoc'; exit 2; }; [ -n \"$vols\" ] || exit 0; dd=''; [ -f .apptown/app.yaml ] && dd=$(awk 'function val(v) { sub(/[ \\t]+#.*$/, \"\", v); sub(/^[ \\t]+/, \"\", v); sub(/[ \\t]+$/, \"\", v); gsub(/^[\"\\047]|[\"\\047]$/, \"\", v); if (v != \"/\") sub(/\\/+$/, \"\", v); print v; exit } { sub(/\\r$/, \"\") } /^[ \\t]*(#|$)/ { next } /^[^ \\t]/ { inapp = ($0 ~ /^app[ \\t]*:/); if (inapp && $0 !~ /^app[ \\t]*:[ \\t]*(#.*)?$/) { print \"?\"; exit } pind = 0; find = 0; next } !inapp { next } { match($0, /^[ \\t]*/); ind = RLENGTH; l = $0 } pind == 0 || ind <= pind { pind = ind; find = 0; r = $0; sub(/^[ \\t]*[^:]*:[ \\t]*/, \"\", r); if (r ~ /^[{\\[]/ && ($0 ~ /dataDir/ || r !~ /[}\\]][ \\t]*(#.*)?$/)) { print \"?\"; exit } next } find == 0 { find = ind } ind == find && l ~ /^[ \\t]*dataDir[ \\t]*:/ { sub(/^[ \\t]*dataDir[ \\t]*:/, \"\", l); val(l) }' .apptown/app.yaml); [ \"$dd\" = '?' ] && { echo 'not judged: this check reads a part'\"'\"'s dataDir only in block form, one field per line, and .apptown/app.yaml writes the app: section, or a part that names dataDir, in braces. Write it in block form to have it judged'; exit 2; }; case $vols in *'$'*) echo \"not judged: a VOLUME names its path through a variable ($(echo \"$vols\" | grep '\\$' | head -1))\"; exit 2;; esac; bad=0; for v in $vols; do [ \"$v\" = \"$dd\" ] && continue; bad=1; if [ -z \"$dd\" ]; then echo \"the Dockerfile declares VOLUME $v and .apptown/app.yaml declares no dataDir. Docker makes a new unnamed volume for each container started from this image, and AppTown does not back it up or move it. Remove the VOLUME line. If the app keeps files in $v, declare dataDir: $v on the public part instead.\"; else echo \"the Dockerfile declares VOLUME $v, which is not the app's dataDir ($dd). Docker makes a new unnamed volume for each container started from this image, and AppTown does not back it up or move it. Remove the VOLUME line and keep files the app must keep in $dd.\"; fi; done; exit $bad", "occurrences": "lines", "title": "Keep data in the declared data directory, not a Docker VOLUME"}, {"name": "runtime-applies-os-updates", "expects": "the Dockerfile's final (runtime) stage applies the distribution's available package updates when the image is built (apt-get upgrade, apk upgrade, dnf or microdnf upgrade), or starts from a base with no package manager (scratch, distroless, chiselled), or the Dockerfile or README says why it does neither; an upgrade only in a build stage does not count", "severity": "medium", "evaluator": "llm", "scope": "tree", "files": ["**/Dockerfile", "README.md"], "rubric": "Unknown evidence first, before any pass: when the evidence says files were omitted or capped, or a Dockerfile is truncated before its final stage, or its final stage cannot be told, answer unclear, never pass. Otherwise judge each Dockerfile in the evidence by its FINAL stage: the instructions after its last FROM line. When that FROM names an earlier stage (FROM debian:bookworm-slim AS base ... FROM base), that stage's RUN lines are part of the final stage too; a stage the final one only copies from (COPY --from) is not. A base given by a build argument is judged by the ARG's default; with no default, answer unclear. PASS when any of these holds: (1) a RUN in the final stage upgrades the installed OS packages: apt-get update then apt-get upgrade (or apt upgrade, apt-get dist-upgrade), apk upgrade, dnf upgrade, microdnf upgrade, or yum update. Installing named packages (apt-get install, apk add) is not an upgrade. (2) The final base ships no package manager: FROM scratch, a distroless image (gcr.io/distroless/...), or a chiselled image (a tag containing chiseled). (3) A comment in the Dockerfile or a line in README.md gives a concrete reason the final stage does not upgrade, such as a base pinned by digest and raised by a named automated updater; a bare \"no upgrade\" without a reason does not count. FAIL when the final stage starts from a base that has a package manager (debian, ubuntu, alpine, a language's slim or alpine image, nginx, caddy, eclipse-temurin, the .NET aspnet image, and the like) and none of the three holds. An upgrade only in a build stage does not count: COPY --from copies files and leaves the runtime base's packages as they were. Removing the package cache is judged by dockerfile-lint, not here. When the evidence is complete and holds no Dockerfile, pass: there is no image to judge. The note is one sentence giving the reason; the evidence cites file:line of the final FROM and, for a pass, of the upgrade or the stated reason.", "title": "Apply OS package updates in the runtime image"}]
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
in a need, which AppTown names, backs up and moves. A `VOLUME` line makes
Docker create a new unnamed volume for every container started from the
image, so leave it out. A `VOLUME` at the `dataDir` path is allowed.

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

**A finding that cannot be fixed** (no fixed release, or not applicable) is
recorded as an exception by a person on the project's Security tab. Report it
and the fix it needs; never record the exception yourself or work around the
scan.
