package agentenv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/dockerlabel"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/dind"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/progress"
	"github.com/flokkit/apptown/internal/strs"
)

// Workshop owns AppTown's disposable Docker environment policy. Console sessions
// borrow its daemon options but keep their own persistent container lifecycle.
type Workshop struct {
	docker         *docker.Scope
	integrations   *integration.Service
	progress       *progress.Feeds
	dataDir, image string
	clock          func() time.Time // AppTown's clock; nil is the real one
	// Limits bound a step's private Docker daemon, where its builds run (the
	// AppTown's task limits).
	Limits execution.Limits
}

// now is AppTown's clock, which stamps a workshop container's
// apptown.created label.
func NewWorkshop(scope *docker.Scope, integrations *integration.Service, feeds *progress.Feeds, dataDir, image string, now func() time.Time) *Workshop {
	return &Workshop{docker: scope, integrations: integrations, progress: feeds, dataDir: dataDir, image: image, clock: now}
}

func (w *Workshop) now() time.Time { return clock.Or(w.clock)() }

// The agent's workshop: a private, disposable Docker daemon per
// implementation step. The design bet: agents perform
// better with the real docker CLI/API they were trained on than with any
// bespoke "rehearse my app" button — so give them real Docker and get
// isolation from disposability, not permission-checking. The sidecar
// daemon knows nothing about the host's Docker (engine containers, other
// projects, credentials) and dies with the step.
//
// The mount story is the same-path trick: the workspace is mounted at
// /work in BOTH the step container and the sidecar daemon, so when the
// agent runs `docker run -v /work:/work ...` the path means the same
// thing on both sides. apptown-cache rides along the same way.
//
// Honest caveats, recorded where the code lives:
//   - The sidecar runs --privileged. On Docker Desktop the blast radius is
//     the Desktop VM; on Linux runners use Sysbox/microVMs for a sealed
//     version of the same shape. -no-private-docker turns the feature off.
//   - Each daemon starts with a cold image cache. AppTown-agent image is
//     preloaded from a tar; everything else pulls from the network (or the
//     registry integration, once bound).
//
// Writable change consoles use the same image, DNS and mirror policy with
// a longer session lifecycle; see internal/console/pi. Steps remain disposable.

const DindImage = "docker:27-dind"

const DockerInstructions = `# Your private Docker

This step has its OWN disposable Docker daemon — DOCKER_HOST is already
set, so docker just works: build images, run containers, read logs.
Nothing you run there can see the host's Docker or other projects.

- The workspace is mounted at /work on the daemon too, so
  ` + "`docker run -v /work:/work -w /work ...`" + ` runs against this
  same working tree.
- Published ports appear on the host named dind, NOT on localhost:
  ` + "`docker run -d -p 8080:8080 myapp` then `curl http://dind:8080`" + `.
- The daemon starts empty: images are pulled (Docker Hub pulls go
  through the org mirror when one is bound) or built here.
- ` + "`docker build` steps do NOT inherit this session's env" + ` — pass
  proxies explicitly when a build needs them, e.g.
  ` + "`docker build --build-arg GOPROXY=$GOPROXY ...`" + ` (and read the
  matching ARG in the Dockerfile).
- Multi-arch: a buildx builder named ` + "`apptown`" + ` is preconfigured and
  set as your default (network-host, mirror-aware — DNS works in RUN
  steps, unlike a self-created builder). One command:
  ` + "`docker buildx build --platform linux/amd64,linux/arm64 .`" + `
  Make the Dockerfile cross-compile (FROM --platform=$BUILDPLATFORM +
  TARGETOS/TARGETARCH) so nothing emulates. Do NOT create your own
  builder.
- USE IT to rehearse the deploy before you finish: boot your app the
  way production will (cold container, only the repo and its declared
  toolchains) and hit its endpoints. The engine repeats the same
  rehearsal as a gate afterwards — a crash there loops back to you.
- Don't push images to a registry: this daemon is logged in to none.
  AppTown builds and publishes the release images when the change lands.

`

// startDind launches the sidecar and returns env for the step container
// (DOCKER_HOST), the network the step container must join, and a cleanup.
// If existingNet is non-empty (services already made one) the sidecar joins
// it; otherwise a network is created and owned by the cleanup. The daemon
// and its client are logged in to no registry, on AppTown's host as on a
// worker: release images are built and published by the landing.
// teardownBound bounds a step sandbox's teardown, which outlives its setup.
const teardownBound = docker.TeardownBound

