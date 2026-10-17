# ADR: Workflows as lanes — no more "install into"

Status: **accepted; phase one shipped 2026-09-03** (owner decision: no pins). Admins customise by cloning at the instance and, for system workflows, binding the stock name to their clone (`system_workflows`). Companion to
design/decisions/interactive-vs-pipeline.md (the seam this keeps), design/decisions/policy-v2.md
(rules move with the lanes), design/subsystems/ui-coherence.md (Ways of working).

## What the numbers say (the live instance, 32 projects, 2026-09-03)

| workflow | installed into | runs | modified copies |
|---|---|---|---|
| land | 27 | 62 | 0 |
| quick-change | 21 | 32 | 0 |
| dependency-uplift | 11 | 12 | 0 |
| ship | 16 | 12 | 0 |
| build | 17 | 11 | 0 |
| patch-dependencies | 10 | 11 | 0 |
| implement | 20 | 8 | 0 |
| change-request | 10 | 7 | 0 |
| product-spec | 20 | 6 | 0 |
| excavate | 13 | 4 | 0 |
| adopt-integration | 12 | 3 | 0 |
| research | 15 | 3 | 0 |
| fix-bug | 17 | 2 | 0 |
| plan-execute | 11 | 2 | 0 |
| retire-integration | 11 | 1 | 0 |

Fifteen stock workflows. **Zero installed copies have ever been modified.**
Every pack installs one of two near-identical lists (twelve for services,
five for games). Three names are invoked by the forge itself, never picked
by a person: `land` (every landing), `ship` (called by land),
`adopt-integration` (the wire-it-in door); `patch-dependencies` runs on a
schedule. The per-project copy — "install into…", "update → v12", the
project workflow editor, policies enforced at project-workflow save — is
ceremony that nobody has used once.

## Diagnosis

Two different things wear one word:

- **System workflows** are how the forge does its own jobs: landing,
  shipping, wiring an integration in or out, the monthly patch. A person
  never picks them; they should never appear in a "run a workflow…" menu,
  and a project should never have to "have" them.
- **Lanes** are ways a person chooses to work on a change: a quick change,
  a full implementation, a bug fix, plan-then-execute, a spec, research,
  an excavation. They are instance-level ways of working; a project
  *selects* which apply, it doesn't own copies.

And the copy mechanism answered a need that hasn't materialised
(per-project customisation) while creating three real costs: a stale-copy
problem ("update → v12" on every project), a second editing surface
(project workflow editor) beside the library's, and a policy check that
runs at project-workflow save instead of where the rule is adopted.

## Decision (proposed)

1. **Workflows live at the instance; projects select, never copy.** The
   library is the only source. A project's "Way of working" is a
   *selection* of lanes (a checklist, defaulted by its pack) plus the
   policies it adopts. Runs record `stock/quick-change@10` exactly as now —
   pinning is a receipt on the run, not a file in the project. A rare
   per-project pin ("stay on v9") is an override row, like a model slot.
2. **System workflows are hidden from choice.** Marked `system: true` in
   their YAML. The forge invokes them by name; the change page's "run a
   workflow…" and the front desk's invoke list never offer them; Ways of
   working shows them under a **System** heading with one line each on
   *when the forge runs it*.
3. **Customising = clone at the instance.** "Clone to yours" already
   exists in the library; a project selects the clone instead of the
   stock one. The project-level workflow editor and `workflows/_new` route
   retire.
4. **Policies apply to lanes where they are adopted.** A rule checks the
   library workflow when it is saved and the project's selection when it
   changes — no project-copy save event to hang off.
5. **Prune to what's load-bearing.** Merge pairs that are one job with two
   triggers or two names:
   - `build` + `implement` → **implement** (from a spec artifact when one
     exists, else from the change body — the same agent step already
     branches on that).
   - `quick-change` + `change-request` → **quick-change** (opening an MR is
     an SCM setting on the project, not a different way of working).
   - `dependency-uplift` + `patch-dependencies` → **dependency-uplift**
     with `schedule:` as a trigger (system when scheduled, lane when a
     person asks).
   Result: system = land (ship folded as its stage), adopt-integration,
   retire-integration; lanes = quick-change, implement, fix-bug,
   plan-execute, dependency-uplift, product-spec, research, excavate.
   Fifteen become eleven, three of them invisible.
6. **Packs stop listing workflows.** A pack = guidance + policies + a
   *lane profile* (`service` or `game`); the profile is the default
   selection. The two copy-pasted lists disappear.

## System workflows are parametrised by attachments, never copied

The objection that decides the design: "wiring in a scanner
potentially customises the land workflow — what if we want container
scans?" Today `land` names its scanners (`scanner: osv`, `gitleaks`,
`sbom`), so a container scan means editing land, and editing a system
workflow is exactly what per-project copies existed to allow.

The forge already answers this everywhere else: `adopt-integration` reads
the attached service, `deploy` reads the bound target, `tests` read the
declared toolchain. **Land reads the attached scanners.** System workflows
declare *scan points*, not scanner names:

- gates: `uses: scan` `with: { on: source }` — every attached scanner whose
  manifest says `target: source` (osv, gitleaks, syft today), on the
  committed tree, as now;
- ship, after the image is built and before it is deployed:
  `uses: scan` `with: { on: image }` — every attached scanner with
  `target: image` (trivy, grype), on the artifact that will actually run.

Attaching trivy with `target: image` puts container scanning into every
landing on the instance — or on the projects it is attached to — with no
edit to land. Per-project control stays where it is: which scanners a
project uses, `scan-failon-ceiling`, suppressions. "Wiring in a scanner"
is an attachment, the same act as wiring in error reporting, with the
same card and the same proof.

