// Package deployment owns targets, named placements, releases, attempts and their
// settlement, together with bundles, retained resources, needs and
// reconciliation records. Driver protocols and contract parsing belong to the
// internal/deploy libraries; HTTP adapters belong to internal/apptown/httpapi.
//
// Workflow releases record the pre-release backup ID from their run's successful
// deployment attempt. Attempts retain that immutable input; failed attempts do
// not associate their backup with a numbered release. Backup availability and
// status belong to dataprotection; Releases owns the reverse association.
//
// # Records and authority
//
// A [Target] is a place an application can run. A [Deployment] is a named placement
// on that target. A [Release] records selected material to ship; an [Attempt]
// identifies one admitted generation. Runtime observations describe the provider's
// current state without replacing those durable records. Each lifecycle has its
// own state type; display state is not transition authority.
//
// [Service] assembles record owners and provider collaborators. Its user commands
// check authority; trusted queue/execution operations and the exposed record APIs
// do not independently authenticate users. Start manual deployment through
// [Service.RequestRelease], not by constructing rows or calling a driver directly.
// [Service.AttachTarget] owns attachment admission, provider checks and create-only
// receipts; [Service.OfferTarget] owns checked group and offer admission. Transports
// decode these commands and render their results without constructing target policy.
//
// Three concrete collaborators hold the implementation releases share with
// previews and copies: [Hosts] reaches Docker daemons, [Environment] prepares
// what a boot is given (secrets, needs, registered keys through a
// [RegisteredKeys] recorder, registry credentials, addresses and the eval
// rule), and [Providers] builds drivers and inventories. The composition root
// builds each once, with [Targets] and [Deployments], and gives the same
// instances to every owner; none of them refers back to Service. Previews
// are the preview package's: it reads these records and collaborators, and
// writes a preview's placement row only through [Deployments.RecordPreview]
// and [Deployments.ForgetPreview], and a key it could not revoke only through
// [Retained.RecordPreviewKeyIn]. Deployment imports no preview code.
//
// Data protection (copies, restores, restored copies and take-downs) is the
// dataprotection package's. It settles what it removes through
// [SelectRemovalIn] and [RemoveRecordedIn] (and [RemoveRestoredIn] for a
// restored copy), places a restored copy through
// [Deployments.PlaceRestoredCopy], and fills a
// deployment started from a copy as this package's [CopyStarter], which
// Deployment is given at construction; the start itself is recorded here
// ([Deployments.RecordStartedFrom]). Deployment imports no data protection
// code.
//
// A deploy replaces what runs in place. It certifies no backwards
// compatibility and restores no application data if the new release fails.
//
// # Request, execution and settlement
//
// RequestRelease resolves the selected bundle and contract, checks deployment and
// protected-target authority, then rechecks placement inside the transaction that
// commits the release and required queue intent. Acceptance means queued work,
// not a deployed application.
//
// [JobWorker.Release] calls [Service.ExecuteRelease], which claims the recorded
// request before source staging or secret resolution. Duplicate delivery does not
// repeat preparation. Managed-need provisioning and artifact rendering can perform
// provider I/O during preparation. Execution then uses [Runner] and [Outcomes.Admit]
// to persist an attempt, immutable [Inputs] and the next generation before calling
// the driver's Reconcile operation. Those guards do not cover earlier preparation.
// Reusing a key requires identical inputs and does not authorize another execution.
//
// The deployment row serializes attempt admission and outcomes; locks are taken
// in deployment/attempt order. Provider I/O stays outside these transactions.
// [Outcomes.Checkpoint] persists provider identities needed for observation.
// [Outcomes.Complete] and [Outcomes.Fail] commit the attempt, deployment changes,
// associated release and required receipts together. A superseded attempt cannot
// overwrite a newer generation. Replay preserves the stored terminal evidence;
// conflicting terminal outcomes return an error.
//
// [Runner.Run] distinguishes provider failure from settlement failure through
// [runError]. An unknown provider result or failure to record an outcome requires
// observation. [Outcomes.Uncertain] records that state and recovery-job intent in
// one transaction, blocking fresh attempt admission until resolved. These database
// guards do not cancel external effects or stop an uncooperative old provider call.
//
// # Recovery and observations
//
// [PrepareRecovery] checks settlement consistency and queues interrupted attempts
// before new execution; it makes no provider calls. A deployment whose records
// contradict each other is marked as needing attention ([Outcomes.NeedsAttention])
// and startup goes on; [Service.CheckAgain] is a person's repair, observing and
// settling it through the usual settlement. [Recoverer] receives an observation-only
// interface: it calls Resume for the admitted identity and cannot reapply a missing
// deployment. Jobs own retry budgets; exhaustion does not prove provider failure.
//
// [Service.RunObservations] refreshes display-only samples, separately from recovery
// and Docker health enforcement. Placement and generation are rechecked after I/O.
// [Service.ProjectRuntime] reads authorized cached observations without contacting
// providers, showing the most urgent deployment state and a separate production
// address. [Service.ObserveContainer] reads them for one deployment: a page
// never waits on a host that does not answer. Missing, replaced or stale observations cannot authorize an action.
// The subsystem operating guide documents freshness, source limits and shutdown.
// Capacity and inventory readings live in Deployment's database, bound to the
// target settings revision. New services read stored evidence after restart.
// Each capacity figure keeps its last successful time: machine totals last seven
// days, live use and free disk are fresh for fifteen minutes and unknown after
// six hours. A failed check retains values and the prior overall read time,
// recording the failure separately. A first partial check takes its read time
// from successfully read figures. Inventory owns its stale/unknown classification;
// transports render that decision.
// Landing builds are separate from execution. RequestLandingReleases runs after
// Work commits completion and creates one request per follows-main destination,
// under its recorded feed revision and setter credential. Landing approvers
// remain provenance. ExecuteRelease rechecks the setting and current grants,
// takes the pre-release copy, and retains copy and continuity evidence apart
// from the running-code fact. Deployment history reads these release requests.
// Deployment reads expose LastApplied from the completed attempt of the observed
// generation. Detail readings keep prior payloads and their identity and age
// after a change, marked unknown and old for display only. Post-act detail,
// protection and capacity checks run on every target.
// [HostSnapshots] stores Docker host facts in five independently timed sections.
// [HostReaders] runs one sequential loop per active Docker target, collecting
// host, containers, resources and images every twenty seconds after a lap. The
// built-in host has no loop when local Docker is disabled; Attention expects no
// reading from it. Use is reserved for capacity. Foreground reads never store
// this evidence. A failed section retains its evidence; an
// unread section stays unknown. BeforeAct increments the act counter before an
// effect. Only a complete lap begun after that act clears the whole-reading
// mark; passive reads compare with the current counter. Docker inventory
// derives from the stored facts. Missing-resource claims compare records
// and facts during the same complete collection; newer records cannot establish
// absence against older facts. Placement verification runs during collection
// so reading pages does not contact providers.
// [Service.InventoryFor] selects the persisted source for both infrastructure
// and Attention. Other providers' inventory stores a complete answer, verified
// placement, witnessed missing claims and its invalidation generation. An act
// during collection cannot clear invalidation, even with equal clock readings:
// completion is fenced by generation. Docker whole-reading act provenance uses
// counters, independent of section timestamp precision.
// Pages only read this evidence; disk admission asks
// the daemon independently and does not trust a display reading.
package deployment
