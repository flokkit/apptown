# Deleting a project

A project's deletion is soft. It moves through four states, each saying
truthfully where it got to (`project.Lifecycle`, `project_lifecycle`):

| State | What holds | Leaves by |
|---|---|---|
| `active` | normal; no lifecycle row | Delete |
| `deleting` | no new work; its deployments taken down one at a time, `main` last | last deployment gone → `deleted`; Cancel deletion before the first take-down → `active` |
| `deleted` | no deployments; read-only; its records, receipts, hub repository and **all** its backup sets kept; slug held; hidden from project lists | Restore project → `active` (until a purge is admitted); kept period over, or Purge now → purge |
| purged | backup sets (disk and store), hub repository and records gone; GitLab copy archived | final |

`importing` is a lifecycle state too: a project being imported takes no new
work until the import ends. Nothing sets it yet.

A project's backups belong to the project: they go when it is purged, not on
a clock of their own.

## No new work unless active

Every owner door that creates or restarts a project's work holds the project
row (`FOR UPDATE`, or `FOR SHARE`) and asks `project.RefuseUnlessActiveIn`
before any effect. A lifecycle change holds the row `FOR UPDATE`, so a door
either commits first (and the deletion then sees its work) or refuses with
`project-not-active` (a `409`). The doors:

- Work: Change creation, start, branch import and uplift; run admission;
  send-back; a step's start, decision and retry; landing and release under
  the run snapshot lock (`workflow/lifecycle_doors.go`).
- Delivery: release creation (explicit and workflow completion), new
  deployments and their configuration, every deployment operation (copy,
  drill, rehearsal, restored copy), and a Docker host's data work: a
  backup (by a person or the schedule, which skips the project), a restore
  in place and a rebuild of a missing container. A restore holds its
  deployment while it writes, and a take-down holds it while it copies and
  deletes: each refuses the other at once, so a deletion's final backup
  never copies, nor its teardown deletes, a volume a restore admitted
  before the marking is still writing. The deletion then stalls, naming
  the restore, until it is asked again. The daily move of
  sets to the backup store leaves a project that is not active as it is. A take-down, and a restored copy's
  take-down, pass while the project is `deleting`: taking its deployments
  down is what a deletion does. A change preview reads the lifecycle
  without the lock; one admitted just before the deletion goes with the
  project's previews.
- Settings and renames; eval start, which records its arms holding the
  source project's row.
- Chats: the console block, taken before the project is marked and held
  while it is `deleting` or `deleted` (re-applied at startup before
  serving, `Deletion.HoldBlocks`): no chat on it is opened, identified or
  used, so no turn, save or landing comes from one.
- Schedules, webhooks and remediation reach work only through these doors;
  the remediation pass and the fleet review skip a project that is not
  active.

Each owner has a table test of its doors against a `deleting` and a
`deleted` project (`TestEveryWorkDoorAsksTheProjectsLifecycle`,
`TestDeploymentDoorsAskTheProjectsLifecycle`, and the root
`TestEveryWorkDoorRefusesAProjectThatIsNotActive`), so a new door without
the check shows up.

## Asking for the deletion

`DELETE /api/v1/projects/{slug}` with `{"confirm": "<slug>"}` (or
`apptown delete <slug>`) needs `project:delete`. It takes the project's chat
block first (refused while a chat is opening, in use or making its last
save), then, holding the project row, refuses running work
(`workflow.ProjectRemovalIn`) and a live eval whose arms still work from the
project's code. It then grants the deletion's **binding**, marks the project
`deleting`, queues the deletion pass and writes `project.deletion-requested`,
all in one transaction, and answers `202` with where the deletion stands
(`GET /api/v1/projects/{slug}/deletion`).

**What accepting a deletion means.** The receipt records the decision:
take the deployments down (main last, each with its final copy where the
AppTown takes one), keep the project read-only with its backups for the kept
period, then purge it. AppTown holds a scoped system binding
(`identity.BindingDeletion`, `system_bindings`) for this project and this
deletion only: it permits the deletion's take-downs and the purge, and
nothing else. The deletion pass and the scheduled purge run under it
(`system:binding/<id>`, asked again before each effect through
`RequireStill`), so the deleter's own session may expire meanwhile.
Receipts name the binding, the deleter and the decision. Revoking it
(`POST …/deletion/revoke`) stops the deletion or purge at its next step,
and the page says so; Cancel and Restore revoke it.

**Cancel deletion** (`POST …/deletion/cancel`) returns the project to
`active` only before its first take-down was admitted.

## The deletion pass

A River periodic pass (`project-deletion-pass`, every five minutes, also
queued for the project at marking) moves each `deleting` project one step,
after a ten-minute grace (`DeletionGrace`) in which nothing is taken down:

