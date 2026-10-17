# What is next

Forge is in its 1.0 release candidates (the release notes are in the
repository's `releases/` folder). 1.0 is meant to be the first version
someone other than its authors can install for a small team they know, run and
upgrade through 1.x without help, and trust the record of.
[What 1.0 promises](/docs/guides/compatibility) lists what stays stable.

## In 1.0

- **Record identities.** Every record has a UUID, with per-project numbers
  such as `slug#12` for people, so links, exports and backups stay meaningful
  outside one forge.
- **A stated stable surface.** Machine-readable output for the command line's
  core loop, a description of the HTTP API, and consistent names for the same
  thing everywhere.
- **Proven recovery and upgrades.** A forge restored from its backup onto a new
  machine, and an upgrade from the previous release that is checked against real
  data, both before every tag.
- **An operable fleet.** Each part declares its memory and CPU, each deployment
  chooses a profile (production can have more than previews), every deployment
  shows what it may use and what it uses now, and an operator can raise a limit
  at once as a recorded act. Packs and the service catalogue supply memory
  defaults, so a part that declares nothing still gets a limit, and checks
  find parts that declare none or outgrow their declaration. A deployment's
  diagnostics say which part is failing and why (its exit, an out-of-memory
  kill, its restarts), and its recent log can be read on request. Container
  logs are bounded on disk, and new work is refused, with what is free, on a
  disk too full to take it.
- **A stabilisation period.** Before 1.0 is tagged, a release candidate is run
  in production by the maintainers for two weeks of real work. A defect found
  then is fixed in a new release candidate.

## Deliberately not in 1.0

High availability and more than one control plane; running untrusted projects
side by side on one worker (and microVM isolation); single sign-on group sync;
signed evidence export; importing a project from another forge; more deployment
targets; more agent harnesses. Each is a known gap, not an oversight.

## After 1.0

Operating deployments: one page of current operational problems. A reserved
`protected-maintenance` deploy procedure (stop writes, verified copies, private
check, reopen), automatic return to the previous release after an early crash
loop, and more than one replica of a part are designed or reserved, not built.

Fleet-operations agents that watch production's logs and memory and propose a
profile change or an override for a person to accept; project import (with
records keeping their identities), more deployment targets
(AWS, Google Cloud), stronger agent isolation on workers, scheduled conformance
reviews across all projects, and a second agent harness.
