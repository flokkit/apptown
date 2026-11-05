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

## Tree rubric evidence

A model check with `scope: "tree"` declares `files` (repository-relative paths
or globs), `sourceFiles: true`, or both. For example:

```json
{"name":"state","expects":"state lives in declared services","evaluator":"llm","scope":"tree","appliesTo":["container"],"sourceFiles":true,"files":[".apptown/app.yaml","**/pom.xml"],"rubric":"Judge where application state is stored."}
```

`files` and `sourceFiles` are hints for the agent's exploration, not an
excerpt boundary. Each rubric gets one isolated Pi session on the exact commit,
with the source mounted read-only at `/src` and writable scratch at `/work`.
Diff rubrics also receive the complete supplied diff as a read-only file. The
agent records pass, fail or unclear with a citation and a short explanation.
Absence can be cited by the checked path and the directory/tree lookup that
showed it absent. Deterministic checks keep their existing execution rules.

`PROJECT-NOTES.md` and `AGENTS.md` describe project intent and claims.
Application behaviour requires independent code, tests or observed results.
A verdict citing only those documents records evidence-related cannot-assess
in fleet reviews and guidance receipts, without raising Attention. A check
that declares `evidenceUse: "wording"` may judge the documents themselves.
The declaration's `evidenceUse` is included in the agent prompt and evaluator
identity; changing it prevents reuse.

Verification's `CheckJudge.JudgeCheck` claims and settles no workflow or review.
Guidance retains its claimed step and receipts; Fleet retains its admitted
conformance execution. Failing tree checks are separately judged on the base
checkout so existing findings are not attributed to the change.

Each result identifies the committed tree, optional exact diff hash, adopted
block/version/check, rubric and prompt hashes, runtime/tool contract, requested
and served model, duration and gateway-observed tokens. The session transcript
and tool observations are retained under the data directory's `check-sessions`.
These observations make no claim to list every file read: searches and scripts
can read files without emitting a complete path list.

A result can be reused for the same project, tree, transition when applicable,
adopted rubric and evaluator. A changed block version or model reruns it even
when the text is unchanged. An explicit Re-check bypasses reuse. Ordinary fleet reviews reuse unchanged
inputs; review admission retains the Re-check choice across queueing. Failed sessions
and unavailable evaluations are never cached as reusable verdicts; an unclear
model verdict can be reused. Reuse is recorded with the original session's
source identity; the caller's receipt separately names the current commit.

AI checks require the private inference gateway to be configured and reachable
from their task containers, on both local Docker and a worker. They use the
`checks` model slot, falling back to `secondary-fast`. Eval
comparisons retain their recorded judge; new evals pin the baseline's `checks`
model. Each session takes one slot from the shared model-binding capacity pool.
The task's wall-clock allowance is three minutes; its inference allowance is
65,536 provider-reported input/cache/output tokens. The gateway sums usage
across turns and admits no further turn after exhaustion. A turn can cross the
allowance before its final usage arrives; the session then supplies no verdict.
Timeout, token exhaustion, agent failure and missing model access are recorded
as cannot-assess with their reason. No wrap-up or automatic retry follows a cap.

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
base: main as the commit being checked forked from it (`scm.ForkBase`, the
same for `apptown check`, a working run and a landing), so after the change
catches up with main, main's own findings are not the change's. The recorded
base stands in only where Git says there is no fork (no default branch, a
commit the repository does not hold yet, no common history); Git failing to
answer is an error, never another base. A scanner finding is on main only
when main's scan reports its fingerprint. Each finding reads:

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
  separate session per rubric the change failed (none when nothing failed). Failing on main as well is preexisting; passing there is introduced.
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
