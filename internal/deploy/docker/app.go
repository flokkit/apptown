package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/strs"
)

// stagedBuildContext stages tracked worktree edits without ignored or untracked
// files. Already materialized source trees have no .git and are used directly.
// A Git or archive failure must never substitute the live worktree.
//
// The index is refreshed first: a folder copied to another path or machine
// (a worker's copy of the change folder) keeps index stat data that no
// longer matches its files, and on such a clean tree `git stash create`
// exits 1 without output instead of printing nothing.
func StagedBuildContext(ctx context.Context, ws, buildContext string) (string, func(), error) {
	if !gitexec.IsRepo(ws) {
		return filepath.Join(ws, buildContext), func() {}, nil
	}
	if _, err := gitexec.Output(ctx, ws, "update-index", "-q", "--refresh"); err != nil {
		return "", nil, fmt.Errorf("refreshing the index before selecting the build source: %w", err)
	}
	ref, err := gitexec.Output(ctx, ws, "stash", "create")
	if err != nil {
		return "", nil, fmt.Errorf("selecting tracked build source: %w", err)
	}
	if ref == "" {
		ref = "HEAD"
	}
	tree, cleanup, err := (deploy.SelectedSource{Repository: ws, Revision: ref}).Stage(ctx, "")
	staged := tree.Directory
	if err != nil {
		return "", nil, err
	}
	return filepath.Join(staged, buildContext), cleanup, nil
}

