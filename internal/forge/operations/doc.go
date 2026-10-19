// Package operations owns the ledger of long actions someone asked for
// (design/subsystems/deployment.md, "Protecting data off-host"): a data copy, a restore drill, a
// rehearsal, a restored copy, a restore of a set, a take-down, a project's purge. An operation is one row that
// says who asked for what, with which credential, on which subjects, which
// steps happened and how it ended.
//
// The model a person can state:
//
//   - An owner's door checks the request and, in one transaction, calls
//     AdmitIn: the row, its River job and the operation.started receipt
//     commit together, or none of them do.
//   - A River worker on the operations queue runs it once. The worker claims
//     queued → running first, so a duplicate delivery never runs a running
//     or ended operation. It runs for up to two hours (jobs.OperationsTimeout)
//     with one attempt; asking again is a fresh admission.
//   - It ends succeeded, failed, unknown or cancelled. Verification is a
//     separate fact (Operation.Verification): a copy can succeed not
//     verified. An unclassified error, a panic or a stop of the forge is
//     unknown. Settlement (state, the kind's SettleIn, the operation.ended
//     receipt) is one transaction; a receipt that cannot be written leaves
//     the operation running, with its holds, until a restart settles it.
//   - A restart (RepairInterruptedIn, exclusive startup) settles a running
//     operation unknown with the steps it confirmed, and the kind's words
//     for what the stop left (Kind.Interrupted, read from what it
//     recorded), and gives a queued one whose job is gone a new job.
//     Nothing is resumed or retried.
//
// Holds. An admission names paths it holds while queued or running, such as
// project:7/deployment:12 or project:7/deployment:12/data. Two holds
// conflict when one is the other or its ancestor by whole segments. An
// owner takes its own row locks first, then LockIn (the admission advisory
// lock), then reads eligibility, including ConflictsIn, then admits: the
// same order everywhere rules out both a gap and a lock-order deadlock. A
// conflict is refused (409) with the holder; nobody waits for a hold.
//
// Authority. An admission captures the caller's principal with its
// credential reference (a token's name, a session's id, an OAuth
// delegation's id, or a system binding), never a secret. Before each
// consequential effect an owner calls StepLog.StillAllowed, which asks
// the Authorities port (Identity) whether that credential is still live
// and its current grants still allow the kind's capability. A refusal ends
// the operation failed, "authority withdrawn", before the effect.
//
// Each consequential effect and where it asks. A new effect goes in this
// table with its guard; one without a guard here is unguarded. "Own
// cleanup" removes what the operation itself made and is the forge acting
// for itself, as its sweeps would; it is not asked again, so production
// data the operation made is never left behind because a person signed out.
//
//	kind (deployment.*)    effect                                     asked in (internal/forge/deployment)
//	copy                   export each item                           takeStoreCopy, before each item
//	                       upload each item                           copyOne, after the export
//	                       prune older sets                           takeStoreCopy, before pruneStoreSets
//	drill                  make each check database                   drillOne, before NewDatabase
//	                       fill it with production data               drillOne, before fill
//	                       drop it                                    own cleanup
//	take-down              delete the app                             runTakeDown, before Teardown
//	                       revoke its AI model keys                   runTakeDown, before releaseDeploymentKeys
//	                       final copy, then its check                 as copy and drill above
//	                       delete each cluster                        runTakeDown, after the inspection, before DeleteInstance
//	                       remove the deployment's records            runTakeDown, before the records' transaction
//	rehearsal              stage each copy on the host                rehearse, before stageOnHost
//	                       start the needs                            rehearse, before Boot
//	                       fill each need from its copy               rehearse (Sealed.Fill), before each import
//	                       start the app                              rehearse (Sealed.Fill), after the imports
//	                       take the rehearsal down                    own cleanup
//	restored-copy          make its databases                         bootRestoredCopy, before reconcileNeeds
//	                       read each copy back from the store         bootRestoredCopy, before fillStartFrom
//	                       make each database new and empty           fillStartFrom(allowed), after its copy is read
//	                       fill each from its copy                    fillStartFrom(allowed), after it is made
//	                       deploy the gated app                       bootRestoredCopy, before the attempt
//	                       take a failed or tested copy down          own cleanup
//	restored-copy-end      remove each app, each cluster, the records takeDownRestored(allowed), before each
//	restore                take the safety set and stop the app       restoreBackup (internal/forge/dataprotection), before the safety set
//	                       put each item back                         restoreBackup, before each item
//	                       start the app again                        own cleanup
//	take-down (a removal)  final backup on the host                   remove(allowed), before finalBackup
//	                       delete what the forge made                 remove(allowed), before Teardown
//	                       revoke its keys, remove its records        remove(allowed), after Teardown
//
//	kind (project.purge)   effect                                     asked in (internal/forge/projectlifecycle)
//	purge                  remove each backup set                     runPurge, then PurgeProjectBackups before each set
//	                       remove the hub repository                  runPurge, before it
//	                       rename and archive the GitLab copy         runPurge, before it
//	                       remove the project's records               runPurge, before the records' transaction
//
// A project's deletion and its scheduled purge run under the deletion's
// system binding (identity.BindingDeletion), which permits only its own
// project's take-downs and purge; Purge now runs under the administrator's
// own credential (design/subsystems/project-deletion.md).
//
// Stops a running kind honours: a take-down stops, as asked, before it
// deletes the app, before its final copy, before checking that copy and
// before each cluster, and ends cancelled; a take-down that is a removal
// stops before its final backup and before its teardown, keeping the
// backup and starting stopped parts again. A copy, a drill, a rehearsal, a
// restore and a restored copy's start and end run to their end once
// started (their CancelRefusal says why); any queued operation can be
// cancelled.
//
// An effect's answer the forge cannot trust (a timeout, a lost response,
// a server error, as deploy.Refused classifies it) leaves the step and the
// operation unknown, never failed; what it may have made (a drill's check
// database) is named as left, never forgotten and never removed on a guess.
//
// The package imports no domain owner: kinds register at construction.
package operations
