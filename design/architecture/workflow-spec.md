# AppTown — Workflow & Policy Spec

For the formats as a team writes them (versions, strict keys, the served
workflow schema, pinned policies), see the shipped
[ways-of-working formats guide](../../stock/docs/guides/ways-of-working-formats.md);
its save-time rules are the authority where this page is looser.

There is no expression language, no `policy` step type and no SCM or
Kubernetes step types. The validator is `internal/schema`; a schema test strict-validates every stock
workflow, so stock cannot drift below the bar it holds everyone else to.

## 1. Document shape

A Kubernetes-style envelope; one loader handles every document kind.

```yaml
apiVersion: apptown.dev/v1
kind: Workflow
metadata:
  name: quick-change
  description: Small change, one glance.
  tier: core            # core | extras — how the library shows it; system workflows have none
  category: change
  system: true          # only on AppTown's own workflows (land, ship, …); never picked
  deprecated: false
spec: ...
```

Workflows live in the instance **library** (`lib_workflows`,
`lib_workflow_versions`), versioned append-only. Stock workflows ship inside
the binary and sync at boot; a changed stock workflow becomes a new version.
A project selects lanes from the library; a run snapshots the definition it
runs (`definition_json`) and records its origin (`stock/land@10`,
`lib/our-land@2`). Workflows do not live in repositories.

## 2. Workflow spec

### 2.1 Top level

```yaml
spec:
  kind: lifecycle          # lifecycle | pipeline — shown to people as "pacing"
  role: working            # working | landing
  workspace: project       # the run works in the change's clone (the only value in use)
  on: ...                  # triggers (§2.2); manual invocation always exists
  defaults:
    model: ...             # agent/pi: "provider/model", resolved through slots × profiles
    thinking: low          # agent/pi thinking level
    executor: docker       # local | docker
    image: apptown-agent     # docker executor image
  stages: [...]            # ordered (§2.4)
```

- **`lifecycle`** ("paced by people"): stages wait at their gates for a
  person; advancing is requested and gate-checked.
- **`pipeline`** ("runs straight through"): stages advance on their own
  when their checks pass. The landing (`land`) and shipping (`ship`)
  workflows are pipelines.
- **`role`** says what the workflow is for: a **working** lane is a way of
  working on a change that a person picks; a **landing** workflow is the
  system's, run for every project when a change is accepted.

There are no declared, typed inputs (`context:`) and no expression language;
see §2.3.

### 2.2 Triggers — `on`

```yaml
on:
  manual: {}                                   # runnable from the UI, CLI, MCP
  schedule: { cron: "0 3 * * *", skipIfActive: true }
```

Built: `manual` (always), `schedule` (30-second resolution, no backfill of
missed fires, `skipIfActive` default true, fires attributed to
`apptown-system`). A scheduled fire opens a change and its run so it is
visible where people look. Not built: webhooks, `scm` triggers (no SCM
adapter) and
`workflow` as a trigger kind — calling a workflow from another is a
*step* (§2.5 `workflow/call`), not a trigger.

### 2.3 Interpolation

There is no expression language. Prompts and a few `with` fields accept
placeholders, substituted as text at step start:

| Placeholder | Meaning |
|---|---|
| `${{change.title}}`, `${{change.body}}` | the change's title and body |
| `${{run.id}}`, `${{project.slug}}` | identity |
| `${{artifact.<kind>}}` | the body of this run's latest artifact of that kind |
| `${{latest.<kind>}}` | the project's latest artifact of that kind, any run |
| `${{feedback.latest}}` | the last rejection comment or reviewer finding on this change |
| `${{preview.url}}`, `${{preview.host}}` | this run's latest recorded preview address, and its Host (with any port) for reaching it through `APPTOWN_URL`; a sentence instead when none is recorded or the preview is not host routed |

No comparisons, no functions, no loops. Anything that needs logic is a
`script` or an agent step. There is no `if:`, `when:` or
`steps.<name>.result.*`.

### 2.4 Stages, steps, gates

```yaml
stages:
  - name: apply
    onEnter: [...]              # steps run when the stage is entered
    steps: [...]                # steps run when dispatched, or on "request advance"
    gate:
      require: [report-exists, review-change, accept]   # default: every step passes
```

The enforcement contract:

