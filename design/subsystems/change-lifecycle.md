# Changes and runs

Workflow owns Change state and run/step lifecycle. A Change records a unit of
repository work; a run executes a pinned workflow definition. A Change can have
several runs over its lifetime.

The main Change states are proposed, in progress, landing, shipped and abandoned.
A state change must go through the owning operation. Finding an existing record
does not grant permission to complete, retry or approve it.

`CreateAndRun` admits a new Change and run together. Preparation and external
execution follow admission; a later failure must report the committed identity
and its recorded outcome. Starting work on an existing Change has a different
failure contract because the Change already exists.

Landing admits the selected source before SCM performs the merge. Workflow
settles the result after observing the external operation. A database transaction
cannot make an external Git/provider effect atomic with all records.

Retrying a step, sending a Change back and abandoning work use distinct commands.
Approvals remain subject to current authority and project policy. Use the
[workflow guide](workflow-domain.md) for state transitions, attempt ownership,
completion and restart behaviour.

## Implementation references

- [internal/apptown/workflow/change_create_run.go](../../internal/apptown/workflow/change_create_run.go)
- [internal/apptown/workflow/execution.go](../../internal/apptown/workflow/execution.go)
- [internal/apptown/scm/README.md](../../internal/apptown/scm/README.md)
