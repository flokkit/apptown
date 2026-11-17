---
name: skill/janitor
tags: skill,maintenance
---
<!-- skill: Repair updates. -->
Make the failing check pass within this change's intent. Keep to the selected
level (routine: patch and minor, a 0.x package within its minor; aggressive:
majors too) and each ecosystem's dependency guidance. Never downgrade, change
sources, make a direct dependency a pre-release, pin an indirect package to
one, or add a direct dependency the update does not need. An indirect
pre-release that a stable direct dependency itself requires is allowed.
For dependency updates and security fixes, change only an existing
`staticServer:` in `.apptown/app.yaml`, and only when base image updates are
on; they never add a missing one.
For a pack adoption, add or change only the declarations in `.apptown/app.yaml`
that the new pack's failing checks name.

Move external Dockerfile FROM images on their declared toolchain line, keeping the
same image name and tag variant (`-alpine`, `-slim`, `-bookworm-slim`,
`-alpine3.22` stay). For each external FROM and `staticServer:`, run
read numeric tool versions from mise, .tool-versions and version files before
the change. A tag and a declaration agree when their shared numeric components
match (for example, node:24-slim and node = "24.22.2"). Use the longest agreeing
declaration: run `apptown image newest <repo>:<declared><variant> --scope line`
(for that example, `node:24.22.2-slim --scope line`). Match by version, not image
name: an unrelated image sharing that version stays on the same line too; say
so in your report. With no agreeing numeric declaration, run
`apptown image newest <current> --scope minor` at routine, or `--scope major`
at aggressive. Choose the printed tag and digest. AppTown permits any registry
build on the allowed line that is not older than the current pin; it proves
the candidate tag's digest with a registry HEAD. If the lookup refuses,
change nothing for that image and say so. Record its registry reading and any
stale-reading warning in the final message. For a security target, use
`--scope patch` when a smaller move clears the finding; a security fix may
choose the smallest digest-pinned build that clears it. Leave stage references,
scratch and operands containing `$` unchanged.
Toolchain versions stay unchanged for dependency updates. For a security finding
in the toolchain itself, use the smallest patch release within its minor that
fixes it, updating every declaration, including mise, version files, go.mod's
toolchain and the build image. Change application code only where a moved
package needs it. Run declared tests, start-up and
scans; if they cannot pass, report what needs a person.

npm: `npm update` in every package folder (`--workspaces` for workspaces)
moves lockfile entries, including indirect packages, within declared ranges;
`npx npm-check-updates -t minor` (any target at aggressive) shows manifest
moves, applied with `-u` and `npm install`; report `npm outdated --all`. uv:
`uv lock --upgrade`, or `--upgrade-package NAME` for one package; report
`uv tree --outdated`. Other ecosystems' dependency guidance names their
commands.

Your final message is what AppTown judges; write no report file.
Everything AppTown judges goes in the final message. When nothing can move,
start it with one sentence saying why, naming the image or package (for example
"Waiting for a newer python:3.12-slim build: the newest build still has CVE-…").
For every part that declares dependencies, give the command you ran to list
newer releases and the lines of its output that show the result; for every
external FROM and `staticServer:`, give the resolver's answer and any refusal.
A part with no manifest or image input needs no listing. Say when a command
could not run.
