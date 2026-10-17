// Package deployment owns targets, named placements, releases, attempts and their
// settlement, together with bundles, retained resources, needs and
// reconciliation records. Driver protocols and contract parsing belong to the
// internal/deploy libraries; HTTP adapters belong to forge/httpapi.
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
// restored copy), places and extends a restored copy through
// [Deployments.PlaceRestoredCopy] and [ExtendRestoredCopyIn], and fills a
// deployment started from a copy as this package's [CopyStarter], which
// Deployment is given at construction; the start itself is recorded here
// ([Deployments.RecordStartedFrom]). Deployment imports no data protection
// code.
//
// Standing placements use [CompatibleUpdate], the name of today's procedure,
// not compatibility certification or automatic data recovery. Configure defaults
// omitted policy input to it and refuses [ProtectedMaintenance] before source,
// settings or provider effects. Only one procedure executes; its read-side value
// is derived without a saved alternate choice. Preview/restored records omit it.
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
// [RunError]. An unknown provider result or failure to record an outcome requires
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
// providers. Missing, replaced or stale observations cannot authorize an action.
// The subsystem operating guide documents freshness, source limits and shutdown.
package deployment
