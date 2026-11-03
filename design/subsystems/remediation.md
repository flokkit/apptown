# Remediation jobs

Remediation creates and manages work to address selected conformance findings.
It owns remediation jobs/items and composes Workflow admission with a subsequent
Conformance review. It is separate from River's generic durable-job delivery.

`Start` resolves the requested scope and records items tied to the findings to be
fixed. The worker prepares a workflow and admits the associated work through
Workflow's transaction participants. Dispatch follows commit. Completion checks
the matching item and attempt; a later review supplies verification evidence.

Pause stops new starts while current work finishes. Cancel skips queued work and
leaves in-flight Changes attended. Retry creates a fresh attempt for a stuck item;
skip records an explicit decision. Item commands include both job and item identity
and refuse an item belonging to another job.

Each pass checks project occupancy and execution state before starting more work.
Work counts a project as occupied while a Change is landing or any Step of a Run
on the project is running. A Run waiting on a person, including an approval,
does not occupy the project. A Run waiting for a worker or between Steps does
not occupy the project either. Known gap: a parent's workflow/call Step stays
running while its child waits on an approval, so the parent still occupies the
project. Queue selection, locked attempt admission and queued
item explanations use this same rule. Explanations name the landing Change or
the Change with a running Step. Active remediation items still reserve their
project until they finish.
A pass is a job on the `remediation` queue, one at a time
([background jobs](jobs.md)): every minute on the schedule, and at once when a
person starts, resumes, retries or skips (`Wake`). A pass stopped by shutdown or
its twenty-minute budget stops between operations and is not resumed. The next
pass reconciles every item from its recorded state. The pool cooldown after a
provider refusal is held in memory, so a restart forgets it. It does not
grant approval authority or implement the earlier unattended-acceptance proposal.
Workflow and project approval rules still apply.

See [quality](quality.md) for the evaluation-to-remediation sequence and the
[workflow guide](workflow-domain.md) for run settlement.

## Implementation references

- [internal/apptown/remediation/service.go](../../internal/apptown/remediation/service.go)
- [internal/apptown/remediation/attempt_store.go](../../internal/apptown/remediation/attempt_store.go)
- [internal/apptown/remediation/outcome_store.go](../../internal/apptown/remediation/outcome_store.go)
- [internal/apptown/remediation/verify.go](../../internal/apptown/remediation/verify.go)

### Findings already being fixed

An open remediation Change covers each of its acceptance checks by project,
block, version and check name. Coverage ends when the Change lands or is
abandoned. Pausing or cancelling a remediation job does not end its Change's
coverage.

Conformance keeps covered findings visible with a link saying “being fixed in
change #N”. The Fix count and remediation preview exclude them. Starting a job
queues only uncovered findings, in the existing order. If every selected finding
is covered, the request is refused with the open Change's number.

Work checks coverage under the existing project lock when creating a remediation
Change. Both single-finding actions and queued attempts use that transaction, so
concurrent starts cannot create two open Changes for the same finding.
