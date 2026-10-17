# Nested workflow runs

Status: current implementation. Checked on 18 September 2026.

`workflow/call` runs another workflow as a child. The child has its own run and
steps, while retaining the parent Change and source ownership. It does not create
an independent branch or perform an independent landing.

Workflowexec reads and interpolates call inputs in the parent's context. Shared
preparation resolves the callee and its pinned definition. Workflow admits the
child and parent link, dispatches it, and settles the matching parent call when
the child completes. Child approval work remains visible through run state.

A maximum call depth bounds recursive invocation. Parent/child identity checks
prevent a late completion from settling a different attempt. Restart handling
preserves a parent call only when its matching child is still live.

`workflow/spawn` from the earlier design is not an implemented general parallel
workflow mechanism. Independent Changes and remediation have their own admission
rules. See [Workflow](workflow-domain.md) for execution and settlement ownership.

## Implementation references

- [internal/forge/workflowexec/call.go](../../internal/forge/workflowexec/call.go)
- [internal/forge/workflow/execution.go](../../internal/forge/workflow/execution.go)
- [internal/forge/startup_recovery.go](../../internal/forge/startup_recovery.go)
