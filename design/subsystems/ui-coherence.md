# Web interface organization

The React application defines navigation and routes in `web/src/App.tsx`.
Dashboard, project, Change, deployment, conformance and console pages consume
HTTP projections and invoke the corresponding application commands.

Dashboard/project lists use visibility-filtered server queries. The interface
distinguishes running, stopped, unknown and unreachable. Status words come from
their owners; a word and light appear together, with reasons in help or details.
A page rendering successfully does not prove every optional request succeeded;
approver hints and policy lists still need the recorded narrow-account usability
smoke.

The interface should show the ordinary action and current result first, with
configuration and execution detail available when needed. This is a presentation
rule, not authority to remove required approval, error or uncertainty information.

## Activity and release summaries

Home's Recently finished shows landed and failed changes. The history response
has no stop-actor field, so stopped changes stay in their project history.
Elapsed time appears only for landed changes.

Project Activity shows changes and releases. A change included in a release
appears once, as a linked change and release record. Cancelled work stays in
history with a neutral state. It includes
a backup only when it failed, stopped or was triggered manually. The Backups page
lists every backup, and Application details retains the latest backup state.
The overview Releases card shows the release list and live marker; release notes
remain in the release record.

## Visual language

The look is "Ledger": warm paper, ink text, ruled lines and serif headings.
Every colour, font, size and radius is a token at the top of
`web/src/styles.css`; components name a role (`--ok`, `--rule`,
`--font-serif`), never a value. Status colours each have one job, and
unknown state has its own dashed, hatched stamp so it never reads
as passed, stopped or empty. Fonts are bundled by the build
(`web/src/fonts.ts`); the app fetches nothing from a font CDN. A dark
variant keeps the same roles and follows the system unless a person pins
light or dark.

## Phones and touch

One interface serves every screen; a narrow one reflows rather than hides.
Under the shell's breakpoint (`NARROW_QUERY` in `web/src/chrome.ts`) the
sidebar is a menu, the type tokens step up so body text is 16px and nothing
read is under 13px, and on any touch screen every control is at least 44px.
Every tabbed page uses `Tabs`: one horizontally scrolling row, a shaded edge
when more tabs lie to the right, and the active tab kept in view on selection
and resize. Links and buttons keep their native keyboard semantics. The phone
top bar shows an Attention icon and count with the full accessible label;
desktop retains the words. Zero known conditions hides the badge, while pending
and failed readings remain visible.

A list table marked `stack` becomes one card per row (its `stack-lead` cell
first, other cells named by `data-label`); a link marked `tap-open` opens its
whole card or row. Other wide tables scroll in their own box with a shaded
edge. Dialogs take the whole screen. On the change page the approval's
comment and decisions are a bar held at the foot of the screen while the
approval, and what approving means, is in view. In the chat the message box
sits at the bottom and rises with the keyboard (`interactive-widget` in
`web/index.html`); what the chat is about takes at most a third of the screen
and steps aside while you type.

## The change page

`web/src/pages/ChangePage.tsx` leads with what the change is
(title, project, when and under which workflow it opened, one line of the
request and summary); its state, with one primary action for the viewer or
the person or thing it waits on (`pages/change/next.ts` derives this from the
page's payload, and a failed refresh shows the answer as not current); and tabs
addressed by `?tab=` (changes, work, conversation, releases, history, about;
`&run=` and `&step=` open a run and a step in Work). Work leads with the current work and keeps
the evidence, merge and release receipts in its Evidence disclosure. History
groups repeated events while retaining every event and timestamp in Details.
A failed machine verdict
in the current stage is shown beside the implementer's claim as "the
disagreement", on the first screen, as `apptown show` prints it.

## Remaining design

The earlier UI pass is a dated implementation record. It did not define the later
plugin contribution-slot vocabulary. That vocabulary remains a prerequisite for
the proposed structured Codex chat surface; see [plugins](plugins.md#ui-extension).

This audit checked routes and page consumers in source. It did not perform a
browser usability review or assert that the current deployment matches local HEAD.

## Implementation references

- [web/src/App.tsx](../../web/src/App.tsx)
- [web/src/pages/Dashboard.tsx](../../web/src/pages/Dashboard.tsx)
- [web/src/pages/Project.tsx](../../web/src/pages/Project.tsx)
- [web/src/pages/Console.tsx](../../web/src/pages/Console.tsx)
