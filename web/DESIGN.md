# Web design

Use the shared patterns before adding page-specific CSS. Preserve every field,
link and action, and keep loading, empty, failed, stale and unknown distinct.
The [web data contract](../design/architecture/web-data.md) owns reads and editor guards.

## Component map

| Need | Use |
| --- | --- |
| Table of records | `ListTable` beside `ListRow`; column headers, one desktop row and two groups of cells on phones; free text wraps within each phone cell. Status keeps its dot and word, technical values use monospace, numbers align right and relative time comes before the final, unlabelled action column. Put the primary action in the section or page header. |
| Inventory row: identity, state, action, optional detail | `ListRow`; `ProjectListRow` for projects and `LibraryRow` for ways of working |
| Compact tabular facts | `GlanceRow`; every cell stays visible at both widths |
| Disk name, used / total and capacity bar | `CapacityStrip` |
| Page section | `Section` |
| Action menu or popover | `MoreMenu`, `SplitButton`, `FormMenu` for menus with form controls |
| Dialog | `Modal`; change decisions use the native dialog in `pages/change/Turn.tsx` |
| Labelled field in a form grid | `FieldRow` with native input, select or textarea |
| State and its explanation | Components in `Status.tsx`; domain status owners supply meaning and freshness |
| Wrapping horizontal group or vertical stack | `.row` or `.stack`, with `.row-1`–`.row-6` or `.stack-1`–`.stack-6` for gaps |

Rows have two contracts: `ListRow` aligns inventory cells; `GlanceRow` keeps
compact facts in reading order. Page facts are inline; long records have their own
page and a link. Only menus and popovers disclose content. Choose by content rather than inventing a universal row.
`.stack` on a table retains the responsive table contract; gap utilities apply
to non-table stacks. Use native links and buttons for actions.

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
