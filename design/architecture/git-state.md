# Forge — Git-Backed State

Status: **as built**, refreshed 2026-09-06. The founding draft (2026-08-24)
had runs as worktrees, GitLab as the remote of record, and process files
synced into the repository. §3a, written 2026-09-01, already recorded the
turn to the collaborative model; this version makes the whole document
describe that model and marks what the founding phases became. Shipped
version for operators and agents: Docs → "How changes reach main".

## 1. Roles: the forge hosts the repository; a mirror is optional

- **The hub is the project repository**, at
  `<dataDir>/workspaces/project-<slug>`. It is where `main` lives and it is
  written by exactly one thing: the landing flow, which merges locally.
  Everything the forge does — branch, commit, merge, deploy — works against
  the hub and its clones. No remote is required for any of it.
- **GitLab is a mirror**, when a project attaches one: landed branches are
  pushed to it for backup and for people who want to read code there. It
  is never the thing the engine blocks on. The founding "remote of record"
  and the SCM adapter behind which GitLab would sit were not built.

The forge owns process state (the ledger, in Postgres); git owns content
state; a mirror hosts a copy.

## 2. Change ↔ branch ↔ clone

The founding scheme was a branch and a worktree per run. As built, the
**Change** is the branch, and every workspace is a **self-contained clone**:

- Opening a change clones the hub into `workspaces/change-<id>` on its own
  branch. The clone has real git inside every container (a worktree's
  pointer file resolved to a host path a container could not see; that is
  why worktrees went). It has **no push remote**: transport is exclusively
  the host's job, so an agent can commit but never push.
- The clone is the **authority for its branch** until it lands. The host
  mirrors it into the hub at every commit point, forced, so a rework that
  rewinds history mirrors faithfully.
- Commits carry trailers, which are the semantic-history link:

  ```
  Forge-Change: 41
  Forge-Run: 133
  Forge-Actor: alice
  Forge-Provenance: console
  ```

  The engine commits a step's leftovers with `Forge-Run` and
  `Forge-Workflow`; a landing **sweeps** uncommitted chat work into a commit
  with console provenance before any gate runs; the landing merge carries
  `Forge-Operation`, the id the recovery pass uses to tell a merge that
  finished from one that did not.
- **Landing is the merge.** A child run (a workflow called from another)
  shares its root's clone and branch and never merges on its own. Failed
  and abandoned changes leave their branches in the hub unmerged.
- **The diff is the compare surface**: the change page and the reviewing
  agent both read the branch's diff against its base.

## 3. Freshness and conflicts

The founding "fetch on start, block on conflict at merge" became a gate:

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

### 3a. The collaborative model (2026-09-01)

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
  adopts it; its files are reported as not written. Until 28 September 2026
  each of the three wrote to any project's hub.

- A **pre-receive hook on the hub** (`installNoPushHook`) refuses every push
  but two narrow ones: a Change ticket's push to its holding ref, and a
  step's push to its own scratch ref (below). Safe for the forge, which never
  pushes to its own hub; it closes the one channel that would bypass the
  gates.
- A **scoped git-over-HTTP endpoint** (`githttp.go`, `git http-backend`
  behind `net/http/cgi`) lets a container **fetch `main` only**:
  `uploadpack.hideRefs` hides every other ref, so no other change's branch
  is listed or fetchable; the credential is a minted read-only, one-project
  token (`git_pull_tokens`). A chat gets the endpoint as `FORGE_MAIN_REMOTE`,
  a plain URL, and the token as an `Authorization` header that git sends to
  that URL only (`GIT_CONFIG_*`), so no remote, error or pasted command
  carries it. Each chat start mints its own token (`forge_pull_…`); only its
  hash is stored, and every token lasts until the project is removed. Containers reach it at `host.docker.internal`, which they already
  resolve, on the read-only listener: a fetch's one POST, to
  `git-upload-pack`, is let through there.
- **A step's scratch push** is the one push the read-only listener takes.
  An agent step's runtime token (live attempt, enabled owner, as for the
  API) may push to exactly one ref, its attempt's
  `refs/forge/scratch/change-<change>/step-<step>-a<attempt>`, so that
  `forge check` can check work the forge has not collected yet. Every other
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
- **Any dirt on the hub is an integrity event.** Only forge writers touch
  the hub's working tree and all of them leave it clean, so a dirty hub is
  recorded with a diff snapshot, shown on the project page, and resolved
  only by an admin's recorded keep-or-discard.

## 4. Push and merge-request policy

- **Pushes to the mirror are automatic and unglamorous**: after a landing,
  the hub pushes the branch to the attached remote (`pushBranch`). Failure
  never blocks the engine; it is logged and surfaced.
- **Merge requests are not the gate.** A `scm/mr` step exists and one stock
  lane uses it to open a merge request for people who review in GitLab, but
  the landing decides, merges locally, and pushes. The founding "merge via
  the GitLab API so the MR closes properly", `scm/mr-check` and commit
  statuses were not built.

## 5. GitLab specifics

- **Attach a remote** per project (`handleAttachRemote`): the URL is stored
  on the project; the token comes from the instance's GitLab integration and
  reaches git through an askpass helper written into the data directory, so
  it never lands in `.git/config`.
- **New project bootstrap**: `forge new` initialises a hub with a first
  commit; importing an existing repository clones it into the hub. Both
  work with no remote at all.
- **The forge's own repository** is on GitLab and uses GitLab CI
  (`testing.md`); that is the only place GitLab is load-bearing.

## 6. What lives in the repository (`.forge/`)

The founding plan exported workflows, the guidance manifest and ADRs into
the tree and then made the tree the truth. **Neither phase was built, and
the library model superseded the idea** (`state-model.md` §3). In the tree:

- `.forge/app.yaml` — the runtime contract, the one `.forge` path that
  belongs in history (`contracts.md`).
- `.agents/skills/` — on-demand skills, content pinned by the same snapshot
  as the code.
- Everything else under `.forge/` is runtime: the steering file for the
  current step, transcripts and session records, scan outputs, attachments,
  the agent's Docker credentials. `stageAll` stages by pathspec and refuses
  all of it after an agent once replaced a project's `.gitignore` and the
  engine committed credentials into branch history.

ADRs live in the `adrs` table. Workflows, guidance and policies live in the
library, and a project's adoption receipt names the versions it is on.

## 7. Build phases, and what became of them

| Phase | Founding contents | Fate |
|---|---|---|
| **G1 — local plumbing** | repo per project, branches, commit-per-step with trailers, local merge, conflict handling, diff materialization | built; worktrees replaced by clones, conflict handling moved into the reconcile gate |
| **G2 — GitLab remote** | adapter, auto-push, `scm/mr` + API merge, push webhook, `forge did` | partly: attach-remote and post-landing push; `scm/mr` opens a request but never merges; no adapter, no webhook, no `forge did` |
| **G3 — state in repo** | export, then repo as truth for workflows, manifests, ADRs; commit statuses | not built; superseded by the library and adoption receipts |

**Scope across protocols (2026-10-03, from an independent review).** Every
credential on the git endpoint is scoped, so the backend speaks protocol v0
only (the client's `Git-Protocol` header is not passed on): protocol v2's
fetch serves any object named by its ID, hidden refs notwithstanding, while v0
refuses a want no advertised ref reaches. Plain-file reads are off for every
credential. receive-pack keeps its own `receive.hideRefs`: a Change ticket's
push discovery shows main, its branch and its holding ref only; a step token's
shows only its scratch ref and what it builds on.
