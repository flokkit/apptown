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
match (for example, node:24-slim and node = "24.22.2"). A vendor version such
as java = "temurin-21.0.5+11.0.LTS" declares only its major, 21. Use the longest agreeing
declaration: run `apptown image newest <repo>:<declared><variant> --scope patch`
when Toolchain patches is on, `--scope line` when it is off (for that example,
`node:24.22.2-slim --scope patch`). The declaration decides the line at every
level: aggressive never moves a declared toolchain's image to a newer major or
minor, because that changes the toolchain and needs a person. Match by version, not image
name: an unrelated image sharing that version stays on the same line too. With no agreeing numeric declaration, run
`apptown image newest <current> --scope minor` at routine, or `--scope major`
at aggressive. Choose the printed tag and digest. AppTown permits any registry
build on the allowed line that is not older than the current pin; it proves
the candidate tag's digest with a registry HEAD and records its own reading.
If the lookup refuses, change nothing for that image and say so. For a security target, use
`--scope patch` when a smaller move clears the finding; a security fix may
choose the smallest digest-pinned build that clears it. Leave stage references,
scratch and operands containing `$` unchanged.
Toolchain versions change only when Toolchain patches is on, and then only to a
patch release within the same minor, updating every declaration together:
mise, version files, go.mod's toolchain line and the build image. A dependency
update moves each toolchain to its newest such patch; for a security finding in
the toolchain itself, use the smallest one that fixes it. Change application code only where a moved
package needs it. Run declared tests, start-up and
scans; if they cannot pass, say what needs a person.

npm: `npm update` in every package folder (`--workspaces` for workspaces)
moves lockfile entries, including indirect packages, within declared ranges;
`npx npm-check-updates -t minor` (any target at aggressive) shows manifest
moves, applied with `-u` and `npm install`; `npm outdated --all` shows what is left.
uv: `uv lock --upgrade`, or `--upgrade-package NAME` for one package;
`uv tree --outdated` shows what is left. Other ecosystems' dependency guidance names their
commands.

A dependency update moves everything it can within the level; a repair never
widens the change. Your final message is a note for
the people who read this change, not evidence: AppTown judges the change, its
tests, its start-up and its own scans. If you leave anything that could move
within the level, or a named finding unfixed, say in one line each what and why
(for example "Waiting for a newer python:3.12-slim build: the newest build
still has CVE-…"). Otherwise no explanation is needed. Write no report file.
