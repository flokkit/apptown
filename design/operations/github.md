# GitHub: CI and images

The repository, `github.com/flokkit/apptown`, is where AppTown's code lives
and changes. GitHub Actions tests every pull request and every commit on
`main`, and builds the container images on a release tag. The workflows
are in `.github/workflows/`.

## How main changes

`main` changes only by pull request, one per change, squash-merged. Its
ruleset requires the pull request, allows only squash merges, forbids force
pushes and deletion, and requires the `ci-ok` check (`ci.yml`) and the
`heavy-ok` check (`heavy.yml`) on a branch that is up to date with `main`,
so the runs that passed tested exactly what `main` becomes.
The squash commit takes the pull request's title and description, which are
the change's public record. Opening a pull request is limited to the
repository's collaborators ([CONTRIBUTING.md](../../CONTRIBUTING.md)).

## Dependency updates

The hosted Renovate GitHub App proposes dependency updates, configured by
`renovate.json5` at the root. It reads Go modules, the npm packages of `web/`
and of the starters, the starters' composer packages, the Actions in
`.github/workflows/` and the bases of every Dockerfile. It skips `testdata/`
and `eval/`. Versions written elsewhere (Go constants, `deploy/tool-pins.txt`,
`stock/scanners.yaml`, `stock/toolchains/`) are not read by it yet and are
updated by hand.

Nothing opens by itself. Renovate keeps one issue, the **Dependency
Dashboard**, listing the available updates, grouped so that one pull request
is one thing to verify: Go modules, `web/` npm, each
starter (its packages and its Dockerfile's bases together), Actions,
AppTown's own Dockerfile bases, and a new Go line (`go.mod` and the `golang`
images). Each major version is its own entry, except that packages released
together (React with React DOM) move together.

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
A bump that fails a check stays open for a person to finish (a coupled pin
such as `deploy/tool-pins.txt`) or close.

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
- `checks`: gofmt and generated code, schema ownership, vet, staticcheck,
  the lint rules and their fixtures, the shape rules and the scripts' tests,
  exec, sqlnow, reachability, the heavy runner's test, the tool and base
  image pins, the npm install-script rule, govulncheck and the
  gate runner's own test. The pinned analyzers are cached by the Makefile.
- `test`, four parts: the root package's two halves, the rest of
  `internal/apptown`, and every other package. The database tests use the
  harness's own disposable PostgreSQL container (`internal/dbtest`), as a
  local `make check` does.
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

**Caches.** Main publishes Go module and compilation caches; pull requests
restore them without publishing. Modules (`go-mod-*`) use dependency hashes.
Compilation keys include platform, architecture, dependencies and commit; each
successful main job publishes its current compilation cache, and later runs
restore the newest compatible prefix. Container-tier keys also include the
runner Dockerfile and tier. Each compilation archive retains at most 256 MiB
of its newest files (`scripts/ci-cache-trim.py`); older generations compete for
the repository's Actions cache budget and may be evicted. Eviction causes
recompilation, never omission of tests. Tests run with `-count=1`.

The container tiers restore the same host module path as the producer and mount
it with their compilation cache into the Linux runner. `CI_HEAVY_GOMOD` and
`CI_HEAVY_GOCACHE` select absolute host paths; local runs default to the existing
named Docker volumes. The runner restores host ownership before archiving.
Neither database state nor Docker daemon state is cached. Hosted tiers build and load both current-checkout images with Buildx, restoring
GitHub Actions layer caches in separate runner and agent scopes. Only main's
integration-root job exports those rolling scopes. The harness's explicit
prebuilt mode requires both images to exist; local runs still build by default.
Layer-cache eviction causes a rebuild, never omission of tests.

The analyzers (`go-tools-*`) use the Makefile hash. setup-node's npm cache uses
the lockfile; a pull request that changes it can save its own copy. Release
builds (`images.yml`) use no Actions cache, since a tag's cache could serve only
that tag.

