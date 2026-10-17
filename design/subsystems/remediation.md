# Remediation jobs

Status: current implementation. Checked on 18 September 2026.

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

- [internal/forge/remediation/service.go](../../internal/forge/remediation/service.go)
- [internal/forge/remediation/attempt_store.go](../../internal/forge/remediation/attempt_store.go)
- [internal/forge/remediation/outcome_store.go](../../internal/forge/remediation/outcome_store.go)
- [internal/forge/remediation/verify.go](../../internal/forge/remediation/verify.go)
