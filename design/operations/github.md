# GitHub: CI and images

The repository, `github.com/flokkit/apptown`, is where AppTown's code lives
and changes. GitHub Actions tests every pull request and every commit on
`main`, and builds the container images on a release tag. The workflows
are in `.github/workflows/`.

## How main changes

`main` changes through its merge queue, one pull request per change, squash-merged. Its
ruleset requires the pull request, allows only squash merges, forbids force
pushes and deletion, and requires the `ci-ok` check (`ci.yml`) and the
`heavy-ok` check (`heavy.yml`) on the queued commit, so the runs that passed tested exactly what `main` becomes.
A pull request's `heavy-ok` is admission to the queue, not heavy execution evidence.
The squash commit takes the pull request's title and description, which are
the change's public record. Opening a pull request is limited to the
repository's collaborators ([CONTRIBUTING.md](../../CONTRIBUTING.md)).

## Dependency updates

The hosted Renovate GitHub App proposes dependency updates, configured by
`renovate.json5` at the root. Native managers read Go modules, npm and
Composer manifests and lockfiles, Actions, Dockerfile bases and the stock mise
toolchains. Custom managers read the agent's central pins and package ARGs,
scanner images and connector presets, Makefile tools, CI gitleaks, and the
stock tool-version catalog and dependency defaults. An annotation beside a
nonstandard pin names its upstream datasource. Fixtures and evaluation material
are excluded.

Agent tools move with their central pins and every scanner or connector preset
that shares the tool. AppTown's build and CI tools have separate groups. Stock
toolchains and catalog entries stay within their declared version line; partial
selectors such as Node `24` and Python `3.12` are held because they already
select that line. Exact versions in the catalog can still receive patch
updates. A new stock line is added deliberately rather than changing the meaning of an existing line.

GitHub archive downloads use `github-release-attachments` to update both
architecture checksums with their version. Each checksum has its own release-tag
annotation so Renovate can replace the two hashes independently. `check-pins`
rejects an annotation whose release differs from the executable version. Keep
these annotations in sync when making a manual update. CI gitleaks has its own
version and checksum, independent of the project scanner.

Runtime catalog proposals include their exact versions and pinned image digests.
Alpine Git and Caddy move with their central pins. BusyBox's version and
its digest-only image reference update together. Postgres and MariaDB stay on
their declared lines. The logical `apptown-sh` line `1` uses upstream Alpine
Git version `2`; it does not constrain the registry to version `1`.

Private Docker proposals group the catalog's Docker 27 daemon, its central
pins and the agent CLI version. The Docker 29 CLI and daemon bases in
`deploy/Dockerfile` form a separate group. Each group stays on its existing
major line. Docker CLI archive checksums remain manually verified for both
architectures; a proposed version change retains the old checksums until a
person supplies the verified replacement hashes, so the download verification
continues to reject unmatched archives. The other remaining manual pins are
glab's GitLab archive and checksums.

Its private Docker proposal notes require the external-worker private Docker
tests ([workers](workers.md), "Private Docker on the worker") to pass against
the changed daemon before that update is accepted or included in a release.
A proposal supplies no release authority. Other native Dockerfile bases retain their existing coverage.

