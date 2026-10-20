---
name: apptown
description: Invoke and drive Forge workflows from the CLI — create work items, advance runs through gates, satisfy checks, and reflect on traces. Use when asked to execute a task through Forge, operate on a apptownd instance, or analyze/distill a workflow run.
---

# Driving Forge workflows

Forge is a workflow-enforcement engine: work items move through YAML-defined
stages, and a run advances **only** when the current stage's gate (checks +
approvals) passes. Your job as an agent is to achieve goals *by invoking
workflows and satisfying their checks* — never by working around them. If a
gate blocks you and you cannot legitimately satisfy it, stop and report; do not
ask for overrides.

Binary: `bin/apptown` (or `apptown` on PATH). Configuration:
- `APPTOWN_URL` — apptownd instance, default `http://localhost:7777`
- `APPTOWN_USER` — attribution identity; set it to who you act for, e.g. `alice` (all mutations are recorded in the ledger under this name)

## The core loop

```sh
apptown projects                          # what exists
apptown workflows -p <slug>               # available workflows = your action space
apptown invoke -p <slug> -w <workflow> -t "<title>" --body "<why>"   # start a run
apptown status <run-id>                   # stages, current checks, STEP IDs
apptown advance <run-id>                  # try the gate; failure lists blockers
```

When `advance` reports blockers, satisfy each one by type, then advance again:

| Blocker type | How to satisfy |
|---|---|
| `script` (fail/error) | fix the underlying problem, then `apptown run-step <step-id>` |
| `artifact/require` | produce the thing, `apptown attach <run-id> -k <kind> -n <name> -r <url>`, then `apptown run-step <step-id>` to re-check |
| `adr/require` | an accepted ADR must be linked to the work item (via the API/UI); re-check with `run-step` |
| `approval` | a human decision — surface it and wait; only approve yourself if the user explicitly delegated that decision to you. `apptown approve <step-id>` prints what approving means (for an acceptance, also what the landing would ask) and, without a terminal, refuses until you rerun with `-y`: show the person those sentences before you do |
| a landing gate (`reconcile`, `scan`, `tests`) fails | the change itself needs work: `apptown sendback <change-id> "<what to change>"` closes the landing and starts a rework round from your note; a false-positive finding is dispositioned with a justification and re-checked, never blanket-overridden |

Repeat until `advanced — run is done ✓`.

## Reflection

- `apptown trace <run-id>` — chronological, actor-attributed event history of a run
- `apptown trace <run-id> --json` — the complete machine-readable trace: run,
  work item, pinned workflow definition (YAML + parsed), all steps with config
  and evidence, events, artifacts, pinned guidance (hash + block versions).
  This is the input for analyzing what happened or drafting a distilled workflow.
- `apptown log [-p slug] [-k adr|workflow|guidance]` — project-state changes that
  happened *outside* runs (workflow saves, ADR transitions, guidance edits), with actors
- `apptown runs [-p slug] [--status all]` — find runs to inspect
- `apptown executing` — before restarting or deploying an AppTown: the steps
  and the operations (copies, restores, take-downs) executing now in every
  project (exit 0 none, 3 some, 1 AppTown cannot say). A run waiting on a
  person survives a restart; `apptown runs` lists it as running, so do not
  read that list as "busy"

## Rules

1. Never claim a check passed without evidence — the ledger records everything.
2. Approvals belong to humans unless explicitly delegated for the specific step.
3. One work item per goal; don't spray runs. Reuse `apptown runs` to find existing ones.
4. When reporting, cite run IDs and step IDs so the user can verify in the UI.
