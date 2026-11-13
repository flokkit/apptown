# Chats and agent runtime

Conversation prepares AppTown-specific session scope and briefing. Console owns
session persistence and process lifetime. Changeconsole owns what becomes of
a change's chat's work: its saves, its end, the boot rehearsal of its work and
the notices AppTown sends it. Agentenv prepares tools, registries and
private Docker for both chats and workflow steps. The [runtime guide](../../stock/docs/guides/runtime-environments.md)
lists tools, mounts, services and credential grants.

## Opening and continuing a chat

Change on Home and the Projects list creates a chat change and opens its
conversation when chat is available. Its menu offers Catalog change and New
change without a chat. Without chat, Change opens that form and the menu
still offers Catalog change. The project overview shares the same Change
control, with its descriptive sub-label.

Project navigation offers Chat only when the viewer can create and invoke a
change and conversation admission reports a configured model and usable placement.
The availability probe has a deadline so an unresponsive worker does not hold up
project pages. Opening a chat repeats admission's full checks.

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

The composer is available from the first paint. Messages sent before the
terminal attaches combine into one pending bubble and are sent through the
same input-readiness path as kickoff once attached. The draft stays in place
while the session opens and when switching to Terminal zoom. The conversation shows “Starting your helper…” while
opening and “Connecting…” while attaching, with elapsed seconds, until attached.

Session startup logs include workspace preparation, launch preparation,
container creation/start and the wait for Pi to accept input. The console owner
logs the whole open; worker logs include checkout preparation and console start.
Helper readiness is measured when delivering a message, including kickoff;
terminal attachment alone does not establish that Pi can accept input.

The chat page fills the space below the topbar at every width without page
scrolling. On narrow screens it follows the visual viewport, including while
the keyboard is open. Only the conversation scrolls as messages arrive, and it follows new activity only while the reader is already at the
bottom. The working indicator and stop button have a reserved row above the
composer, so pauses between tool calls do not change the conversation's height.
Tool folds keep their place and open state when the helper's busy state changes.

On phones, the change's state panel, tab strip and chat stack in the available
viewport. The chat's actions and session menu remain above the conversation;
only the conversation and bounded history rows scroll, keeping the input
reachable while the keyboard is open.

