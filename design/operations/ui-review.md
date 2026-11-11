# UI review

Run a review of every page on the release commit before each release, and
again after its fixes land. A change's lane checks its own pages. The full
review catches what automatic camera checks cannot judge: squeezed columns,
text over text, contradictory statements, empty states that look broken and
raw identifiers. Camera checks cover measurable faults such as overflow,
clipping, row heights and labels.

CI makes the images and contact sheets. Model review runs locally; CI holds
no model API key. The image run completes before publication. Model findings
and owner decisions remain separate from release approval.

## Get the full set

Push `ui-review/<first 12 characters of the commit>` at the commit to inspect:

```sh
commit=$(git rev-parse HEAD)
short=$(git rev-parse --short=12 "$commit")
git push origin "$commit:refs/tags/ui-review/$short"
```

Alternatively, run the `ui-review` workflow by hand on a branch in GitHub
Actions. Download its `ui-review-<sha12>` artifact. It contains `captures/`
and `sheets/`. A failed run keeps partial captures for diagnosis; read the
failed step before trying again.

Locally, use the [camera command and prerequisites](../architecture/testing.md),
without `APPTOWN_DESIGN_PAGES`, then make sheets from the captures folder
printed by the camera:

```sh
export APPTOWN_PLAYWRIGHT_MODULE="$PWD/web/node_modules/playwright/index.mjs"
TMPDIR=/tmp node scripts/ci-web-camera.mjs -timeout 45m
node scripts/ui-review.mjs sheets <captures> <sheets>
```

Each page has its own folder, with `<desktop|phone>[-large]-<light|dark>.png`
and the page's text beside it (`.txt`). Additional shots show open states
such as `-disclosure`, `-decision`, `-receipt` and `-history`. `pages.json`
maps page names to routes; `capture.json` describes the scene;
`layout-errors.json` and `content-exclusions.json` record the camera's checks
and explicit content exclusions.

## Make contact sheets

A phone shot is 390 pixels wide and can be thousands tall. Scaling that image
to fit a model's view makes the text unreadable. The `sheets` command cuts
every PNG into 1300-pixel segments and lays them side by side: as many columns
as fit in 1800 pixels, with 24-pixel gaps. That gives four phone columns or
one desktop column. A yellow header names the page, variant and pixel range.
Grey is padding. `sheets/index.json` lists each page's sheet names.

## Read the sheets with a model

Split pages into batches of about twenty. Give each batch to a separate
image-reading model session, in parallel. Each session reads every sheet for
its pages. Give it this brief, filling in the input and output locations:

```text
AppTown is the accountable home for a team's internal software: projects,
changes (governed units of work), chats, previews, approvals, landing,
deployments, releases, backups and checks.

Read every sheet of every page in your batch. Sheets/index.json lists the
files; captures/pages.json supplies routes and each shot has adjacent text.
Each column shows a 1300-pixel slice of the full page. Its yellow header
names the page, variant and y range in page pixels.

Read phone-light and desktop-light for layout; large-text variants for
wrapping and squeezing; dark variants for contrast and differences from
light. In additional open-state shots, inspect the open menu or dialog.

Report visible problems:
- Layout: squeezed columns, overlap, cut-off or off-screen content,
  horizontal scroll, odd gaps, crowded or small tap targets.
- Readability: low contrast, tiny text, walls of text on phones,
  truncation with no way to see the rest.
- Contradictions or confusing state: incompatible statements, actions that
  make no sense in the shown state, raw identifiers or placeholder text.
- Empty and error states that look broken.

The scene is made up: a test host, local addresses and tiny durations can
be scene artifacts. Report them too, marked kind=scene: they hide the real
layout. Otherwise use kind=product. A screenshot alone does not prove the
cause; describe what you see.

High severity means unusable or clearly broken: overlap, unreadable text,
cut-off controls or contradictory state. Medium means clumsy but usable;
low means polish. Ignore sheet padding, yellow headers, pixel nitpicks and
personal colour taste. Do not propose code. Report each problem once per
page, naming its worst variant, even if it occurs on many pages.

Write a JSON array, one object per finding:
{"page":"page directory","variant":"exact shot name without .png",
 "y":[from,to],"kind":"product|scene","category":"layout|overlap|truncation|wrapping|spacing|tap-target|contrast|readability|contradiction|wording|empty-state|other",
 "severity":"high|medium|low","what":"one plain sentence beside a crop",
 "element":"short name of the element"}
Derive y from the column header plus position within the column.
Finish with the output path, finding count and five worst findings.
```

