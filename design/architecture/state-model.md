# Forge — State Model

Status: **as built**, refreshed 2026-09-06. The founding draft (2026-08-24)
was the "going meta" round: what a project *is*, why runs are branches,
where effects live, how guidance steers the instance. Its thesis (§0) and
its boundaries (§2, §8) held and are kept nearly verbatim. Its mechanisms
were built differently in three places, each marked: state did not move
into the repository (§3), the branch became the Change rather than the run
(§4), and guidance grew versions, packs and adoption receipts rather than
governance bindings (§6, §7). Section numbers are kept because code and
other documents cite them.

## 0. Purpose

The step beyond coding agents. Coding agents are general — they can do
almost any task, correctly scaffolded — but by default nothing compounds
across projects, and nothing about *how* work happened is demonstrable. The
forge is the machine that fixes both:

> **A project is checkpointed state plus the attested history of the
> repeatable tasks that produced it. The forge runs those tasks — by agents
> or humans — under pinned steering, through evidenced gates, so that effort
> compounds across projects and *how* is as demonstrable as *what*.**

Consequences the design committed to, and how each stands:

- **Features and merge requests are the wrong unit.** Built: the unit is the
  **Change** — a branch, a workspace, a story — and a run is a procedure
  executed on it. A diff is a review surface, not the work.
- **Attestation is audit-grade, not forensic-grade.** Built: receipts (the
  steering text and its hash, the model, the workflow origin, the adoption
  revision, who approved against which written standard) and the ledger.
  No cryptographic envelope; git trailers and rows are the chain.
- **Compounding is the store.** Built: the guidance library, the workflow
  library, packs with pinned versions, skills as blocks — the versioned,
  governed cross-project memory.
- **Judgment is the protected resource.** Built: gates are the product; the
  enemy is approval fatigue, so every approval names its standard and the
  change page leads with the one next action.
- **Conversation is the interface; workflows are the actuator.** Built: the
  chat sessions (`design/subsystems/console.md`); the engine never chooses.

## 1. What a project is

A project has three components. As built:

1. **State** — the code, in the project's hub repository; the app's runtime
   contract `.forge/app.yaml` in that tree; and the project's **adoption
   revision** — the receipt naming every guidance block at its version and
   every workflow at its library version, plus additions and exclusions.
   Settings rows (members, environments, secrets bindings, decisions,
   integrations) round it out. Process definitions are state, but they are
   state in the library, referenced by receipt, not files in the repo (§3).
2. **Processes** — workflows: typed, gated procedures. A run is a proposed
   change moving through gates, and a landing is the transaction that
   commits it.
3. **Ledger** — runs, steps, events, artifacts, approvals, authorization
   decisions, token usage, the audit log. Append-only. The ledger records;
   it is not itself state.

## 2. The boundaries that matter

### 2.1 Pure vs effectful

Computation over state the forge owns can be branched, rerun, discarded and
compared. An **effect** — deploy to a tier, push to a mirror, provision a
cloud resource — is a merge into a store that cannot be branched: reality.
**Gates belong at effect boundaries.** Built exactly so: previews are
branch-side and free; landing, releasing and every cloud reconcile are
effects behind gates, and each records a generation so a restart resumes
or fails it rather than repeating it.

### 2.2 Diffable vs opaque

Branch and compare pay off only where the branch states diff meaningfully.
Built for text: the change's diff against its base is the compare surface
on every change page and in the reviewing agent's brief. Not built:
schema-aware diffs for data, and the fan-out/compare/select workflows that
would need them.

### 2.3 Replayable vs deterministic

With models in the loop, bit-determinism is unattainable. The contract is
**recorded nondeterminism**: pin the inputs, record the outputs. Built as
receipts — every agent step records the exact steering, the model, the
usage and its output, and the session record (JSONL) is the source of
truth for what was said. Not built: a replay mechanism that re-executes
from the record.

## 3. Where state lives

The founding rule was "state lives in git": workflows, policies and ADRs as
files under `.forge/`, with the database demoted to an index. **That was not
built, and will not be.** What was built:

- **Code lives in git**, in a hub the forge hosts (`git-state.md`). The only
  process file in the tree is `.forge/app.yaml`, the runtime contract, and
  optionally skills under `.agents/skills`. The rest of `.forge/` is runtime
  scratch the engine refuses to stage.
- **Ways of working live in the library**, versioned append-only, and a
  project holds a **receipt** naming the versions it is on. This gives the
  reproducibility the founding draft wanted from git — a run's steering is a
  pure function of its adoption revision — without asking every project to
  carry copies of instance-level documents, and it lets the instance cut a
  new stock version without touching any project
  (`design/subsystems/ways-of-working-versions.md`).
