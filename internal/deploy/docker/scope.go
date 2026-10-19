package docker

import (
	"context"
	"errors"
	"flokkit.io/forge/internal/dockerlabel"
	"fmt"
	"log"
	"strings"
	"time"

	"flokkit.io/forge/internal/dind"
	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/internal/strs"
)

// Scope is which Docker daemon a command talks to: the forge's own
// (Host == "") or an attached host's, reached over SSH by the alias the
// forge keeps for it (target_attach.go). The environment path carries one
// from the driver down through boot, parts, services and probes, so a
// standing deployment on a droplet is booted exactly like one here; every
// other caller receives the local scope assembled in App.New.
type Scope struct {
	Client   Client
	Host     string // DOCKER_HOST, "" for the forge's own daemon
	Hostname string // the attached host's address, for probes and plain URLs
	Domain   string // the attached host's ingress domain, "" when it has none
	// Config is a Docker configuration folder of this scope's own, for one
	// boot (WithBootConfig); "" is the ambient one.
	Config string
	// Lifetime bounds every command through this scope: when it ends, each
	// command still running is stopped (a boot's lifetime, WithLifetime). nil
	// is no bound beyond each command's own.
	Lifetime context.Context
	// Now is the clock a container's forge.created label is stamped from,
	// which the reaper judges a container's age by; nil is the real one.
	Now func() time.Time
	// NoStreams is a daemon reached through a proxy that carries plain
	// requests and answers but not attached streams (Portainer's Docker
	// proxy): a step that streams runs as a job instead (RunStep).
	NoStreams bool
	// Redact replaces the values a boot generated (its needs' credentials)
	// in what a failure keeps of a container's own output, before that is
	// cut short (Boot sets it once its needs are resolved); nil keeps the
	// text. It is never applied to what the forge compares or acts on.
	Redact func(string) string
}

// redacted is a container's own output as a failure may keep it (Redact).
func (dk Scope) redacted(out []byte) string {
	if dk.Redact == nil {
		return string(out)
	}
	return dk.Redact(string(out))
}

// WithLifetime is dk with every command bounded by ctx.
func (dk Scope) WithLifetime(ctx context.Context) Scope {
	dk.Lifetime = ctx
	return dk
}

// lifetime is what bounds a wait between this scope's commands (a probe's
// interval, a stack's grace): its Lifetime, or nothing when it has none.
func (dk Scope) lifetime(ctx context.Context) context.Context {
	if dk.Lifetime != nil {
		return dk.Lifetime
	}
	return ctx
}

func (dk Scope) Remote() bool { return dk.Host != "" }

func (dk Scope) Run(ctx context.Context, args ...string) ([]byte, error) {
	return dk.Command(ctx, Command{Args: args})
}

func (dk Scope) OK(ctx context.Context, args ...string) bool {
	// The answer cannot say "unknown", so a stop must not shape it: this
	// bounded read runs to its end though the caller stops.
	ctx = context.WithoutCancel(ctx)
	_, err := dk.Run(ctx, args...)
	return err == nil
}

