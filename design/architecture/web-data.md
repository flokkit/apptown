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

Runtime display words come from the deployment owner, including `unreachable`,
the target display name and the first failure time. Expired runtime evidence is
`unknown`. `web/src/Status.tsx` maps server display words to existing light tokens;
the generated union types and completeness test cover each label. Change,
integration-health, check, release, update, copy, job and inventory display labels
come from their owners too. Backup sets, operations, data coverage, step evidence, remediation and what-if results expose owner labels as well. Runs, evals, deployment readiness, restore checks, managed needs, decisions and suppression expiry also use owner labels. Preview discovery alone reports `unknown`. `running` describes runtime; work uses `working` and
a release in progress uses `deploying`. Waiting labels name the awaited party only when known; saved changes and queued work use `waiting`.
The word stays visible. Reasons and check times use a disclosure that opens with
a tap, click or keyboard. Help opens beside its chip, stays within the viewport and does not resize the table. Only one status help is open at a time; tapping outside or pressing Escape closes it. Static chips are not live regions.

The authorization response exposes `actorId` only for a verified person with a
stable principal UUID. The display user name is not a durable identity. An absent
actor ID supplies no identity for recovering a retained personal draft; capability
checks remain separate and current.

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