- **Decisions (ADRs) live in the database**, per project, with a status
  lifecycle, and the `adr/require` step gates on them.
- **The ledger is relational** and never branches, as always intended.

Bulk data behind manifests (the DVC pattern) was never needed and is not
built.

### 3.1 Checkpoints: working state vs committed state

The distinction held, realised as **versions and receipts** rather than
commits of process files:

- A guidance block, a library workflow, a pack: editing makes a new version;
  old versions stay resolvable forever because a receipt may name them.
- A project's way of working: **prepare** shows the candidate revision and
  its consequences (new checks, weakened checks, conflicts); **adopt**
  records a new revision. Nothing reachable from a run's receipt is ever
  rewritten.
- A change's working tree is lawless; a landing **sweeps** uncommitted chat
  work into an attributed commit before the gates run, so gates judge
  exactly what will merge.

The **escape hatch** was built as the chat door rather than as `forge did`:
a person talks, the helper edits the workspace directly, and the landing is
the same gated path as everything else. The check-level sibling — an
override with a recorded reason — exists. `forge did`, reconcile-history and
the coverage view were not built; every accepted change already has a run,
because landing is the only way to `main`.

The payoff stands: **git history is the syntactic record; run history is
the semantic record.** Commits carry `Forge-Change`, `Forge-Run`,
`Forge-Actor`, `Forge-Provenance` and, on a landing merge,
`Forge-Operation` trailers.

## 4. The Change is the branch; landing is the merge

The founding draft made each run a branch. As built, the **Change** is the
branch and the workspace; runs attach to it and share it:

- Opening a change clones the hub into `workspaces/change-<id>` on a branch.
  Chat turns and working runs accumulate commits there; the clone is the
  authority for its branch until it lands, mirrored into the hub at every
  commit point.
- A run may **call** another workflow as a child run: its own ledger and
  steps, the same workspace and branch, version pinned at invocation,
  approvals bubbling up (`design/subsystems/composition.md`).
- **Landing** is the one path to `main`: catch up with main, tests, scans,
  guidance conformance, the approval, the current-rules check, the merge,
  the deploy, the release record. Failed and abandoned changes leave their
  branches unmerged.

Fan-out, compare and select were not built. The vocabulary they implied
(`fanout`, `compare`, `select`, `effect`) is not in the schema.

### 4.1 Materializations: the review surface of a branch

Kept as the rule: **checks without evidence are invalid; gates without
materializations produce rubber stamps.** The ladder, as built:

| Rung | Form | As built |
|---|---|---|
| **read** | rendered text | change report, review verdict, steering text, trace |
| **compare** | diffs | the change's diff against its base, on the page and in the reviewer's brief |
| **browse** | served artifacts | attachments on a change (`.forge/attachments/`), scan reports, SBOMs |
| **use** | preview environment | one preview per change from its working tree; the boot gate; **rehearsal** after every chat turn that leaves a runnable app |
| **inhabit** | a shell in the workspace | the change chat's terminal — the workshop *is* the workspace |

The **preview / release split** is exactly as designed: a preview
materializes a branch (disposable, built from the working tree, never
cached); a release is an effect (committed tree only, exact digest,
never rebuilt at deploy). Environments sit between: committed `main`,
redeployed per landing.

### 4.2 Servable HTML artifacts

Not built. The security caveat stands unchanged: agent-generated HTML must
never be served from the API origin. Scan reports render as structured
findings instead.

## 5. High-cardinality work

Batches with element tables and aggregate gates were not built. The nearest
thing is the **remediation queue**: a conformance review fans over every
project, and fixes proceed one Change per project at a time, with the
queue itself recoverable across a restart
(`design/subsystems/remediation.md`). If a batch stage is ever needed, that
queue is the pattern to generalise.

## 6. Guidance: decisions, rules, conventions

The three genres held, with one collapse: **rules and conventions are the
same block.** A guidance block is a markdown document with front matter —
`name`, `tags`, `conflicts` (the blocks it cannot be held with), and
`checks`. Its body steers every agent step; its checks run at the
conformance gate and in reviews. A check has a name, what it expects, a
severity (`note` … `critical`), an evaluator (`sh` for the deterministic
subset, `llm` with a rubric and a file scope for judgment) and whether it
reports occurrences by line. Decisions stay separate as ADRs (§3).

### 6.2 Blocks, not monoliths

