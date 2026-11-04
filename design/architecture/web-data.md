# Web data: reads, acts and editors

How the web app reads from and writes to AppTown. `web/src/data.ts` holds
the rules; this page states them.

## Reads

Every page read goes through `useGet(path, { poll, kind })`, on TanStack
Query.

- **Keys follow the resource.** A path is keyed by its segments, then its
  query string: `/projects/shop/settings` is `['get', 'projects', 'shop',
  'settings']`. Refreshing a resource refreshes everything under it.
- **Kinds say how long an answer stays current.** `live` (the default) for
  operational state, asked again on every mount, focus and reconnect.
  `config` for settings and catalogues, current for 30 seconds. `immutable`
  for versioned or content-addressed answers, never asked again while held.
  Polling is separate: a page sets a pace, a failing poll backs off up to a
  minute, and a hidden tab does not poll.
- **Retries.** A one-off read retries a network failure or a 5xx at most
  twice; a 4xx is shown at once. A poll does not retry; its backoff paces it.
- **Cancellation.** A read passes its signal to `fetch`, so a read nobody
  waits for stops at the network.

What a reader gets back is the whole state, not one flag:

| Field | Meaning |
| --- | --- |
| `data` | the last good answer, kept while a refresh runs or fails |
| `loading` | no answer yet, one on its way |
| `err` | why the read failed; for a failed refresh it ends "(showing the last answer read)" |
| `reason` | why the latest read failed, in AppTown's words, without that ending: what a stale note quotes |
| `refreshing` | an answer is shown and a newer one is being read |
| `refreshFailed` | an answer is shown but its latest refresh failed, so it may be old |
| `paused` | the browser is offline and the read waits |
| `current` | this reader received the answer itself, for its current path, and no refresh has failed since |
| `denied` | AppTown refused the read to this caller (403): not available to them, which is neither empty nor broken |
| `answeredAt` | when the answer shown arrived; for a reading the server takes as it answers, when it was taken |

Display rules: loading, empty, failed and denied reads remain distinct. A failed
refresh keeps the last answer under an `unknown` light; its help includes the
failure and last successful read time. `StaleScope` says once that the refresh
failed and the last answer is shown. `ReadGate`, `ReadStatus` and `StaleNote` own
these displays; pages reuse them. First-read failures show `unknown`, with the resource and reason in a
disclosure. Initial reads show `Loading…` without a status light. A
browser-to-AppTown failure never establishes a deployment host's reachability. Offline is stated once at the top of the page.