func (h *Host) StartAppContainer(ctx context.Context, t BootRequest, c *appcontract.Part, network string, secretEnv map[string]string) (int, error) {
	dk, name, ws, hostPort, persistent := t.Docker, t.Name, t.Workspace, t.HostPort, t.Persistent
	// A static part's browser values are read before anything is replaced:
	// an attachment that cannot be read fails the boot while the running
	// container still serves.
	var browser string
	if c.IsStatic() {
		if t.browser != nil {
			browser = *t.browser // Boot read them before touching the stack
		} else {
			var err error
			if browser, err = h.browserEnv(ctx); err != nil {
				return 0, fmt.Errorf("reading the values a browser may be given: %w", err)
			}
		}
	}
	dk.Remove(ctx, name)
	// Build only an explicit build declaration. An image remains the selected
	// recipe even when unrelated Dockerfiles exist in the source tree.
	if t.Identity == nil && c.Build != "" && c.Image == "" {
		tag := t.names().BuildTag(name)
		buildDir, cleanup, err := StagedBuildContext(ctx, ws, c.Build)
		if err != nil {
			return 0, err
		}
		defer cleanup()
		if out, err := buildPreviewImage(ctx, dk, tag, buildDir); err != nil {
			return 0, BuildFailure("building the app image from the tree's Dockerfile", out, err)
		}
		c.Image, c.Build = tag, ""
	}
	// A declared static build is built and packed here for a change's
	// preview, as a build: part is just above; a standing deployment's was
	// packed by PrepareImages. Either way it then runs as an image.
	if t.Identity == nil && c.StaticBuild != nil && c.Image == "" {
		tag := t.names().BuildTag(name)
		buildDir, cleanup, err := StagedBuildContext(ctx, ws, c.StaticBuild.Context)
		if err != nil {
			return 0, err
		}
		defer cleanup()
		before := dk.imageID(ctx, tag)
		if err := PackStaticBuild(ctx, dk, buildDir, *c.StaticBuild, h.staticServer(), c.Port, tag); err != nil {
			return 0, err
		}
		removeReplacedImage(ctx, dk, tag, before)
		c.Image, c.StaticBuild, c.ServesStatic = tag, nil, true
	}
	// Loopback on AppTown's own daemon: nothing legitimate reaches an app
	// there on its raw host port. A browser on another device arrives via
	// Traefik → apptownd's in-process proxy (vhost.go), which reaches the
	// container over 127.0.0.1 because apptownd is co-located with it. A
	// 0.0.0.0 bind there only opens a LAN bypass AROUND the proxy's header
	// sanitization — a client could hit the backend directly with a apptownd
	// X-Forwarded-Proto/Host, spoofing the very front-door signal the health
	// render trusts (design/subsystems/ingress.md, trust boundary). An
	// attached host is not co-located with AppTown: its port is published
	// where AppTown's probe and the plain front door reach it
	// (Scope.PublishAddress).
	bind := dk.PublishAddress()
	portSpec := fmt.Sprintf("%s::%d", bind, c.Port)
	if hostPort > 0 {
		portSpec = fmt.Sprintf("%s:%d:%d", bind, hostPort, c.Port)
	}
	// A sealed boot (a restore rehearsal) publishes no port and joins no
	// ingress: nothing outside its host reaches it (sealed.go).
	sealed := t.Sealed != nil
	// An app on an attached host behind its Traefik is created, joined to
	// the traefik network and only then started: Traefik reads a container
	// when it starts and does not see a network joined later, so a container
	// that joined after starting is routed to an address Traefik cannot
	// reach. Only main is routed there (RoutesDeployment).
	routed := dk.Remote() && dk.RoutesDeployment(t.Deployment) && !sealed
	args := []string{"run", "-d", "--name", name}
	if routed {
		args = []string{"create", "--name", name}
	}
	// class: a preview is ephemeral and reapable; a deployed env is
	// persistent and NEVER reaped (design/subsystems/container-lifecycle.md).
	appClass := "preview"
	if persistent {
		appClass = "env"
	}
	args = append(args, dk.Labels(appClass, name)...)
	args = append(args, ResourceArgs(c.Resources)...)
	args = append(args, execution.LogFlags()...)
	if !persistent && t.StackID != "" {
		args = append(args, "--label", StackLabel+"="+t.StackID)
	}
	if t.Identity != nil {
		for label, value := range t.Identity.Labels() {
			args = append(args, "--label", label+"="+value)
		}
	}
	// The source the container serves, on the container itself: the reuse
	// check asks the container, never the workspace.
	args = append(args, "--label", dockerlabel.Source+"="+WorktreeFingerprint(ws))
	if t.Incarnation != "" {
		args = append(args, "--label", dockerlabel.Incarnation+"="+t.Incarnation)
	}
	// Deployed deployments survive daemon/host restarts on their own;
	// previews stay ephemeral. Learned twice in one day: env containers died
	// silently with the host at 10:44 and with a Docker Desktop crash at 19:20.
	if persistent {
		args = append(args, "--restart", "unless-stopped")
	}
	if c.Static != "" {
		// Client-only site: mount the served directory read-only into the
		// pinned file server. No build, no toolchain, no ambient env, no
		// data volume — subtraction is the feature.
		root := filepath.Join(ws, filepath.Clean(c.Static))
		args = append(args, "-v", root+":/srv:ro")
	} else if c.Image != "" {
		// Image contract: the app is self-contained — no workspace mount, no
		// toolchain layer, no build. Pull first (with the project's registry
		// pull credential) so failures are attributable. Local preview
		// builds (<prefix>preview-build-*) exist only on this daemon — never
		// pulled — and a release's recorded images were prepared already
		// (PrepareImages).
		if t.Identity == nil && t.Recorded == nil && !IsLocalBuildTag(c.Image) {
			h.login(ctx, dk, c.Image)
			if out, err := dk.Run(ctx, "pull", c.Image); err != nil {
				return 0, fmt.Errorf("docker pull %s: %s", c.Image, strs.Truncate(string(out), 300))
			}
		}
	} else {
		return 0, fmt.Errorf("app requires an explicit build, image, or static source")
	}
	if c.DataDir != "" {
		if t.own.Record != nil {
			if err := ensureVolume(ctx, dk, t.names().DataVolume(name), name, "", t.own); err != nil {
				return 0, err
			}
		}
		args = append(args, "-v", t.names().DataVolume(name)+":"+c.DataDir)
		// The volume belongs to the image's own user before the app starts,
		// so a container that drops to an unprivileged USER can write its
		// data without a root entrypoint to chown it first. An image with no USER, or one
		// AppTown cannot resolve, is left as it was.
		if c.Image != "" {
			HandVolumeToImageUser(ctx, dk, t.names().DataVolume(name), c.Image, h.chownImage())
		}
	}
	args = append(args, "--add-host", "host.internal:host-gateway")
	if !sealed {
		args = append(args, "-p", portSpec)
	}
	args = append(args, "-e", fmt.Sprintf("PORT=%d", c.Port))
	if routed {
		// The attached host's own Traefik routes <slug>.<domain> to this
		// container, main's (design/subsystems/ingress.md): the labels are
		// the contract, entrypoint websecure, resolver letsencrypt, and the
		// container joins the network named traefik before it starts, which
		// Traefik is told to use when the container is on several.
		slugName := strings.TrimSuffix(strings.TrimPrefix(name, string(t.names())+"env-"), "-"+t.Deployment)
		router := string(t.names()) + slugName
		args = append(args,
			"-l", "traefik.enable=true",
			"-l", "traefik.docker.network=traefik",
			"-l", fmt.Sprintf("traefik.http.routers.%s.rule=Host(`%s.%s`)", router, slugName, dk.Domain),
			"-l", fmt.Sprintf("traefik.http.routers.%s.entrypoints=websecure", router),
			"-l", fmt.Sprintf("traefik.http.routers.%s.tls.certresolver=letsencrypt", router),
			"-l", fmt.Sprintf("traefik.http.services.%s.loadbalancer.server.port=%d", router, c.Port),
		)
	}
	// Where the app is reached from a browser (every tier, as wildcards):
	// frameworks trust their front door from this instead of a hand-typed
	// hostname that is wrong in the next deployment.
	if o := appOrigins(h.origins(ctx), dk, routed); o != "" {
		args = append(args, "-e", "APPTOWN_ORIGINS="+o)
	}
	// Attached-integration runtime contributions: the errors DSN plus every attached
	// service kind's rendered env. Opt-in by integration — unattached
	// projects get nothing.
	for _, kv := range h.contributions(ctx) {
		args = append(args, "-e", kv)
	}
	if network != "" {
		args = append(args, "--network", network)
	}
	for k, v := range c.Env {
		args = append(args, "-e", k+"="+v)
	}
	// Apply resolved secret environment values after the contract environment so
	// a committed placeholder cannot shadow the bound value. Receipts use names.
	for k, v := range secretEnv {
		args = append(args, "-e", k+"="+v)
	}
	// The volume's location is deploy topology, not app preference: injected
	// LAST so a stray DATA_DIR in the contract env cannot point the app away
	// from its persistent mount (docker: last -e wins).
	if c.DataDir != "" {
		args = append(args, "-e", "DATA_DIR="+c.DataDir)
	}
	// A static part's browser values, for /_apptown/env.js: set last, so no
	// contract or secret value can stand in for them (docker: the last -e
	// wins), and only for a static site, so a server app never depends on
	// them. They were read before the old container was removed.
	if c.IsStatic() {
		args = append(args, "-e", BrowserEnvVar+"="+browser)
		// Its release identity, answered at ReleasePath, set last for the
		// same reason: what the boot witnesses is AppTown's alone.
		if t.release != "" {
			args = append(args, "-e", appcontract.ReleaseEnvVar+"="+t.release)
		}
	}
	if c.Static != "" {
		args = append(args, h.staticServer())
		args = append(args, StaticServerCommand(c.Port)...)
	} else if c.Image != "" {
		args = append(args, c.Image)
		args = append(args, c.Command...)
	}
	out, err := dk.Run(ctx, args...)
	if err != nil && StaleEndpoint(string(out)) {
		// The previous container is gone but the daemon still holds its
		// network endpoint under this name (seen after disk-quota errors).
		// The failed run leaves a Created container under the name; remove
		// THAT first, or disconnect resolves the name to it and the ghost
		// endpoint survives. Then release the ghost by name and start once
		// more; a second failure is reported as itself.
		_, _ = dk.Run(ctx, "rm", "-f", name)
		_, _ = dk.Run(ctx, "network", "disconnect", "-f", strs.FirstNonBlank(network, "bridge"), name)
		out, err = dk.Run(ctx, args...)
	}
	if err != nil {
		return 0, fmt.Errorf("docker run: %s", strs.Tail(strings.TrimSpace(dk.redacted(out)), startFailureTail))
	}
	if t.own.Record != nil {
		if err := t.own.created(deploy.Owned{Kind: "container", ID: createdID(ctx, dk, out, name), Name: name}); err != nil {
			return 0, err
		}
	}
	if routed {
		// Best effort: the host's Traefik network is the owner's to make;
		// without it the container is still reachable by address and port.
		_, _ = dk.Run(ctx, "network", "connect", "traefik", name)
		if out, err := dk.Run(ctx, "start", name); err != nil {
			return 0, fmt.Errorf("docker start: %s", strs.Tail(strings.TrimSpace(dk.redacted(out)), startFailureTail))
		}
	}
	if sealed {
		return 0, nil // no host port: reached on its own network only
	}
	// resolve the mapped host port
	got, err := ContainerHostPort(ctx, dk, name, c.Port)
	if err != nil {
		return 0, err
	}
	if got == 0 {
		return 0, fmt.Errorf("could not resolve host port for %q", name)
	}
	return got, nil
}

