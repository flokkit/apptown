# Releasing

How an AppTown release is built, qualified and published, in order. A
release is built once, in CI, before its version tag exists; qualification,
rehearsal, publication and deployment all use those exact bytes. What a
release promises is [What 1.0 promises](../../stock/docs/guides/compatibility.md).

1. **The notes, as an ordinary change.** Write `releases/v<version>.md` and
   run `sh scripts/release-prepare.sh <version>`: it adds the previous
   release's upgrade fixture (`internal/apptown/testdata/upgrade/<previous>`),
   from that release's `upgrade-fixture-<previous>.tar.gz` asset, checked
   against the digest GitHub records, and checks the notes
   (`scripts/release-notes-check.sh`). Land both through the merge queue like
   any change. Like every landed commit it gets a `heavy-main.yml` run with
   every test fresh, and CI's unit tests upgrade every frozen release's
   AppTown, the previous one's included, to it. The notes file is
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

   Write "None." under a section with nothing to say. The check refuses
   notes without either section, and an API description
   (`stock/docs/api/openapi.json`) changed since the previous release tag
   while the HTTP section says "None.". It cannot check the rest: whether
   every change is named, and whether a removal was announced a minor
   release before. That is the person releasing's job.

   Until the next release's notes change lands, `main` is not tested against
   upgrading from the newest release's frozen AppTown; its real-data
   rehearsal (step 3) is.
2. **The candidate.** Choose the commit C: a commit of `main` with the notes,
   whose `heavy-main.yml` run passed on its first attempt
   ([GitHub](github.md)); a queue run, which may reuse cached results,
   admission and a passing rerun do not count. For an infrastructure failure,
   start a new `heavy-main.yml` run by hand on the same commit. Choosing C freezes the release's scope; a
   change to it is a new candidate. Push the tag
   `candidate/v<version>/<first 12 characters of C>` at C. `candidate.yml`
   builds everything the release publishes, once: the three images for
   `linux/amd64` and `linux/arm64`, pushed by digest, smoke-checked on their
   own runners and joined into indexes tagged `candidate-<sha12>`; the
   release archives (`scripts/release-archives.sh`), `SHA256SUMS`, the update
   manifest and the notes; the upgrade fixture this release leaves for later
   ones; a stranger's install of the linux/amd64 archive
   (`scripts/stranger-install.sh`, first preview without a model key); and the
   UI tour. A failure stops the release: fix `main` and push a new candidate
   tag for the same version, since no version tag was pushed.
3. **Qualification on the candidate's bytes.** From the candidate's
   `archives` artifact, in parallel: rehearse the upgrade on copies of
   maintained installations' data (below), install the darwin archive
   natively on a Mac, and deploy the candidate to a running installation
   that exercises chat on a worker and check a chat receives a reply. A
   failure stops the release before publication.
4. **Publish by pushing the version tag.** Push `v<version>` at C. The
   pre-push hook checks the candidate run instead of rerunning the gate.
   `images.yml` finds the candidate run and publishes its bytes without
   building anything: it gives each image index the version's tags and signs
   it, signs `SHA256SUMS`, creates the GitHub Release from the candidate's
   `release` artifact with the upgrade fixture beside it, and publishes the
   site with the candidate's tour. There is no later manual approval. A
   failed job (a signing service's error, say) is re-run as it is. Verify
   that the published `SHA256SUMS` is the candidate's, its signature, and
   that each image's version tag names the candidate's index digest.

   A final release (`vX.Y.Z`, no `-`) also updates the Homebrew formula:
   the workflow's `homebrew` job writes `Formula/apptown.rb` from the
   release's SHA256SUMS and pushes it to `flokkit/homebrew-tap`
   ([GitHub](github.md)). A release candidate or test tag leaves the tap
   alone, so `brew install flokkit/tap/apptown` never installs one. After
   the run, `brew update && brew info flokkit/tap/apptown` names the new
   version; a failed `homebrew` job is re-run once its cause is fixed.

   For a local tour preview, use `make tour`. A pushed test tag (`-test.N`)
   still publishes images and creates a draft release, so it is not a way to
   bypass the candidate. `make tour` makes the same pictures locally, as a
   preview, with Playwright and Chromium (the version
   `deploy/ci-heavy/Dockerfile` pins, named by `APPTOWN_PLAYWRIGHT_MODULE`).
   After a release, the published tour is at apptown.dev/tour/. A wrong
   picture is fixed on `main` and goes out with the next release.

## The upgrade rehearsal

Rehearse each release's upgrade on the data of the installations that will
take it, by restoring a whole-AppTown backup with the candidate
([AppTown's own backups](../../stock/docs/guides/apptown-backups.md)):
`apptownd backup` on the installation, nothing stopped, then
`scripts/apptown-recovery-rehearsal.sh` restores the archive with the
candidate's `apptownd` onto a spare host cut off from everything a restored
AppTown would act on (upstream Git, cloud APIs, attached hosts, model
providers) before it starts. It compares API reads taken before the backup
with the restored candidate's, and starts it normally. Every release thereby
also proves restore. The comparison is a sample of activity history and
recent changes per project, not every record; the frozen fixtures cover the
retained history of every earlier release. Never give a rehearsal the
host's production Docker socket or live data. Record the source version,
the candidate, what was compared and the cleanup. A failure stops the
release: fix the candidate and rehearse its replacement.

## While a release candidate runs in production

The maintainers run each release candidate in production before the release:

- **Main keeps moving.** Reviewed and gated work lands on `main` as usual.
  There is no separate release branch; the candidate is its tag.
- **Production stays at the candidate tag.** It runs the tagged build and is
  not redeployed from `main` while the candidate runs. Its `X-AppTown-Version`
  response header names the tag.
- **A fix for the candidate is a new candidate.** A defect found while the
  candidate runs is fixed on `main` and released as the next release candidate
  (the steps above). Routine corrected RC version
  increments belong to completing an authorized release; the number alone is
  not a reason to stop for approval. Production then moves to that tag.
- **Published bytes are not silently replaced.** Withdraw a failed release and
  issue a corrected candidate under a new version. An explicit owner instruction
  to withdraw or delete a release overrides the usual preservation convention;
  carry it out, record what was removed and verify the public release, tag and
  image references. Keep private evidence and rollback backups. Without such an
  instruction, do not move or delete published tags or overwrite their artifacts.
- **Work after the tag goes into the next notes.** A release candidate cut
  from a later `main` carries everything since the previous tag, so its notes
  name every HTTP and declaration change since then, not only the fix.