1. A run is in exactly one stage, or terminal: `done`, `failed`, `cancelled`.
2. Entering a stage runs its `onEnter` steps in order; the stage's `steps`
   run when dispatched (automatically for a pipeline; on activity or an
   advance request for a lifecycle).
3. The run advances **only** when the gate holds over the check results.
   There is no other path.
4. A check may be **overridden** by someone holding the override capability,
   with a reason; the override is a recorded result, permanently visible.
5. A rejected approval or a failed review feeds `${{feedback.latest}}`;
   re-running the producing step is the rework loop. Step history is
   append-only.
6. At a landing's merge the **current-rules check** re-evaluates the
   project's policies; if they changed while the landing waited, it pauses
   with a "restart landing" action rather than finishing under old rules.

Manual retries and change send-backs share a transactional persistence boundary:

- A step retry requires a finished automatic step in the active stage. Queued
  or running work cannot be queued twice, and failed entry prerequisites must
  be retried first. Retrying an entry step or agent invalidates later entry
  results and gate verdicts, including approvals and overrides; their previous
  evidence remains in the event ledger.
- Retry mode, full feedback, pending state, cleared timestamps and receipts
  commit together. A retry revision prevents an advancement decision made
  before the retry from completing or advancing the changed work.
- Sending back completed work, or a producer in an earlier stage, creates a
  continuation run at the producer's stage. It inherits the pinned definition,
  adoption, inputs, workspace and artifact context, then reruns that stage's
  materialized steps with fresh checks and approvals. The original completed
  run stays completed; any replaced active root and its descendants are
  abandoned in the same transaction. Running steps must finish first.
- `workflow_continuations` links old work to its replacement and fences old
  landing jobs. A later completion has its own landing and completion receipts.
- The managed workflow dispatcher executes only committed work, using the same
  stage plan for immediate dispatch and restart recovery. Session identity is
  recorded at agent admission; repeated continues retain the actual conversation.
  Feedback is consumed with a successful outcome, so a crash while preparing
  or executing the attempt does not lose the human's message.

Automatic step outcomes belong to a specific execution attempt. Admission locks
the project, run and step, checks that the stage is active and the step is still
pending with the expected configuration, then increments its attempt number and
records the start in one transaction. Only that admitted runner can record the
outcome. A late result after retry, restart, override or abandonment cannot
replace newer evidence or request another rework. Reading a running step does
not grant ownership of it. This fences stored outcomes; it does not cancel an
external operation already in progress.

Workflow calls record admission with child creation: the child pins the parent's
step attempt. Child completion checks both that attempt and the recorded child
identity before passing the parent gate or promoting artifacts. A retried call
creates a new child; repeated creation of the same invocation remains idempotent.

After all agent work in a stage finishes, stale machine verdicts and rejected
approvals are reset together with their receipts and a new retry revision.
Overrides and verdicts newer than the work stand. Approval history and previous
check evidence remain in the ledger. The managed dispatcher receives the
committed stage plan, and restart recovery finds the same pending checks if the
process stops before dispatch.

Landing rechecks requested from a console or a remediation worker use one
transaction too. Only failed or errored automatic steps in the active landing
stage are reset; entry steps skipped because of those failures are also queued
again. Passed checks, intentional skips, approvals, overrides and earlier stages
remain unchanged. Rechecks require running steps to finish first. Previous
evidence is retained in receipts, and timestamps are cleared with the retry revision, before
the managed dispatcher resumes the pinned stage plan. A failed entry check still
blocks later entry work on both immediate dispatch and restart recovery.
Remediation's transient retry count commits with the batch; a failed, duplicate
or stale request cannot consume its budget without queuing checks.

### 2.5 Step types

Common fields: `name`, `uses`, `with`. Two families: kinds the engine judges
from state, and kinds it runs in the background. Each kind takes exactly the
`with` options listed here (`schema.StepOptions`); a save refuses any other key
by name, in any casing.