The rule this gives the whole design: **if you ever need to edit a system
workflow, the forge is missing an attachment point.** Add the point; don't
fork the workflow.

Costs: one manifest field (`target`), one scan-step mode (`on:` expands to
the attached set, in manifest order, each as its own step with its own
receipt), and ship gains an image scan point where it builds.

## What changes where

- **Engine**: workflow starts resolve from the library at the project's
  adoption pins. The retired per-project copy tables were kept temporarily;
  schema 69 archives their rows in `audit_log` and removes the tables.
- **UI**: Settings › Way of working becomes "which lanes this project uses"
  (checklist with one line each) + rules + guidance. "Install into…",
  "update → vN", per-project YAML editor, `/projects/:slug/workflows/*`
  retire. Ways of working › Workflows gains System/Lanes headings; a lane
  shows *when to use it* before *what it does*.
- **CLI**: `forge invoke -w` accepts lane names only (system names are
  rejected with a sentence saying who runs them).
- **Migration**: every existing project's installed set becomes its
  selection; modified copies (there are none) would become instance-level
  "yours" clones selected by that project.

## Consequences

- One editing surface, one version of every workflow, no stale copies.
- The run receipt is unchanged: `origin` and `workflowVersion` already say
  exactly what ran.
- Losing: per-project YAML divergence. Not lost in practice — it never
  happened — and recoverable through clone-and-select.

## Open questions

- Does a project-level pin ever matter, or is "always latest" plus the
  receipt enough? Lean: ship without pins; add the override row if a real
  case appears.
- `ship` is also a callable pipeline from `land`; folding it is cosmetic.
  Keep it callable, mark it system, don't list it.
- Custom instance workflows (`team/nightly-audit`, 0 runs): keep the
  mechanism, since "clone to yours" is the customisation story.

## Phase one (shipped 2026-09-03)

- `metadata.system: true` on land, ship, adopt-integration,
  retire-integration, patch-dependencies. The change page's "run a
  workflow…" hides them.
- The engine resolves every run — invoked, landing, wiring, `workflow/call`
  — from the library's latest version (`resolveWorkflow`). **Superseded
  2026-09-06** by `ways-of-working-versions.md` §4.1/§9 step 1: a run
  resolves the version the project's adoption revision pins (a lane
  selected since resolves as of that revision), and the library's latest
  only for a project that has no revision. A workflow
  authored on a project and absent from the library still runs, recorded
  as `project/<name>@v`, until phase two retires that editor.
- `project_lanes` is the selection; migrated once from every project's
  installed set; a row in the old table counts as selected unless a lane
  row disables it. Installing/removing keeps the selection in step.
- `system_workflows` binds a stock system name to a custom library
  workflow that is itself marked system (GET/PUT `/system-workflows`,
  admin, audited). This is how an admin customises landing or the
  dependency uplift for the whole instance.
- Runs record `workflow_origin` (`stock/quick-change@10`,
  `lib/our-land@1`, `project/flow@1`) — the receipt that replaces pins.
- Policies the project adopted are checked at resolve time as well as at
  library save.

Not yet (phase two): the Way of working tab as a lane checklist; retiring
install-into, update→vN and the project editor; the merges (build+implement,
quick-change+change-request, dependency-uplift+patch-dependencies); scan
points in land/ship; packs as guidance + policies + lane profile; a UI for
the system bindings.

## Phase two, stages 1–2 (shipped 2026-09-03)

- **Projects select, nothing is copied.** Every reader of the per-project
  table (list, steering, first-time-user flow, catalog, pack status/pull, policy
  conformance, call cycles, scheduler, webhooks, slot consumers, traces)
  reads the library plus the selection. Settings › Way of working is a
  checklist under Core / Extras / System. Per-project workflow pages and
  editor retired; "install into…" retired; "used by" is a count.
- **The merges.** `implement` absorbs `build` (brief or spec; self-verify
  with declared needs running; one acceptance). `quick-change` absorbs
  `change-request` (the GitLab MR step is a clone-and-add). The monthly
  schedule lives on `dependency-uplift`, which absorbs `patch-dependencies`
  (and gains emulators for its verification). The absorbed three stay in
  the library as `deprecated: true` under Extras so old runs still read;
  selections migrated once. Packs list the merged names. System = land,
  ship, adopt-integration, retire-integration.

## Phase two, stage 3 (shipped 2026-09-03): scan points

`land` and `ship` no longer name scanners. Their gates declare `uses: scan`
with `on: source`; their ship stage declares `on: image` before deploy. At
stage entry each point expands to one step per attached scanner with that
`target` (a new manifest field, default source), each with its own
receipt; a point with nothing attached becomes one passing step that says
so. Image-target scanners get the contract's built image (or its pulled
`image:`) as a saved tarball via `${IMAGE_TAR}`; apps that ship no image
pass with a note. Stock gains `trivy` (`target: image`), so every landing
of a containerised app now scans the container it will run. The
container-scan test from the review is met with no edit to land.

## Phase two, stage 4 (shipped 2026-09-03)

The System tab carries the bindings control (what the forge runs for
each stock system workflow; a stand-in must say `system: true`). Packs
call their workflow list the default lanes a new project starts with —
that is the "lane profile"; a separate profile field was not needed.
`product-spec` and `research` stay as Extras rather than retiring: the owner's
tiering puts the unsure ones in Extras, visible, instead of deleting them.

Phase two is complete. Fifteen stock workflow files: four Core, four
Extras, three deprecated stubs kept for old runs, four System.
