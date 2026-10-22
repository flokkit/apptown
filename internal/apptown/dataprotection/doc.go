// Package dataprotection owns the answer to what data a deployment holds,
// whether it has been copied and witnessed coming back, and which data
// effects a restore or a removal may perform.
//
// # What it owns
//
// Ordinary and final copies of a Docker host's data, copies of managed
// databases to the attached backup store, the store itself with its pruning
// and purge, in-place restore, drills (a copy filled into a new database
// and checked), sealed-host rehearsals, restored copies of production
// behind AppTown's access gate, and take-down orchestration: whether a
// deployment's data may be deleted, its final copy first. Its tables are
// backups, backup_store, backup_drills, restore_rehearsals,
// rehearsal_places, restored_copies and restored_copy_tickets; their
// history survives the deployment and is kept with its project until the
// project is purged.
//
// # What Deployment keeps
//
// Deployment owns the placements these touch and settles them: a take-down
// selects its placement with [deployment.SelectRemovalIn] and settles it
// with [deployment.RemoveRecordedIn] (retained resources, the release it
// served, the row and the receipt, in one transaction, after rechecking the
// placement is the one selected); a restored copy's placement is made and
// extended with Deployment's operations. Data protection writes Deployment's
// tables only through those participants.
//
// # Starting from a copy
//
// A [Starter] is built before Deployment's service and given to it as its
// [deployment.CopyStarter]: Deployment asks it whether a start from a set is
// allowed and to fill a deployment that has never run, and records the
// start itself. The [Service] is built over the same Starter, so the fill a
// restored copy runs is the one a release runs.
//
// # Operations
//
// Copies, drills, take-downs, rehearsals, restored copies and restores
// of a set over a deployment run as operations in the ledger
// (internal/apptown/operations). A restore and a Docker host's removal
// (which is not an operation) of the same deployment also exclude each
// other through one in-memory claim, which a restart releases
// (design/subsystems/daemon-lifecycle.md, "Shutdown"). A restore the
// AppTown's stop cut off ends unknown naming what it may have left stopped;
// nothing starts it again on a guess (restore_operation.go).
package dataprotection
