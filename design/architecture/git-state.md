# AppTown — Git-Backed State

The shipped version for operators and agents is Docs → "How changes reach
main".

## 1. Roles: AppTown hosts the repository; a mirror is optional

- **The hub is the project repository**, at
  `<dataDir>/workspaces/project-<slug>`. It is where `main` lives and it is
  written by exactly one thing: the landing flow, which merges locally (or
  through a recorded GitLab merge request, §4).
  Everything AppTown does — branch, commit, merge, deploy — works against
  the hub and its clones. No remote is required for any of it.
- **GitLab is a mirror**, when a project attaches one: landed branches are
  pushed to it for backup and for people who want to read code there. It
  is never the thing the engine blocks on.

AppTown owns process state (the ledger, in Postgres); git owns content
state; a mirror hosts a copy.

## 2. Change ↔ branch ↔ clone

The **Change** is the branch, and every workspace is a **self-contained
clone**:

- Opening a change clones the hub into `workspaces/change-<id>` on its own
  branch. The clone has real git inside every container (a worktree's
  pointer file would resolve to a host path a container cannot see). It has **no push remote**: transport is exclusively
  the host's job, so an agent can commit but never push.
- The clone is the **authority for its branch** until it lands. The host
  mirrors it into the hub at every commit point, forced, so a rework that
  rewinds history mirrors faithfully.
- Commits carry trailers, which are the semantic-history link:

  ```
  AppTown-Change-Id: 41
  AppTown-Run-Id: 133
  AppTown-Actor: alice
  AppTown-Provenance: console
  ```

  The engine commits a step's leftovers with `AppTown-Run-Id` and
  `AppTown-Workflow`; a landing **sweeps** uncommitted chat work into a commit
  with console provenance before any gate runs; the landing merge carries
  `AppTown-Operation`, the id the recovery pass uses to tell a merge that
  finished from one that did not.
- **Landing is the merge.** A child run (a workflow called from another)
  shares its root's clone and branch and never merges on its own. Failed
  and abandoned changes leave their branches in the hub unmerged.
- **The diff is the compare surface**: the change page and the reviewing
  agent both read the branch's diff against its base.

## 3. Freshness and conflicts

Freshness is a gate, not a fetch at start:

- The first landing gate is **reconcile** (`reconcile.go`): the host fetches
  the hub's `main` into the change's own clone and merges it there, before
  tests, scans, conformance and the approval. A clean catch-up is silent;
  a conflict surfaces early on the branch the change's helper can reach;
  every later gate then runs on the tree that will actually be on `main`,
  and the final merge is a fast-forward. Merge, not rebase, so the change
  keeps its real trajectory.
- The change's **base commit** is recorded at open and used by the
  conformance baseline to tell findings the change introduced from ones it
  inherited.

### 3a. The collaborative model

The rule that explains everything: **every workspace is a real,
self-contained clone; the hub is the project repo; the host moves commits
between them; nothing but a landing touches `main`.** Enforced, not
trusted:

- **Before the first Change, a new project's first files.** The wizard's
  services, a pack's starter files and a stock toolchain are committed
  straight to a new project's `main` through one door,
  `projectlifecycle.CommitGenesis`. It holds the repository lock that every
  Change workspace takes before cloning and refuses once the project has any
  Change (`409`, code `project-established`), so after that `main` changes
  only through a landing. Applying a pack to an established project still
  adopts it; its files are reported as not written.

- A **pre-receive hook on the hub** (`installNoPushHook`) refuses every push
  but two narrow ones: a Change ticket's push to its holding ref, and a
  step's push to its own scratch ref (below). Safe for AppTown, which never
  pushes to its own hub; it closes the one channel that would bypass the
  gates.
- A **scoped git-over-HTTP endpoint** (`githttp.go`, `git http-backend`
  behind `net/http/cgi`) lets a container **fetch `main` only**:
  `uploadpack.hideRefs` hides every other ref, so no other change's branch
  is listed or fetchable; the credential is a minted read-only, one-project
  token (`git_pull_tokens`). A chat gets the endpoint as `APPTOWN_MAIN_REMOTE`,
  a plain URL, and the token as an `Authorization` header that git sends to
  that URL only (`GIT_CONFIG_*`), so no remote, error or pasted command
  carries it. Each chat start mints its own token (`apptown_pull_…`); only its
  hash is stored, and every token lasts until the project is removed. Containers reach it at `host.docker.internal`, which they already
  resolve, on the read-only listener: a fetch's one POST, to
  `git-upload-pack`, is let through there.
