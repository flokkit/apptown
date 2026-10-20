# GitHub: CI and images

The public repository, `github.com/flokkit/apptown`, tests every push and
pull request with GitHub Actions and builds the container images on a
release tag. The workflows are in `.github/workflows/`. The repository is
exported from the maintainers' development repository, one commit per
export, so its history starts fresh.

## GitHub Actions

The repository is on GitHub's Free plan while private: about 2,000 Actions
minutes a month on 2-core runners and 500 MB of package storage.

**`ci.yml`** runs on a push to main, on pull requests and by hand; a newer
run cancels an older one on the same ref, and a push that changes only
`design/`, `releases/`, root Markdown or `.claude/` runs nothing. Jobs:

- `web`: npm ci, typecheck, Vite build, lint and unit tests; it passes
  `web/dist` to the Go jobs, since Go embeds it.
- `checks`: gofmt and generated code, schema ownership, vet, staticcheck,
  the lint rules and their fixtures, the shape rules and the scripts' tests,
  exec, sqlnow, reachability, govulncheck and the gate
  runner's own test. The pinned analyzers are cached by the Makefile.
- `test`, four parts: the root package's two halves, the rest of
  `internal/forge`, and every other package. The database tests use the
  harness's own disposable PostgreSQL container (`internal/dbtest`), as a
  local `make check` does.

Not on GitHub: the Docker integration, journey and opt-in tiers, the race
detector and the semgrep SQL rule. Run them locally
([testing](../architecture/testing.md) has each tier's command).

**`images.yml`** runs on a tag `v*` only. Every tag pushes each image to
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
| `ghcr.io/flokkit/apptown-agent` | `deploy/apptown-agent/Dockerfile` | the agent image; workers and forges still build their own from the recipe |

The server image is a forge with no local Docker
(`APPTOWN_NO_LOCAL_DOCKER=true`). A forge bind-mounts its own folders and
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
