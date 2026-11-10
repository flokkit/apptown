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
| Records compared on the same facts | `ListTable`: column headers and one row per record on desktop; on phones each row stacks, the name with its action beside it, then the status and each other value on its own line under its column's label. Words stay whole; technical values break anywhere. Status keeps its dot and word, technical values use monospace, numbers align right, relative time comes before the final, unlabelled action column. `rowTo` opens the record. |
| Items read and acted on one at a time: identity, state, action, optional detail | `ListRow`; `ProjectListRow` for projects and `LibraryRow` for ways of working. `to` opens the record. |
| A list's filter | `SectionFilter` in its `Section`'s action: on the heading line, narrowing as the person types; on phones it takes its own line |
| Disk name, used / total and capacity bar | `CapacityStrip` |
| Completed work out of a known total, or unknown progress | `ProgressBar` |
| Page section | `Section`; a page's beta mark is `BetaChip` after the title's help button |
| Action menu or popover | `MoreMenu`, `SplitButton`, `FormMenu` for menus with form controls (given a `title`, a dialog on phones); a menu is as wide as its labels, and an item's hint wraps under its label |
| Dialog | `Modal`, with its closing buttons in `actions` (on phones they stay at the screen's foot); `Modal fit` for a dialog sized to its text (help and a status's details; on phones also Find, which would otherwise lie over the page's heading). Change decisions use the native dialog in `pages/change/Turn.tsx`, opened on phones from a Decide bar fixed to the screen's foot |
| Labelled field in a form grid | `FieldRow` with native input, select or textarea |
| State and its explanation | `Status` and `StatusDot` in `Status.tsx`; domain status owners supply meaning and freshness |
| A short name in running text or a heading (a target, pack or policy) | `Ident`: the name moves to the next line whole instead of breaking at its hyphen; a name wider than the line still breaks. In a list of names, `after` keeps the comma with its name, so no line starts with one |
| An amount of money | `fmtCost` and `fmtPrice` in `format.ts`: whole cents rounded down, so a total never reads below the parts shown beside it |
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

- **A link is underlined unless it is the item.** The link ink is close to
  the body ink, so words that open something carry an underline, and so does
  a `button.link`. A link that is itself the item is not: navigation, tabs, a
  list's name (a table's name cell, `.project-name`, `.pack-name`, a row
  title set in bold), a link drawn as a button or a card. The exceptions are
  one selector in `styles/base.css`.
- **Disabled is drawn by colour, never by fading** (`--disabled-*` tokens),
  so its words stay readable in both themes. Where the missing input is not
  the empty field right above, a muted line beside the button names it.
- **A quiet button keeps an edge on phones,** where there is no hover to
  reveal it; a quiet destructive act is red.
- **Code is shown whole on its own page, never in a summary.** Guides,
  workflow and policy source (`pre.source`) keep each line unwrapped and
  scroll sideways in their own box, with the scroll cue. A list row or
  summary says what happened in words and links to the record; a summary's
  inline code keeps its backticks and is set as code (`inlineCode`). Short
  code tokens in a sentence do not break; longer code, addresses, paths and
  keys (`Tech`) break at their separators, not inside a word. Diffs keep
  their phone wrapping.
- **A row's action slot is there from the first paint.** A `ListTable`
  action cell keeps the ⋯'s size (`--row-action-size`) whether or not its
  menu has arrived, so a row does not grow when late reads answer.
- **A choice's words stay beside its button,** wrapping in their own column.

## Tokens and CSS

`src/styles/tokens.css` owns colours, typography, spacing, radii and depth.
`src/theme.ts` and `src/size.ts` select the viewer's theme and text size.
Small is the 14px reference; rem tokens scale with the viewer's choice.
Use `--space-1` through `--space-6` for 4, 8, 12, 16, 24 and 32 reference pixels.
Use named depth roles (`--z-base`, `--z-sticky`, `--z-topbar`, `--z-popover`,
`--z-modal`, `--z-toast`) and their contextual variants rather than numbers.
Responsive widths are 600px and 800px, mirrored in `src/chrome.ts`;
use `(width > 800px)` for the desktop complement.

`src/layers.css` declares tokens, base, type, components, pages and utilities.
Shared CSS lives by owner in `src/styles/`; page CSS uses the pages layer.
Keep phone rules beside their desktop rules. Every source stylesheet wraps
its rules in one declared layer and stays within 400 lines. Xterm's external
CSS stays unlayered. Important declarations reverse layer priority.

`make check-web-css` runs Stylelint over every source stylesheet. IDs are
anchors, not selectors. Specificity stays at or below `0,5,3`. Colours, radius,
font size and z-index use custom properties outside `tokens.css`. Existing
`!important` and pixel-spacing debt is counted per file in `css-baseline.json`;
remove debt and lower its entry together. New files have no allowance.
A font weight, font family or `font` shorthand is a type token or `inherit`
(`apptown/type-values`, counted the same way).
Exceptions are zero, auto, inherit, currentColor, transparent, hairlines and
border widths, circle radii (50% or 999px), and safe-area or viewport arithmetic.
Do not copy pixel spacing, add importance or raise a baseline to pass.

## Text roles

Text is set by role. `src/styles/typography.css`, in the type layer, maps
elements to roles and defines each role's class, built only from type tokens;
`.small` and `.muted` stay in `text.css`.

| Role | Class | Elements | Used for |
| --- | --- | --- | --- |
| Page title | `.text-title` | `h1` | the page's name |
| Section heading | `.text-heading` | `h2` | a section or dialog title |
| Subheading | `.text-subheading` | `h3`–`h6` | a heading inside a section or card |
| Name | `.text-name` | `strong`, `b` | a record's name in a row (in a `ListTable` name cell and a `ListRow` alike), and emphasis |
| Body | `.text-body` | `body`; family and weight of controls | running text, buttons, fields |
| Label | `.text-label` | `th`, `dt` | a column head, a fact's name, a phone row's value label |
| Group label | `.group-label` | | the uppercase label over a group of rows |
| Small | `.small` | | meta lines and hints |
| Muted | `.muted` | | secondary words; regular weight inside a heading or a name |
| Code | `.text-code` | `code`, `pre`, `kbd`, `samp`; family of `textarea` | commands, keys, source, paths |

Use an element or a role class rather than setting type in a component. A
component may adjust a role with tokens only, for a state or a part of
itself. Literal type values live only in `tokens.css`. Form control sizes and
a button's line height stay with the controls in `forms.css`, so a phone
field keeps its no-zoom size. Inline styles do not set weight, family or a
literal ink or size; `make check-web-inline-style` counts them per file.
Every role is shown at `/text-roles`, which the camera captures in every
width, text size and theme; a role change shows up there.

## Checking appearance

Install the lockfile's browser with `cd web && npx playwright install chromium`,
then run `make check-web-layout`. Its fixtures bundle the production entry's
CSS order and check text sizes, phone fields, rows, plot bounds and popovers.
The page-disclosure gate allows zero fold-downs outside the menu components.
Rows of the same component use the same text sizes; free text wraps at phone
width. The top bar derives its sole breadcrumb trail from registered routes,
linking every ancestor page and ending at the parent; on phones it shows
only the parent, as a back link. Pages supply only dynamic
segment names. Dependent project lists live on per-item projects pages; their
parents show count links.
The whole-page camera is `internal/apptown/web_ui_browser_test.go` with
`testdata/web-ui.mjs`; inspect its screenshots at phone and desktop widths in
both themes as well as running assertions. Fixture checks do not replace
looking at real pages.