**`heavy.yml`** runs the Docker integration tier, the journeys, the opt-in
browser, pi and inference_docker tests (each must pass; a skip fails the
job) and the race detector ([testing](../architecture/testing.md)). It runs
on every pull request to `main`, once for each commit pushed to it (a newer
commit's run cancels the older one), on every push to `main`, so each
merged commit gets its own result, and by hand on any branch or tag. No
label changes what runs: the repository's `heavy` label has no effect, and
is deleted. Each job first pulls the images its tests start, listed in
`deploy/ci-heavy/test-images.txt` (`scripts/ci-heavy.sh pull`, three
attempts each), so a registry error while fetching them fails that step,
naming the image, and not a test. A test that starts a new image adds it to
the list. Tests can still reach a registry: AppTown's own deliberate pulls
(a selected image, an image preview, a Portainer stack), BuildKit resolving
a Dockerfile's FROM tag, and private Docker daemons inside a test pulling
their own images; a registry outage there still fails that test. Its last
job, `heavy-ok`, passes only when `web`, every `tier` entry and `race`
succeeded, and the ruleset requires it, so a red heavy run blocks merging.
As with `ci-ok`, a job added to `heavy.yml` is added to its `needs:` list.
Race uses four package partitions: the root package, backup/data protection,
workflow execution, and the rest. Integration uses four: root, dependencies,
worker, and the rest. `scripts/ci-test-partition.sh` lists packages with the
execution build tags and assigns every package to exactly one partition; new
packages default to the rest. Every matrix entry must succeed. Local
`make check-race` still runs the entire race suite.

**`images.yml`** runs on a tag `v*` only. Its first job, `heavy-evidence`
(`scripts/heavy-evidence.py`), asks the API for a `heavy.yml` run (on
`main` or by hand) on the tagged commit that passed on its first attempt,
and builds nothing without one: CI must pass, not pass eventually. A run
that failed and then passed on a re-run is not evidence, nor is a red,
missing or unreadable result. A re-run of failed jobs keeps the green jobs'
results and replaces the failure inside the same run; a new run by hand is
a complete run of its own, with the red run left beside it. So when heavy
failed, fix the cause in a new commit and tag that, or, when the failure
was not the code's (a lost runner, a registry outage), start a new run by
hand on the commit (Actions, heavy, Run workflow; not "Re-run"). Then
re-run `images.yml`. The job's log lists the red runs on the commit beside
the one it accepts. Every tag pushes each image to
GHCR by digest with BuildKit's provenance (`mode=max`) and SBOM attached,
joined into one tag per version (`1.0.0`; also `1.0` and `latest` for a
release that is not a pre-release). Each image is built natively on amd64
and arm64 runners; arm64 runners are free for public repositories and
billed by the minute for private ones, within the organisation's Actions
budget. The repository's visibility decides the rest:

- While private, the images go to private packages. Private package storage
  and downloads beyond GitHub's free allowance are billed to the
  organisation, capped by its Packages budget. Pulling one needs `docker
  login ghcr.io` with a token that can read packages. Nothing is signed.
- When public, each image is signed keyless with cosign. GitHub's own
  artifact attestations need Enterprise Cloud for a private repository, so
  the provenance and SBOM are BuildKit's, stored with the image. Signing
  waits for public because a keyless signature is recorded in Sigstore's
  public transparency log with the repository's name.

GHCR packages start private, also for a public repository. For anonymous
pulls, make each package public in its package settings on GitHub. A
failed run can be retried by re-running the tag's workflow.

After every image is merged, the `tour` job makes the UI tour from the tag's
source: `make tour` in the ci-heavy image (`scripts/ci-heavy.sh tour`). Its
screenshots and page are the run's `tour` artifact, kept 30 days. The `site`
job needs it: it downloads the artifact into `site/tour` and publishes
apptown.dev (`site/publish.sh <version>`): the landing page, the tour, and
the `try` script, which starts the version just published. A failed tour
publishes nothing, and `publish.sh` refuses a tour without every screenshot.
A test tag (`-test.N`) runs the tour and skips the site, so the public
install command never names a test build; its artifact is how to look at a
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
