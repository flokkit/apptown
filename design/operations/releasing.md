# Releasing

How an AppTown release is qualified and published, in order. Pushing a `v*`
tag starts publication automatically; a local tag does not. What a release promises is
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
3. **Exact candidate qualification.** Land the notes and fixes on `main`.
   Require a `heavy.yml` full execution run on that exact commit that passed
   on its first attempt ([GitHub](github.md)); admission, reused execution,
   cache warming and a passing rerun do not count. If execution was reused,
   dispatch a full heavy run on the candidate. Fix a code failure in a new
   commit; for an infrastructure failure, start a new full run on the same
   commit, retaining the failed evidence.
4. **Local tag and builds.** Create `git tag -a v<version>` at that qualified
   commit, without pushing it. From a clean checkout at the local tag,
   `sh scripts/release.sh <version>` checks the notes, reruns the upgrade
   tests and writes local qualification artifacts under `dist/release/v<version>/`.
   It does not publish. Its archive set and names differ from the workflow's:
   also build the publication archive set with
   `sh scripts/release-archives.sh <version> <commit> <output-directory>`.
   That script is the workflow's archive owner; it requires the built web
   bundle and does not replace the preceding qualification checks.
   Also build the workflow's container targets from this exact source before
   pushing the tag: `deploy/Dockerfile` targets `server` and `worker`, each for
   `linux/amd64` and `linux/arm64`. Set build arguments `VERSION=v<version>` and
   `REVISION=<commit>`. Keep these builds local (no push), retaining their build
   logs and image IDs. Archive builds do not exercise the container's source
   copies or notices stage. A failed image build stops publication; fix and
   qualify the new candidate before its tag is pushed.
5. **Fresh install and real-data upgrade rehearsal.** Test the publication
   archives with `sh scripts/stranger-install.sh <archive>`. It installs into
   a disposable Docker daemon and reaches a first preview without a model
   key, using the admitted change's exact UUID. Check the other supported
   installation paths affected by the release, including native Mac storage
   when changed. Then complete the isolated rehearsal below with matched
   database and file copies from maintained installations. Frozen fixtures and
   a fresh install do not substitute for this. A failure stops publication:
   fix the candidate and repeat qualification, builds and rehearsal at its
   new exact commit. Keep the failure evidence.
6. **Publish by pushing the tag.** Only after those checks pass, push the
   qualified local tag. `.github/workflows/images.yml` automatically builds
   and publishes images and the signed release archives using
   `scripts/release-archives.sh`; it does not upload the local `release.sh`
   output. It also publishes the site and versioned tour. There is no later
   manual approval between the tag push and these publication steps.
   Verify the published checksums, signatures, source identity, archive contents
   and image digests, then perform and verify the maintained rollout with
   matching rollback backups. Record the binaries actually running.
7. **Freeze the release's AppTown** for later releases, from the exact tag:
   `go test ./internal/apptown -run '^TestAnUpgradeKeepsWhatThePreviousReleaseRecorded$' -update-upgrade-fixture=<version>`.
   Land only the generated fixture in its own follow-up pull request after
   maintained rollout verification; do not move the release tag.

   A final release (`vX.Y.Z`, no `-`) also updates the Homebrew formula:
   the workflow's `homebrew` job writes `Formula/apptown.rb` from the
   release's SHA256SUMS and pushes it to `flokkit/homebrew-tap`
   ([GitHub](github.md)). A release candidate or test tag leaves the tap
   alone, so `brew install flokkit/tap/apptown` never installs one. After
   the run, `brew update && brew info flokkit/tap/apptown` names the new
   version; a failed `homebrew` job is re-run once its cause is fixed.

   For a local tour preview, use `make tour`. A pushed test tag (`-test.N`)
   still publishes images and creates a draft release, so it is not a way to
   bypass prepublication qualification. Its `tour` job runs and the site is
   left alone; the run's `tour` artifact
   holds the screenshots and the page (open `index.html` from the
   downloaded archive). `make tour` makes the same pictures locally, as a
   preview, with Playwright and Chromium (the version
   `deploy/ci-heavy/Dockerfile` pins, named by `APPTOWN_PLAYWRIGHT_MODULE`).
   After a release, the published tour is at apptown.dev/tour/ and the run
   keeps its artifact for 30 days. A wrong picture is fixed on `main` and
   goes out with the next release.

## Isolated rehearsal before publication

Take a matched database dump and data-directory copy with application writers
stopped, following [upgrading](../../stock/docs/guides/upgrading.md). Keep that
pair unchanged for rollback and rehearse on a disposable copy. Include retained
history: archived and deleted projects, older consoles and sessions, repository
metadata, and pending or interrupted operations. Do not filter the copy down to
active projects or repair troublesome rows just to make the rehearsal pass.

Before any copied application starts, isolate its database, files and Docker
resources from the maintained instances. Deny outbound access to real model
providers, agents, workers, deployment targets and webhook destinations; copied
credentials must not authorize effects outside the rehearsal. Use isolated
networking and local substitutes where needed. Disabling one scheduler or using
`-verify-only` is not a general side-effect boundary. Never mount live data
writable or give the rehearsal the host's production Docker socket.

Run the candidate's migration/startup checks against the copy, then exercise
normal startup and representative reads within that isolation. Record the
source version and backup pair, candidate commit and binary hashes, included
record/file counts and retained-history cases, checks performed, exclusions,
results and cleanup. A subset rehearsal proves only that subset; missing state
or an unexercised migration is a qualification gap to resolve before publication.
These are release procedures, not a new automated gate: the tag workflow checks
CI evidence but does not enforce this rehearsal.

## While a release candidate runs in production

The maintainers run each release candidate in production before the release:

- **Main keeps moving.** Reviewed and gated work lands on `main` as usual.
  There is no separate release branch; the candidate is its tag.
- **Production stays at the candidate tag.** It runs the tagged build and is
  not redeployed from `main` while the candidate runs. Its `X-AppTown-Version`
  response header names the tag.
- **A fix for the candidate is a new candidate.** A defect found while the
  candidate runs is fixed on `main` and qualified as the next release candidate (a new
  tag, archives, fixture and the steps above). Routine corrected RC version
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