func (w *Workshop) StartStep(ctx context.Context, stepID ids.ID, ws, existingNet, clientConfigDir string) ([]string, string, func(), error) {
	name := fmt.Sprintf("apptown-dind-step-%d", stepID)
	mirror, err := w.Mirror(ctx)
	if err != nil {
		return nil, "", nil, err
	}
	network := existingNet
	ownNet := false
	if network == "" {
		network = name
		// Labelled like the daemon's container, so the periodic reaper
		// finds the network by its owner rather than by its name.
		if out, err := w.docker.Run(ctx, "network", "create", "--label", dockerlabel.Class+"=dind", "--label", dockerlabel.Owner+"="+name,
			"--label", dockerlabel.Created+"="+w.now().UTC().Format(time.RFC3339), network); err != nil {
			return nil, "", nil, fmt.Errorf("network create: %s", strs.Truncate(string(out), 200))
		}
		ownNet = true
	}
	// The teardown runs after setup and the step, though their context has
	// ended (a stopping AppTown), bounded so it cannot hang the step's end.
	cleanup := func() {
		teardown, cancel := context.WithTimeout(context.WithoutCancel(ctx), teardownBound)
		defer cancel()
		_, _ = w.docker.Run(teardown, "rm", "-f", "-v", name)
		if ownNet {
			_, _ = w.docker.Run(teardown, "network", "rm", network)
		}
	}

	args := []string{"run", "-d", "--privileged", "--name", name,
		"--network", network, "--network-alias", "dind",
		"--label", dockerlabel.Class + "=dind", "--label", dockerlabel.Owner + "=" + name, "--label", dockerlabel.Created + "=" + w.now().UTC().Format(time.RFC3339),
		// It mounts the step's folder writable, and so can the containers
		// the agent starts in it: it is one of the folder's writers.
		"--label", docker.WritesLabel + "=" + ws, "--label", fmt.Sprintf(dockerlabel.Step+"=%d", stepID),
		"-e", "DOCKER_TLS_CERTDIR=", // plain tcp://:2375 inside the private network only
		"-v", ws + ":/work",
	}
	// The step's own project cache, at the same path as in the step, so a
	// nested build sees what the step sees; an unscoped step has none.
	if cache := CacheVolume(ctx); cache != "" {
		args = append(args, "-v", cache+":/apptown-cache")
	}
	args = append(args, execution.LimitFlags(w.Limits)...)
	if tar, err := w.agentImageTar(ctx); err == nil {
		args = append(args, "-v", filepath.Dir(tar)+":/preload:ro")
	}
	// Two startup stalls that, together, killed the daemon on a ZFS-backed
	// host ("docker unavailable" mid-step).
	// 1. containerd's zfs snapshotter probes `zfs list`; the ZFS userland
	//    waits ZFS_MODULE_TIMEOUT seconds (default 10) for /dev/zfs, which
	//    a nested daemon on a ZFS-backed host never gets — so it waits the
	//    full ten, and dockerd's own wait for containerd expires meanwhile.
	//    Zero makes the probe fail at once (containerd boots in 36ms,
	//    measured; the plugin is skipped, overlay2 serves as ever).
	// 2. dockerd pauses fifteen seconds on purpose for a plain-TCP listener
	//    unless --tls=false is explicit — so say it; the listener lives on a
	//    private network anyway.
	args = append(args, "-e", "ZFS_MODULE_TIMEOUT=0")
	args = append(args, dind.Start(DindImage, "--tls=false")...)
	// Name resolution for the containers the nested daemon runs (a plain
	// `docker build`, `docker compose build`, a rehearsal container): dind's
	// own resolv.conf points at Docker's embedded DNS (127.0.0.11), which
	// dockerd swaps for 8.8.8.8 inside those containers — so a LAN name like
	// a package mirror on the local network never resolves there. Hand dockerd the host's real resolvers
	// instead; they are reachable from a privileged nested container.
	args = append(args, dind.DaemonOptions(mirror)...)
	if out, err := w.docker.Run(ctx, args...); err != nil {
		cleanup()
		return nil, "", nil, fmt.Errorf("dind start: %s", strs.Truncate(string(out), 300))
	}

	// Block until the daemon answers: an agent whose FIRST docker probe
	// fails spends its next ten turns debugging connectivity instead of
	// working. ~5-10s, bounded.
	// StartStep carries no context yet; the wait is bounded by its timeout.
	up := poll.Until(ctx, 40*time.Second, 2*time.Second, func() bool {
		return w.docker.OK(ctx, "exec", "-e", "DOCKER_HOST=tcp://localhost:2375", name, "docker", "info")
	})
	if !up {
		cleanup()
		return nil, "", nil, fmt.Errorf("dind daemon did not come up")
	}

	// Preload the prod base image in the background — by the time the agent
	// wants it (minutes in), it's loaded.
	go func() {
		if tar, err := w.agentImageTar(ctx); err == nil {
			_, _ = w.docker.Run(ctx, "exec", "-e", "DOCKER_HOST=tcp://localhost:2375", name,
				"docker", "load", "-i", "/preload/"+filepath.Base(tar))
		}
	}()

	// Pre-provision a WORKING buildx builder. Left alone, `buildx create`
	// spawns a buildkit container whose sandboxed build netns inherits
	// dind's resolv.conf pointing at Docker's embedded DNS (127.0.0.11) —
	// unreachable from that netns, so every RUN step's DNS dies (change
	// 100 burned a round on exactly this). network=host puts buildkit in
	// dind's own netns where 127.0.0.11 answers; a buildkitd.toml carries
	// the hub mirror, which buildkit does not read from the daemon. The
	// builder is created under the agent's DOCKER_CONFIG, so
	// plain `docker buildx build` uses it by default; it bootstraps
	// lazily on first use.
	// buildkitd.toml is ALWAYS written: buildkit swaps localhost
	// nameservers for 8.8.8.8 in build sandboxes, so the embedded DNS
	// must be named explicitly (reachable because of network=host). The
	// hub mirror rides along when bound — buildkit ignores the daemon's
	// --registry-mirror.
	// The embedded DNS first (it knows the private network's names, e.g.
	// `dind`), then the host's resolvers for LAN names like the mirror.
	toml := dind.BuildkitConfig(mirror)
	_ = os.WriteFile(filepath.Join(ws, ".apptown", "buildkitd.toml"), []byte(toml), 0o644)
	buildxArgs := []string{"exec",
		"-e", "DOCKER_HOST=tcp://localhost:2375",
		"-e", "DOCKER_CONFIG=" + filepath.Join("/work", clientConfigDir),
		name, "docker", "buildx", "create", "--name", "apptown",
		"--driver", "docker-container", "--driver-opt", "network=host",
		"--config", "/work/.apptown/buildkitd.toml", "--use"}
	if out, err := w.docker.Run(ctx, buildxArgs...); err != nil {
		// Non-fatal: plain docker builds still work; the agent just lacks
		// the preconfigured builder. What it said is the failure's words.
		w.progress.Append(stepID, fmt.Errorf("buildx builder not provisioned: %s", strs.Truncate(string(out), 120)).Error())
	} else if err := dind.TrustBuilder(ctx, w.docker.Run, name, filepath.Join("/work", clientConfigDir)); err != nil {
		w.progress.Append(stepID, strs.Truncate(err.Error(), 200))
	}

	return []string{"DOCKER_HOST=tcp://dind:2375"}, network, cleanup, nil
}

