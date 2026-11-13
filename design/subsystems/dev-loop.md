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

Stock code-changing workflows run declared tests against committed work before
preview, review and acceptance, using the same `tests` step and applicability
as landing. Failures return to the stage’s agent with full evidence through
the existing bounded rework loop. The recorded result appears in the change’s
checks and acceptance dialog. Landing runs its own tests again under current
law. See [handover checks](workflow-domain.md#handover-checks) for the round
bound and failure handling.

The application layer coordinates admission and preparation; Workflow owns state,
SCM owns repository effects, Conversation owns chat scope and Deployment owns
preview execution. See [Changes](change-lifecycle.md), [console](console.md),
[quality](quality.md) and [deployment](deployment.md) for those contracts.
UI layout and action wording are not part of the domain state machine.

## Destinations and outcomes

The change and its chat show the project's default branch and deployments
currently following main. Deployment owns this selection through
`Deployments.FollowingMain`, using the landing release query. These settings
can change before landing; they do not promise successful deployment.

Overview keeps landing releases inline, with each release's stages, state,
failure, deployment endpoint and release link. An empty release list says
“No release requested”: requests arrive after landing commits. Only a successful
feed read establishes that no deployment follows main. The existing cockpit
poll keeps reading through that interval. Failed refreshes retain old facts
marked old and pause execution progress. Historical stages never establish
current execution when progress is uncertain.

Endpoints come from Deployment's `Reach`. Host-only endpoints explain where
they answer without an Open app action. A known endpoint does not prove that
a particular release reached it.

Workflow's check summary counts and names not-applicable and conditionally
skipped checks once per distinct displayed name, in first-seen order across
the working and landing runs. Passed counts retain each run's evidence.
Conditional skips keep their unknown tone. Only the owner's
verdict counts a check as passed. A running check shows elapsed time from its
step start and the last live log line in both Now and the Checks table; no
line yet is neither failure nor completion. A failed progress read says so.
The current stage's earliest step start supplies “Started”. Slot waits carry
the binding, position and server-formatted words in `WaitingOn.queue`, and
identify the queued run, step and attempt (`Step.attempt` is read-only
identity, not an execution claim) while remaining work without
person actions. Preview absence is shown only after a successful `none` reading
of the same live preview query used by Try it.

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

The inline decision card keeps “AppTown's summary” beside the recorded words and
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

A person rejects an approval through the approval operation. With an agent in
its stage, the existing rework resumes that agent. Without one, rejection joins
the change send-back transaction: the landing stops and its working agent
continues with the note, or the change returns to its person in progress.
The rejection retains its vote, actor and credential; the `change.sendback`
receipt separately names the rejected approval. This grants no independent
change send-back or landing authority. The change's `returnedBy` reading keeps
the receipt's full note until another landing or agent round starts; web,
CLI show/wait and new change-chat briefings share that reading.

## Implementation references

- [internal/apptown/changeactions/service.go](../../internal/apptown/changeactions/service.go)
- [internal/apptown/conversation/continuation.go](../../internal/apptown/conversation/continuation.go)
- [internal/apptown/workcommands/README.md](../../internal/apptown/workcommands/README.md)
- [internal/apptown/preview/command.go](../../internal/apptown/preview/command.go)