Owners supply typed recorded outcomes and freshness classifications. Health and
observation chips use the shared [status vocabulary](../../stock/docs/guides/glossary.md#status-words).
Work and evidence retain their own words and colours: waiting for you, in progress,
deploying, landed, passed, excepted, stopped, abandoned and update available.
Only Status assigns chip wording and colour; pages supply reasons and reading
times. Metadata labels (ownership, version, source and finding counts) remain
separate from status chips. Unknown is dashed. Old observations are amber and
show `last seen <age>`; fresh readings show `checked <age>`. Integration chips
carry state while adjacent text carries age once. The server classifies fresh,
old and unknown; the browser formats ages without duplicating expiry thresholds.
Low disk is an amber warning. An exception remains excepted.
The word stays visible. Outside change pages, reasons and check times use a
disclosure that opens with a tap, click or keyboard. Help opens beside its chip, stays within the viewport and does not resize the table. Only one status help is open at a time; tapping outside or pressing Escape closes it. Static chips are not live regions. A completed backup remains finished when its old
stored copy is pruned. A guidance/conformance step with unmet blocking rules displays failed even when
the gate permits existing failures; its recorded gate outcome stays unchanged.
Checks marked non-gating remain advisory and do not change a passing step’s
state. A tests step with no reported test count shows an explicit unknown marker
with the reason, including on a folded card. An omitted gating flag means blocking. Script and scan steps retain
their recorded outcome.

Change pages show help without disclosures. The decision screen shows the app-contract
comparison from the approval question whose digest the vote submits, including
known changes alongside unread declarations or starting commits. Overview's
Next steps and the Decide… screen show at most three equal decision buttons:
one highlighted forward act, Send back… when send-back or rework is offered,
and Abandon change last and red when offered. The acts come from `view.next`
and `detail.next`; the page only words and places them. When several forward
acts are offered, the existing next primary chooses one. Acceptance says
Accept and put live when there is a landing destination, otherwise Accept;
advance says Continue, a failed check's exception says Accept anyway, an
execution error's retry says Try again, and a record check's retry says Re-check.
A reviewer rejection has no reviewer retry button. Failure advice keeps these
choices and moves only their highlight. A recommendation for an act outside
those shown keeps the default highlight. Advice polls until it has a non-empty
answer and reads again when the holding step or attempt changes. Send back… replaces the
buttons in place with What should change?, a focused field, Send to agent and
Cancel. Sending stays disabled until there is text; held decisions prefill the
failure reason. Cancel restores the choices. Sending back an approval records the approver’s rejection and comment on the approval step; held recovery uses the offered send-back or rework operation. Acceptance has no optional note.
Abandon asks inline: “Abandon this change? The branch is kept; the workspace is
removed.” A plain conversation link sits below the choices and closes the
screen. When the Conversation tab cannot resume a stuck agent, that link opens
the existing worker console continuation. Stop this attempt stays only in
non-decision Next steps for a running run.
Details keeps conflict catch-up commands and
the complete state account beside the run history. Details groups the chronological
record into Work cards, one per round, and Landing cards, one per attempt,
followed by About this change. A sent-back round names the person and their note.
Each event has fixed time, label, facts and action columns. Dates appear only
when the day changes. On phones, time sits above the label, facts use the full
width and actions follow. Checks use an aligned title, result, duration and action
table inside their phase. Check actions share one line; produced records link beside the result. Failures retain their reason and any recorded action that would satisfy them. Run-level work shows its changed-file count when available, with session and code links; missing output says “no report”. Session links open a separate work record at `/changes/:id/work/:stepId`, with the run and attempt in the query string. It shows recorded inputs, the retained agent-log summary in order, produced files and records, and change-level worker chats. Unknown facts use — or not recorded; the header omits missing facts. Each section keeps at most one muted context line. Workflow facts omit empty separators. File counts from the committed diff are labelled as whole-change counts. Only Technical detail shows the trace JSON. Agent work rows link Code to the whole change and keep session evidence on the work record page.
Evidence links stay on the row that produced the evidence; plain links open the
record in place. Attach a record ends the Work card. About this change contains
only valued Branch, Started from, Kind, Cost and Attachments rows, with an attach
a file link when allowed. Workflow names and versions appear as technical detail
inside the event facts. Empty rows and sections are omitted. Overview shows the
request's first line and links to its full text in Details. Details shows the
first four request lines once, with a full request link for longer text.
Rejected reviews show a red failure mark and Rejected before the complete recorded
explanation. Structured findings lead when present; sentences are not selected
as inferred rejection reasons. Step names stay in Techno Goggles. Long reports and
reviewer conclusions also link to their full evidence.
Overview counts not-applicable checks and skipped rules within a passing check as fine and omits them; their reasons stay in Details. Conversation action summaries
are plain text; the Show agent actions switch reveals their commands and files.
Security results from before a change to main cannot claim main has passed.

The authorization response exposes `actorId` only for a verified person with a
stable principal UUID. The display user name is not a durable identity. An absent
actor ID supplies no identity for recovering a retained personal draft; capability
checks remain separate and current.

Event timestamps use `fmtStamp` (also exposed as `fmtWhen`) in `web/src/format.ts`: `30 Oct, 17:26` in the reader's own zone. Freshness uses relative ages. Pages do not slice server timestamps for display.

## Editors

A form that saves server state follows these rules. General project settings
(`pages/project/settings/General.tsx`) is the first complete example.

1. It starts only from a `current` answer. An answer another view held may
   be old, and a form seeded from it would send old values back.
2. Until someone edits a field it follows each newer reading, never an older
   one that lands late. Once edited, the draft is the person's and survives
   refreshes and failed saves.
3. It remembers the record's revision when the draft started, and sends only
   the fields that differ from that start, with the revision.
4. A success takes the server's answer as the new starting point and writes
   it into the resource's cache entry.
5. The server refuses a save whose revision has moved on (`409`, code
   `settings-moved`). The form keeps the draft, shows what changed, and lets
   the person keep their edits on top of the new values or discard them.
   Nothing is merged automatically.

On the server, the record carries an integer revision that every writer of
the protected fields advances, checked and advanced in the write's own
transaction (see `internal/apptown/project/settings.go`). A write that names no
revision is refused, so no client can save over changes it never saw.

## Acts

`useAction(onDone, { affects })` runs one act at a time: a second run while
one is under way is ignored, so a double click cannot send an act twice.
After a successful act it refreshes the resource family of every path the
act wrote to (the first segment: `/projects`, `/changes`, `/targets`), which
the API client reports as each write is sent. A few families reach others,
listed once in `data.ts`: a step or a run belongs to a change, and a console
session works on one. `affects` adds paths for an act whose effects reach
further. Only reads on screen ask again; nothing refreshes every read on the
page. `useRefresh(...paths)` does the same for code outside `useAction`.
`failure` is the error itself, for a page that answers one failure specially.

## Identity

The app reads for one identity at a time: the signed-in user, or the actor
named in open mode. When it changes (signing in or out, a session expiring,
another name in open mode), every read except "who am I" is reset: held
answers are dropped, readers on screen stop showing theirs and read again,
reads in flight are cancelled, and an answer that still arrives for the old
identity is discarded. The page area is keyed by identity too, so no draft
or local state carries over to the next person. A read refused with `401`
means the session ended: the app asks who this is again, which shows the
sign-in page.

## Retention

An answer is kept five minutes after its last reader leaves, so a revisited
page shows it at once while it reads again. That is safe because a held
answer is never `current`: no form seeds from it until the page's own read
arrives, and a change of identity drops every held answer. A secret's
value is never read into the cache at all.

## Guarded editors

| Editor | Guard | Conflict code |
| --- | --- | --- |
| Project name and face | `projects.settings_revision` | `settings-moved` |
| Change title, body, external reference | `changes.edit_revision` (human edits only) | `change-edit-moved` |
| Target settings | `targets.settings_revision` (never moved by status checks) | `settings-moved` |
| Guidance list | the project's adoption revision | `settings-moved` |
| Administrator networks | the list the edit started from | `settings-moved` |
| App services (`needs:`) | a hash of `main`'s `.apptown/app.yaml` | `settings-moved` |
| Architecture decision (ADR) title, body, tags | the text each changed field started from (an ADR keeps no versions) | `adr-text-moved` |

## Last-write-wins editor

This editor follows rules 1 and 2 (it starts from a `current` answer and
keeps a draft once edited) but the server records no revision for it, so
a save replaces whatever is there without a conflict check. That is
tolerable only because nothing is lost: a record that keeps no versions
needs a guard instead, as the ADR editor has.

| Editor | Why it is safe enough |
| --- | --- |
| Guidance block body and tags | a body save adds a version, so the text it replaced stays readable as the version before; tags are replaced in place |

## Reading at a glance

Home and Projects share app rows: identity and optional one-line purpose, state
and age when stale, then Open app for running production. Fresh observations omit
the age line; unreachable, unchecked and undeployed rows retain their context. Phones keep the name and state
beside a compact Open app action, with purpose
and age beneath. Home reads at most 50 projects, selecting running production
apps before the limit, and links to All projects for the rest. Creation stays secondary unless no projects
exist. Other operational rows lead with a name, a status light and word, and at most one
fact on a phone. A row opens its secondary facts inline when clicked or activated with Enter
or Space; links and actions keep their own meaning. Commands, identifiers and
detailed evidence belong in that expansion or on the detail page. Disclosures
are plain and reserved for long content needed rarely. Pack actions stay visible
on their rows. Neutral states have no status dot. Conformance findings use amber
counts; red failed labels describe checks that could not run. The attention
header keeps the condition count beside the number of sources not known. Backup history, activity, job groups, change events and departures
start with a bounded selection and offer the next ten records. Successful jobs
with the same operation and subject collapse; failures and open jobs stay apart.
The change page keeps its full evidence in Details, after the tab navigation.

Project Settings › Deployments lists each deployment with its status, display
name, address, backup warning when needed, and serving release or preview change.
Disk warnings and uncertain readiness stay visible in the summary. Rows place
status first, then name and address, summary facts and the action. Each row opens
`/projects/:slug/deployments/:name`, which keeps the full deployment card and its
controls. The page title keeps status beside the deployment name and its address
below. Overview, the deployment list, the detail heading and the instance fleet
use `deploymentStatus.tsx` for overall status: a running generation with missing,
stale or unknown diagnostics, or any part not reporting, is unknown. Stopped
and unreachable runtime observations keep their words. The Backups row states
missing copies, host-only retention and the declared-needs coverage limit once;
provider names and assessed-item detail use Techno Goggles.
Creation, release preferences and old resources stay on the list page.

The Conversation tab and chat view show the person's messages and the agent's
text replies, including its final report. Agent actions fold into quiet summaries
between replies. Each summary opens only its own run. “Show agent actions” opens
all actions in record order; the browser remembers the choice for each viewer.
The switch works when browser storage is unavailable. Full tool results remain
reachable in the chat terminal and the change's recorded agent activity.
Overview has two bordered cards: The change and an action card. The change uses
shared label, value and evidence-link columns, with preview thumbnails first,
followed by the request, report, checks, reviewer when present and app contract
when checked and changed. Without a report, the latest chat reply appears as
“The agent says”, with its first few lines and a full conversation link. Only an
empty report and empty chat show “No report from the agent”. Before any checks
run, the check row says “Not checked yet. Checks run when you press Check and
land.” Unread app-contract comparisons do not create a separate heading then. Once checks start, unfinished checks and an unread app contract share one “Still running” line. Failed checks show their title and the first sentence of the recorded finding, with a Details link; evaluator instructions stay out of the summary. Rejected agent
reviews say “Review: rejected” and show a bounded excerpt of the complete review,
with structured findings first and the redundant “reviewer rejected:” prefix
omitted. Review excerpts do not select a first sentence. Commands appear only under Technical detail.
On phones, each label sits above its value and evidence link. Failures,
exceptions and unknown results remain visible with what would satisfy them.
Grey trailers link to addressable phase entries or Code; browser Back returns
to Overview. The Technical detail switch adds tool names, workflow, run, branch,
commit, model and cost as grey second lines inside facts, without moving columns.
Hidden words leave no blank gap. The choice is remembered per viewer and works
without browser storage.

The context card is “Your decision” when an eligible approval waits for the viewer,
and “Next steps” otherwise. For the viewer's turn, the context card shows what is
ready, failed checks and the acceptance meaning without its own buttons. The
prototype Decision switch beside Technical detail selects a bar or floating
Decide… button, remembered per browser. Both appear on every change tab only
while it is the viewer's turn. The pinned bar aligns with the content column,
leaves the sidebar usable and reserves its measured height below the page.
Phones use one equal-width row: Accept, Chat, Rework, Abandon. Desktop uses full
labels; acceptance says Accept when releases are manual, otherwise Accept and
put live. Bar choices open a confirmation for that choice with the full meaning.
The floating button opens a decision screen with the readiness, checks and
acceptance meaning before four equal buttons: Accept, Go to chat, Rework with
comment, Abandon change, using the full acceptance label. It fills the screen on
phones. Closing returns to the same tab and scroll position. Acceptance is
primary, abandonment red and the other choices secondary. Rework requires a
comment; abandonment and stopping a landing for chat retain their confirmations.
Step recovery operations remain beside their steps in Details. Existing changes
do not offer Run a workflow. Request-started changes with no chat show the request and agent
report as conversation messages.
Approvals appear in the decision card. A placeholder summary (“Project work”)
uses the agent's change report. The change section links its diffstat to Code.