- **A step's scratch push** is the one push the read-only listener takes.
  An agent step's runtime token (live attempt, enabled owner, as for the
  API) may push to exactly one ref, its attempt's
  `refs/apptown/scratch/change-<change>/step-<step>-a<attempt>`, so that
  `apptown check` can check work AppTown has not collected yet. Every other
  ref is hidden from that push, which git itself refuses, and the hook
  refuses any other ref and a deletion again. The token fetches nothing:
  it is admitted only for the receive-pack discovery (`GET info/refs?service=git-receive-pack`)
  and RPC (`POST git-receive-pack`), read from the method and path as git's
  backend reads them, never from the query alone, and its backend runs with
  upload-pack and file serving off. A service query that names another
  service than the path is refused for every credential; a Change ticket pushes nothing on that listener. A scratch commit
  moves no branch and is never evidence: a check of it is feedback only.
  The ref is removed when its attempt settles, and the scheduler's sweep
  removes any an attempt no longer live left behind. No size limit beyond
  git's own applies to the push.
- **Any dirt on the hub is an integrity event.** Only AppTown writers touch
  the hub's working tree and all of them leave it clean, so a dirty hub is
  recorded with a diff snapshot, shown on the project page, and resolved
  only by an admin's recorded keep-or-discard.

## 4. Push and merge-request policy

- **Pushes to the mirror are automatic and unglamorous**: after a landing,
  the hub pushes the branch to the attached remote (`pushBranch`). Failure
  never blocks the engine; it is logged and surfaced.
- **Merge requests are not the gate.** A `scm/mr` step opens a merge request
  for people who review in GitLab, but the landing decides. A landing whose
  run records a merge request merges through it only when GitLab guarantees
  that what merges is exactly the checked candidate; otherwise it is refused
  ([SCM package guide](../../internal/forge/scm/README.md)). There is no
  `scm/mr-check` and no commit status.

## 5. GitLab specifics

- **Attach a remote** per project (`handleAttachRemote`): the URL is stored
  on the project; the token comes from the instance's GitLab integration and
  reaches git through an askpass helper written into the data directory, so
  it never lands in `.git/config`.
- **New project bootstrap**: `apptown new` initialises a hub with a first
  commit; importing an existing repository clones it into the hub. Both
  work with no remote at all.
- **AppTown's own repository** is on GitHub and uses GitHub Actions
  (`testing.md`); GitLab is load-bearing only for projects that attach it.

## 6. What lives in the repository (`.apptown/`)

Workflows, the guidance manifest and ADRs are not kept in the tree; the
library and adoption receipts hold them (`state-model.md` §3). In the tree:

- `.apptown/app.yaml` — the runtime contract, the one `.apptown` path that
  belongs in history (`contracts.md`).
- `.agents/skills/` — on-demand skills, content pinned by the same snapshot
  as the code.
- Everything else under `.apptown/` is runtime: the steering file for the
  current step, transcripts and session records, scan outputs, attachments,
  the agent's Docker credentials. `stageAll` stages by pathspec and refuses
  all of it, so an agent that replaces a project's `.gitignore` cannot get
  credentials committed into branch history.

ADRs live in the `adrs` table. Workflows, guidance and policies live in the
library, and a project's adoption receipt names the versions it is on.

## 7. Scope across protocols

Every
credential on the git endpoint is scoped, so the backend speaks protocol v0
only (the client's `Git-Protocol` header is not passed on): protocol v2's
fetch serves any object named by its ID, hidden refs notwithstanding, while v0
refuses a want no advertised ref reaches. Plain-file reads are off for every
credential. receive-pack keeps its own `receive.hideRefs`: a Change ticket's
push discovery shows main, its branch and its holding ref only; a step token's
shows only its scratch ref and what it builds on.
