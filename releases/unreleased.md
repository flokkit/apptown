# Unreleased changes

## Janitor settings

Choose what to keep updated: dependencies, security fixes, base images, toolchain
patches for security fixes, and packs. Saving records the person and time.
Projects retain their own update levels and deployments retain their release
settings. Changes to workflow versions or approval meanings keep these choices
in place; each Change records exactly what ran.

Migration 242 converts saved approvals into one current choices record and
preserves earlier revisions for attribution. Installations without saved
approvals start with everything off. Live installations receive the migration
through the release's normal upgrade step. Accepted work that still references
an old approval cannot land after conversion; the next scheduled run starts
fresh work.

## HTTP API changes

`GET` and `PUT /api/v1/janitor/choices`,
`GET /api/v1/janitor/choices/history` and `POST /api/v1/janitor/assess`
replace the standing-approvals routes. Saving accepts `choices` and
`expectedRevision`; assessment accepts `run`. The Janitor view returns one
nullable `choices` record and `unknown`, replacing the rule lists and
changed-meaning diagnostics.

## Command-line changes

`apptown janitor choices`, `set`, `history` and `assess --run RUN` replace
list, preview, enable, disable and rule-specific history and assessment.
Set requires a dependency level and expected revision; boolean flags select the
other choices. Exact project-set and deployment consent are removed.

## Deprecations

None. These experimental interfaces change directly before 1.0.