Nothing opens by itself. Renovate keeps one issue, the **Dependency
Dashboard**, listing the available updates, grouped so that one pull request
is one thing to verify: Go modules, `web/` npm, each
starter (its packages and its Dockerfile's bases together), Actions,
AppTown's own Dockerfile bases, each project tool and its central pin, build
and CI tools, stock tool lines, and a new AppTown build Go line (`go.mod`
and its build images). Project Go tools stay separate from AppTown's build/CI
compiler. Major updates stay separate from minor and patch updates; coupled
pins and packages released together (React with React DOM) move together.

An update whose release is at least three days old waits under "Pending
Approval". One less than three days old, or one whose registry gives no
release date, is listed under "Pending Status Checks". Ticking a box there
opens the pull request at once, skipping the three-day wait. Leave a dated
release until it is three days old, when it moves to "Pending Approval". An
undated one stays pending, so tick it there once you have checked it. Actions stay pinned by commit SHA with the version in a comment, and a
base image pinned by digest keeps its digest.

To take an update, bls ticks its box on the dashboard and Renovate opens its
pull request, with bls as reviewer. Tick one at a time: a ticked box overrides
Renovate's limit of one open pull request, so the limit holds only for what
Renovate opens unasked. Bump pull requests land like any other change, as in
the section above: bls reviews the change and its notes, `ci-ok` passes on a
branch up to date with `main`, and it is squash-merged; nothing merges
automatically. Renovate rebases a branch only when it conflicts; to bring one
up to date, tick the rebase box in the pull request. GitHub's "Update branch"
button adds a merge commit, after which Renovate stops updating that branch.
A bump that fails a check stays open for a person to finish or close.
`make check-pins` requires the agent Go image, Go scanner images and central
pin to agree; Renovate updates them in one group. Each image variant retains
its registry availability and release-age check. If their eligible versions
differ temporarily, the pin check refuses the update until they agree. The
same pin check rejects disagreement between other project tools, their
central pins and scanner/catalog references. A group with unavailable or
ineligible variants stays blocked until its pins agree.

Security fixes are the exception. Renovate reads GitHub's Dependabot alerts,
which are on in the repository's security settings (`renovate.json5` does not
turn them on). When an alert names a dependency it reads, Renovate opens the
fix without approval or the three-day wait,
assigned to bls, one at a time. So at most two Renovate pull requests are open:
one ticked update and one security fix. Land the fix first.

To check the file after editing it (pinned, and with npm's install scripts
off, as for every npm command here; a warning that RE2 is not usable is
expected):

```sh
npm_config_ignore_scripts=true npx --yes --package renovate@44.149.0 -- \
  renovate-config-validator --strict
```

To exercise extraction and real replacement with the same pinned Renovate
implementation (temporary files only):

```sh
npm_config_ignore_scripts=true npm exec --yes --package renovate@44.149.0 -- \
  sh -c 'node scripts/renovate-test.mjs "$(dirname "$(command -v renovate)")/../renovate"'
```

This checks the custom pins, native manifests and mise files, owner groups,
paired architecture checksum edits, CI pins, stock and runtime versions, digest refreshes
and the pin gate on the resulting temporary tree. It supplies fixture update
values without fetching releases; registry availability and the three-day age
remain checks on each actual update.

Renovate also checks the file on every run and opens an issue when it cannot
read it.

## GitHub Actions

The repository is public, so standard runners cost nothing.

**`ci.yml`** runs on every pull request, a documentation-only one too, on
every push to `main`, and by hand. A pull request's newer run cancels its
older one; main's runs, and runs by hand, are each kept. The pull request's
run already tested what `main` becomes; main runs it again so that each
landed commit has its own result and so that main writes the caches every
pull request restores. Jobs:

- `web`: npm ci, the guides' checks (`make check-docs`, which renders
  links with the Docs page's Markdown library from `web/`), typecheck, Vite
  build, lint and unit tests, and the npm
  audit (`make check-npm-audit`, `scripts/npm-audit-check.py`): a high or
  critical advisory in what `web/` ships (dev dependencies left out) or in
  any starter seed's lockfile (dev dependencies included, since every new
  project starts from them) fails the job, unless
  `scripts/npm-audit-exceptions.txt` lists it with the date and the reason,
  which is only for an advisory with no fixed version. The audit asks the
  registry's advisory service, so it runs here and not in the offline
  `make check`; when npm cannot get a report the job fails. An advisory
  published against an unchanged lockfile fails the next pull request,
  whatever it changes: take the fixed version. It passes `web/dist` to the
  Go jobs, since Go embeds it.
- `checks`, three jobs, so the analyzers that type-check every package do
  not share one runner's cores: gofmt and generated code, schema ownership,
  vet, staticcheck, the lint rules and their fixtures, the shape rules and
  the scripts' tests, exec, sqlnow, reachability, the heavy runner's test,
  the tool and base image pins, the npm install-script rule, govulncheck and
  the gate runner's own test. Together they run every stage once
  (`scripts/ci_workflows_test.py`). The pinned analyzers are cached by the
  Makefile.
