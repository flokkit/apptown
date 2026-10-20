# Releasing

How a Flokkit release is made, in order. The release script is
`scripts/release.sh`; what a release promises is
[What 1.0 promises](../../stock/docs/guides/compatibility.md).

1. **The upgrade test.** On the candidate commit, run `make upgrade-test`.
   Every earlier release's frozen forge (`internal/forge/testdata/upgrade`)
   must upgrade to it and keep its records.
2. **The notes.** Write `releases/v<version>.md` and commit it. It says what
   the release is for, then has two sections the compatibility guide
   promises:
   - `## HTTP API changes`: each route or response shape that changed since
     the previous release. The HTTP API is experimental, so it may change
     in a 1.x release, but never silently.
   - `## Deprecations`: what will go, in which later release (at least one
     minor release ahead), and what to use instead.

   Write "None." under a section with nothing to say. The release script
   refuses a release without the file or either section. It also refuses a
   release whose API description (`stock/docs/api/openapi.json`) changed
   since the previous release tag while the HTTP section says "None.". It
   cannot check the rest: whether every change is named, and whether a
   removal was announced a minor release before. That is the person
   releasing's job.
3. **The tour.** Run `make tour` and commit `site/tour`: the UI tour's
   screenshots, made from a small example team's AppTown (`site/tour/tour.yaml`
   lists the stops). It needs Playwright with Chromium
   (`private/scripts/nightly-tiers.sh prepare`, or `APPTOWN_PLAYWRIGHT_MODULE`), and
   compresses the shots when `oxipng` is installed. Look at the shots before
   committing them.
4. **The tag.** `git tag -a v<version>` on that commit.
5. **The build.** `sh scripts/release.sh <version>` from a clean checkout at
   the tag. It refuses to build when the tour is older than the sources it
   shows (`site/tour/stamp.json`), checks the notes, runs the upgrade test again and writes the
   archives, `SHA256SUMS` and `RELEASE-NOTES.md` (the notes and the build's
   facts) under `dist/release/v<version>/`.
6. **Freeze the release's forge** for the releases after it, at the tag, and
   commit the result on main:
   `go test ./internal/forge -run '^TestAnUpgradeKeepsWhatThePreviousReleaseRecorded$' -update-upgrade-fixture=<version>`.
   Before publishing, `sh scripts/stranger-install.sh <archive>` installs the
   archive in a fresh container, inside a
   Docker daemon of its own on any Linux machine that may run a privileged
   container, and reaches a first preview with no model key; it removes that
   daemon and touches nothing else. The harness reads the admitted change's
   exact UUID from its JSON response; failed admission or an invalid identity
   fails the check before previewing. Its disposable host installs its own
   JSON parser.
7. **Publish** the archives. This is a separate step: where they go is
   decided per release. The site, apptown.dev with its tour, is published by
   the release workflow's `site` job (design/operations/github.md).


## While a release candidate runs in production

The maintainers run each release candidate in production before the release:

- **Main keeps moving.** Reviewed and gated work lands on `main` as usual.
  There is no separate release branch; the candidate is its tag.
- **Production stays at the candidate tag.** It runs the tagged build and is
  not redeployed from `main` while the candidate runs. Its `X-AppTown-Version`
  response header names the tag.
- **A fix for the candidate is a new candidate.** A defect found while the
  candidate runs is fixed on `main` and qualified as the next release candidate (a new
  tag, archives, fixture and the steps above); a tag is never moved or
  rebuilt. Production then moves to that tag.
- **Work after the tag goes into the next notes.** A release candidate cut
  from a later `main` carries everything since the previous tag, so its notes
  name every HTTP and declaration change since then, not only the fix.
