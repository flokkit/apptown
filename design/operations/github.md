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

**`images.yml`** runs on a tag `v*` only. One line, `PUBLISH`, decides
the mode; it follows the repository's visibility, and setting it to `true`
or `false` overrides that.

- While private, each image is built for linux/amd64 and not pushed:
  nothing is stored in GHCR. The run proves the images build at the tag.
- When public, each image is built natively on amd64 and arm64 runners
  (arm64 runners are free for public repositories), pushed by digest with
  BuildKit's provenance (`mode=max`) and SBOM attached, joined into one tag
  per version (`1.0.0`; also `1.0` and `latest` for a release that is not a
  pre-release), and signed keyless with cosign. GitHub's own artifact
  attestations need Enterprise Cloud for a private repository, so the
  provenance and SBOM are BuildKit's, stored with the image. Signing waits
  for public because a keyless signature is recorded in Sigstore's public
  transparency log with the repository's name.

GHCR packages start private, also for a public repository. For anonymous
pulls, make each package public in its package settings on GitHub. The
public mode has not run yet; the first tag after the repository goes public
is its first run, and a failure there can be retried by re-running the
tag's workflow.

## The images

| Image | Built from | Runs |
| --- | --- | --- |
| `ghcr.io/flokkit/apptown` | `deploy/Dockerfile`, target `server` | `forged`, with `forge`, `forge-linux` and the agent recipe beside it in `/opt/forge`, as the release archive lays them out |
| `ghcr.io/flokkit/apptown-worker` | `deploy/Dockerfile`, target `worker` | `forge-worker -agent-context /opt/forge-worker/deploy/forge-agent` |
| `ghcr.io/flokkit/apptown-agent` | `deploy/forge-agent/Dockerfile` | the agent image; workers and forges still build their own from the recipe |

The server image is a forge with no local Docker
(`FORGE_NO_LOCAL_DOCKER=true`). A forge bind-mounts its own folders and
`bin/forge-linux` into the containers it starts; from inside a container
those are the container's paths, which the host's Docker does not have. So
the image runs all project work on a worker, needs an external database
(`FORGE_DB_URL`), and refuses to start without them. Its data directory is
`/var/lib/forge`.

The worker image runs work on the host's Docker through the mounted socket.
It mounts its state folder into the containers it starts, so mount that
folder at the same path inside and out and pass it as `-state`; and it
publishes previews on the host's loopback and dials `127.0.0.1` to reach
them, so run it on the host's network (`--network host`, Linux). The
header of `deploy/Dockerfile` has a `docker run` line for each.

Both run as root, as the systemd units do. Base images are pinned by
digest. The Go and web stages run on the build machine's platform and
cross-compile.