- `test`, four parts (`scripts/ci-test-partition.sh unit`): the root
  package's two halves, the rest of `internal/apptown`, and every other
  package. The database tests use the harness's own disposable PostgreSQL
  container (`internal/dbtest`), as a local `make check` does. Results are
  reused from Go's test cache as **Test results** below says.
- `public`: the public gate (`make check-public`, `scripts/public-check.py`):
  the committed tree has nothing under `private/` and none of the
  maintainers' root files, names none of their home-lab hosts (listed in
  `scripts/public-check-names.txt` as hashes, so the list does not publish
  them; a hash of an address is easy to reverse, and the point is only not
  to advertise them), and gitleaks, pinned by version and SHA-256, finds no
  secret.
- `sql-writes`: the semgrep rule against silently discarded database writes
  (`make check-sql`).
- `ci-ok`: passes only when every job above succeeded; a failed, cancelled
  or skipped job fails it. It is the check the ruleset requires of `ci.yml`,
  so its `needs:` list names every job, and a job added to `ci.yml` is added
  there too.

**Caches.** Main publishes Go module caches and Go caches (compiled packages
and test results), the heavy tiers' from `heavy-main.yml`; pull requests and
the queue restore them without publishing. Modules (`go-mod-*`) use
dependency hashes. Go cache keys include platform, architecture, dependencies
and commit; each successful main job publishes its current cache, and later
runs restore the newest compatible prefix. Container-tier keys also include
the runner Dockerfile and tier. Each archive retains its newest entries
(`scripts/ci-cache-trim.py`), never Go's own control files beside them: at
most 1 GiB for a heavy or race job, whose partitions' compiled packages and
test results together need it, and 256 MiB for ci.yml's jobs. One main
commit's archives are then about 3.5 GB compressed, inside the repository's
10 GB Actions cache, which GitHub keeps by evicting the least recently
accessed archives, usually older generations. Eviction causes recompilation and re-execution,
never omission of tests.

**Test results.** A pull request's and the queue's test jobs reuse a
package's passing result from main's when its inputs are identical, and run
it otherwise. Go reuses a result only for the same test binary (sources,
dependencies, embedded files, build flags, Go version), the same test flags,
the same values of every environment variable the tests read and the same
size and modification time of every module file they opened. Three things
make that sufficient here:

- `scripts/ci-content-mtimes.py` sets every file's and directory's time from
  its content after checkout, so equal content looks equal in every run and
  any difference, a same-size edit too, looks different.
- `scripts/ci-test-environment.sh` records, with the stored results, what Go
  cannot see: the runner image, the Docker daemon and client, git, the ID of
  every image in `deploy/ci-heavy/test-images.txt` (pulled first; tags move)
  and of the images CI builds from the checkout. A different record, or none,
  expires every stored result before the tests run; compiled packages stay.
- A test that hands repository files to a child process (a script, a
  `docker build` context, a binary built with `go build`) reads them first
  with `internal/testinputs`, so they are its inputs too. A test whose outcome
  depends on what it downloads while it runs (a seed build's package
  installs) is listed in `scripts/ci-uncached-tests.txt` and runs with
  `-count=1` every time; nothing detects a missing line.

Main never reuses: a push to main runs every test fresh
(`ci-test-environment.sh fresh`) and only its passing jobs save, so every
stored result is a full execution of a landed commit, and each landed commit
is a full chance for a flaky test to fail, visibly on main. A failed job
saves nothing; until a later main commit passes, pull requests and the queue
may still reuse the last successful main result of that test for identical
inputs. A reused result is that same execution, not a new one: the job log
marks the package `(cached)`. Local `make` gates keep `-count=1`: a
developer machine records no environment.

The container tiers restore the same host module path as the producer and mount
it with their compilation cache into the Linux runner. `CI_HEAVY_GOMOD` and
`CI_HEAVY_GOCACHE` select absolute host paths; local runs default to the existing
named Docker volumes. The runner restores host ownership before archiving.
Neither database state nor Docker daemon state is cached. Hosted tiers build and load both current-checkout images with Buildx, restoring
GitHub Actions layer caches in separate runner and agent scopes. Only
`heavy-main.yml`'s integration-root-a job on main exports those rolling
scopes. The harness's explicit
prebuilt mode requires both images to exist; local runs still build by default.
Layer-cache eviction causes a rebuild, never omission of tests.

