---
name: lang/selection/go-for-services
tags: lang,go
checks: [{"name": "lockfile-in-step", "expects": "go.mod and go.sum match what the code imports (go mod tidy leaves no diff), so a build resolves exactly what is committed", "severity": "medium", "evaluator": "tool", "tool": "go-mod-tidy", "title": "Keep the dependency lockfile in sync", "appliesTo": ["tool:go"]}, {"name": "runtime-stage-not-build-image", "expects": "the Dockerfile's final stage is not FROM golang (any tag), the image this pack's seed builds with: a build stage makes the artifact and the final stage starts from gcr.io/distroless/static-debian12:nonroot with only that artifact copied in. Judged from the FROM instructions as Docker reads them (continued lines, comments and a heredoc's body; a stage name resolves to the image it names); a base image given by a build argument is not judged, an instruction with two heredocs, or heredoc-like text inside a quoted shell string whose end is never found, is reported as not judged", "severity": "high", "evaluator": "sh", "sh": "[ -f Dockerfile ] || { w=\"part $APPTOWN_PARTS_HERE is\"; case \"$APPTOWN_PARTS_HERE\" in *\" \"*) w=\"parts $APPTOWN_PARTS_HERE are\";; esac; d=$APPTOWN_FOLDER; [ \"$d\" = . ] && d=\"the repository root\"; echo \"$w built with go (tools: in .apptown/app.yaml) and there is no Dockerfile in $d to build its image: add it, or correct the part's tools\"; exit 1; }; img=$(awk 'function inst(i, s, a, d, c) { if (toupper($1) == \"FROM\") { i = 2; while ($i ~ /^--/) i++; s = $i; a = tolower(s); if (a in st) s = st[a]; if (toupper($(i+1)) == \"AS\") st[tolower($(i+2))] = s; last = s } else if (toupper($1) ~ /^(RUN|COPY|ADD)$/ && match($0, /(^|[^<])<<-?[ \\t]*[\"\\047]?[A-Za-z_][^ \\t\"\\047<>|;&]*/)) { d = substr($0, RSTART, RLENGTH); c = $0; if (gsub(/(^|[^<])<<-?[ \\t]*[\"\\047]?[A-Za-z_]/, \"\", c) > 1) many = 1; sub(/^[^<]?<</, \"\", d); hs = (d ~ /^-/); sub(/^-?[ \\t]*[\"\\047]?/, \"\", d); hd = d } } { sub(/\\r$/, \"\") } hd != \"\" { t = $0; if (hs) sub(/^\\t+/, \"\", t); if (t == hd) hd = \"\"; next } /^[ \\t]*#/ { next } b != \"\" && /^[ \\t]*$/ { next } /\\\\[ \\t]*$/ { sub(/\\\\[ \\t]*$/, \"\"); b = b $0 \" \"; next } { $0 = b $0; b = \"\"; inst() } END { if (b != \"\") { $0 = b; inst() } if (many || hd != \"\") print \"<<\"; else print last }' Dockerfile); [ \"$img\" = \"<<\" ] && { echo 'not judged: a heredoc whose end this check cannot find (heredoc-like text inside a quoted string reads as one), or an instruction with more than one heredoc'; exit 2; }; r=${img%@*}; case ${r##*/} in *:*) r=${r%:*};; esac; r=${r#docker.io/}; r=${r#library/}; [ \"$r\" = golang ] || exit 0; echo \"the final stage runs FROM $img, the image your pack's seed builds with: its compiler, toolchain and their known vulnerabilities ship with the app. Build in a stage FROM golang, then copy only the binary (CGO_ENABLED=0) into a final stage FROM gcr.io/distroless/static-debian12:nonroot (lang/selection/go-for-services shows the recipe)\"; exit 1", "title": "Run the application from a runtime image, not the build image", "appliesTo": ["tool:go"]}]
---
Anything that serves, daemonizes, or ships as a binary is Go. Single static
binary, cross-compilation, stdlib HTTP, one formatter.

Deviate by: data/ML → python-for-data; web UI → typescript-for-web; no-GC memory
safety as hard requirement → Rust via the innovation token, with ADR.

Build in a stage `FROM golang` with `CGO_ENABLED=0` and ship only the
binary on `gcr.io/distroless/static-debian12:nonroot` (the go-small
seed: about 15 MB, no shell, no package manager; its build writes the
extra certificate authorities into `/etc/ssl/certs`, as build/container-image
says). Use `alpine` or
`debian:bookworm-slim` when the app needs a shell or cgo. Never run from
the `golang` image: it is the compiler, its module cache and a full
Debian.

Dependencies are what the files say: `go mod tidy` leaves no diff, so
`go.mod` and `go.sum` name exactly what the code imports and a clean
build resolves the same thing everywhere.

**Fits a container limit.** A Go service needs nothing set; a small one uses
5–15 MB, so declare `resources: { memory: 48Mi }` in `.apptown/app.yaml` and
raise it when the deployment's card shows it near the limit.
deploy/memory-declared says how to choose and check it.