// ResourceArgs are the docker run flags that hold a part to its declared
// resources: memory with swap at the same value (the declared figure is
// swap included), and CPUs. A part that declares nothing gets none: no
// limit, as declared.
func ResourceArgs(r appcontract.Resources) []string {
	var args []string
	if r.MemoryBytes > 0 {
		m := strconv.FormatInt(r.MemoryBytes, 10)
		args = append(args, "--memory", m, "--memory-swap", m)
	}
	if r.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(r.CPUs, 'f', -1, 64))
	}
	return args
}

// Probe owns the beat between an app's first answer and the second
// that proves it stayed up. A nil prober retains the three-second production
// default; a test can configure its own instance without changing other apps.
type Probe struct{ Settle time.Duration } // Settle 0 is the three-second production default

// check answers "is the app actually up?": an HTTP response from the app
// itself, still alive after a settle window. A raw TCP dial is NOT evidence —
// Docker Desktop's port proxy accepts connections whether or not anything
// inside the container listens, so a dead app would pass such a health gate
// in one second. On failure the container's
// log tail is returned so the crash reaches ${{feedback.latest}} as rework
// feedback instead of dying unobserved.
func (p *Probe) Check(ctx context.Context, dk Scope, container, url string, timeout time.Duration, probe RenderedProbe) (bool, string) {
	return p.check(ctx, dk, container, url, timeout, probe, poll.Wait)
}

