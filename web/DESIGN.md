# Web design

Use the shared patterns before adding page-specific CSS. Preserve every field,
link and action, and keep loading, empty, failed, stale and unknown distinct.
The [web data contract](../design/architecture/web-data.md) owns reads and editor guards.

## Now and history

Every page leads with now — the newest evidence for each thing, with its age and
its source — and keeps history as a dated list underneath; a past snapshot never
stands in for now. Unknown and old are never shown as current.

Existing examples are deployment status + deploy history, backups, conformance
current + reviews, and change state + timeline. Conformance shows the newest
verdict per adopted check, with its commit, date and source. Older commits are
labelled as such. Reviews open dated snapshot reports.

## Component map

| Need | Use |
| --- | --- |
| Records compared on the same facts | `ListTable`: column headers, one desktop row, two groups of cells on phones; free text wraps within each phone cell. Status keeps its dot and word, technical values use monospace, numbers align right, relative time comes before the final, unlabelled action column. `rowTo` opens the record. |
| Items read and acted on one at a time: identity, state, action, optional detail | `ListRow`; `ProjectListRow` for projects and `LibraryRow` for ways of working. `to` opens the record. |
| A list's filter | `SectionFilter` in its `Section`'s action: on the heading line, narrowing as the person types; on phones it takes its own line |
| Disk name, used / total and capacity bar | `CapacityStrip` |
| Completed work out of a known total, or unknown progress | `ProgressBar` |
| Page section | `Section`; a page's beta mark is `BetaChip` after the title's help button |
| Action menu or popover | `MoreMenu`, `SplitButton`, `FormMenu` for menus with form controls |
| Dialog | `Modal`, with its closing buttons in `actions` (on phones they stay at the screen's foot); `Modal fit` for a dialog sized to its text. Change decisions use the native dialog in `pages/change/Turn.tsx`, opened on phones from a Decide bar fixed to the screen's foot |
| Labelled field in a form grid | `FieldRow` with native input, select or textarea |
| State and its explanation | `Status` and `StatusDot` in `Status.tsx`; domain status owners supply meaning and freshness |
| Wrapping horizontal group or vertical stack | `.row` or `.stack`, with `.row-1`–`.row-6` or `.stack-1`–`.stack-6` for gaps |

Choose a list by its content: if column headers would help, it is a table
(`ListTable`); if each item reads as a sentence with its own act, it is a row
card (`ListRow`). Label-and-value facts about one thing are not a list.
A diff, a change's story timeline, forms laid out as rows, pickers and banners
stay their own components; a list that needs behaviour neither list component
has stays hand-built, with the reason beside it, rather than gaining a prop
only it uses. Page facts are inline; long records have their own page and a
link. Only menus and popovers disclose content. Use native links and buttons
for actions.

## Lists and statuses

- **Open by clicking.** The way into a list item is to click its row
  (`rowTo`, `to`), never a View or Open button. The row's name stays a real
  link for the keyboard and for opening in a new tab; links and buttons inside
  the row keep their own act. A button on a row is for an act (Fix it,
  Review, Restart), not for going to the item.
- **Status is a word, its details behind it.** A status shows its dot and word
  only; why, and when it was checked, open from the word on hover (after the
  mouse rests a second) or tap, in the same panel either way. On a phone a
  tap opens them as a dialog sized to its text (`Modal fit`), as the page's
  "?" help does, so they never lie over the buttons beside the status. Never write a
  checked time or reason under or beside a status word. Dates that are facts of
  the record (when a release went out, when a backup was taken) have their own
  column or place in the row's sentence.
- **Unknown is not empty.** Unknown, old and failed readings stay visibly
  different from none, stopped or passed; a refresh that fails marks the shown
  answer as an old reading.
- **One filter per list,** the `SectionFilter` on the list's heading line.

## Links, buttons and code

- **A link in a sentence is underlined.** The link ink is close to the body
  ink, so words that open something carry an underline. A link that is
  itself the item (a row's name, a table cell, a tab, navigation) is not.
- **Disabled is drawn by colour, never by fading** (`--disabled-*` tokens),
  so its words stay readable in both themes. Where the missing input is not
  the empty field right above, a muted line beside the button names it.
- **A quiet button keeps an edge on phones,** where there is no hover to
  reveal it; a quiet destructive act is red.
- **Code is shown whole on its own page, never in a summary.** Guides,
  workflow and policy source (`pre.source`) keep each line unwrapped and
  scroll sideways in their own box, with the scroll cue. A list row or
  summary says what happened in words and links to the record. Short code
  tokens in a sentence do not break. Diffs keep their phone wrapping.

## Tokens and CSS

`src/styles/tokens.css` owns colours, typography, spacing, radii and depth.
`src/theme.ts` and `src/size.ts` select the viewer's theme and text size.
Small is the 14px reference; rem tokens scale with the viewer's choice.
Use `--space-1` through `--space-6` for 4, 8, 12, 16, 24 and 32 reference pixels.
Use named depth roles (`--z-base`, `--z-sticky`, `--z-topbar`, `--z-popover`,
`--z-modal`, `--z-toast`) and their contextual variants rather than numbers.
Responsive widths are 600px and 800px, mirrored in `src/chrome.ts`;
use `(width > 800px)` for the desktop complement.

`src/layers.css` declares tokens, base, components, pages and utilities.
Shared CSS lives by owner in `src/styles/`; page CSS uses the pages layer.
Keep phone rules beside their desktop rules. Every source stylesheet wraps
its rules in one declared layer and stays within 400 lines. Xterm's external
CSS stays unlayered. Important declarations reverse layer priority.

`make check-web-css` runs Stylelint over every source stylesheet. IDs are
anchors, not selectors. Specificity stays at or below `0,5,3`. Colours, radius,
font size and z-index use custom properties outside `tokens.css`. Existing
`!important` and pixel-spacing debt is counted per file in `css-baseline.json`;
remove debt and lower its entry together. New files have no allowance.
Exceptions are zero, auto, inherit, currentColor, transparent, hairlines and
border widths, circle radii (50% or 999px), and safe-area or viewport arithmetic.
Do not copy pixel spacing, add importance or raise a baseline to pass.

## Checking appearance

Install the lockfile's browser with `cd web && npx playwright install chromium`,
then run `make check-web-layout`. Its fixtures bundle the production entry's
CSS order and check text sizes, phone fields, rows, plot bounds and popovers.
The page-disclosure gate allows zero fold-downs outside the menu components.
Rows of the same component use the same text sizes; free text wraps at phone
width. The top bar derives its sole breadcrumb trail from registered routes,
linking every ancestor page and ending at the parent. Pages supply only dynamic
segment names. Dependent project lists live on per-item projects pages; their
parents show count links.
The whole-page camera is `internal/apptown/web_ui_browser_test.go` with
`testdata/web-ui.mjs`; inspect its screenshots at phone and desktop widths in
both themes as well as running assertions. Fixture checks do not replace
looking at real pages.