A chat is not private to the person who opened it.
Anyone who can read its project may look at it: its conversation, its
terminal's screen and who opened it. Acting on it needs `change:invoke-lane`
on its project, the authority that opens a chat: saying something, attaching a
file, stopping its turn, typing into its terminal, ending it (which saves
its work). A chat wakes only through one of those acts (a message, or a
terminal opened to type), so its waking is recorded under whoever woke it.
A chat's own credential reaches its own chat and no other, for reading as
for acting; no other instrument acts on a chat.
`conversation.Chats` holds this rule at each operation, whichever door asks,
and records each denial in the activity log; the routes check only
that the caller can read the project (`project:read`, however it is held:
a project role, an owning group's or an instance grant such as Auditor). The
terminal opens read-only for everyone: a key pressed to look around (Escape
to close what seems a menu) would otherwise stop the helper's turn. Someone
who may act presses Take control to type; the server takes their keystrokes
only on a terminal opened in control (`control=1`). Opening the terminal
still wakes the chat and counts as a viewer, in control or not. In control,
the keys are pi's: AppTown reports a stop only when pi records one. One
exception follows from that: pi records nothing when Escape cancels its wait
to retry a failed model call, so the chat keeps reading working until ⏹ stop
or the next message. A terminal asks again before each keystroke or resize,
so someone made a viewer while their page stays open only watches. A viewer is refused in the chat's own
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

Button requests keep the transcript role `you` and start with
`[apptown button: <label>]`. The chat shows the label as an AppTown event,
with the request underneath. The mark records the button press; it confirms
only what the request asks. Check and land confirms checking and landing;
build kickoffs request building; Open the conversation asks for an explanation
and waits. Unmarked messages keep the person's bubble. Button events do not
cancel the helper's preceding answer as a failed boot check does.

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
belong here. Files attached to a chat stay available with its words:
`GET /changes/{id}/transcript/{chat}/pictures/{name}` and
`GET /projects/{slug}/transcript/{chat}/pictures/{name}`, with the same
evidence read, name a file by its chat and the name the chat's words give
it (`console.Service.RecordedAttachment`), only from a chat of that change or
project, and list nothing. A deleted project's cleanup removes its ended
chats' folders, words and pictures included (`console.Service.RemoveEnded`).

Chats are shared, not yet multiplayer. The conversation does not say which
person typed each message (the ledger does), and plain messages do not tell the agent who is
speaking. File notices name who attached the file. Explicitly multiplayer chats would need: the speaker carried into
each message the agent sees and the page shows, a decision on whose
authority the agent then acts with (the opener's, or the speaker's for that
turn), and presence (who else has the chat open). Nothing here blocks that:
the rule already admits several people acting on one chat.

## Files in a chat

Use Attach, drop files on the composer, or paste files and pictures. A message
can include up to 10 files, each non-empty and at most 25 MB. Any file type is
accepted. AppTown removes paths and unsafe characters from names and keeps each
name unique within the chat.

Files are stored in the chat's `uploads` folder beside its conversation, outside
the repository. For worker chats, AppTown transfers the files to the worker before
delivering the message. The agent receives who attached each file, its safe name,
its byte size and its path under `/console/uploads/`. Pictures keep their inline
previews and the agent can read them as images. Other files appear by name and
size with a download link in the conversation and retained history. Downloads
use the untrusted-file handler: only recognised raster pictures display inline.

A file is not committed unless the change includes it. The agent and person
decide whether it belongs in the repository; copying it into the change is an
ordinary edit visible in the diff. AppTown does not scan files. File scanning
is planned.

## Runtime preparation

AppTown CLI is a packaged Linux executable: `apptown-linux` beside `apptownd`, or
the path configured by `-console-cli` / `APPTOWN_CONSOLE_CLI`. Build it with
`make console-cli`. AppTown checks availability once per daemon process. A missing
or non-executable artifact leaves sessions without the CLI; restart after fixing
it. No session-time Go build occurs.

Writable change chats can receive private Docker. Project chats do not. Eligible
steps request it with `docker: true`. Chats do not automatically
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
still behind main (main conflicts, or moved after the landing was asked for)
is the catch-up to do. When a landing merges main into the change itself,
the chat is told once, and told how to take the merge when its folder could
not. A chat that cannot be told fails that job,
where the failure is kept. `apptown wait -c` reads the same `Ending` when no run
is going. A new change chat's briefing includes the workflow owner's latest
`returnedBy` note in full, with who sent it back, while no later landing or
agent round has started. It does not post feedback into an already open chat.
The briefing does not state the change's state: it is written once
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

- [internal/apptown/conversation/admission.go](../../internal/apptown/conversation/admission.go)
- [internal/apptown/conversation/acting.go](../../internal/apptown/conversation/acting.go)
- [internal/apptown/conversation/environment.go](../../internal/apptown/conversation/environment.go)
- [internal/apptown/changeconsole](../../internal/apptown/changeconsole/service.go): rehearsal, ending, saves on the worker, worker placement and the change's worker chats
- [internal/console/service.go](../../internal/console/service.go)
- [internal/console/pi/runtime.go](../../internal/console/pi/runtime.go)
- [internal/apptown/agentenv/cli.go](../../internal/apptown/agentenv/cli.go)
- [internal/apptown/workflowexec/environment.go](../../internal/apptown/workflowexec/environment.go)

## Declared repository context

Guidance can declare repository files with a body line such as
`<!-- context: [{"path":"PROJECT-NOTES.md","maxBytes":6144}] -->`.
The declaration belongs to the guidance version; adoption compiles it alongside
rules and skills. It permits 1–16 unique repository-relative regular files per
declaration, each bounded to 1–65536 bytes. Invalid declarations fail publication
and compilation. No file is included implicitly.

Workflowexec includes declared files from the change's starting commit (the
run's base for repository runs without a change). Conversation includes them in
the initial steering for writable chats from the change's base, and for project
chats from the default branch. The retained briefing identifies the resolved
commit, file and any truncation. Missing files add nothing; unreadable repositories
and non-regular entries fail session preparation. Files describe project claims;
the adopted guidance and workflow take precedence. The declaration itself is
removed from the guidance prose. See the [project notes guide](../../stock/docs/guides/project-notes.md)
for the stock convention using this capability.

## Change conversation page

A change's live chat is its Conversation tab at
`/changes/:id?tab=conversation`. It opens on in-progress changes; landing,
shipped and abandoned changes show their recorded conversation. Without an
explicit tab, open work leads with Conversation unless the viewer has a
decision to make; other states lead with Overview. Project chat remains at
`/projects/:slug/console`. Change chat links using that project address redirect
to Conversation, retaining kickoff and the selected session.

The change's state panel supplies its title, status and destination. The chat
keeps its actions, terminal, session menu, worker save reading and started
changes. Its address key includes the project, change and explicit session,
so navigation never reuses another change's session selection. The first-build
kickoff uses the same once-only guard across refreshes.

Ended sessions appear as rows below the live chat alongside agent work records.
Selecting a session with `chat=<id>` reads the change transcript and its picture
endpoints without mounting the live session opener. The selected conversation
is read-only and links back to the current chat. Incomplete transcript readings
continue to warn about unreadable records.

Above 1100px, a live HTTPS preview that is not host-only offers Preview beside
chat. The browser remembers the choice when storage is available. The pane
shows the build facts, a sandboxed iframe and Open in new tab. Phones keep the
chat alone. HTTP and host-only previews retain Try it's existing behavior.
AppTown permits frames only from its configured preview tier domain (including
its configured port). Preview applications keep their own framing headers;
a refusal to frame remains visible in the browser, with the separate-tab link
available beside it.