// check is Check with its waits supplied: the settle beat and the retry
// cadence both go through wait, which production gives poll.Wait. A test
// that reads only what an answer is judged to be can return at once.
func (p *Probe) check(ctx context.Context, dk Scope, container, url string, timeout time.Duration, probe RenderedProbe, wait func(context.Context, time.Duration) error) (bool, string) {
	// Do NOT auto-follow redirects: a behind-proxy-hardened app 301s a naked hit
	// to https://, and the default client would follow it into a TLS handshake
	// against a plain-HTTP listener, error, and read the boot as dead — the
	// office-poll ten-commit thrash (design/subsystems/healthchecks.md §3). The render's
	// front-door headers stop the 301 in the first place; refusing to follow
	// makes any stray 3xx a visible status, not a masked failure.
	client := &http.Client{
		Transport:     check.ProbeTransport,
		Timeout:       2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	// startupGrace is a FLOOR on how long boot may take before failures count
	// (design §5.3): extend the deadline to it, never shrink below the caller's.
	if probe.Grace > timeout {
		timeout = probe.Grace
	}
	target := strings.TrimRight(url, "/") + "/" + strings.TrimLeft(probe.Path, "/")
	settle := 3 * time.Second
	if p != nil && p.Settle != 0 {
		settle = p.Settle
	}
	answers := func() bool {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			return false
		}
		for k, v := range probe.Headers {
			req.Header.Set(k, v) // the ingress contract's front-door signal (ingress.go)
		}
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer drainAndClose(resp.Body)
		if probe.Body != "" {
			body, err := io.ReadAll(io.LimitReader(resp.Body, int64(len(probe.Body))+1))
			if err != nil || string(body) != probe.Body {
				return false // a witness answers exactly what it was told to
			}
		}
		if probe.Expect != 0 {
			return resp.StatusCode == probe.Expect // a declared status must match exactly
		}
		return true // any HTTP status is proof something in the app is serving
	}
	running := func() bool { return dk.Running(ctx, container) }
	logTail := func() string { return containerLogTail(ctx, dk, container) }
	// The boot's lifetime ends a wait early: the boot is over, and so is
	// the question.
	ctx = dk.lifetime(ctx)
	// The timeout is a time left, so it is a timer's, not a deadline read
	// on a clock.
	budget, spent := context.WithTimeout(ctx, timeout)
	defer spent()
	for budget.Err() == nil {
		if !running() {
			return false, logTail() // crashed during boot — fail fast with the evidence
		}
		if answers() {
			// settle: a from-source app can answer once and then die (or a
			// sidecar proxy can answer for it) — require alive-and-answering
			// again after a beat before calling it up.
			if wait(ctx, settle) != nil {
				break
			}
			if running() && answers() {
				return true, ""
			}
			continue
		}
		if wait(ctx, time.Second) != nil {
			break
		}
	}
	return false, logTail()
}