| `uses` | What it does | Key `with` fields |
|---|---|---|
| `agent/pi` | an agent works in the workspace with a generated home and the project's compiled guidance | `prompt`, `skills` (`skill/*` blocks), `output` `{kind, name, file}` — the named file *is* the output, `model` (a slot name or provider/model), `thinking`, `executor`, `image`, `services` (sidecar containers on the step's network), `docker` (`true` gives the agent a private Docker daemon), `mayChangeNothing` (`true`: an empty result is a complete answer, see below), `scm` (a project-scoped token: `read` or `write`), `resources`, `requires` (artifact kinds that must exist first), `needs` (`true` starts the app's declared needs for the step), `timeoutMinutes` |
| `agent/review` | an independent agent checks the implementer's claim against the real diff (see below) | `of` (the report artifact kind), `model`, `prompt` (a lens), `question` (replaces the shared first question, "does the diff fully satisfy the request?", for a step not asked to satisfy it, such as a reproduction), `thinking`, `timeoutMinutes` |
| `approval` | people decide; one rejection stops it; every vote is kept | `approvers` (people or `group:` names), `quorum` (how many approvals settle it, default 1), `means` (the required standard of approval), `instructions`, `inAdvance` (what a yes given before the work means), `standing` (explicit bounded routine-update consent meaning; see [Janitor standing consent](../subsystems/janitor.md)) |
| `artifact/require` | a required artifact exists on the run | `kind`, `name` |
| `adr/require` | an accepted decision is linked to the change | `status` (default `accepted`), `tags` |
| `preview` | cold-boot the app from the workspace contract; the boot gate | `services` |
| `tests` | run the committed tree's own tests through the declared toolchain | `command`, `require`, `timeoutMinutes` |
| `scan` | a **scan point**: run every attached scanner for a target, each as its own check | `on: source \| image`, or `scanner` for one scanner; `failOn` (bounded by policy), `timeoutMinutes` |
| `guidance/conformance` | run the project's guidance checks against the tree, classified against the change's base | `failOn` (default `high`) |
| `reconcile` | bring `main` into the change branch before the other gates | — |
| `deploy` | deploy the landed contract to a deployment | `deployment` (default `main`), `services` |
| `scm/mr` | open a merge request on the attached mirror for people who review there | — |
| `scm/merged` | the landing merge into `main` (engine-judged) | — |
| `workflow/call` | run another library workflow as a child run in the same workspace | `workflow`, `with` (its inputs) |
| `script` | run a command; exit 0 passes | `run`, `env`, `executor`, `image`, `timeoutMinutes` |

Results: `pass`, `fail`, `needs_human`, `error`, `skipped`, always with
evidence. There are no `policy`, `scm/branch`, `scm/mr-check` or
`deploy/health` step types: guidance checks and `agent/review` judge prose
rules, and health is *declared* in the contract and rendered per environment
(`design/subsystems/healthchecks.md`). `workflow` is not a sub-invocation
trigger.

`artifact/require` and `adr/require` are judged from state each time the
stage's evidence changes. While another working step of the same stage is
still pending or running, a missing fact leaves them pending rather than
failed, because that step (a `deploy` attaching its release) may supply it.
They fail only once the stage's work has finished; approvals do not hold them.

## 3. Policies

Rules come in two kinds:

- **Prose rules with teeth are guidance blocks.** A block's front matter
  carries `checks` — shell or model-judged, with a severity — and the same
  block steers the agent that has to satisfy them
  (`design/subsystems/guidance-checks.md`, `state-model.md` §6).
- **Policies are rules about workflows**, checked when a workflow is saved
  and again at merge. The stock library:

  ```yaml
  policies:
    - name: deploy-requires-scan
      rule: requireStepBefore
      with: { uses: deploy, requires: scan }
    - name: scanner-bound
      rule: requireCapabilityBound
      with: { capability: scanner }
    - name: scan-failon-ceiling
      rule: scanFailOnCeiling
      with: { max: high }
  ```

  Packs reference policies by name; a project adopts them through its pack
  or directly; a policy's text is written so a person who is not a
  developer can be told the rule (`design/decisions/policy-v2.md`).

## 4. Worked example: the stock quick-change lane

The friction-floor working lane, as shipped (prompt abbreviated):

```yaml
apiVersion: apptown.dev/v1
kind: Workflow
metadata: { name: quick-change, tier: core, category: change }
spec:
  kind: lifecycle
  role: working
  workspace: project
  defaults: { thinking: low }
  stages:
    - name: apply
      onEnter:
        - name: apply-change
          uses: agent/pi
          with:
            skills: [skill/implementation]
            docker: true
            output: { kind: change-report, name: change-report, file: .apptown/CHANGE.md }
            timeoutMinutes: 10
            prompt: |
              Apply the SMALL change below: the minimal edit that satisfies it.
              If a reviewer has previously rejected this change, their
              feedback is: ${{feedback.latest}}
              WRITE a short change report to .apptown/CHANGE.md.
              --- CHANGE ---
              ${{change.body}}
        - name: boot
          uses: preview
      steps:
        - name: report-exists
          uses: artifact/require
          with: { kind: change-report }
        - name: review-change
          uses: agent/review
          with: { model: review, of: change-report }
        - name: accept
          uses: approval
          with:
            approvers: ["role:project-approver"]
            means: "This change is small and low-stakes enough that a glance is the right amount of scrutiny — and I glanced."
```

`role:project-approver` names whoever holds the approver role on this
project, so one stock workflow serves every project and each project says
who approves on its Members tab.

Accepting the change hands it to the system landing workflow, which is a
pipeline: `reconcile`, `tests`, `scan` on source, `guidance/conformance` and
`scan` on the image, then the landing approval in a stage of its own, then
the merge, `deploy` to main and the release record. The acceptance shows the
landing approval's sentence and counts for it where the accepter could
approve that step themselves and the checks raised nothing new; otherwise
the landing asks again. What enforcement buys: the agent's work
cannot be accepted without a report that an independent reviewer checked
against the diff and the app booting cold; nothing reaches `main` without
the tests, scans and the project's own rules passing on the exact tree
that merges; and every one of those facts has evidence attached.

## 5. Non-goals

- No expression language in YAML; logic belongs in scripts and agents.
- No parallel stages; steps within a stage run as dispatched.
- No cross-project workflows; `workflow/call` is project-local, with a
  depth cap and save-time cycle detection.
- No in-repository workflow files.

## agent/review — the cop model

Self-reports are unreliable: an implementer that silently delivers part of
what was asked passes report-based review (observed live: a "bump the Go
directive" run that bumped only dependencies, approved on the strength of
its own report). `agent/review` is adversarial verification as a step:

- an independent agent — fresh context, its own model slot — receives the
  request, the authoritative diff (branch vs base) and the implementer's
  report as a claim to check, explicitly told not to trust it;
- it inspects the workspace when the diff is not conclusive and writes a
  structured verdict (`.apptown/review.json`: verdict, summary, findings) that
  gates like any check;
- it judges the work and never changes it: nothing a reviewer leaves in the
  workspace (a test cache, a scratch edit) is committed or published, and
  the folder goes back to the commit it was given;
- a fail feeds `${{feedback.latest}}` exactly like a human rejection, so
  re-running the producing step delivers the findings to the implementer;
- a review `of` a step's report makes that step an implementing step: its
  report is a claim about a change, so if the change is still empty after
  it (nothing committed against the change's base), the step fails with
  "made no change" and its report is kept for the reviewer and a person.
  A step nobody reviews against the diff (research, a plan, a spec) may
  change nothing. On an adoption Change (one the adoption door opened, with
  its recorded target) such a step passes with "nothing to change" and its
  report instead: the adoption may need no code, the run still asks a
  person to accept it, and the landing's conformance gate judges the
  adoption's recorded acceptance checks on the tree, outright, before the
  landing moves the project to the target (checks only a running deployment
  can answer are left to later conformance reviews). Nothing is merged;
- a step that declares `mayChangeNothing: true` says an empty result is a
  complete answer (a maintenance lane with nothing to update). When it
  leaves the change empty it passes with "nothing to change" and its
  report. The stage's other steps run as declared. AppTown closes the
  change instead of asking anyone to accept it, with the step's account as
  the reason, when the run would next wait only for an approval in the same
  stage, every other step of the stage passed or was skipped, an
  independent review `of` the step's report passed, no review of the stage
  left a finding, and
  the change is still empty when it closes. Nothing lands. The change's
  state is abandoned, and it records how it closed (`closedAs:
  nothing-to-change`, which only this close sets), so the change page,
  lists and CLI say "Closed: nothing to change", never "Abandoned". A
  finding (a
  deferred major), a failed scan, a change made by a later step, or an
  approval in a later stage leaves the run waiting for a person;
- people remain the judgment layer: the verdict sits beside the approval
  as evidence and never merges or ships anything itself.

Several review steps with different lenses are just more checks. Every
stock working lane carries one reviewer between the producing step and the
human approval.
