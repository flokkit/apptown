// Package preview owns disposable previews of a change, a run or a release:
// the checked requests a person makes, the trusted boots of a run's preview
// step and a chat's rehearsal, where each boots (this machine, or the worker
// when AppTown has one), what each serves, opening a running one for
// probing, discarding them, and the AI models keys previews are given.
//
// # Boots and status
//
// A [Runtime] serializes boots by [Key] and keeps the last outcome in memory;
// it starts no work of its own and an AppTown that starts again holds none, so
// every preview is cold until it boots again. [Service.ChangeStatus] gives a
// boot in progress precedence over what the place previews run says.
// [Runtime.BootChange] is the trusted boot a chat rehearsal and the checked
// [Service.RequestChangePreview] share: the [Host] it is given prepares and
// boots the source inside the boot's interval. A release's preview
// ([Service.RequestReleasePreview]) boots what the release recorded, never
// input the request supplies: its digest bundle where changes' previews boot,
// or the image it recorded on this AppTown's Docker.
//
// # What a preview serves
//
// AppTown records what each preview it booted serves (its incarnation, the
// commit, and the public values it was booted with) in memory, and judges a
// running preview against that record, never today's settings ([Served]).
// Deciding to discard a preview is counted, so a boot already under way keeps
// no record; on the worker, [Service.ReconcileWorkerPreviews] removes every
// preview AppTown has no record of and is not booting, and AppTown's
// start-up removal ([Service.RemoveWorkerPreviews]) is retried until the
// worker confirms it. One Service holds the boot locks, the records and the
// discard counts; there is never a second instance of them.
//
// # Looks and lifetime
//
// A preview stops about eight hours after anyone last looked at it, and a
// change's preview keeps its build images and its snapshot for thirty days
// after the last look ([Service.Tend], design/subsystems/container-lifecycle.md).
// A look is a request through AppTown's preview proxy ([Service.Viewed])
// or a boot; the last one is this package's preview_views record. Every
// stop removes the whole stack (docker.RemovePreview).
//
// Permanent project cleanup passes retained run, change and preview-step IDs
// to Preview. RemoveRunForGood, RemoveChangeForGood and RemoveRunStepsForGood
// own their disposable local/worker resources and snapshots, without reading
// deleted source rows. Project lifecycle retains the inventory and retry
// evidence and removes ordinary workflow workspaces separately. Independent
// effects are attempted after a failure; an error never confirms retirement.
//
// # Keys and records
//
// A preview of an app that needs AI models gets its own key, recorded on
// preview_owned by the preview's name; it is
// revoked once the preview no longer stands where previews run
// ([Service.ReleaseEndedPreviewKeys]). A revocation that fails is retained by
// Deployment ([deployment.Retained.RecordPreviewKeyIn]) in the same
// transaction that forgets the key here. A change preview's placement row is
// Deployment's, written through its operations as a best-effort projection.
//
// Preview imports deployment for its records and its shared Hosts,
// Environment and the release material a release preview boots; deployment
// imports no preview code.
package preview
