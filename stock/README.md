# Stock workflows

Example applications of the framework — the library of repeatable tasks
AppTown exists to run (state-model §0: the unit of work is the task-run; software
development is the special case). Each entry: shape, gates/receipts, and what
engine features it needs. **Shipped** = YAML in this directory, exercised.
**Runnable** = YAML here, works on today's engine. **Sketch** = awaits the
named engine feature.

## Development lifecycle

| Workflow | Shape | Receipts / gates | Status |
|---|---|---|---|
| **product-spec** | idea → agent drafts spec (skills+template, steered) → review → agent tightens → accept | spec artifacts, review trail | **Experimental** (`product-spec.yaml`) |
| **implement** | brief or spec → agent builds in workspace (verify-as-you-go) → review → agent self-verifies w/ services → accept | REPORT.md/VERIFY.md, workspace | **Shipped** (`implement.yaml`) |
| **plan-execute** | agent writes a plan with acceptance criteria → person approves the plan → agent implements → review against the criteria | the approved plan as an artifact | **Shipped** (`plan-execute.yaml`) |
| **fix-bug** | agent commits a failing reproduction → smallest fix → same reproduction passes; "cannot reproduce" stops for a person | repro → fix → repro passing | **Shipped** (`fix-bug.yaml`) |
| **quick-change** | small change → one approval → landing ships it (console C2 pairs with it) | CHANGE.md | **Shipped** (`quick-change.yaml`) |
| **land** | the only path from a change's branch to main: catch up with main, tests, scans, guidance checks → one person's approval → merge, deploy, release record | the approval's stated meaning; scan and check evidence; release record | **Shipped** (`land.yaml`, a system workflow) |
| **ship** | callable pipeline: scan source and image, deploy to `main`, require the release record (`workflow/call: ship`) | release artifact | **Shipped** (`ship.yaml`, a system workflow); `land` carries its own ship stage, and no stock workflow calls it today |
| **dependency-uplift** | agent applies patch/minor updates → build+tests → dep scan → report → approve; majors surfaced, never applied | the auditor's "do you patch monthly?" answered by completed runs | **Shipped** (`dependency-uplift.yaml`) — scheduled monthly |
| **security-triage** | CVE/advisory list → agent assesses applicability → patch (sub-workflow) or waive with ADR | waivers are ADRs; patches are runs | Sketch — `workflow/call` exists; the triage prompt does not |
| **release** | promotion to the intranet: stage the committed tree, build, push, deploy the stack, numbered record with its log | release rows + release events | Not a workflow: built into AppTown (`internal/apptown/deployment/intranet_release.go`, `apptown release`); an internet rung is not |

## Integrations

| Workflow | Shape | Receipts / gates | Status |
|---|---|---|---|
| **adopt-integration** | an integration is attached to the project → agent wires the app to it, following the integration's guidance → checks it builds and runs with and without it | the wiring change and its checks | **Shipped** (`adopt-integration.yaml`, a system workflow; started by wiring an attached integration, or `apptown adopt`) |
| **retire-integration** | an integration is retired → agent removes it → checks nothing still refers to it and the app runs without it | the removal change and its checks | **Shipped** (`retire-integration.yaml`, a system workflow; started by `apptown retire`) |

## Knowledge & meta

| Workflow | Shape | Receipts / gates | Status |
|---|---|---|---|
| **excavate** | existing code → agent writes REQUIREMENTS.md, each requirement pointing at its code → second agent reviews → person rules on open questions and accepts | REQUIREMENTS.md committed | **Experimental** (`excavate.yaml`) |
| **research** | question → agent investigates (web via curl, files), NOTES.md w/ sources → scope check → synthesis w/ citations → accept | notes + cited report into the project record | **Experimental** (`research.yaml`) |
| **adr-draft** | decision context → agent drafts options/tradeoffs ADR → human edits/ratifies via ADR lifecycle | the ADR itself | Runnable in spirit today (agent step + adr/require) |
| **distill-workflow** | run trace (`apptown trace --json`) → agent drafts workflow YAML, parameterized → review → ratify as provisional | the new workflow, its provenance = the trace | Sketch — wants process-change governance |
| **process-change** | proposed edit to workflows/guidance → ADR required → approval → applied | governance receipts (state-model §7) | Sketch — needs governance bindings |
| **reconcile-history** | find unattested commits → agent classifies into semantic groups → propose retroactive runs → human ratifies | synthetic `did` records; the escape hatch closing itself (§3.1) | Sketch — needs git-backed state (schedule trigger available) |
| **guidance-review** | periodically: compare pinned stances against actual ledger behavior → propose supersessions/bumps | taste kept honest by data | Sketch — needs evidence views (schedule trigger available) |
| **audit-report** | period + question set → compile receipts from ledger → report artifact ("SDLC conformance Q3") | the audit is itself an attested run | Sketch — needs evidence views |

## Operations (the non-dev loops)

| Workflow | Shape | Receipts / gates | Status |
|---|---|---|---|
| **bug-triage-batch** | scheduled: read public tracker → agent compiles/deduplicates complaints → batches into work units → human approves batch → implement sub-runs → track | triage decisions + per-unit runs | Not built — needs a webhook trigger, batches (§5) + `workflow` step |
| **incident-response** | monitoring webhook → agent diagnoses (reads free, effects gated) → proposed remediation → gated effect steps → postmortem doc | timeline is the run; postmortem is an artifact | Not built — needs a webhook trigger and effect steps |
| **email-batch** (reference) | ingest 1M elements → foreach transform (recorded) → aggregate gate (≥99.9% + failures triaged) → gated send (effect) | element table, aggregate evidence | Sketch — the state-model §5 exemplar |

## Conventions all stock workflows follow

- Reports are **files** (`REPORT.md`, `NOTES.md`, …) captured via `output.file`;
  chat is not the record.
- Services (databases etc.) come from the contract's `needs:` (fulfilled
  as emulators for agent steps that say `needs: true`) — agents never
  install servers.
- Rejection feeds back: prompts include `${{feedback.latest}}` so reject →
  re-run → rework works everywhere.
- Effects (deploy, send, post) sit behind their own gates, always last.
- A workflow whose outcome is a record rather than commits (research,
  product-spec) declares `role: record`: accepting it closes the change,
  and nothing lands or redeploys. Work that changes the app is
  `role: working`, and its acceptance starts the landing.
- Long-lived state uses `workspace: project`; scratch tasks use per-run
  workspaces.