func (dk Scope) Text(ctx context.Context, args ...string) string {
	// The answer cannot say "unknown", so a stop must not shape it: this
	// bounded read runs to its end though the caller stops.
	ctx = context.WithoutCancel(ctx)
	out, err := dk.Run(ctx, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (dk Scope) Running(ctx context.Context, container string) bool {
	return dk.Text(ctx, "inspect", "-f", "{{.State.Running}}", container) == "true"
}

// EnsureRunning makes the named container run: a running one is left alone,
// a stopped one is started, and a missing one is created with `docker run`
// and runArgs (which must name it). Another process may create it at the same
// moment; a failed run that leaves the container existing is treated as that
// race and the existing container is started instead. Docker reserves the
// name before the winner's container can be inspected, so after a name
// conflict the loser waits a bounded time for it to appear: test binaries
// starting together on a machine without the image (a fresh CI runner) all
// pull it, and one that asked once read the winner's container as missing.
func (dk Scope) EnsureRunning(ctx context.Context, name string, runArgs []string) error {
	if dk.OK(ctx, "inspect", name) {
		return dk.startExisting(ctx, name)
	}
	out, err := dk.Run(ctx, append([]string{"run"}, runArgs...)...)
	if err == nil {
		return nil
	}
	appears := dk.OK(ctx, "inspect", name)
	if !appears && strings.Contains(string(out), "is already in use") {
		appears = poll.Until(ctx, nameConflictWait, 200*time.Millisecond, func() bool { return dk.OK(ctx, "inspect", name) })
	}
	if appears {
		return dk.startExisting(ctx, name)
	}
	return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
}

// nameConflictWait bounds how long a lost create race waits for the
// winner's container to become visible.
const nameConflictWait = 30 * time.Second

func (dk Scope) startExisting(ctx context.Context, name string) error {
	if dk.Running(ctx, name) {
		return nil
	}
	if out, err := dk.Run(ctx, "start", name); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Remove, RemoveNetwork and RemoveVolume treat an object already gone as
// removed. A failure is logged and returned; callers that act on it (a
// deleted project's cleanup) retry and report it, the rest may ignore it.
func (dk Scope) Remove(ctx context.Context, container string) error {
	return dk.removeObject(ctx, "docker rm -f "+container, "rm", "-f", container)
}

func (dk Scope) RemoveNetwork(ctx context.Context, network string) error {
	return dk.removeObject(ctx, "docker network rm "+network, "network", "rm", network)
}

func (dk Scope) RemoveVolume(ctx context.Context, volume string) error {
	return dk.removeObject(ctx, "docker volume rm "+volume, "volume", "rm", volume)
}

func (dk Scope) removeObject(ctx context.Context, what string, args ...string) error {
	out, err := dk.Run(ctx, args...)
	if err == nil || dind.Missing(out) {
		return nil
	}
	detail := strs.Truncate(strings.TrimSpace(string(out)), 200)
	log.Printf("%s: %v: %s", what, err, detail)
	return fmt.Errorf("%s: %v: %s", what, err, detail)
}

// Exists asks the daemon whether an object is there (args: the inspect
// command, such as "volume", "inspect", name). Only the daemon's own "no
// such" answer is absence; any other failure is an error, never "gone".
func (dk Scope) Exists(ctx context.Context, args ...string) (bool, error) {
	out, err := dk.Run(ctx, args...)
	switch {
	case err == nil:
		return true, nil
	case dind.Missing(out):
		return false, nil
	}
	return false, fmt.Errorf("docker %s: %v: %s", strings.Join(args, " "), err, strs.Truncate(strings.TrimSpace(string(out)), 200))
}

// List is Lines for a caller that must tell an empty answer from a failed one.
func (dk Scope) List(ctx context.Context, args ...string) ([]string, error) {
	out, err := dk.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("docker %s: %v: %s", strings.Join(args, " "), err, strs.Truncate(strings.TrimSpace(string(out)), 200))
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// ImageDigest resolves a pulled image ref to its content digest — the
// receipt that names the exact bytes.
func (dk Scope) ImageDigest(ctx context.Context, imageRef string) string {
	return dk.Text(ctx, "image", "inspect", "--format", "{{index .RepoDigests 0}}", imageRef)
}

// ProbeURL is where the forge reaches a container's published port: its
// own loopback, or the attached host by address.
func (dk Scope) ProbeURL(hostPort int) string {
	if dk.Host == "" {
		return loopbackURL(hostPort)
	}
	return fmt.Sprintf("http://%s:%d", dk.Hostname, hostPort)
}

// PublishAddress is the address a container's port is published on. On
// the forge's own daemon it is loopback: the forge is on the same host and
// its proxy (vhost.go) is the only front door, so a wider bind would open a
// LAN path around the proxy's header sanitisation. On an attached host it
// is every interface: the forge probes the app there, and people reach it
// there when the host has no domain, at the host's address and the
// published port (ProbeURL, FrontDoor). A loopback bind on that host is
// reachable from neither. Who else may reach the port is the host's
// firewall, the operator's.
func (dk Scope) PublishAddress() string {
	if dk.Host == "" {
		return "127.0.0.1"
	}
	return "0.0.0.0"
}

// DialAddress is host:port for a published port: loopback here, the
// attached host by address there.
func (dk Scope) DialAddress(hostPort int) string {
	if dk.Host == "" {
		return fmt.Sprintf("127.0.0.1:%d", hostPort)
	}
	return fmt.Sprintf("%s:%d", dk.Hostname, hostPort)
}

// RoutesDeployment says whether the host's ingress serves this deployment
// at <slug>.<domain>: the host has an ingress domain (only an attached host
// does; a scope naming it for its address only, as attachedFrontDoor
// builds, has no Host), and the deployment is main. The project's name is
// main's, as the main tier domain is on the forge's own host (vhost.go):
// Traefik joins every container that declares one router into one service
// and splits requests between them, so a second deployment carrying main's
// route would take main's visitors (soak finding 55). Any other deployment
// is reached at the host and its published port.
func (dk Scope) RoutesDeployment(deployment string) bool {
	return dk.Domain != "" && deployment == "main"
}

// FrontDoor is where people reach a deployment on an attached host: main
// under the target's ingress domain when it has one (RoutesDeployment,
// design/subsystems/ingress.md), else the host by address and published
// port.
func (dk Scope) FrontDoor(slug, deployment string, hostPort int) string {
	if dk.RoutesDeployment(deployment) {
		return "https://" + slug + "." + dk.Domain
	}
	return fmt.Sprintf("http://%s:%d", dk.Hostname, hostPort)
}

// ImageExists: is this image present in the local daemon? This is the one
// image-inspect probe; callers use it rather than writing their own.
func (dk Scope) ImageExists(ctx context.Context, ref string) bool {
	// The answer cannot say "unknown", so a stop must not shape it: this
	// bounded read runs to its end though the caller stops.
	ctx = context.WithoutCancel(ctx)
	_, err := dk.Run(ctx, "image", "inspect", ref)
	return err == nil
}

// Lines runs a listing and returns its non-empty Lines. A non-zero
// exit is logged — a reaper that cannot list is a reaper that silently
// leaks — and yields nothing.
func (dk Scope) Lines(ctx context.Context, args ...string) []string {
	// The answer cannot say "unknown", so a stop must not shape it: this
	// bounded read runs to its end though the caller stops.
	ctx = context.WithoutCancel(ctx)
	out, err := dk.Run(ctx, args...)
	if err != nil {
		log.Printf("docker %s: %v: %s", strings.Join(args, " "), err, strs.Truncate(strings.TrimSpace(string(out)), 200))
		return nil
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// ReapStack removes a throwaway stack: every container labelled as owned by
// it (its services, and any init container still standing), then its
// network. Exact label matches — nothing is reaped by name coincidence.
func (dk Scope) ReapStack(ctx context.Context, stackID string) {
	// A clean-up: it runs though the caller stops.
	ctx = context.WithoutCancel(ctx)
	if err := dk.RemoveStack(ctx, stackID); err != nil {
		log.Printf("reaping stack %s: %v", stackID, err) // best effort, but never silent
	}
}

// RemoveStack is ReapStack for a caller that must know it worked: a failed
// listing or removal is returned, so a cleanup cannot record a stack removed
// while its services still run.
func (dk Scope) RemoveStack(ctx context.Context, stackID string) error {
	return dk.removeStack(ctx, stackID, dk.Remove)
}

// RemoveWithVolumes removes a container with its anonymous volumes, for a
// container whose data goes with it.
func (dk Scope) RemoveWithVolumes(ctx context.Context, container string) error {
	return dk.removeObject(ctx, "docker rm -f -v "+container, "rm", "-f", "-v", container)
}

// RemoveDisposable removes a disposable container (a preview's app, a
// deleted project's deployment) with its anonymous volumes and its named
// data volume: nothing of it carries over to the next one of its name.
func (dk Scope) RemoveDisposable(ctx context.Context, container string) error {
	return errors.Join(dk.RemoveWithVolumes(ctx, container), dk.RemoveVolume(ctx, DataVolumeName(container)))
}

// BuildImages lists the local build images made for one container: its own
// (BuildTag(container)) and its parts' and release generations'
// (BuildTag(container)+"-…"). The caller decides which belong to it: the
// "-…" form can also match another owner's longer name.
func (dk Scope) BuildImages(ctx context.Context, container string) ([]string, error) {
	tag := BuildTag(container)
	return dk.List(ctx, "image", "ls", "--format", "{{.Repository}}:{{.Tag}}", "--filter", "reference="+tag, "--filter", "reference="+tag+"-*")
}

// RemoveBuildImagesOf removes the build images made for one container (a
// preview's: its app's and its parts'), never forcing: inUse says one a
// container still uses stayed.
func (dk Scope) RemoveBuildImagesOf(ctx context.Context, container string) (inUse bool, err error) {
	refs, err := dk.BuildImages(ctx, container)
	if err != nil {
		return false, err
	}
	var failed []error
	for _, ref := range refs {
		used, err := dk.RemoveImageUnlessUsed(ctx, ref)
		inUse = inUse || used
		failed = append(failed, err)
	}
	return inUse, errors.Join(failed...)
}

// BuildCacheBudget is how much unused build cache a daemon keeps: the
// forge's own and a worker's alike.
const BuildCacheBudget = "4GB"

// TrimBuildCache trims dk's unused build cache to BuildCacheBudget.
func TrimBuildCache(ctx context.Context, dk Scope) error {
	if out, err := dk.Run(ctx, "builder", "prune", "-f", "--keep-storage", BuildCacheBudget); err != nil {
		return fmt.Errorf("docker builder prune: %v: %s", err, strs.Truncate(strings.TrimSpace(string(out)), 200))
	}
	return nil
}

// RemoveUnusedImage removes an image no container uses. One a container
// still uses, running or stopped, is kept, and that is not a failure: the
// daemon refuses it without force, and nothing here forces.
func (dk Scope) RemoveUnusedImage(ctx context.Context, ref string) error {
	_, err := dk.RemoveImageUnlessUsed(ctx, ref)
	return err
}

// RemoveImageUnlessUsed is RemoveUnusedImage for a caller that reports
// what it removed: inUse says a container still uses the image, which
// therefore stays.
func (dk Scope) RemoveImageUnlessUsed(ctx context.Context, ref string) (inUse bool, err error) {
	out, err := dk.Run(ctx, "rmi", ref)
	switch {
	case err == nil || dind.Missing(out):
		return false, nil
	case imageInUse(out):
		return true, nil
	}
	detail := strs.Truncate(strings.TrimSpace(string(out)), 200)
	return false, fmt.Errorf("docker rmi %s: %v: %s", ref, err, detail)
}

// imageInUse is the daemon's refusal to remove an image a container uses.
func imageInUse(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, "error response from daemon") && strings.Contains(s, "conflict") && strings.Contains(s, "container")
}

// RemoveDisposableStack is RemoveStack for a disposable stack (a
// preview's): its containers go with their anonymous volumes.
func (dk Scope) RemoveDisposableStack(ctx context.Context, stackID string) error {
	return dk.removeStack(ctx, stackID, dk.RemoveDisposable)
}

func (dk Scope) removeStack(ctx context.Context, stackID string, remove func(context.Context, string) error) error {
	var failed []error
	for _, class := range []string{"service", "step"} {
		ids, err := dk.List(ctx, "ps", "-aq", "--filter", "label="+dockerlabel.Class+"="+class, "--filter", "label="+dockerlabel.Owner+"="+stackID)
		if err != nil {
			failed = append(failed, err)
			continue
		}
		for _, id := range ids {
			if err := remove(ctx, id); err != nil {
				failed = append(failed, err)
			}
		}
	}
	if err := dk.RemoveNetwork(ctx, NetworkName(stackID)); err != nil {
		failed = append(failed, err)
	}
	return errors.Join(failed...)
}

// AgentRuntimeVersion reads the pi version label baked into the agent image
// (cached per image ID — receipts should say which pi ran, and the mutable
// tag "forge-agent" doesn't).
func (dk Scope) AgentRuntimeVersion(ctx context.Context, image string) string {
	return dk.AgentImageLabel(ctx, image, dockerlabel.PiVersion)
}

// AgentImageLabel reads a version label baked into the agent image —
// receipts should say which runtimes ran, and the mutable tag doesn't.
func (dk Scope) AgentImageLabel(ctx context.Context, image, label string) string {
	return dk.Text(ctx, "image", "inspect", "-f", "{{with index .Config \"Labels\"}}{{index . \""+label+"\"}}{{end}}", image)
}

func loopbackURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// WritesLabel marks a container that mounts a host folder writable:
// forge.writes=<host path>. Whoever starts such a container labels it, so the
// folder's writers can be listed without knowing who started them.
const WritesLabel = dockerlabel.Writes

// RemoveLabelled removes every container carrying all of labels (key=value)
// and returns those Docker still lists afterwards. A daemon that cannot be
// asked is an error: it confirms nothing.
func (dk Scope) RemoveLabelled(ctx context.Context, labels ...string) ([]string, error) {
	list := func() ([]string, error) {
		args := []string{"ps", "-a", "--format", "{{.Names}}"}
		for _, l := range labels {
			args = append(args, "--filter", "label="+l)
		}
		out, err := dk.Command(ctx, Command{Args: args})
		if err != nil {
			return nil, fmt.Errorf("listing containers labelled %s: %w: %s", strings.Join(labels, ","), err, strings.TrimSpace(string(out)))
		}
		return strings.Fields(string(out)), nil
	}
	names, err := list()
	if err != nil || len(names) == 0 {
		return names, err
	}
	for _, name := range names {
		if out, err := dk.Command(ctx, Command{Args: []string{"rm", "-f", name}}); err != nil {
			log.Printf("removing container %s: %v: %s", name, err, strings.TrimSpace(string(out)))
		}
	}
	return list()
}

// ListLabelled lists the containers carrying all of labels (key=value), each
// with the value of its label key ("" when it has none).
func (dk Scope) ListLabelled(ctx context.Context, key string, labels ...string) (map[string]string, error) {
	args := []string{"ps", "-a", "--format", `{{.Names}}\t{{.Label "` + key + `"}}`}
	for _, l := range labels {
		args = append(args, "--filter", "label="+l)
	}
	out, err := dk.Command(ctx, Command{Args: args})
	if err != nil {
		return nil, fmt.Errorf("listing containers labelled %s: %w: %s", strings.Join(labels, ","), err, strings.TrimSpace(string(out)))
	}
	found := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if name, value, _ := strings.Cut(line, "\t"); name != "" {
			found[name] = value
		}
	}
	return found, nil
}
