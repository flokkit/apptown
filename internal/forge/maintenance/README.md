# Host maintenance

`Reaper` owns periodic cleanup policy. Forge constructs it once and schedules its
four operations; Docker owns the command mechanisms. It receives the database,
Docker scope, Console service/runtime, Deployment preview records and data directory.
It cannot reach App or unrelated application operations.

- `Containers` protects persistent classes and active execution owners. It uses
  observed container IDs for execution cleanup. Previews are not its: Preview
  stops them by the last look (`preview.Service.Tend`). It also removes a finished
  execution's step and private-daemon networks, selected by the same
  `forge.class`/`forge.owner` labels as its containers and removed by their
  listed ID; an unlabelled network (a deployment's, or one made before the
  labels) is never selected. Idle console eviction retains
  the saved session and its identity.
- `Images` and `Cache` decide from recorded run/change state and bounded-age
  conformance images. Unknown ownership is retained. For images, an owner
  that no longer exists (a deleted project's run) is finished, and its build
  images are reaped; one whose record cannot be read is retained. A change's
  preview's build images are Preview's (kept thirty days after the last
  look). Removal never forces, so an image a container uses stays, quietly.
- `Workspaces` checks recorded closure and saved conversation ownership, preserves
  transcripts, then removes the workspace after its grace period. Preservation
  failure leaves the source for a later sweep.

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

`LocalWorkLeft` and `ExecutionContainers` answer what project work this host may
still hold, from the same records and container labels: unsettled local task
attempts, steps running with no task, unended chats placed on the forge,
unfinished project or workflow cleanups, and containers carrying an execution
label. The root's `-drain-local-docker` startup asks both and keeps the mode
record. `RemoveBuildImages` removes a preview's build images without force for
project cleanup.
