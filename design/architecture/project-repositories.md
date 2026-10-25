# Project repository locations

`project.RepositoryRef` is an identified project UUID and its stored address.
Its `Path` method owns the hub layout:
`<data>/workspaces/project-<slug>`. A project record's `Repository` method
uses the same locator. Missing identity and invalid address shape are errors;
location calculation does not create or inspect a directory.

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

## Active references and storage boundaries

These are current writers and readers of locations. A change to hub layout or
to the installation's data-root must account for active persisted references,
not just calls to the locator.

| Location or record | Writer and reader | Boundary |
| --- | --- | --- |
| Project hub directory | `projectlifecycle` initialization, seed, copy and purge; repository edits, source reads, exports, Git transport and deployments | Located by project UUID plus stored address through the owner; current layout is address-based. |
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
repository attachment, and a deployed app's serving address are distinct. This
location boundary changes neither the current layout nor any of those addresses.