// Mirror returns the bound docker-hub mirror URL, or "" when none is bound.
// Stored on the registry-format binding docker-hub (same registry-binding
// family as the npm/go proxies). A binding that cannot be read is an error:
// pulls would go straight to Docker Hub.
func (w *Workshop) Mirror(ctx context.Context) (string, error) {
	if w == nil {
		return "", nil
	}
	cfg, err := w.integrations.ConnectorConfig(ctx, "docker-hub", "registry")
	if err != nil {
		return "", fmt.Errorf("reading the Docker Hub mirror: %w", err)
	}
	u, _ := cfg["url"].(string)
	return u, nil
}

// agentImageTar caches `docker save apptown-agent` under _data/cache, keyed
// by image ID so a rebuilt image invalidates the tar. Stale tars are
// removed. Returns the tar's absolute path.
func (w *Workshop) agentImageTar(ctx context.Context) (string, error) {
	out, err := w.docker.Run(ctx, "image", "inspect", "-f", "{{.Id}}", w.image)
	if err != nil {
		return "", fmt.Errorf("agent image not present: %w", err)
	}
	id := strings.TrimPrefix(strings.TrimSpace(string(out)), "sha256:")
	if len(id) > 12 {
		id = id[:12]
	}
	dir := filepath.Join(w.dataDir, "cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tar := filepath.Join(dir, "agent-image-"+id+".tar")
	if _, err := os.Stat(tar); err == nil {
		return tar, nil
	}
	stale, _ := filepath.Glob(filepath.Join(dir, "agent-image-*.tar"))
	tmp := tar + ".tmp"
	if out, err := w.docker.Run(ctx, "save", "-o", tmp, w.image); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("docker save: %s", strs.Truncate(string(out), 200))
	}
	if err := os.Rename(tmp, tar); err != nil {
		return "", err
	}
	for _, s := range stale {
		_ = os.Remove(s)
	}
	return tar, nil
}

// reapDindOrphans removes sidecars (and their networks) left behind by a
// crashed apptownd — same safety-net shape as run-container reaping.
func ReapWorkshops(ctx context.Context, dk docker.Scope) {
	prefix := string(docker.Current) + "dind-step-"
	out, _ := dk.Run(ctx, "ps", "-aq", "--filter", "name="+prefix)
	for _, id := range strings.Fields(string(out)) {
		_, _ = dk.Run(ctx, "rm", "-f", "-v", id)
	}
	nets, _ := dk.Run(ctx, "network", "ls", "-q", "--filter", "name="+prefix)
	for _, id := range strings.Fields(string(nets)) {
		_, _ = dk.Run(ctx, "network", "rm", id)
	}
}
