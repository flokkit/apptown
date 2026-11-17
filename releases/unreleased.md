# Unreleased changes

## Janitor settings

Choose what to keep updated: dependencies, security fixes, base images, toolchain
patches for security fixes, and packs. Saving records the person and time.
Projects retain their own update levels and deployments retain their release
settings. Changes to workflow versions or approval meanings keep these choices
in place; each Change records exactly what ran.

Migration 242 converts saved approvals into one current choices record and
preserves earlier revisions for attribution. On first start, a fresh installation
or an upgraded instance that has never saved choices gets routine patch and minor
dependency updates, security fixes, base images, toolchain patches for security
fixes and packs all on, set by AppTown. Saved choices are untouched. Turn choices
off in Maintenance settings, or turn the Janitor off for a project in its
Settings › General. Restore turns choices off before recovery. Live installations
receive the migration through the release's normal upgrade step. Accepted work
that still references an old approval cannot land after conversion; the next scheduled run starts
fresh work.

## Static sites pin their server

A static app now names the image that serves it in `.apptown/app.yaml`
(`staticServer: caddy:<version>-alpine@sha256:<digest>`), and the Janitor
moves that pin like any base image. AppTown no longer ships a Caddy version
for apps. New projects start on the newest build of each base-image line,
read from the registry when the project is created.

**Breaking:** release builds, previews and local container development refuse
a static part without the pin; running deployments keep running. **Operator
step, once, before the first static build after upgrading:** for each project
with a static part and no `staticServer:`, open a change adding
`staticServer: <answer>`, where the answer is the first line of
`apptown image newest caddy:2-alpine --scope line`, and let it land. The
Janitor's updates do not add a missing pin.

## HTTP API changes

`GET` and `PUT /api/v1/janitor/choices`,
`GET /api/v1/janitor/choices/history` and `POST /api/v1/janitor/assess`
replace the standing-approvals routes. Saving accepts `choices` and
`expectedRevision`; assessment accepts `run`. The Janitor view returns one
nullable `choices` record and `unknown`, replacing the rule lists and
changed-meaning diagnostics.

`GET /api/v1/image-lines?ref=…&scope=…` answers the newest stable build on an
image line, with the registry reading it came from.

## Command-line changes

`apptown image newest <ref> --scope <line|patch|minor|major>` prints that answer.

`apptown janitor choices`, `set`, `history` and `assess --run RUN` replace
list, preview, enable, disable and rule-specific history and assessment.
Set requires a dependency level and expected revision; boolean flags select the
other choices. Exact project-set and deployment consent are removed.

## Deprecations

None. These experimental interfaces change directly before 1.0.
