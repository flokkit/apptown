# Releasing

How a Flokkit release is made, in order. The release script is
`scripts/release.sh`; what a release promises is
[What 1.0 promises](../../stock/docs/guides/compatibility.md).

1. **The upgrade test.** On the candidate commit, run `make upgrade-test`.
   Every earlier release's frozen AppTown (`internal/apptown/testdata/upgrade`)
   must upgrade to it and keep its records.
2. **The notes.** Write `releases/v<version>.md` and commit it. The file is
   also the GitHub Release's text, where a relative link does not resolve,
   so it links a repository file by its full address,
   `https://github.com/flokkit/apptown/blob/main/<path>` (`make check-docs`
   holds this). It says what the release is for, then has two sections the
   compatibility guide promises:
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
3. **The tag.** `git tag -a v<version>` on that commit, once it is on
   `main` (the notes land by their pull request first), and
   push the tag. Its images and archives are built only when a `heavy.yml`
   full execution run on that exact commit passed on its first attempt
   ([GitHub](github.md)); a run that passed only on a re-run does not count.
   Wait for the merge-queue run or a manual heavy run to complete before
   pushing the tag. PR admission and main cache warming are not release
   evidence. When execution failed, fix the cause in a new commit,
   or, when the failure was not the code's, start a new run by hand on the
   commit (not a re-run).
4. **The build.** `sh scripts/release.sh <version>` from a clean checkout at
   the tag. It checks the notes, runs the upgrade test again and writes the
   archives, `SHA256SUMS` and `RELEASE-NOTES.md` (the notes and the build's
   facts) under `dist/release/v<version>/`.
5. **Freeze the release's AppTown** for the releases after it, at the tag, and
   land the result on main by its own pull request:
   `go test ./internal/apptown -run '^TestAnUpgradeKeepsWhatThePreviousReleaseRecorded$' -update-upgrade-fixture=<version>`.
   Before publishing, `sh scripts/stranger-install.sh <archive>` installs the
   archive in a fresh container, inside a
   Docker daemon of its own on any Linux machine that may run a privileged
   container, and reaches a first preview with no model key; it removes that
   daemon and touches nothing else. The harness reads the admitted change's
   exact UUID from its JSON response; failed admission or an invalid identity
   fails the check before previewing. Its disposable host installs its own
   JSON parser.
6. **Publish** the archives. This is a separate step: where they go is
   decided per release. The site, apptown.dev with its tour, is published by
   the release workflow (design/operations/github.md): its `tour` job runs
   `make tour` on the tag's source, and its `site` job publishes the site
   with those screenshots, with no further approval. The screenshots are not
   committed.

   A final release (`vX.Y.Z`, no `-`) also updates the Homebrew formula:
   the workflow's `homebrew` job writes `Formula/apptown.rb` from the
   release's SHA256SUMS and pushes it to `flokkit/homebrew-tap`
   ([GitHub](github.md)). A release candidate or test tag leaves the tap
   alone, so `brew install flokkit/tap/apptown` never installs one. After
   the run, `brew update && brew info flokkit/tap/apptown` names the new
   version; a failed `homebrew` job is re-run once its cause is fixed.

   To look at the tour before a release, push a test tag (`-test.N`): the
   `tour` job runs and the site is left alone; the run's `tour` artifact
   holds the screenshots and the page (open `index.html` from the
   downloaded archive). `make tour` makes the same pictures locally, as a
   preview, with Playwright and Chromium (the version
   `deploy/ci-heavy/Dockerfile` pins, named by `APPTOWN_PLAYWRIGHT_MODULE`).
   After a release, the published tour is at apptown.dev/tour/ and the run
   keeps its artifact for 30 days. A wrong picture is fixed on `main` and
   goes out with the next release.

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