The analyzers (`go-tools-*`) use the Makefile hash. setup-node's npm cache uses
the lockfile; a pull request that changes it can save its own copy. Release
builds (`candidate.yml`) use no Go Actions cache, since a tag's cache could
serve only that tag.

**`heavy.yml`** executes the Docker integration tier, journeys, opt-in browser,
pi and inference_docker tests, and the race detector on `merge_group` and
manual dispatch, reusing main's stored results as above. A pull request runs the qualification report and reports
admission-only `heavy-ok`; the queue qualifies the actual commit before
merging. Manual runs always execute every heavy job. No label
changes what executes. Each job first pulls the images its tests start, listed in
`deploy/ci-heavy/test-images.txt` (`scripts/ci-heavy.sh pull`, four at once,
three attempts each), so a registry error while fetching them fails that step,
naming the image, and not a test. A test that starts a new image adds it to
the list. Tests can still reach a registry: AppTown's own deliberate pulls
(a selected image, an image preview, a Portainer stack), BuildKit resolving
a Dockerfile's FROM tag, and private Docker daemons inside a test pulling
their own images; a registry outage there still fails that test. Its last
job, `heavy-ok`, requires the qualification job to succeed. Execution requires
`web`, every `tier` entry and `race` to succeed; queue reuse requires all three
groups to be skipped. Partial execution fails. The ruleset requires the
aggregate, so a red heavy run blocks merging.
As with `ci-ok`, a job added to `heavy.yml` is added to its `needs:` list.
Race uses four package partitions: the root package, backup/data protection,
workflow execution, and the rest. Integration uses six: the root package's tests
split by name three ways (two named sets of its longest serial tests, and
every other test, where a new one lands), dependencies, worker, and the rest.
`scripts/ci-test-partition.sh` lists packages with the execution build tags
and assigns every package to exactly one partition; new packages default to
the rest. Every matrix entry must succeed. Local
`make check-race` still runs the entire race suite.

The queue can reuse full heavy execution only when its exact base and candidate
have identical inputs apart from regular non-executable Markdown under
`design/` and `releases/`. Embedded docs, deployment files, workflow changes,
executable files and every other path remain inputs. The newest applicable
main, queue or manual run on the exact base must have complete successful
first-attempt execution. Reuse-only runs cannot supply another reuse; a newer
failure or unfinished run prevents an older green source from winning.
Missing or uncertain evidence selects execution. The qualification artifact
records the candidate, source commit/run, input digest, policy and decision.
PR reports do not activate reuse, and manual dispatch always executes. A
source run's jobs may themselves have reused cached test results. Reuse
qualifies a queue landing but supplies no release execution evidence.

Both CI workflows handle `merge_group: checks_requested`; a PR check alone
is never release evidence. **`heavy-main.yml`** runs heavy.yml's jobs on every
push to main, and by hand, with every test fresh: it re-validates each landed
commit, saves the caches heavy.yml restores, refreshes image layers, and is
the release evidence below. It is a separate workflow so that landing, which
waits for heavy.yml's run on the landed commit, does not also wait for it.
Its matrix and cache keys are heavy.yml's (`scripts/ci_workflows_test.py`).

