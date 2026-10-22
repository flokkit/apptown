# Checks, conformance and remediation

## From a declaration to a decision

Ways of working (`wow`) publishes versioned guidance and workflows. Adoption
owns a project's selected versions and receipts. Publishing a new version does
not silently change an existing project's selection. Conformance consumes those
pinned declarations, including historical versions when comparing evidence.

`internal/check` owns evaluator declarations, judgments, findings and provenance
classification. It knows nothing of AppTown's tables. Shell, tool, model and runtime
checks provide different evidence; error or inability to assess is not success.
An evaluator result alone is not an approval or an exception.

`internal/apptown/verification` supplies execution and comparison policy. Its
Evaluator invokes process/model/observation effects. Scanners prepare and run
tools; Runtime observes deployment facts; Changes checks a selected change and
its baseline. Fleet evaluates committed project source. Guidance and ScanSteps
claim and settle admitted Workflow executions. None of these need App or own a
second conformance database.

Conformance owns review records, verdicts, confirmations, exceptions and their
read projections. `Service.BeginReview` checks authority and returns an admitted
`Execution`; its selected projects, source facts and checks cannot be rebound by
a caller. Evaluation takes place outside SQL locks. Recording and finishing
check the review's current lifecycle. Reading a row is not permission to record
a verdict for it. A fleet review records findings; it does not itself open a
Change or rewrite a landing gate.

A gate compares the head and, where appropriate, base evidence with its policy,
threshold and applicable decisions. Introduced, pre-existing and undecided
findings must remain distinguishable. A current standing is a projection over
recorded evidence, not a promise the repository has not changed since evaluation.

## Turning findings into work

Remediation composes Conformance and Workflow. `Start` resolves the requested
scope and creates items tied to the findings being addressed. A worker resolves
and prepares a workflow, admits work through Workflow's transaction participants,
and dispatches after commit. Outcome recording checks the matching item/attempt;
a subsequent review supplies evidence that the problem was fixed.

Pause prevents new items starting; current runs finish. Cancel skips queued work
and lets in-flight work finish, leaving its Change attended. Retry and skip name
both the remediation job and item; an item in another job is refused. A stuck
item carries a reason rather than authorizing an infinite retry. The remediation
worker has its own lifecycle; it is not interchangeable with River's durable
job delivery.

Remediation does not create a separate approval bypass: there are no
unattended-approval levels. Workflow and [authorization](auth.md) decide
approval and landing.

## Evidence and open design

Prefer maintained static tools for properties they understand. For interpretive
requirements, explain the intent to the agent and demand relevant evidence.
README checks should require needed information without forbidding extra
sections; commands can be exercised where feasible. Not every stock check
follows this yet.

For individual operations, see [conformance](conformance.md),
[findings and decisions](conformance-surface.md), [guidance checks](guidance-checks.md)
and [remediation](remediation.md).

## Implementation references

- [internal/apptown/conformance/review.go](../../internal/apptown/conformance/review.go)
- [internal/apptown/verification/evaluator.go](../../internal/apptown/verification/evaluator.go)
- [internal/apptown/remediation/service.go](../../internal/apptown/remediation/service.go)
- [internal/check/evaluate.go](../../internal/check/evaluate.go)
