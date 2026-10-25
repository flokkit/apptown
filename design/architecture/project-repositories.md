# Project repository locations

`project.RepositoryRef` is an identified project UUID and its stored address.
Its `Path` method owns the hub layout:
`<data>/workspaces/project-<UUID>`. A project record's `Repository` method
uses the same locator. Missing identity is an error; the stored address does not select a path.
Location calculation does not create or inspect a directory.

The reference is a projection of an owner read, not authorization or proof that
an arbitrary UUID/address pair belongs together. Callers retain their existing
project authorization and UUID repository locks. Workflow and deployment source
projections carry that identity; transports resolve an authorized project rather
than constructing a path from a route string. Git and deployment libraries take
already resolved locations and remain independent of the application database.

Purge resolves its project from the operation's project UUID before effects. The
same current record supplies the retained deletion address and cleanup payload.
After the project row is removed, cleanup uses the retained deletion record's
UUID and address. A historical operation or cleanup payload's display address
cannot select another directory. A missing or unreadable owner does not create a
replacement repository. Removing a directory already absent remains repeatable.

Boot hook installation and scratch-ref cleanup enumerate observed directories
through `project.RepositoriesOnDisk`. This preserves their maintenance scope;
it does not adopt those directories as identified projects. Enumeration neither
moves nor removes an unowned directory. The `hubpaths` architecture gate admits
no production layout constructors outside the project owner.

`workflow.Layout` owns ordinary `change-<UUID>` and `run-<UUID>` workspace
locations. Readers, writers and cleanup use the same owner. `workflow.Source`
selects which workspace a run uses from its checked change, root-run and
continuation facts; location calculation does not choose that ownership.
Preview snapshots retain their separate `asof-change` and `asof-run` namespaces.

## Active references and storage boundaries

These are current writers and readers of locations. A change to hub layout or
to the installation's data-root must account for active persisted references,
not just calls to the locator.

| Location or record | Writer and reader | Boundary |
| --- | --- | --- |
| Project hub directory | `projectlifecycle` initialization, seed, copy and purge; repository edits, source reads, exports, Git transport and deployments | Located by immutable project UUID through the owner; display/address changes do not move its hub. |
| Change/run workspace directories | `workflow.GitWorkspace`, workflow sources, console admission and execution | Separate `change-<UUID>` and `run-<UUID>` identities; a hub is not a substitute for a writing workspace. |
| Current workspace Git metadata | `workflow/workspace_git.go`, `gitexec/folder.go` | Full `--no-hardlinks` clones; origin is removed after clone. Hardened folder reads refuse gitdir/commondir pointers and alternates. Ordinary current clones do not borrow their hub's objects or retain its origin. |
| Legacy linked-worktree metadata and local origins | Existing Git directories, read by Git when explicitly selected | Pointer files, hub `worktrees` metadata and local origin/alternate paths can retain absolute locations. They are distinct from today's standalone-clone contract; a location change must inspect them without pruning or discarding dirty work. Remote HTTPS/SSH origins are upstream attachment identity. |
| Branch publication `repository` | `scm/publication.go`, `scm/schema.sql` | Persisted hub path used to inspect incomplete publications at startup. Active recovery rows are not merely historical display strings. |
| Task invocation `workspace` | `tasks/schema.sql` and task/workflow execution | Persisted execution location; distinguish remote/backend-owned workspaces from local hub/change/run paths before any relocation. |
| Console `session.json` checkout | `console.Service.Open`, console session recovery/resume/removal | Persisted host checkout path plus ownership flag. Change consoles borrow the change folder; isolated snapshots may be owned by the session. |
| Selected source, staged tree and temporary bundle | Workflow/verification/deployment source projections, SCM tree/bundle helpers, lifecycle copy/export | Per-operation resolved inputs or temporary staging. `gitexec/bundle.go` creates temporary quarantine alternates into the source object store; their lifetime differs from a persistent workspace. |
| Preview snapshots and cleanup directories | Preview and workcommands | `asof-change`, `asof-run`, change/run directories retain their separate keys; project cleanup does not reinterpret them as hubs. |
| Soft-deleted and purged ownership | Project lifecycle and `project_deletions` | Soft-deleted project records remain available for purge. After row removal, the retained deletion UUID/address remains the cleanup authority. Interrupted removal may already have removed the hub. |
| Whole-instance archive paths | `instancebackup` manifest/archive and restore, daemon startup | Archive holds the data directory and its original `Manifest.DataDir`. Existing `AfterRestore` relocation concerns disk backup sets; it is not a general repair of repository, console, task or Git metadata paths. Such repairs must precede their recovery consumers. |

Historical log messages, human-facing labels and completed receipts are evidence,
not automatically active filesystem references. Local hub ownership, an upstream
repository attachment, and a deployed app's serving address are distinct. Local repository relocation does not rename upstream repositories or change application addresses.

## Offline layout transition

Layout 2 uses UUID hub directories. The daemon validates the layout marker before
startup effects, takes its database exclusion, opens the supported schema, and
runs `datalayout.Upgrade` before constructing the application. Verify-only startup
performs this required transition too; application recovery, workers and
scheduling remain disabled. Password reset does not hold the exclusion and
refuses any incomplete layout transition.

The journal binds project ownership, source/destination paths and content digests.
All hub sources are staged before any destination is installed, including address
cycles. Missing initialized hubs refuse; uninitialized and irreversible deletion
ownership can preserve an intentional absence. Writer observation includes console
sidecars/scripts and recorded task backends. Unknown ownership, unavailable
observation, active writers and immutable task requests retaining affected local
inputs refuse before relocation. Task requests and backend-owned workspace keys
are never rewritten.

The transition preserves dirty/staged/untracked working files. Identified linked
Git metadata and same-project borrowed objects are staged into independent
metadata; unsupported configuration or foreign dependencies refuse. Original
metadata remains in a journal-owned evidence directory. Active publication and
console paths use exact old/new updates; remote HTTPS/SSH origins and historical
console records stay unchanged. Completion verifies main commits and destination
repositories before writing layout 2. A completed journal remains until any
restore continuation settles, then finalization records its receipt and removes
active migration journals. Rollback uses the paired database/data backup, not an
old binary against a partly relocated directory.

## Upstream identity

`scm.RepositoryAttachments` owns verified GitLab attachments and pending create
requests. Local hub UUID, project address and upstream repository name are
independent. Provider numeric IDs have meaning only within the saved connector,
canonical base URL and authenticated account. A changed connector or base refuses
before an account probe; effects retain the credential client that observed the
account. Changing an origin is not an implicit rebind.

MR artifacts record their original identity in the same transaction as their
artifact. Merge snapshots read that provenance in their existing batched source
query and retain it by value. Scoped token resources keep the same identity and
refuse unproven cached credentials before reuse or revocation. Purge admission
records provider scope before removing the hub; retained cleanup never infers
that historical scope from a currently configured provider.
