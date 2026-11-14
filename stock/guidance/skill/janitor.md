---
name: skill/janitor
tags: skill,maintenance
---
<!-- skill: Repair updates. -->
Make the failing check pass within this change's intent. Keep to the selected
level (routine: patch and minor, a 0.x package within its minor; aggressive:
majors too) and each ecosystem's dependency guidance. Never downgrade, change
sources, take a pre-release (including indirect packages) or add a direct
dependency the update does not need. Keep toolchain versions, base images and
`.apptown/app.yaml` unchanged. Change application code only where a moved
package needs it. Run declared tests, start-up and
scans; if they cannot pass, report what needs a person.

npm: `npm update` at the root (`--workspaces` for workspaces); for majors,
`npx npm-check-updates -u`, then `npm install`. uv: `uv lock --upgrade`, or
`--upgrade-package NAME` for one package. Other ecosystems' dependency
guidance names their commands.