**`candidate.yml`** builds a release before its version tag exists. Pushing
a tag `candidate/v<version>/<first 12 characters of the commit>` at a commit
of `main` starts it, so the run's commit is exactly that commit. Its first job
checks the tag names that commit, the commit is on `main`, the release notes
(`scripts/release-notes-check.sh`), that the previous release's upgrade
fixture is at the commit, and the heavy evidence: `scripts/heavy-evidence.py`
asks the API for a `heavy-main.yml` run (a push to `main`, or by hand) on
the exact commit that passed on its first attempt; a queue run, which may
reuse cached results, is not evidence, and nothing is built without one: CI
must pass, not pass eventually. Both its first and latest attempt must
contain the complete successful execution jobs. The newest applicable run
decides; an older success never hides a newer failure or a run still
executing. A run must name the exact SHA; matching trees do not count. A run that
failed and then passed on a re-run is not evidence, nor is a red, missing or
unreadable result. A re-run of failed jobs keeps the green jobs' results and
replaces the failure inside the same run; a new run by hand is a complete run
of its own, with the red run left beside it. So when heavy failed, fix the
cause in a new commit, or, when the failure was not the code's (a lost runner,
a registry outage), start a new run by hand on the commit (Actions,
heavy-main, Run workflow; not "Re-run"). The job's log lists the red runs on the commit beside
the one it accepts.

The candidate then pushes each image to GHCR by digest with BuildKit's
provenance (`mode=max`) and SBOM attached, checks each on its own runner (its
labels name the version and commit; the server's and worker's binaries say
the version) and joins each image's platforms into one index tagged
`candidate-<sha12>`. Each image is built natively on amd64 and arm64 runners;
arm64 runners are free for public repositories and billed by the minute for
private ones, within the organisation's Actions budget. Beside the images it
builds the release archives (`archives`, as soon as they exist), then the
update manifest over them and the indexes (`release`), the upgrade fixture
(`upgrade-fixture`), a stranger's install of the linux/amd64 archive, and the
UI tour (`tour`), each kept 30 days. A failed candidate burns no version.

**`images.yml`** runs when a `v*` tag is pushed and builds nothing. Its first
job (`scripts/candidate-evidence.py`) finds the successful candidate run on
the tag's exact commit for that version, the newest such run deciding, and
publishes nothing without one. It gives each candidate index the version's
tags (`1.0.0`; also `1.0` and `latest` for a release that is not a
pre-release), checking each tag resolves to the candidate's digest; uploads
the candidate's `release` artifact as it is, with the upgrade fixture beside
it, on a GitHub Release; and publishes the site with the candidate's tour. A
failure here, such as a signing service's error, is fixed by re-running the
failed jobs: nothing is rebuilt. The workflow keeps its file name because
every signature names it (`.github/workflows/images.yml@refs/tags/<tag>`),
which installed AppTowns and the guides verify. The repository's visibility
decides the rest:

- While private, the images go to private packages. Private package storage
  and downloads beyond GitHub's free allowance are billed to the
  organisation, capped by its Packages budget. Pulling one needs `docker
  login ghcr.io` with a token that can read packages. Nothing is signed.
- When public, each image index and `SHA256SUMS` are signed keyless with
  cosign. GitHub's own artifact attestations need Enterprise Cloud for a
  private repository, so the provenance and SBOM are BuildKit's, stored with
  the image. Signing waits for public because a keyless signature is
  recorded in Sigstore's public transparency log with the repository's name.

GHCR packages start private, also for a public repository. For anonymous
pulls, make each package public in its package settings on GitHub. A
failed run can be retried by re-running the tag's workflow.

The `site` job publishes apptown.dev once every image is tagged
(`site/publish.sh <version>`): the landing page, the candidate's tour, and
the `try` script, which starts the version just published. A failed tour
fails the candidate, so nothing is published, and `publish.sh` refuses a tour
without every screenshot. A test tag (`-test.N`) skips the site, so the public install command never
names a test build; the candidate's tour artifact is how to look at a
release's tour beforehand. The job signs in to AWS with
GitHub's OIDC token as `apptown-site-publisher`, a role that may only write the
site bucket and invalidate its distribution. The role trusts one thing: a job
in this repository's `apptown.dev` environment, which only `v*` tags may
deploy to. GitHub names the repository by its ids in the token
(`repo:flokkit@101402407/apptown@1423238106:environment:apptown.dev`), so the
trust follows this repository through a rename and never passes to another
one given its name. No AWS key is stored in GitHub. Once the repository is public, a
tag ruleset limits creating and moving `v*` tags to admins. The role, the bucket and the
distribution are made by `tf/products/apptown/site` in the maintainers'
infrastructure repository. A maintainer with AWS credentials can still publish
by hand with the same script, after `make tour`.

