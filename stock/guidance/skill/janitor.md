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
For dependency updates and security fixes, keep `.apptown/app.yaml` unchanged.
For a pack adoption, add or change only the declarations in `.apptown/app.yaml`
that the new pack's failing checks name.

Move external Dockerfile FROM images within the selected level, keeping the
same image name and tag variant (`-alpine`, `-slim`, `-bookworm-slim`,
`-alpine3.22` stay). List the image's tags from its registry: for Docker Hub
library images `https://hub.docker.com/v2/repositories/library/<name>/tags?name=<version>`
(follow pagination), or `crane ls` or `skopeo list-tags` when present. Pin a
newer build of the same tag by digest. For a security target choose the
smallest release whose image no longer has the finding. Leave stage
references, scratch and operands containing `$` unchanged.
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

Your final message is what AppTown judges; files you write and do not commit
are not seen. For every part, give the command you ran to list newer releases
and the lines of its output that show the result; for every FROM, give the
tag listing you read. Say when a command could not run; that is not proof
that nothing can move.
