# Host maintenance

`Reaper` owns periodic cleanup policy. AppTown constructs it once and schedules its
three operations; Docker owns the command mechanisms. It receives the database,
Docker scope, Console service/runtime, Deployment preview records and data directory.
It cannot reach App or unrelated application operations.

- `Containers` protects persistent classes and active execution owners. It uses
  observed container IDs for execution cleanup. Previews are not its: Preview
  stops them by the last look (`preview.Service.Tend`). It also removes a finished
  execution's step and private-daemon networks, selected by the same
  `apptown.class`/`apptown.owner` labels as its containers and removed by their
  listed ID; an unlabelled network (a deployment's, or one made before the
  labels) is never selected. Idle console eviction retains
  the saved session and its identity.
- `Images` and `Cache` decide from recorded run/change state and bounded-age
  conformance images. Unknown ownership is retained. For images, an owner
  that no longer exists (a deleted project's run) is finished, and its build
  images are reaped; one whose record cannot be read is retained. A change's
  preview's build images are Preview's (kept thirty days after the last
  look). A conformance image's age runs from the later of its creation and
  the last time Docker tagged it: a rebuild from cache keeps the old creation
  time, and the tag says when it was built or reused. Removal never forces,
  so an image a container uses stays, quietly. `Cache` reads each cache
  volume through a throwaway container, and each start costs the host's init
  a scope, a network interface and mounts; after a complete sweep it reads
  the volumes again only once a change has closed or a run has stopped
  running since that sweep began (`workflow.SettledSince`), so an idle
  AppTown starts no container on this schedule.


These are best-effort backstops. Normal execution cleanup is still the fast path.
Project deletion remains a separate authorized operation: it may remove selected
persistent resources, which this periodic reaper must never select. Startup
preview cleanup also remains distinct from periodic TTL policy.

Private policy tests live here. Root integration tests combine real Work
admission/settlement, saved Console ownership and filesystem/Docker observations.
No exported fixture API or shared App fixture is introduced.

This ownership move preserves existing scheduling and command lifetimes. The
scheduler observes daemon cancellation between duties. The daemon separately
waits for it within the shared five-second shutdown deadline; a stalled cleanup
does not extend that deadline. This does not claim every underlying legacy
command has stopped. Per-command cancellation throughout cleanup and the console
runtime remains a separate hardening opportunity.

`RemoveBuildImages` removes a preview's build images without force for
project cleanup.

`DiskObserver` stores the latest successful reading of each controller and worker
filesystem in the maintenance database tables, bound to the configured worker.
Restart preserves those readings. A failed check retains each filesystem's
values and original time and records the failed attempt and safe reason.
Free disk is fresh for fifteen minutes, then last seen with its age, and unknown
after six hours; disk size remains valid for seven days. Attention raises an age
item before six hours only when the latest check failed. These display readings
do not change worker or Docker admission, which read disk independently.

Work owns workspace cleanup (`workflow.Service.Workspaces`), on the existing
maintenance schedule. It skips saved conversations, waits a day after closure,
and preserves workspace-only session transcripts before removing a checkout.
A failed preservation retains the checkout. Change and run records remain.

Disk observations also record category sizes. Folder measurements count regular
file contents without following symlinks. Docker reports daemon-wide volumes,
images and build cache, including resources without AppTown tags; measuring
these resources does not authorize their deletion. Docker sizes are rounded.
Database size comes from PostgreSQL, including when the database is external.
Categories can overlap and are never summed as a filesystem's used space.
Usage shows these measurements and their times, including unknown categories.
Attention warns strictly above 80% full as well as the existing admission
conditions. Its existing failed, stale and unknown observation rules still apply.
Cache expiry after 14 days and pressure eviction are not implemented.
