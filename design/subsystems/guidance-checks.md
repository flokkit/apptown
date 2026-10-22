# Guidance checks

Guidance blocks can carry check declarations alongside prose. Ways of Working
publishes their versions; Adoption records the project's selection. Checks run
against those pinned declarations, not whichever version is newest in the library.

The supported evaluator families are shell, tool/scanner, model and runtime.
Verification prepares sources and effects; Check interprets evaluator results;
Conformance records judgments. Workflow guidance/scanner runners execute under
an admitted step and settle through Workflow.

A base/head comparison separates introduced findings from existing findings where
the evidence supports that distinction. An unavailable base must not make findings
disappear. Exceptions and human confirmation are applied as decisions over the
recorded evidence.

## Feedback and evidence

`apptown check` (and MCP `run_change_checks`) runs a change's declared
deterministic checks, source scans and image scans against one named
commit, as the landing gate would, so an agent can fix a failure before
handing back. The image scan point looks at the image of that commit,
compared with the same base as the landing's `scan-image` step, and fails on
introduced findings at or above `high` (the point's default). Its
answer is feedback, never evidence: nothing of it is recorded, no gate reads
it, and the landing runs the checks again on the commit it lands. Every
answer says so (`feedbackOnly: true`) and names the commit it is for.

A named commit is checked only when AppTown has it as the change's work:
in the change's folder, on its published branch, or on one of the change's
scratch refs. An agent step on a worker commits where AppTown cannot see
until the step's work is collected, so the packaged CLI first pushes the
agent's `HEAD` to its attempt's scratch ref
(`refs/apptown/scratch/change-<change>/step-<step>-a<attempt>`, given to the
step as `APPTOWN_SCRATCH_REF`) with the step's runtime token. That token may
push that one ref and nothing else (see the
[git state](../architecture/git-state.md) endpoint). An answer for a scratch
commit is marked `scratch: true` and says the landing checks again. Scratch
refs are removed when their attempt settles, and swept if left.

## Comparing a change with main

Every finding at a change's gate reads one of three ways against the change's
base (main as the change forked from it, or current main for a landing):

- **introduced** — the change's; it blocks at the step's threshold.
- **preexisting** — main already fails it; shown, never blocks, left for the
  fleet review.
- **undecided** — not known whether the change introduced it; shown with the
  reason, never blocks, and handed to the reviewer to judge from the diff.

How main is judged depends on the evaluator:

- **Shell and tool checks** run on main's tree (cached per base and check set).
  A check that lists occurrences is judged by them: only new occurrences are
  the change's. One that lists none and fails on both trees is undecided.
- **Model rubrics** scoped to the tree are judged on main's tree too, in one
  more checklist call for the rubrics the change failed (none when nothing
  failed). Failing on main as well is preexisting; passing there is introduced.
  A model that cannot judge main's tree leaves the finding undecided. A
  diff-scoped rubric judges the change itself and has no base.
- **Running-app checks** (runtime probes such as readiness or the front page)
  are compared with main's standing deployment only when the deployment owner
  witnesses it serving exactly the change's base commit: its running container
  carries the deployment's settled generation, which recorded that commit. Then
  the same probe runs against main: failing there is preexisting, passing is
  introduced. Otherwise — no deployment, another commit, a container that
  cannot be asked or is replaced during the probe — the finding is undecided
  and says why ("main's deployment runs <commit>, not this change's base
  <commit>").

A probe or a rubric judges one property of the whole app, so failing on main
is enough to call it preexisting; a shell check without occurrences could
have listed what it found and did not, so it stays undecided. A remediation's
own target is always judged absolutely. The web and `apptown check` mark
undecided and preexisting checks apart from failures.

## Evaluator policy

Use maintained static tools for properties they understand. For requirements that
need interpretation, supply intent and context to the agent and require evidence.
A README requirement can mandate information while allowing additional sections;
run its instructions where feasible rather than checking only headings.

Not every stock check follows this yet; do not assume one does. See
[quality](quality.md) for ownership and limitations.

## Implementation references

- [internal/apptown/verification/guidance.go](../../internal/apptown/verification/guidance.go)
- [internal/apptown/verification/changes.go](../../internal/apptown/verification/changes.go) (`apptown check`)
- [internal/apptown/scm/scratch.go](../../internal/apptown/scm/scratch.go) (scratch refs)
- [internal/apptown/verification/evaluator.go](../../internal/apptown/verification/evaluator.go)
- [internal/apptown/verification/provenance_test.go](../../internal/apptown/verification/provenance_test.go)
- [internal/apptown/verification/baseline.go](../../internal/apptown/verification/baseline.go)
- [internal/check/provenance.go](../../internal/check/provenance.go)
- [internal/apptown/wow/check_versions.go](../../internal/apptown/wow/check_versions.go)
