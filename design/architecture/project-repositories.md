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
| Local origins | Console snapshot clones, read by Git when explicitly selected | A local origin path retains an absolute location; restore relocation rewrites it. Remote HTTPS/SSH origins are upstream attachment identity. |
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

## Data layout and restore relocation

Layout 2 uses UUID hub directories; the data directory's `layout` file records
it. Before any other startup effect, the daemon refuses a directory in another
layout and marks one without a marker as layout 2 when it holds no
`workspaces/project-*` entry, the only thing layouts place differently. This
build does not move hubs between layouts; a directory from before layout 2 is
upgraded by a release from 1.0.0-rc.9 to 1.0.0-rc.11.

A whole-AppTown restore into a path other than the archive's original data
directory leaves absolute local references naming that original path. Under its
database exclusion and before constructing the application, the daemon runs
`datalayout.Relocate`, which journals and rewrites them: unfinished branch
publication repositories, console checkout paths, and local Git remote URLs of
change, run and console workspaces. Verify-only startup performs it too;
application recovery, workers and scheduling remain disabled. Password reset
does not hold the exclusion and refuses a pending relocation.

The journal binds project ownership and the original and destination roots.
Missing initialized hubs refuse; uninitialized and irreversible deletion
ownership can preserve an intentional absence. Writer observation includes
console sidecars/scripts and recorded task backends. Unknown ownership,
unavailable observation, active writers and immutable task requests retaining
affected local inputs refuse before relocation. Linked or borrowed Git metadata
(a `.git` file, `commondir` or alternates) predates layout 2 and refuses. Task
requests and backend-owned workspace keys are never rewritten; remote HTTPS/SSH
origins and historical console records stay unchanged. Completion verifies the hub
directories. A completed journal remains until the restore continuation
settles, then finalization removes it. Rollback uses
the paired database/data backup.

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