// drainAndClose reads what is left of a probe's answer (up to 64 KiB) before
// closing it, so the client can use the connection for the next check: an
// answer closed unread costs a new connection each time, and every closed
// connection holds a local port for 30 s.
func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64<<10))
	_ = body.Close()
}

// startFailureTail bounds what a failed container start reports: the end of
// the daemon's answer, where its error is, at the size of a container's last
// words (containerLogTail).
const startFailureTail = 2000

// containerLogTail is a container's last words, for a failure's evidence.
func containerLogTail(ctx context.Context, dk Scope, container string) string {
	logs, _ := dk.Run(ctx, "logs", "--tail", "40", container)
	return strs.Truncate(dk.redacted(logs), 2000)
}

// WorktreeFingerprint is a print of what a boot would bake, so a container
// can say what it serves: every source file's path, size and mtime (a
// content edit bumps mtime, so a second edit to an already-dirty file
// changes the print; `git status` alone doesn't see that, so a preview would
// stay stale), plus the contract bytes. Skips runtime and derived dirs.
// Deliberately mtime-based, not a content hash: a spurious mtime bump costs
// one needless reboot — the safe direction to be wrong in.
func WorktreeFingerprint(ws string) string {
	skip := map[string]bool{".git": true, ".apptown": true, ".forge": true, "node_modules": true, "dist": true}
	h := sha256.New()
	_ = filepath.Walk(ws, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr // a vanishing file just changes the print
		}
		rel, _ := filepath.Rel(ws, path)
		if info.IsDir() {
			if skip[info.Name()] && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		fmt.Fprintf(h, "%s %d %d\n", rel, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	// The contract is part of the print — an edit to services/build/env must
	// re-boot the preview. The native contract is part of the selected source.
	for _, name := range []string{"app.yaml"} {
		if b, err := os.ReadFile(filepath.Join(ws, ".apptown", name)); err == nil {
			h.Write(b)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ContainerHostPort is the host port a container's inner port is mapped to,
// asked of `docker port` and parsed from its "0.0.0.0:PORT" tail. inner is
// the container port to ask about (0 = the first mapping). This is the one
// parse of that answer.
func ContainerHostPort(ctx context.Context, dk Scope, name string, inner int) (int, error) {
	args := []string{"port", name}
	if inner > 0 {
		args = append(args, strconv.Itoa(inner))
	}
	out, err := dk.Run(ctx, args...)
	if err != nil {
		return 0, fmt.Errorf("docker port %s: %w", name, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if i := strings.LastIndex(line, ":"); i > 0 {
			if p, e := strconv.Atoi(strings.TrimSpace(line[i+1:])); e == nil && p > 0 {
				return p, nil
			}
		}
	}
	return 0, fmt.Errorf("no host port for %s", name)
}

// StaleEndpoint says whether a run failed because the daemon still holds a
// gone container's network endpoint under the name.
func StaleEndpoint(out string) bool {
	return strings.Contains(out, "endpoint with name") && strings.Contains(out, "already exists in network")
}

// appOrigins is the APPTOWN_ORIGINS an app is given: this AppTown's tiers and,
// for an app routed by an attached host's own Traefik, that host's ingress
// domain, where its visitors actually arrive. Without it an app on such a
// host trusts only the preview tier, and a framework that checks the host
// (Rails) refuses every page at <slug>.<domain>.
func appOrigins(tiers string, dk Scope, routed bool) string {
	if !routed {
		return tiers
	}
	own := "https://*." + dk.Domain
	for _, o := range strings.Split(tiers, ",") {
		if o == own {
			return tiers
		}
	}
	if tiers == "" {
		return own
	}
	return tiers + "," + own
}
