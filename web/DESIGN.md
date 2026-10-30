# Precision instrument

AppTown uses one visual system on every page. `src/styles.css` owns its tokens;
`src/theme.ts` selects light, dark or the system preference. IBM Plex Sans and
IBM Plex Mono ship with the application.

Show outcomes before mechanisms. Reveal detail progressively. Stay calm in
normal operation and explicit about uncertainty, failures and decisions. Spend
density deliberately. Keep development, AI work, checks and operation attached
to the application. Proposed, working, verified, approved and deployed are
separate facts.

Typography and fine rules establish hierarchy. Colour communicates state.
Use ruled collections, compact headings and status strips. Controls have 2px
corners; work surfaces have square corners and no shadows. Use 4, 8, 12, 16 and
24px spacing. Desktop body text is 14px, metadata 12px, sections 18px and titles
26px. Phones use 16px body text and controls at least 44px high.

The light page, sheet and chrome are #F5F6F6, #FFFFFF and #EEF0F0. Dark uses
#171B1D, #1D2225 and #14181A. Semantic colours keep their existing roles:
green for success, blue for machine work, amber for a person, red for failure,
purple for exceptions and an open dashed light for unknown state.

Use fewer words. A status has a state word and a light; its meaning opens from
the word. Required approval meaning and consequential warnings stay visible.
Loading, empty, denied, failed, stale and unknown stay distinct. The shared
data layer owns reads, identities and editor guards.

Avoid gradients, decorative metrics, heavy shadows, excessive rounding,
competing primary actions and collections of cards. Expert detail deserves the
same care as the overview. Every screen answers where the reader is, what is
happening, what needs attention, what they can do next and where detail lives.

## Whole-site review

`internal/apptown/web_ui_browser_test.go` and `testdata/web-ui.mjs` capture the
real UI against disposable seeded application records. Restart the web dev server
on a loopback port, then run:

```sh
APPTOWN_DESIGN_VITE=http://127.0.0.1:5180 \
APPTOWN_DESIGN_OUT=/tmp/apptown-ui-shots \
go test -tags browser ./internal/apptown -run '^TestWebUICapture$' -count=1 -v
```

The camera captures desktop 1440×1000 and phone 390×844 in both themes. It records
rendered word counts and checks one page heading, horizontal overflow, and that
approval controls leave assent meaning visible. It also rejects an account
control clipped by the top bar. Inspect the images as well as the assertions. Use `APPTOWN_DESIGN_BASELINE=1` only to measure the original UI;
its existing layout defects are recorded rather than rejected.

## Measuring visible words

Compare the camera's `pages.json` records from the original and updated UI with
the same seed, route, viewport and theme. Its word count splits rendered
`body.innerText` on whitespace, including content below the first viewport.
Closed disclosures and hidden navigation are excluded; CSS-generated labels
are excluded too. Numbers and control labels count. Record every page and all
four variants, since responsive navigation and asynchronous records can differ.
Keep the baseline revision with the comparison. The initial rollout covers 81
page states and 324 screenshots, compared with `7d87e51e`.