- a stalled deletion waits for a person; a revoked binding stalls it;
- a live operation under the project is waited for;
- the last take-down admitted under the deletion's current binding that
  failed or whose end is unknown stalls it, named; a failed or unknown
  take-down never counts as gone. Re-confirming the deletion grants a new
  binding, so the pass tries again; the earlier attempt stays as it ended;
- otherwise one take-down is admitted for the next deployment: the
  project's standing deployments in their own order (`ForProject`), `main`
  last, so a refusal anywhere else leaves the live app serving;
- when no deployment and no live work remain (rechecked under the row
  lock) the project becomes `deleted`, kept until `kept_until`:
  30 days later by default (the instance setting `project.kept-days`,
  which no page or command sets yet). A
  `project.deleted-kept` receipt says so.

Each take-down is the one every deployment gets
([deployment.md](deployment.md#removing-a-deployment)): its data backed up,
then exactly what AppTown recorded creating for it deleted. The deletion
admits every one, on any target, as a `deployment.take-down` operation
under its binding, which asks it again before each effect (the final
backup, the teardown, the keys and records); its receipt names the binding
and the deleter. A take-down
that is refused (a final copy that falls short, a host that cannot be
reached, a Portainer stack AppTown did not record creating) stalls the
deletion with the take-down's own sentence; the project stays `deleting`
until a person deals with the deployment and asks again (deleting it again
re-confirms the deletion under a new binding, the old one revoked). A
person's own take-down of a deployment is still allowed while the project
is `deleting`. Nothing of a deployment is removed by name or label: what
AppTown did not record creating stays, visible on Infrastructure as
unrecorded. An eval copy's deployments go with no final copy, its data
being synthetic.

No cursor is stored: remaining deployments and operation outcomes decide.

## While deleted

The project's page reads "Deleted — kept until <date>", with its backups
listed on the Backups page. **Restore project** (`POST …/restore`, or
`apptown restore <slug>`) returns it to `active` with no deployments, its
chat block released and its binding revoked. It needs `project:delete` and
`deployment:restore`, and is allowed only until the first purge admission
commits: both decide under the project row lock, so exactly one of a
restore and a purge admission wins. After that, Restore is closed for this
deletion even if the purge fails, and the page says "purge has started;
restoring is no longer available". A final set restores into a new
deployment through the start-from-copy path.

**Purge now** (`POST …/purge`, `apptown purge <slug>`) is for instance
administrators, under their own authority; it admits the purge at once.

## The purge

A River pass (`project-purge-pass`, daily) admits, for each project whose
kept period has ended, one `project.purge` operation in the ledger under
the deletion's binding. An eval copy has no kept period: its purge is
admitted as soon as it is deleted. The purge's steps, each asking its
authority again first:

1. recheck that the project is `deleted` and idle;
2. remove each backup set, its files on AppTown's disk or its objects in
   the backup store, then its record (`dataprotection.PurgeProjectBackups`);
3. remove the hub repository, then rename the GitLab copy, named by its
   permanent GitLab project id, to `<slug>-deleted-<AppTown project id>` and
   archive it read-only: never destroyed, and never another project by
   slug. A later purge, the hub being gone, takes the copy the earlier
   purge named;
4. in one transaction, remove the project's records (every owner's
   `RemoveProjectIn`, and the project row with the `project.deleted`
   receipt), write the `project.purged` receipt (binding, deleter, sets
   removed) and the **cleanup plan** (`project_deletions`), and queue the
   cleanup job.

Every step makes something absent or archived, and an already-done step
counts as done. A step that fails or whose end is unknown ends the purge
`failed` or `unknown`, reading "purge incomplete: …" with what is left. The
records stay and the project stays `deleted`; the next day's pass admits a
fresh purge, its own row. Usage, the ledger and receipts are kept.

## The cleanup, as a job

The `project-cleanup` job (key `project/<id>/<round>`) carries out the plan
the purge recorded: each run's and Change's preview container, preview
stack, workspace and data volume, their build images, the project's
console sessions and its kept agent transcripts. Every effect treats an
already-absent resource as done. A failure is retried up to five times;
after the fifth the cleanup **needs attention**: the Infrastructure page
lists it with what failed, and an administrator's **Try again** starts a
new round (its job, its receipt and the round commit together). A pending
cleanup whose round has no job queued or running is shown as needing
attention and may be retried; while its job is live, a retry is refused.
The outcome is a `project.cleanup` receipt.

Docker's answer that an object is already gone counts as removed only when
it comes from the daemon; a failure to reach Docker at all is a failure.

## The name

Containers, volumes and the hub are named by the project's slug. The slug is
held while the project is kept and, after the purge, until the cleanup is
done: creating a project with it, or renaming one to it, is refused
(`project.ErrSlugHeldByCleanup`, a `409`). Change and run ids are never
reused, so their resources need no hold.

## What it does not do

It does not find or remove resources AppTown did not record: past
deletions' leftovers on hosts are a separate reconciliation, never cleaned
up by matching a slug or prefix.
