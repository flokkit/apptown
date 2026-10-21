# Chats and agent runtime

Conversation prepares Forge-specific session scope and briefing. Console owns
session persistence and process lifetime. Changeconsole owns what becomes of
a change's chat's work: its saves, its end, the boot rehearsal of its work and
the notices AppTown sends it. Agentenv prepares tools, registries and
private Docker for both chats and workflow steps. The [runtime guide](../../stock/docs/guides/runtime-environments.md)
lists tools, mounts, services and credential grants.

## Opening and continuing a chat

Conversation resolves project/change identity and authority, selects or prepares
the workspace, then opens the session through `console.Service`. Project chats
receive a read-only repository snapshot and writable scratch; change chats receive
a writable change worktree.

A continuation copies the producing agent's session into a new chat identity.
It gets its own credential and can continue after the originating run finishes.
Reopening refreshes launch configuration. An unpinned chat follows the project's
chat model slot; an explicit model pin remains selected. Missing provider bindings
can fall back to the instance model.

The harness is Pi. Model selection does not select a harness; other harnesses
(Codex) are not supported.
The Pi adapters render Pi configuration and manage its container, PTY and tmux.
Workflowexec owns step execution and settlement through Workflow.

## Looking and acting

A chat is not private to the person who opened it.
Anyone who can read its project may look at it: its conversation, its
terminal's screen and who opened it. Acting on it needs `change:invoke-lane`
on its project, the authority that opens a chat: saying something, pasting a
picture, stopping its turn, typing into its terminal, ending it (which saves
its work). A chat wakes only through one of those acts (a message, or a
terminal opened to type), so its waking is recorded under whoever woke it.
A chat's own credential reaches its own chat and no other, for reading as
for acting; no other instrument acts on a chat.
`conversation.Chats` holds this rule at each operation, whichever door asks,
and keeps an authorization receipt of each decision; the routes check only
that the caller can read the project (`project:read`, however it is held:
a project role, an owning group's or an instance grant such as Auditor). A
terminal opened to type asks again before each keystroke or resize, so
someone made a viewer while their page stays open only watches. A viewer is refused in the chat's own
words, for example "Viewing only: saying something to this chat needs the
Project contributor role."; a disabled account or a credential's narrower
consent keeps its own reason.

Looking changes nothing, for anyone: reading the messages never wakes a
stopped chat. A viewer's terminal is a watcher
(`console.Service.Watch`): it does not wake a stopped chat (watching one
that is not running is refused), takes no keystrokes or resizes, and is not
counted as a viewer for the idle clock. The chat page shows a viewer the
conversation without its message box, stop, end or start-fresh controls, and
says whose chat it is. Reading the messages still marks the chat used, so a
viewer's open page delays its idle stop; it never starts it.

The chat keeps running with its opener's authority: its agent's acts are the
opener's. Each act on the chat is recorded under the person who did it, with
the opener beside them (`console.say`, `console.upload`, `console.interrupt`,
`console.terminal`, `console.end`); an end also records what became of the
chat's work, for example "sam ended alex's chat; its work was saved".

An ended chat's words stay readable from its record on disk
(`console.Service.Transcript`, which says whether each chat has ended). A
change's chats, live and ended, are on the change page
(`GET /changes/{id}/transcript`); the ended chats opened on the project itself
are under "Past chats" on the project page
(`GET /projects/{slug}/transcript`). Both need evidence read on the project,
name who opened each chat, and mark a chat moved from another AppTown
(`conversation.ImportedLabel`) as history only. A chat with no messages is
listed in neither. A chat folder whose record or words cannot be read is not
left out silently and does not stop the others: both reads return the chats
they could read and say how many records on AppTown could not be read
(`unreadable`; administrators also see the folders), because any of them may
belong here. A project's export refuses instead, naming them. Pictures pasted into a chat stay visible with its words:
`GET /changes/{id}/transcript/{chat}/pictures/{name}` and
`GET /projects/{slug}/transcript/{chat}/pictures/{name}`, with the same
evidence read, name a picture by its chat and the name the chat's words give
it (`console.Service.RecordedPicture`), only from a chat of that change or
project, and list nothing. A deleted project's cleanup removes its ended
chats' folders, words and pictures included (`console.Service.RemoveEnded`). A project's export reads its ended chats the same way,
without registering or waking them, and refuses while one has not ended.

Chats are shared, not yet multiplayer. The conversation does not say which
person typed each message (the ledger does), and the agent is not told who is
speaking. Explicitly multiplayer chats would need: the speaker carried into
each message the agent sees and the page shows, a decision on whose
authority the agent then acts with (the opener's, or the speaker's for that
turn), and presence (who else has the chat open). Nothing here blocks that:
the rule already admits several people acting on one chat.

## Runtime preparation

AppTown CLI is a packaged Linux executable: `apptown-linux` beside `apptownd`, or
the path configured by `-console-cli` / `APPTOWN_CONSOLE_CLI`. Build it with
`make console-cli`. AppTown checks availability once per daemon process. A missing
or non-executable artifact leaves sessions without the CLI; restart after fixing
it. No session-time Go build occurs.

Writable change chats can receive private Docker. Project chats do not. Eligible
steps request Docker through the implementation skill. Chats do not automatically
receive application databases or production secrets. Some toolchain and Docker
preparation failures have degraded paths, so successful agent startup does not
establish that every requested tool is available.

The console API allows a restricted set of mutations under scoped credentials.
Installed tools do not grant approval or deployment authority. Model access uses
a live session token and an allowed worker network through the inference gateway;
provider keys remain in AppTown. See [inference access](inference-security.md).
Privileged DinD does not isolate hostile tenants from the host.

## What AppTown tells a chat

AppTown sends a change's chat a message, waking it, when something happens
that the chat would otherwise act wrongly without: its save was refused
(`[apptown save]`), its saved commit failed to boot (`[apptown check]`), or the
change's landing ended without landing (`[apptown landing]`), for example
because main moved under it. The landing notice is sent once per ended landing
by the run's abandonment follow-up job (`workcommands.FollowUps.Abandon`), only
while the change is in progress. It carries the run's `Ending`: the reason the
landing was abandoned and the failing step's own reason, which for a branch
behind main is the catch-up to do. A chat that cannot be told fails that job,
where the failure is kept. `apptown wait -c` reads the same `Ending` when no run
is going. The briefing does not state the change's state: it is written once
at opening and would go stale, so the chat queries AppTown instead.

## Retention and recovery

The Pi session record supplies durable conversation history. Terminal output is
a view of the running process. Stop and end have different retention rules;
stopping an idle process does not delete the chat. Project deletion blocks new
session admission before teardown, and is refused while one of the project's
chats is being opened, is mid-conversation, or is ending (its last save needs
the change the deletion removes).

An AppTown restart preserves console containers and files. Resume refreshes the
environment and recreates missing runtime resources. It cannot restore an
arbitrary tool process or nested container that has already disappeared. Session
records, scratch, generated homes and change worktrees have separate retention
rules, described in the runtime guide.

[Daemon shutdown](daemon-lifecycle.md) cancels console control loops within the
shared deadline without stopping every long-running user job. Live restart and
reconnect checks remain separate from source and unit-test verification.

## Chats on a worker

With a worker configured
([operating guide](../operations/workers.md)), a change's chat runs on the
worker by default. The person can choose to open it on AppTown. Two kinds of
chat always open on AppTown: a project's chat, and a chat that continues a
step's agent. An AppTown started with `-no-local-docker` runs no chats itself:
it refuses those two kinds, and a chat chosen for AppTown, with the reason. The model, its guarantees and its accepted failures are
requirement
[RE11](../requirements/runtime-environments.md#re11-work-on-a-separate-machine).

**Where it runs.** The chat's record carries its placement, set when it opens
and never changed. `console.Placed` gives each chat the runtime its record names:
pi on this machine, or `worker.RemoteAgent`, which answers the same interface by
calling the worker. The chat service does not change. Messages, the terminal and
interrupts go through to the worker. The conversation is copied back with the
time of the last good copy, and the chat's page shows it. A worker that cannot
be reached shows the chat as unavailable, not dead.

**Opening.** The change's existing chat is returned wherever it runs. Opening
a new chat on the worker refuses, with the reason, in three cases:
- a bound registry the worker cannot reach (it must answer at all from the
  worker; this does not show that its packages install there);
- a folder whose committed work the checks refuse;
- a folder that has diverged from the published branch.

Otherwise the folder's committed work is published first. The worker is then
sent the change's branch as a Git bundle.

**Saving.** Work reaches the change when it is saved, meaning AppTown has put
it on the branch. After a turn the chat hands back, before a landing or a
recheck, and when the chat ends, AppTown asks the worker for a bundle of the
chat's commits. It imports the bundle through the checked import and publishes
it under the chat's hold (`changeconsole.Service` calls
`repositoryedit.SaveChat`; one chat's saves run one at a time). A save's result is one of:
- saved;
- refused, told to the chat with the fix, and kept on AppTown;
- not confirmed;
- skipped;
- discarded: the chat's project was deleted, and its change with it, so
  there is nowhere to save to. Only the project's recorded deletion makes a
  save moot; a change missing for any other reason is "not confirmed". The
  ledger records `chat.save-discarded` before the save counts as settled,
  so a deleted project's cleanup can end the chat.

The change's folder follows the saved branch, so the change's views show saved
work.

**Ending.** Ending stops the chat first: its turn, its terminal, what it
started, and its private Docker. Then it saves what the chat left uncommitted
as its leftovers, and removes the chat once that save is settled. A refused
last save is kept, with its reason, for download from the change page, and the
chat ends. What the change page offers for download is decided by an ended
chat's own last save record, "refused and kept", not by the kept ref. A later
save, including "nothing new" after the chat discards the refused work,
settles a turn's refusal.

**What the change's pages read.** A change's pages read its folder, which
follows the saved branch: each save brings the folder along. A chat save,
and a worker task's result, records the commit it saves for the folder
before publishing it (`scm.MarkSaved`); the record is kept beside the
folders (`workspaces/.saved/<folder>`), never in the folder's `.git`. A save
that cannot record it is not published and not confirmed. The folder is
brought along only by a confined Git operation (`gitexec.Folder`), which
first checks the folder's metadata is what AppTown's own clone makes and
refuses any other. The folder is **behind** while the branch has that commit
and the folder does not, for example when a left-behind Git lock stopped the
save bringing it along, or AppTown refused to run Git in the folder (then
it is behind without Git being asked). A folder still behind an earlier save
keeps that record,
so a later save refused does not hide it. If Git cannot answer whether the
folder is behind, the pages report an error rather than pick a history.
While it is behind, the pages read the branch instead:
- its diff, detail and narrative read the branch;
- its files are not shown, and the page says the folder has not caught up.

Anything that brings the folder to the branch ends this without clearing
anything: a later save, a desktop sync, or a merge. Preparing the folder for
a new writer (a chat on AppTown or a run) clears the record, so that
writer's own history, a reset included, is read as its work. Who holds the
change does not decide which history is read. The change page
lists the change's worker chats, live and ended, each with its last save. A save
the worker cannot confirm keeps the chat. The change is free only once the
worker shows nothing of the chat.

**Checks.** `apptown check` (over HTTP and MCP alike) on a change a worker chat
holds saves the chat first and checks what was saved (`verification.Changes.Check`
asks `changeconsole.Saves`); a refused or
unconfirmed save refuses the check with its reason. The CLI names the
commit it asks about, its folder's `HEAD`; AppTown checks it only when it
is in the history of the change's folder or of its published branch, and
otherwise refuses naming it, never answering with another commit. Outside a
Git repository and over MCP, no commit is named and the check reads the
commit the change's views read, the chat saving first. `apptown check
--published` asks for the published tip explicitly (`{"published": true}`):
AppTown checks the tip of the change's branch in the project's repository,
not the folder, saves nothing from a chat to do it, and refuses when the
branch does not exist. Every answer names the commit it is for.

**Previews.** A change a worker chat holds is previewed as saved: the try-it
button, `apptown preview` and MCP previews save the chat first and boot the
saved commit, and a refused or unconfirmed save refuses the preview. After
each save that succeeds, AppTown rehearses the saved commit once, as the
try-it button would, and tells the chat when the app fails to come up
([operating guide](../operations/workers.md#previews-on-the-worker)). When
the worker refuses a new chat, the chat page offers to open it on AppTown
instead.

## Implementation references

- [internal/forge/conversation/admission.go](../../internal/forge/conversation/admission.go)
- [internal/forge/conversation/acting.go](../../internal/forge/conversation/acting.go)
- [internal/forge/conversation/environment.go](../../internal/forge/conversation/environment.go)
- [internal/forge/changeconsole](../../internal/forge/changeconsole/service.go): rehearsal, ending, saves on the worker, worker placement and the change's worker chats
- [internal/console/service.go](../../internal/console/service.go)
- [internal/console/pi/runtime.go](../../internal/console/pi/runtime.go)
- [internal/forge/agentenv/cli.go](../../internal/forge/agentenv/cli.go)
- [internal/forge/workflowexec/environment.go](../../internal/forge/workflowexec/environment.go)
