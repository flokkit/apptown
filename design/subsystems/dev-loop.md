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

## Briefs, advice and summaries

One shared `briefs.Service` builds approval briefs and failure advice. Workflow
supplies scoped evidence and persists the resulting artifacts; HTTP parses IDs,
maps faults and shapes responses. Approval briefs retain their freshness check
against later completed checks. Each in-process flight includes the cache read,
model work and completed artifact write.

For `WaitingOn.kind = check`, `GET /api/v1/steps/{id}/advice` returns `text` and
`recommend`. Failure advice uses `main-strong`; no model or a failed model call
leaves it empty. Its first output line must name an exact offered act: send-back,
rework, override or retry. Invalid recommendations are empty. The run stores the
prose as `failure-advice`, named `step-<id>-attempt-<n>`. Every read checks the
recommendation against current offered acts for that check. Missing models,
failed calls or failed persistence may allow a later build.

The decision screen leads with “AppTown's summary”, then the recorded words, and
highlights the recommended act. The change head keeps the shared factual `says`
beside the recommendation. Without advice, existing wording and highlighting
remain. Advice never changes the check result or decides for the person.

Workflow requests the existing narrative job when its classifier observes a
held check after evidence refresh or advancement. Requests use the existing
narrative job deduplication; pages do not request summaries. The summary
uses `main-strong` and keeps the existing publication checks protecting a person's
title and a newer summary. No model leaves the summary unchanged; errors retain
the existing failure behavior. “What was made” shows the summary. Without one,
“The agent says” shows the agent's report with self-reported “Verified with …”
lines removed. Identifier underscores are preserved.

## Implementation references

- [internal/apptown/changeactions/service.go](../../internal/apptown/changeactions/service.go)
- [internal/apptown/conversation/continuation.go](../../internal/apptown/conversation/continuation.go)
- [internal/apptown/workcommands/README.md](../../internal/apptown/workcommands/README.md)
- [internal/apptown/preview/command.go](../../internal/apptown/preview/command.go)