## Homebrew

`brew install flokkit/tap/apptown` installs the CLI from the tap
repository `github.com/flokkit/homebrew-tap`, which holds one file,
`Formula/apptown.rb`. The formula takes `bin/apptown` from the release
archive for the computer's system and processor, checked against its
sha256, and its test runs `apptown version`.

`images.yml`'s `homebrew` job writes that file on a final release tag
(`vX.Y.Z`, no `-`), once the `archives` job has published the release:
`scripts/homebrew-formula.py` fills `deploy/homebrew/apptown.rb.in` with
the version and each archive's sha256 from the release's own `SHA256SUMS`,
and the job commits it to the tap as `apptown <version>` and pushes it.
Edit the template here, never the tap's copy. A re-run with an unchanged
formula pushes nothing. A release candidate or test tag skips the job, so
the formula only ever names a final release: release candidates are run by
the maintainers from the archive, and a second formula for them would
install the same `apptown` command and conflict with the first.

The push uses an SSH deploy key with write access to the tap alone,
belonging to no person. Its private half is the secret
`HOMEBREW_TAP_DEPLOY_KEY` of this repository's `homebrew-tap` environment,
which only `v*` tags may deploy to, so a workflow run on a branch cannot
read it. GitHub's host key is pinned in the job. Without the secret the job
fails with an error naming it, and the tap is not updated.

One-time setup, by an organisation admin with `gh` signed in:

```sh
gh repo create flokkit/homebrew-tap --public --add-readme \
  --description "Homebrew formulae for Flokkit AppTown"
ssh-keygen -t ed25519 -N '' -C 'apptown release workflow' -f ./homebrew-tap-key
gh repo deploy-key add ./homebrew-tap-key.pub -R flokkit/homebrew-tap \
  --allow-write --title 'apptown release workflow'
gh api -X PUT repos/flokkit/apptown/environments/homebrew-tap \
  -F 'deployment_branch_policy[protected_branches]=false' \
  -F 'deployment_branch_policy[custom_branch_policies]=true'
gh api -X POST repos/flokkit/apptown/environments/homebrew-tap/deployment-branch-policies \
  -f name='v*' -f type=tag
gh secret set HOMEBREW_TAP_DEPLOY_KEY --env homebrew-tap -R flokkit/apptown < ./homebrew-tap-key
rm ./homebrew-tap-key ./homebrew-tap-key.pub
```

To replace the key, make a new pair, add it as a deploy key, set the
secret again and delete the old deploy key from the tap's settings.

## The images

| Image | Built from | Runs |
| --- | --- | --- |
| `ghcr.io/flokkit/apptown` | `deploy/Dockerfile`, target `server` | `apptownd`, with `apptown`, `apptown-linux` and the agent recipe beside it in `/opt/apptown`, as the release archive lays them out |
| `ghcr.io/flokkit/apptown-worker` | `deploy/Dockerfile`, target `worker` | `apptown-worker -agent-context /opt/apptown-worker/deploy/apptown-agent` |
| `ghcr.io/flokkit/apptown-agent` | `deploy/apptown-agent/Dockerfile` | the agent image; workers and AppTown installations still build their own from the recipe |

The server image is an AppTown with no local Docker
(`APPTOWN_NO_LOCAL_DOCKER=true`). An AppTown bind-mounts its own folders and
`bin/apptown-linux` into the containers it starts; from inside a container
those are the container's paths, which the host's Docker does not have. So
the image runs all project work on a worker, needs an external database
(`APPTOWN_DB_URL`), and refuses to start without them. Its data directory is
`/var/lib/apptown`.

The worker image runs work on the host's Docker through the mounted socket.
It mounts its state folder into the containers it starts, so mount that
folder at the same path inside and out and pass it as `-state`; and it
publishes previews on the host's loopback and dials `127.0.0.1` to reach
them, so run it on the host's network (`--network host`, Linux). The
header of `deploy/Dockerfile` has a `docker run` line for each.

Both run as root, as the systemd units do. Base images are pinned by
digest. The Go and web stages run on the build machine's platform and
cross-compile.
