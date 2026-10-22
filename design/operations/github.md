# GitHub: CI and images

The repository, `github.com/flokkit/apptown`, is where AppTown's code lives
and changes. GitHub Actions tests every pull request and every commit on
`main`, and builds the container images on a release tag. The workflows
are in `.github/workflows/`.

## How main changes

`main` changes only by pull request, one per change, squash-merged. Its
ruleset requires the pull request, allows only squash merges, forbids force
pushes and deletion, and requires the `ci-ok` check on a branch that is up to
date with `main`, so the run that passed tested exactly what `main` becomes.
The squash commit takes the pull request's title and description, which are
the change's public record. Opening a pull request is limited to the
repository's collaborators ([CONTRIBUTING.md](../../CONTRIBUTING.md)).

## GitHub Actions

The repository is public, so standard runners cost nothing.

**`ci.yml`** runs on every pull request, a documentation-only one too, and by
hand. A newer run cancels an older one on the same ref. Jobs:

- `web`: npm ci, typecheck, Vite build, lint and unit tests; it passes
  `web/dist` to the Go jobs, since Go embeds it.
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
  or skipped job fails it. It is the one required check, so its `needs:`
  list names every job, and a job added to `ci.yml` is added there too.

**`heavy.yml`** runs the Docker integration tier, the journeys, the opt-in
browser, pi and inference_docker tests (each must pass; a skip fails the
job) and the race detector ([testing](../architecture/testing.md)). It runs
on every push to `main`, so each merged commit gets its own result, on a
pull request that carries the `heavy` label, and by hand on any branch or
tag. It is not required for merging: a red run on `main` is fixed by the
next change. The heavy tiers take from a few minutes (journeys) to about
an hour (integration, at its limit).

**`images.yml`** runs on a tag `v*` only. Its first job, `heavy-evidence`,
asks the API for a successful `heavy.yml` run (on `main` or by hand) on the
tagged commit and builds nothing without one: a red, missing or unreadable
result is not evidence. Run `heavy.yml` on the tag by hand when its `main`
run is missing, then re-run the workflow. Every tag pushes each image to
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

After every image is merged, the `site` job publishes apptown.dev
(`site/publish.sh <version>`): the landing page and the `try` script, which
starts the version just published. A test tag (`-test.N`) skips it, so the
public install command never names a test build. The job signs in to AWS with
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
by hand with the same script.

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
