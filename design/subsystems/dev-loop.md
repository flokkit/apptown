# Working on a Change

A Change is the shared unit for editing, checking, previewing and landing work.
Its repository branch and worktree are separate from the project's published
branch. Workflow runs and chats can work on that Change under their own authority.

A project chat reads a snapshot and can request new work. A change chat edits the
Change's worktree. Continuing a workflow agent in chat copies its session record
into a chat with its own identity; it does not keep using a completed step token.

Checks and previews evaluate selected source. A successful local process or warm
chat environment does not establish that a cold deployment will boot. Committing
work, running checks, accepting a Change and landing it are separate operations
with separate evidence and permission requirements.

The application layer coordinates admission and preparation; Workflow owns state,
SCM owns repository effects, Conversation owns chat scope and Deployment owns
preview execution. See [Changes](change-lifecycle.md), [console](console.md),
[quality](quality.md) and [deployment](deployment.md) for those contracts.
UI layout and action wording are not part of the domain state machine.

## Implementation references

- [internal/apptown/changeactions/service.go](../../internal/apptown/changeactions/service.go)
- [internal/apptown/conversation/continuation.go](../../internal/apptown/conversation/continuation.go)
- [internal/apptown/workcommands/README.md](../../internal/apptown/workcommands/README.md)
- [internal/apptown/preview/command.go](../../internal/apptown/preview/command.go)
