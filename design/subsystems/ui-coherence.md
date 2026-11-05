# Web interface organization

The React application defines navigation and routes in `web/src/App.tsx`.
Dashboard, project, Change, deployment, conformance and console pages consume
HTTP projections and invoke the corresponding application commands.

Dashboard/project lists use visibility-filtered server queries. The interface
keeps owner outcomes and their evidence distinct. Status chips use the shared
[status vocabulary](../../stock/docs/guides/glossary.md#status-words) in
`web/src/Status.tsx`; reasons and next actions appear in help or details.
A page rendering successfully does not prove every optional request succeeded;
approver hints and policy lists still need the recorded narrow-account usability
smoke.

The interface should show the ordinary action and current result first, with
configuration and execution detail available when needed. This is a presentation
rule, not authority to remove required approval, error or uncertainty information.

The sidebar starts each page with only its current area expanded. A person can
open or close other areas; those choices last while they stay on that page.
Desktop navigation scrolls within the window when needed, using a thin ruled
scrollbar and reserving its gutter so links stay in place.

The project sidebar starts with Overview, Changes and Deployments, followed by
the Security, Settings and Memory groups. Memory holds Notes and Decisions.
Overview shows the project's work and deployments; the notes agents read are
under Memory › Notes.

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
`web/src/styles/tokens.css`; components name a role (`--ok`, `--rule`,
`--font-serif`), never a value. Status colours each have one job, and
unknown state has its own dashed, hatched stamp so it never reads
as passed, stopped or empty. Fonts are bundled by the build
(`web/src/fonts.ts`); the app fetches nothing from a font CDN. A dark
variant keeps the same roles and follows the system unless a person pins
light or dark.

Text size is per browser, beside the theme controls in the account menu:
Small, Medium (the default), and Large. The root uses 87.5%, 100%, and 112.5%
of the browser's font size. Type and space tokens use rem against the 14px
Small reference; at the browser default, body text is 14px, 16px, or 18px.
`web/src/size.ts` applies the saved choice before rendering. A `?size=` URL
hint applies for that load without changing the saved choice. The terminal
resolves its font through an element and refits when the size changes.
`make check-web-type` rejects pixel font sizes, pixel line heights, numeric
React font sizes, and type or space tokens outside rem. `--fs-field` is the
named exception: `max(1rem, 16px)` keeps phone fields from zooming on focus.
The browser fixture `web/type-scale.browser-test.mjs` checks computed phone
field sizes at all three settings using the installed Playwright module
(`APPTOWN_PLAYWRIGHT_MODULE`). `make check-web-layout`, included in
`make precheck`, runs it alongside the row and disclosure browser checks.
The pixel grid-track count may only decrease from `scripts/web-grid-baseline.json`.

Static page styles belong in the page's stylesheet under `@layer pages`;
repeated spacing and layout use the existing shared classes. Converted spacing
names the rem space tokens, retaining the Small reference lengths. Dynamic
lengths and colours use CSS custom properties where practical. `Spinner` owns
the shared progress ring. `make check-web-inline-style` limits inline style
objects and pixel lengths per production TSX file against
`scripts/web-style-baseline.json`. Both counts may only decrease; a new file
starts at zero and deleted files no longer contribute. Check, precheck and CI
run the ratchet.

## Phones and touch

One interface serves every screen; a narrow one reflows rather than hides.
Under the shell's breakpoint (`NARROW_QUERY` in `web/src/chrome.ts`) the
sidebar is a menu. Text follows the browser size choice; form fields, including
the console composer, stay at least 16px. On any touch screen controls have
a 44px minimum.
Every tabbed page uses `Tabs`: one horizontally scrolling row, a shaded edge
at either edge where more tabs lie off screen, and the active tab kept in view on selection
and resize. Links and buttons keep their native keyboard semantics. The phone
top bar shows the same Attention count as desktop: items to fix and sources
not known are separate. Zero parts are omitted; when both are zero it says
All quiet. Pending and failed readings remain visible. The account control
truncates long names; its menu shows the full identity.

Home, Projects, Deployments, Infrastructure inventories, Integrations, People
accounts and Conformance projects share `ListRow`: identity and purpose, state
and freshness, then the next action. Actions size to their contents; identity
text wraps in the remaining space. Lists supply a container query so cells
stack in the same reading order when their available width is narrow. Technical evidence and maintenance open beneath a row
or on its linked page. Packs and workflows lead with their authored purpose;
versions and maintenance remain secondary. Usage labels its chart in tokens and
its money total as recorded cost, with incomplete pricing stated explicitly.

Shared disclosures use `web/src/Popover.tsx`: an opaque, scrolling panel capped
by the visual viewport. Opening focuses the first control; Escape and outside
click close it and return focus to the trigger. Controls keep ordinary Tab
navigation. Account navigation remains links. Find focuses its input, keeps
focus there while typing, and follows the first result on Enter. Status help
keeps its separate placement code. Phone chat controls wrap with 44px targets.
Dialogs derive their accessible name from the visible title, and `FieldRow`
connects a label to its control by id.

Home and Projects offer Change only where current authority permits
creation. It opens the ordinary change form. Project navigation offers Chat
only with creation and invocation authority and conversation availability; it
creates an ordinary change before opening that change's chat. Unavailable
maintenance destinations are absent from navigation. Saved consent remains
accessible by its address.

Other wide tables scroll in their own box with a shaded edge. Dialogs take the whole screen. On the change page the approval's
comment and decisions are a bar held at the foot of the screen while the
approval, and what approving means, is in view. In the chat the message box
sits at the bottom, including in an empty conversation, and rises with the
keyboard. On narrow screens `useChatViewport` in `web/src/chrome.ts` sizes
and positions the chat shell within the visual viewport; without that API,
CSS uses `100dvh`. The conversation scrolls inside the shell rather than
scrolling the page. The top bar and chat head stay above it. What the chat
is about takes at most a third of the visible screen and steps aside while
you type; the change's About line and agent-actions switch stay visible.

## The change page

`web/src/pages/ChangePage.tsx` leads with the change's title and current state,
with one primary action for the viewer (`pages/change/next.ts` derives this from
the page's payload; a failed refresh shows the answer as not current). The four
tabs are Overview, Code, Conversation and Details. Overview contains the report,
request's first line, checks and acceptance controls. Code contains the diff.
Conversation contains messages and the switch for showing agent actions.

Details tells the change's story as one chronological timeline, including earlier
runs and abandoned landings. Each entry has its time on the right and plain links
that open its evidence inline. Records attach to their producing entry; actions
sit on the entry they affect. A small facts table follows. Full request text and
its guarded editor live on the request entry. Empty sections and workflow labels
are omitted from the timeline; workflow names and versions belong in the facts.
`&run=` and `&step=` address evidence in Details.

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