Built as designed: the smallest unit is one stance or one rule
(`data/networked-database`, `prose/no-claude-speak`, `lang/go/formatting`),
individually versioned, append-only. `skill/*` blocks are procedures an
agent follows; `resource/*` blocks were designed but the repository path
`.agents/skills` took that role.

### 6.3 The instance level is a library, not a scope

Held: **nothing steers a run unless the project's adoption revision names
it.** The library is a registry; a project is on one or more **packs** plus
its own additions and exclusions; the union, resolved and written down, is
the revision. Stock releases cut new block and pack versions and stop
there. New-project defaulting exists only at creation: the wizard offers
packs, and the platform pack is applied to every repo-backed project.

### 6.3a Compilation: the monolith is a build output

Built: effective guidance is compiled deterministically from the revision
in precedence order, materialised as the steering file, and its hash is
recorded in the run's evidence. The contradiction lint became **declared
conflicts**: a block names its rivals, prepare refuses a revision that
holds both, and a project already holding both shows a condition only a
person can resolve.

### 6.4 Packs: sharing the way of working

Built: a pack bundles guidance blocks at pinned versions, workflows at
pinned library versions, policies, a toolchain, extensions and starter
files. Packs have versions; a version is cut when the pins change. The
platform pack `forge` carries what running behind the forge asks of any
app and the system workflows; it is never offered in the wizard. Rivalry
holds — `go-small` and `conventions-only` and the game packs coexist.
Publishing packs across instances is not built.

### 6.5 Guidance is state, so guidance is governable

Built as **adoption**: moving a project between revisions is a recorded
act, prepare shows what it would change, weakening a gating check needs
the standards capability, and a move that would need code changes can be
carried by a Change of type uplift that flips the revision when it lands.
The general path-to-workflow binding (§7) was not built.

## 7. Governance

The founding "governance bindings" — state paths mapped to required
workflows, with a constitution-style amendment clause — were not built.
What governs, as built:

- **Capabilities** decide who may do what, and every decision is a receipt
  (`design/subsystems/authorization-capabilities.md`).
- **Policies** are rules checked when a workflow is saved and re-checked at
  merge (the current-rules check); a landing that was waiting when the law
  changed is paused, never grandfathered.
- **Adoption** is the recorded act by which a project's law changes (§6.5).
- **System bindings** let an admin stand a custom workflow in for a stock
  system one for every project at once, with a receipt.

### 7.1 System workflows: the forge's own pack

Built as designed in spirit: workflows carry an **origin** (`stock/…` or
`lib/…`), stock entries ship inside the binary and are synced into the
library at boot with a versioned receipt when they change, a customised
copy is a clone plus a binding rather than an edit, and the run records
what ran. `metadata.system: true` marks the workflows nobody picks —
`land`, `ship`, `adopt-integration`, `retire-integration` — and the
platform pack pins them.

Of the vendor-pack contents the founding draft listed, the built ones are
the conformance reviews and the remediation queue, the container reaper,
and the recovery passes at boot. Reconcile-history, distillation, guidance
review, the self-improvement loop and forge-doctor are not built. The
safety property stands as a rule: anything that proposes a change to the
law opens a Change; nothing mutates directly.

## 8. What never branches

Unchanged, with one addition:

1. **The ledger** — what happened is a fact.
2. **Identity and approvals** — who decided, against which standard.
3. **The engine itself** — its configuration is state and governable.
4. **Receipts** — an adoption revision, a pack version, a block version, a
   run's steering hash: once anything references it, it is immutable.

## 9. The founding migration path, and what became of each step

| Step | Fate |
|---|---|
| 1. Guidance library | built (Aug 25) |
| 2. Manifests + compile | built; manifests became adoption revisions (Sep 6) |
| 2a. CLI, trace, audit log | built |
| 3. In-repo state sync | **not built; superseded** by the library and receipts (§3) |
| 3a. Materializations | built: diff, attachments, previews, rehearsal, the terminal |
| 3b. Docker executor + previews | built, plus the private daemon and the front door |
| 4. Effect step type | not built as a type; effects are the landing and the drivers, each generation-recorded |
| 5. Batch + foreach | not built; the remediation queue is the nearest (§5) |
| 6. Fan-out / compare / select | not built |
| 7. Governance bindings | not built; capabilities, policies and adoption instead (§7) |
| 8. Break-glass lane | not built; the chat door and recorded overrides cover it (§3.1) |
| 9. Triggers | built: schedule and webhook (`workflow-spec.md` §2.2) |
| 10. Evidence views | partly: conformance reviews as saved records, the token ledger, the audit log; no saved query reports |