## Merge findings and make crops

```sh
node scripts/ui-review.mjs crops <captures> <report> <batch-1.json> <batch-2.json>
```

Batches are arrays with the brief's required fields. The command assigns
`f001`, `f002`, … in file then array order and writes `report/findings.json`
with each finding's `id` and relative `crop`. Crops have 60 pixels of context
on each side of the vertical range, clamped to the shot, at least 200 pixels
(where the shot permits) and at most 900 pixels tall. A missing shot is
reported without a crop; the command writes the other crops and exits 1.
Correct the finding's page or variant and rerun it.

## Group by cause

Give all findings to another model session, or group them by hand. Use this
brief:

```text
Group these findings by the shared cause: a component, a repeated pattern
or the camera scene. Do not group by page. Put the scene group first because
fixing the scene changes what the other images show. Keep every finding id.
Write groups.json as [{"id":"g01","title":"plain title",
"about":"two sentences describing the cause","items":["f001","f002"]}].
For each group show counts of findings, distinct pages and high-severity
findings, with its worst crops, one per page. Describe uncertainty plainly.
```

## The owner decides

Show the owner each group with its crops. The owner decides **Fix** (now,
before this release or the next), **Later** (record follow-up work), or
**Skip** (not a problem). Keep the decision and reason beside `groups.json`,
for example in `decisions.json`:

```json
{"g01":{"decision":"Fix","note":"Controls must remain readable."}}
```

## Fix at the cause

Fix a shared component in the [component map](../../web/DESIGN.md#component-map),
its shared stylesheet, or the scene. When a machine can see the class of
fault (overflow, text over text, tap target size), add a camera or layout
check so it stays fixed. Larger groups touching the same components land
one after another; each runs the full camera before landing.

## Check before and after

Make a fresh full set and fresh sheets on the commit containing the fixes.
Give a model the approved findings, the new sheets and the old crops, using
this brief:

```text
For each approved finding on your pages, read the relevant new sheets.
Use the old crop and old sheets when the finding is unclear. Say:
- fixed: the described problem is gone;
- not fixed: still visibly there; describe what remains;
- changed: gone, but another problem appears there; describe it;
- cannot tell: the state or element is absent; say why.
Report new serious problems too, with id "new". Write a JSON array of
{"id":"f001","page":"page name","verdict":"fixed|not fixed|changed|cannot tell",
 "note":"one plain sentence","y":[from,to]} using NEW page coordinates.
Finish with counts per verdict and the worst remaining or new problems.
```

Take unresolved findings back to the owner's group decisions.

## Practical lessons

- `APPTOWN_DESIGN_PAGES=home,change` narrows a run to named pages. The log
  says “shooting N of M pages”. `DESIGN_PAGES` is refused.
- Keep `TMPDIR` short (`TMPDIR=/tmp`). SSH connection-sharing sockets have
  a macOS path limit; the wrapper refuses a run folder longer than 40 characters.
- Run one full camera at a time per machine. Four browser tabs use most of
  the CPU. Short narrowed runs may overlap: each run owns a fresh folder.
- A shared UI change runs the full camera before landing. A narrowed run
  can miss other pages; heavy CI runs after merge.
- A model sees the shot. Explain the made-up scene so its artifacts can be
  distinguished from product faults.
