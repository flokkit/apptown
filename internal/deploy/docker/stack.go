package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/containerresult"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/execution"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/strs"
	"github.com/flokkit/apptown/internal/trust"
)

// Labels tags a spawned container so the reaper (design/subsystems/container-lifecycle.md)
// identifies it by class + owner without name-parsing, and judges its age
// by apptown.created, stamped from the scope's clock. class is one of
// preview|service|step|dind|console|env; env/infra are never reaped.
func (dk Scope) Labels(class, owner string) []string {
	return append([]string{
		"--label", dockerlabel.Class + "=" + class,
		"--label", dockerlabel.Owner + "=" + owner,
		"--label", dockerlabel.Created + "=" + clock.Or(dk.Now)().UTC().Format(time.RFC3339),
	}, dk.InstanceLabel()...)
}

// TeardownBound is how long a step's returned teardown may take: it runs
// after its caller's context may have ended, so it brings its own bound.
const TeardownBound = 2 * time.Minute

// startServices creates a per-step network and launches sidecars on it, then
// waits for the ones that declare a healthcheck to become healthy (a real
// readiness gate, not a blind sleep) so migrations/apps start against a ready
// dependency. Returns the network name and a teardown func (always non-nil).
//
// The teardown removes the stack by its owner label, not by what this call
// started: a container an earlier attempt left (an AppTown that stopped mid
// step) still holds the network, and the network goes only once nothing
// is on it. Whatever it still cannot remove, the periodic reaper takes
// once the owner has finished (maintenance.Reaper.Containers).
func StartServices(ctx context.Context, dk Scope, id string, svcs []deploy.ServiceSpec) (string, func(), error) {
	network := Current.Network(id)
	// The teardown runs after the step, though its context has ended (a
	// stopping AppTown), bounded so it cannot hang the step's end.
	cleanup := func() {
		teardown, cancel := context.WithTimeout(context.WithoutCancel(ctx), TeardownBound)
		defer cancel()
		if err := dk.RemoveStack(teardown, id); err != nil {
			log.Printf("tearing down stack %s: %v", id, err) // the reaper's backstop takes what is left
		}
	}
	// The network carries its stack's labels, as its containers do, so the
	// periodic reaper finds it by its owner rather than by its name.
	create := append(append([]string{"network", "create"}, dk.Labels("service", id)...), network)
	if out, err := dk.Run(ctx, create...); err != nil {
		// A stale network from an earlier failed attempt is reusable — only
		// error if it truly doesn't exist.
		if !dk.OK(ctx, "network", "inspect", network) {
			return "", cleanup, fmt.Errorf("network create: %v: %s", err, out)
		}
	}
	if err := runServiceContainers(ctx, dk, id, network, svcs); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err := waitStackReady(ctx, dk, svcs, id); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return network, cleanup, nil
}

// serviceRunOptions: how a service container lives. Throwaway (the default: class
// service, --rm) for previews and runs; persistent (class env, restart
// unless-stopped) for a deployed environment's own parts, which the janitor
// must never reap by age.
type serviceRunOptions struct {
	class      string
	persistent bool
}

// runServiceContainers launches each service on an EXISTING network (aliased by
// its name, healthcheck applied when declared), labelled as owned by the stack.
// Shared by startServices (the needs/sidecar stack) and the app-parts phase.
func runServiceContainers(ctx context.Context, dk Scope, id, network string, svcs []deploy.ServiceSpec) error {
	return runServiceContainersWith(ctx, dk, id, network, svcs, serviceRunOptions{class: "service"}, Ownership{})
}

func runServiceContainersWith(ctx context.Context, dk Scope, id, network string, svcs []deploy.ServiceSpec, opts serviceRunOptions, own Ownership) error {
	for _, svc := range svcs {
		cname := Current.Service(id, svc.Name)
		dk.Remove(ctx, cname)
		args := []string{"run", "-d", "--init", "--name", cname,
			"--network", network, "--network-alias", svc.Name}
		if opts.persistent {
			args = append(args, "--restart", "unless-stopped")
		} else {
			args = append(args, "--rm")
		}
		args = append(args, dk.Labels(opts.class, id)...)
		args = append(args, own.Info...)
		args = append(args, ResourceArgs(svc.Resources)...)
		args = append(args, execution.LogFlags()...)
		for k, v := range svc.Env {
			args = append(args, "-e", k+"="+v)
		}
		if svc.AppPart {
			args = append(args, trust.ContainerArgs(!dk.Remote())...)
		}
		args = append(args, healthArgs(svc.Health)...)
		// A need gets a disposable folder where it keeps its data: an
		// anonymous volume, which the stack's removal (rm -f -v) deletes
		// with the container. --rm does not: a container removed with rm -f
		// keeps its anonymous volumes. Only throwaway needs come here: a
		// standing need's named volume is mounted by EnsureStandingServices.
		if svc.Data != "" {
			args = append(args, "-v", svc.Data)
		}
		args = append(args, svc.EntrypointFlag()...)
		args = append(args, svc.Image)
		args = append(args, svc.RunArgs()...)
		out, err := dk.Run(ctx, args...)
		if err != nil {
			return fmt.Errorf("service %s: %v: %s", svc.Name, err, out)
		}
		if err := recordService(out, cname, svc.Name, own); err != nil {
			return err
		}
	}
	return nil
}

// waitStackReady gates on the health-declaring services becoming healthy
// (waitStackHealthy); if none declare health it falls back to the
// historical readiness grace.
func waitStackReady(ctx context.Context, dk Scope, svcs []deploy.ServiceSpec, id string) error {
	declared, err := waitStackHealthy(ctx, dk, svcs, id)
	if err != nil || declared {
		return err
	}
	_ = poll.Wait(dk.lifetime(ctx), 4*time.Second) // no healthchecks: the old readiness grace
	return nil
}

// healthBound is how long a need that declares a health check has to
// answer it. A first database start on a busy host takes tens of seconds.
// Docker is given the same bound as the check's start period, so a check
// that fails while the service starts is not yet "unhealthy": it says
// unhealthy only when the service failed its checks after it started, or
// stayed not ready the whole bound.
const healthBound = 2 * time.Minute

// healthArgs are the docker run flags of a health check, none for none.
func healthArgs(cmd string) []string {
	if cmd == "" {
		return nil
	}
	return []string{"--health-cmd", cmd, "--health-interval", "3s", "--health-timeout", "3s",
		"--health-retries", "20", "--health-start-period", healthBound.String()}
}

// waitStackHealthy waits on the health-declaring services alone and reports
// whether there were any. A standing deployment's stack gets no blind
// grace: a sidecar that is already running and healthy is reused at once.
// A service that is not healthy within healthBound fails the boot, naming
// it, before any init step or part starts: its health check is what says
// it is ready for the app (an s3 store's bucket and account are made), so
// carrying on would start the app against something not ready.
func waitStackHealthy(ctx context.Context, dk Scope, svcs []deploy.ServiceSpec, id string) (bool, error) {
	declared := false
	for _, svc := range svcs {
		if svc.Health == "" {
			continue
		}
		declared = true
		cname := Current.Service(id, svc.Name)
		status := ""
		// Bounded by its timeout, and by the boot's lifetime when it has one.
		// Docker's own verdict, unhealthy (its retries spent), ends the wait.
		poll.Until(dk.lifetime(ctx), healthBound, time.Second, func() bool {
			status = dk.Text(ctx, "inspect", "-f", "{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", cname)
			// While Docker restarts a container under its restart policy it
			// reports the health as unhealthy, because the checks stopped
			// with the process. That is not a verdict: the service starts
			// again with a new start period. A first database start on a
			// busy host does this when the image's setup gives up and exits.
			// Whether it is restarting is read together with its health: a
			// container started again between two reads is no longer
			// restarting, and the first read's unhealthy is then stale.
			if status == "unhealthy" {
				switch now := dk.Text(ctx, "inspect", "-f", "{{.State.Restarting}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", cname); {
				case strings.HasPrefix(now, "true "):
					status = "restarting"
				case strings.HasPrefix(now, "false "):
					status = strings.TrimPrefix(now, "false ")
				}
			}
			return status == "healthy" || status == "none" || status == "unhealthy"
		})
		if status == "healthy" || status == "none" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return declared, fmt.Errorf("waiting for the %s service to be ready: %w", svc.Name, err)
		}
		// Docker's own verdict is a known outcome: nothing of the app was
		// started, so a deployment attempt ends failed, not in doubt. A
		// status the daemon did not give stays unknown.
		if status == "unhealthy" {
			// An exited container keeps its last health status: one the
			// daemon says is not running did not fail a check "after it
			// started". An unreadable answer keeps Docker's verdict.
			if dk.Text(ctx, "inspect", "-f", "{{.State.Running}}", cname) == "false" {
				return declared, deploy.Terminal(fmt.Errorf("the %s service is not running, so it is not ready for the app and nothing of the app was started", svc.Name))
			}
			return declared, deploy.Terminal(fmt.Errorf("the %s service is unhealthy: it failed its health check after it started, so it is not ready for the app and nothing of the app was started", svc.Name))
		}
		if status == "" {
			return declared, fmt.Errorf("the %s service is not running, not healthy, after %s: it is not ready for the app, so nothing of the app was started", svc.Name, healthBound)
		}
		return declared, deploy.Terminal(fmt.Errorf("the %s service is %s, not healthy, after %s: it is not ready for the app, so nothing of the app was started", svc.Name, status, healthBound))
	}
	return declared, nil
}

// PreparePreviewBuilds builds each explicit build context once. Different
// contexts cannot silently share the public image. Only the private plan
// receives tags.
func PreparePreviewBuilds(ctx context.Context, dk Scope, ws, name string, c *appcontract.Plan) error {
	built := map[string]string{}
	prepare := func(build, part string) (string, error) {
		if image := built[build]; image != "" {
			return image, nil
		}
		tag := Current.BuildTag(name)
		if len(built) > 0 {
			tag = Current.BuildTag(name + "-" + part)
		}
		buildDir, cleanup, err := StagedBuildContext(ctx, ws, build)
		if err != nil {
			return "", err
		}
		defer cleanup()
		if out, err := buildPreviewImage(ctx, dk, tag, buildDir); err != nil {
			return "", BuildFailure("building the app image", out, err)
		}
		built[build] = tag
		return tag, nil
	}
	for i := range c.Parts {
		part := &c.Parts[i]
		if part.Build == "" {
			continue
		}
		image, err := prepare(part.Build, part.Name)
		if err != nil {
			return err
		}
		part.Image, part.Build = image, ""
	}
	for i := range c.Init {
		init := &c.Init[i]
		if init.Build == "" {
			continue
		}
		image, err := prepare(init.Build, "init-"+init.Name)
		if err != nil {
			return err
		}
		init.Image, init.Build = image, ""
	}
	return nil
}

// nonPublicPartServices turns the non-public app parts into services that run
// on the app network (alias = part name). The plan already contains each
// part's image and resolved connections; scoped secrets are delivered last.
func nonPublicPartServices(c *appcontract.Plan, secrets Delivery) []deploy.ServiceSpec {
	var out []deploy.ServiceSpec
	for _, p := range c.Parts {
		if p.Public {
			continue
		}
		spec := deploy.ServiceSpec{Name: p.Name, Command: p.Command, Health: p.Health, Env: map[string]string{}, Resources: p.Resources, AppPart: true}
		for k, v := range p.Env {
			spec.Env[k] = v
		}
		for k, v := range secrets.EnvFor(p.Name) {
			spec.Env[k] = v // this part's own secrets, and only those
		}
		if p.Port != 0 {
			spec.Env["PORT"] = fmt.Sprintf("%d", p.Port)
		}
		switch {
		case p.Image != "":
			spec.Image = p.Image
		default:
			continue // a non-public static part is exotic; skip
		}
		if spec.Image != "" {
			out = append(out, spec)
		}
	}
	return out
}

// runInitContainers runs each init container to completion on the app network,
// in order, BEFORE the app parts start. A non-zero exit fails the boot loudly
// with the init's output (a failed migration must stop the app coming up).
func runInitContainers(ctx context.Context, dk Scope, id, network string, c *appcontract.Plan, secrets Delivery) error {
	for _, it := range c.Init {
		img := it.Image
		if img == "" {
			return fmt.Errorf("init %q has no image to run", it.Name)
		}
		cname := Current.Init(id, it.Name)
		dk.Remove(ctx, cname)
		args := []string{"run", "--rm", "--init", "--name", cname, "--network", network}
		args = append(args, dk.Labels("step", id)...)
		args = append(args, execution.LogFlags()...)
		for k, v := range it.Env {
			args = append(args, "-e", k+"="+v)
		}
		// An init step's own secrets (scope init-<name>): a migration
		// credential no standing part ever receives.
		for k, v := range secrets.EnvFor("init-" + it.Name) {
			args = append(args, "-e", k+"="+v)
		}
		args = append(args, trust.ContainerArgs(!dk.Remote())...)
		args = append(args, img)
		args = append(args, it.Command...)
		if out, err := dk.Run(ctx, args...); err != nil {
			return fmt.Errorf("init %q failed: %s", it.Name, strs.Truncate(dk.redacted(out), 400))
		}
	}
	return nil
}

// bringUpParts runs the init containers (to completion) then starts the
// non-public app parts on the network — the phase between "needs are healthy"
// (startServices already waited) and "start the public app" (startAppContainer).
// It ensures a network exists when there is init/extra-part work but no needs
// stack created one. Returns the prepared execution plan and
// the network to run the public app on.
func bringUpParts(ctx context.Context, dk Scope, id, name, ws, network string, c *appcontract.Plan, secrets Delivery, own Ownership) (*appcontract.Plan, string, error) {
	hasExtra := len(c.Init) > 0
	for _, p := range c.Parts {
		if !p.Public {
			hasExtra = true
		}
	}
	if !hasExtra {
		return c, network, nil // single-part, no init: nothing new to do
	}
	if network == "" {
		network = Current.Network(id)
		if err := ensureNetwork(ctx, dk, network, own); err != nil {
			return nil, "", err
		}
	}
	if err := PreparePreviewBuilds(ctx, dk, ws, name, c); err != nil {
		return nil, "", err
	}
	if err := runInitContainers(ctx, dk, id, network, c, secrets); err != nil {
		return nil, "", err
	}
	if extra := nonPublicPartServices(c, secrets); len(extra) > 0 {
		// A deployed deployment's own parts live with the deployment:
		// class env (never idle-reaped) and restart unless-stopped. Preview
		// and run stacks stay throwaway.
		opts := serviceRunOptions{class: "service"}
		if own.Persistent {
			opts = serviceRunOptions{class: "env", persistent: true}
		}
		if err := runServiceContainersWith(ctx, dk, id, network, extra, opts, own); err != nil {
			return nil, "", err
		}
	}
	return c, network, nil
}

// ServiceDrift names what differs between a standing service container
// and the service the contract declares now: "entrypoint", "command",
// "image", or env keys whose values moved. Empty when the container still
// serves the declaration. An entrypoint or command the declaration leaves
// to the image is not compared: the image's own is not known here.
func ServiceDrift(ctx context.Context, dk Scope, cname string, svc deploy.ServiceSpec) ([]string, error) {
	var drift []string
	inspected := func(what, field string) ([]string, error) {
		var have []string
		raw, err := dk.Run(ctx, "inspect", "-f", "{{json .Config."+field+"}}", cname)
		if err != nil {
			return nil, fmt.Errorf("reading the standing %s %s: %w", svc.Name, what, err)
		}
		if err := json.Unmarshal(raw, &have); err != nil {
			return nil, fmt.Errorf("reading the standing %s %s: %w", svc.Name, what, err)
		}
		return have, nil
	}
	if want := svc.EntrypointFlag(); len(want) > 0 {
		have, err := inspected("entrypoint", "Entrypoint")
		if err != nil {
			return nil, err
		}
		if !slices.Equal(have, want[1:]) {
			drift = append(drift, "entrypoint")
		}
	}
	if want := svc.RunArgs(); len(want) > 0 {
		have, err := inspected("command", "Cmd")
		if err != nil {
			return nil, err
		}
		if !slices.Equal(have, want) {
			drift = append(drift, "command")
		}
	}
	img := dk.Text(ctx, "inspect", "-f", "{{.Config.Image}}", cname)
	have := map[string]string{}
	for _, line := range strings.Split(dk.Text(ctx, "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", cname), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			have[k] = v
		}
	}
	// Unknown is not drift: an image reported as a digest cannot be
	// compared with a name, and no env lines means the daemon said nothing.
	if img != "" && !strings.HasPrefix(img, "sha256:") && img != svc.Image {
		drift = append(drift, "image")
	}
	if len(have) > 0 {
		keys := make([]string, 0, len(svc.Env))
		for k := range svc.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if have[k] != svc.Env[k] {
				drift = append(drift, k)
			}
		}
	}
	return drift, nil
}

// EnsureStandingServices starts a deployment's standing services
// (<prefix>svc-env-<slug>-<env>-<name>, owner = the env's stack id) and
// returns the network name, or "" when the deployment needs none.
// Idempotent: existing containers stand — their data is the persistence.
// A standing container whose declared parameters have MOVED (another
// user, database or image than the contract names now) cannot serve the
// app: a database made under one user, booted against a contract that now
// names another, fails "password authentication failed". Synthetic data is
// replaced; anything else is refused with the words, never silently replaced.
func EnsureStandingServices(ctx context.Context, dk Scope, stackID string, svcs []deploy.ServiceSpec, dataClass string, own Ownership) (string, error) {
	// A changed catalogue selector cannot discard a previously witnessed engine.
	if own.Storage != nil {
		for _, svc := range svcs {
			if svc.KeepsImage && own.Needs[svc.Name] != "" {
				continue
			}
			_, found, err := own.Storage.Get(ctx, svc.Name, Current.NeedVolume(stackID, svc.Name))
			if err != nil {
				return "", err
			}
			if found {
				return "", fmt.Errorf("the %s service has a storage binding but no longer declares its persistent engine; preserve its data and restore the recorded declaration", svc.Name)
			}
		}
	}
	// A logical fill requires empty targets for every engine, not only the first
	// service reached by the start loop. No engine starts on partial admission.
	if own.RestoreStorage != nil {
		if own.Storage == nil {
			return "", errors.New("restore has no storage evidence owner")
		}
		for _, svc := range svcs {
			if !svc.KeepsImage || own.Needs[svc.Name] == "" {
				continue
			}
			volume := Current.NeedVolume(stackID, svc.Name)
			exists, err := dk.Exists(ctx, "volume", "inspect", volume)
			if err != nil {
				return "", err
			}
			if exists {
				return "", fmt.Errorf("restore refuses preexisting engine-data volume %s; recover it explicitly before retrying", volume)
			}
			binding, found, err := own.Storage.Get(ctx, svc.Name, volume)
			if err != nil {
				return "", err
			}
			var source *deploy.StorageBinding
			if found {
				source = &binding
			}
			if _, _, err := restoreEngine(ctx, dk, svc, own.Needs[svc.Name], source, own.RestoreStorage); err != nil {
				return "", err
			}

		}
	}

	if len(svcs) == 0 {
		return "", nil
	}
	network := Current.Network(stackID)
	if err := ensureNetwork(ctx, dk, network, own); err != nil {
		return network, err
	}
	for _, svc := range svcs {
		cname := Current.Service(stackID, svc.Name)
		// Absent and unreadable are different answers: a service that could
		// not be asked about is not made again under its own name.
		there, err := dk.Exists(ctx, "inspect", cname)
		if err != nil {
			return network, fmt.Errorf("the standing %s service: %w", svc.Name, err)
		}
		path := own.Needs[svc.Name]
		runImage := svc.Image
		if svc.KeepsImage && path != "" && own.Storage != nil {
			runImage, err = bindService(ctx, dk, stackID, cname, path, svc, there, own)
			if err != nil {
				return network, fmt.Errorf("admitting the %s engine: %w", svc.Name, err)
			}
		}
		// Without the container, the requested image is not evidence of the
		// engine that wrote retained data. Refuse before any writable start.
		if !there && svc.KeepsImage && path != "" && own.Storage == nil {
			vol := Current.NeedVolume(stackID, svc.Name)
			retained, err := dk.Exists(ctx, "volume", "inspect", vol)
			if err != nil {
				return network, fmt.Errorf("reading the standing %s service's data volume %s: %w", svc.Name, vol, err)
			}
			if retained {
				return network, fmt.Errorf("the standing %s service has no container, but its data volume %s remains; its previous engine is unknown, so AppTown will not start an engine on that data: establish the previous engine before recovering the service", svc.Name, vol)
			}
		}
		if there {
			drift, err := ServiceDrift(ctx, dk, cname, svc)
			if err != nil {
				return network, err
			}
			if len(drift) == 0 {
				// Its declared size is held now, in place: data and container
				// stay. Only a limit taken away needs the container made
				// again (Docker cannot lift one in place); its storage stays.
				recreate, err := holdServiceLimits(ctx, dk, cname, svc.Resources)
				if err != nil {
					return network, fmt.Errorf("the standing %s service: %w", svc.Name, err)
				}
				if !recreate {
					// Reused as it is: one that stopped (exited during its own
					// start, or stopped by hand) is started again with every
					// mount it has, so its storage stays. Unreadable is left
					// for the health wait to judge.
					if err := startIfStopped(ctx, dk, cname); err != nil {
						return network, fmt.Errorf("the standing %s service: %w", svc.Name, err)
					}
					continue
				}
			} else {
				if err := replaceDrifted(ctx, dk, stackID, cname, svc, drift, dataClass, own); err != nil {
					return network, err
				}
				if svc.KeepsImage && path != "" && own.Storage != nil {
					exists, err := dk.Exists(ctx, "volume", "inspect", Current.NeedVolume(stackID, svc.Name))
					if err != nil {
						return network, err
					}
					if !exists {
						runImage, err = bindService(ctx, dk, stackID, cname, path, svc, false, own)
						if err != nil {
							return network, err
						}
					}
				}
			}
		}
		{
			args := []string{"run", "-d", "--restart", "unless-stopped", "--name", cname,
				"--network", network, "--network-alias", svc.Name}
			// class=env: a deployed deployment's own service — persistent,
			// never idle-reaped (it lives with the env).
			args = append(args, dk.Labels("env", stackID)...)
			args = append(args, own.Info...)
			args = append(args, ResourceArgs(svc.Resources)...)
			args = append(args, execution.LogFlags()...)
			for k, v := range svc.Env {
				args = append(args, "-e", k+"="+v)
			}
			args = append(args, healthArgs(svc.Health)...)
			// A need keeps its data in a named volume of the deployment's,
			// so take-down knows it, backs it up and deletes it.
			if path != "" {
				vol := Current.NeedVolume(stackID, svc.Name)
				if err := ensureVolume(ctx, dk, vol, stackID, svc.Name, own); err != nil {
					return network, err
				}
				args = append(args, "-v", vol+":"+path)
			}
			args = append(args, svc.EntrypointFlag()...)
			args = append(args, runImage)
			args = append(args, svc.RunArgs()...)
			// A service that did not start fails the boot: the app would run
			// without it, and its size would read as applied.
			out, err := dk.Run(ctx, args...)
			if err != nil {
				return network, fmt.Errorf("starting the standing %s service: %v: %s", svc.Name, err, strs.Truncate(strings.TrimSpace(string(out)), 200))
			}
			if err := recordService(out, cname, svc.Name, own); err != nil {
				return network, err
			}
		}
	}
	return network, nil
}

// Data volumes: handing a volume to the user the app image runs as.

// HandVolumeToImageUser makes a data volume owned by the user an image
// declares (USER in its Dockerfile), resolving a name to a uid inside the
// image. Root, no USER, or an image without a shell to resolve a name:
// nothing is done and the volume keeps its ownership.
func HandVolumeToImageUser(ctx context.Context, dk Scope, volume, image, chownImage string) {
	if chownImage == "" {
		return
	}
	out, err := dk.Run(ctx, "image", "inspect", "--format", "{{.Config.User}}", image)
	if err != nil {
		return
	}
	uid, gid, ok := ParseImageUser(strings.TrimSpace(string(out)))
	if !ok {
		return
	}
	if !isNumeric(uid) {
		// A named user: ask the image itself, as root, what its ids are,
		// read from a file in the container (an answer that did not come
		// back leaves the volume as it is).
		res, err := dk.Result(ctx, containerresult.Spec{Image: image, Options: []string{"--user", "0", "--network", "none"}, Script: "id -u " + uid + " && id -g " + uid})
		if err != nil {
			return
		}
		ids := strings.Fields(string(res))
		if len(ids) != 2 || !isNumeric(ids[0]) || !isNumeric(ids[1]) {
			return
		}
		uid, gid = ids[0], ids[1]
	}
	if uid == "0" {
		return
	}
	if gid == "" {
		gid = uid
	}
	_, _ = dk.Run(ctx, "run", "--rm", "--entrypoint", "", "--network", "none", "-v", volume+":/mnt",
		chownImage, "chown", "-R", uid+":"+gid, "/mnt")
}

// ParseImageUser reads a Dockerfile USER value ("app", "10001",
// "10001:10001", "app:app") into uid and gid parts; ok is false for an
// unset or root user.
func ParseImageUser(s string) (uid, gid string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "root" || s == "0" {
		return "", "", false
	}
	if i := strings.IndexByte(s, ':'); i >= 0 {
		uid, gid = s[:i], s[i+1:]
	} else {
		uid = s
	}
	if uid == "root" || uid == "0" {
		return "", "", false
	}
	if gid == "root" {
		gid = "0"
	}
	// A USER is a uid or a POSIX account name; anything else is not one.
	if !isNumeric(uid) && !isAccountName(uid) {
		return "", "", false
	}
	if gid != "" && !isNumeric(gid) && !isAccountName(gid) {
		return "", "", false
	}
	return uid, gid, true
}

func isAccountName(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r == '_':
		case i > 0 && (r >= '0' && r <= '9' || r == '-'):
		default:
			return false
		}
	}
	return true
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// BuildKit puts the compiler/test failure after its setup and progress output.
// Keep the end of long logs so previews and console feedback retain the cause,
// while bounding the error sent through HTTP, session messages and audit rows.
func BuildFailure(operation string, output []byte, cause error) error {
	const limit = 16 * 1024
	detail := strings.TrimSpace(string(output))
	if len(detail) > limit {
		detail = "[earlier build output omitted]\n" + strings.ToValidUTF8(detail[len(detail)-limit:], "�")
	}
	if detail == "" {
		return fmt.Errorf("%s: %w", operation, cause)
	}
	return fmt.Errorf("%s: %w\n%s", operation, cause, detail)
}

// startIfStopped starts a container the daemon says is not running. It
// keeps the container and its mounts, so a need's data stays where it is.
func startIfStopped(ctx context.Context, dk Scope, cname string) error {
	if dk.Text(ctx, "inspect", "-f", "{{.State.Running}}", cname) != "false" {
		return nil
	}
	if out, err := dk.Run(ctx, "start", cname); err != nil {
		return fmt.Errorf("starting it again: %v: %s", err, strs.Truncate(strings.TrimSpace(string(out)), 200))
	}
	return nil
}

// holdServiceLimits makes the limits Docker enforces on a running
// standing service the declared ones, without touching its data: a limit
// set or changed is applied in place (docker update); a limit taken away
// cannot be lifted in place, so the container is stopped and removed, its
// storage kept, and recreate says to make it again. A failure is an error: a
// limit not applied is never reported as applied.
func holdServiceLimits(ctx context.Context, dk Scope, cname string, want appcontract.Resources) (recreate bool, err error) {
	out, err := dk.Run(ctx, "inspect", "--type", "container", "--format", "{{.HostConfig.Memory}} {{.HostConfig.NanoCpus}}", cname)
	if err != nil {
		return false, fmt.Errorf("reading its limits: %v: %s", err, strings.TrimSpace(string(out)))
	}
	var mem, nano int64
	if _, err := fmt.Sscan(strings.TrimSpace(string(out)), &mem, &nano); err != nil {
		return false, fmt.Errorf("reading its limits: %q", strings.TrimSpace(string(out)))
	}
	wantNano := int64(want.CPUs * 1e9)
	if mem == want.MemoryBytes && nano == wantNano {
		return false, nil
	}
	if (mem > 0 && want.MemoryBytes == 0) || (nano > 0 && wantNano == 0) {
		if out, err := dk.Run(ctx, "stop", cname); err != nil {
			return false, fmt.Errorf("stopping it to lift a limit: %v: %s", err, strings.TrimSpace(string(out)))
		}
		if out, err := dk.Run(ctx, "rm", cname); err != nil {
			return false, fmt.Errorf("removing it to lift a limit: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return true, nil
	}
	if out, err := dk.Run(ctx, append(append([]string{"update"}, ResourceArgs(want)...), cname)...); err != nil {
		return false, fmt.Errorf("applying its declared limits: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return false, nil
}

// replaceDrifted replaces a standing service whose declared parameters
// moved, when its data may go: only data declared synthetic, in a volume
// the deployment recorded making; anything else is refused with what to
// do. Its size alone moving never comes here (holdServiceLimits). Either
// way the service is then made on its named volume: kept, or new when its
// synthetic data was let go.
func replaceDrifted(ctx context.Context, dk Scope, stackID, cname string, svc deploy.ServiceSpec, drift []string, dataClass string, own Ownership) error {
	// A service that sets everything it is configured with again at every
	// start keeps nothing of the old configuration in its data: it is made
	// again on the same storage, whatever the data, as a lifted limit is.
	// Unless its data is in its image's own format and the image moved:
	// that is not configuration, and the rule below holds.
	if svc.ConfigAtStart && !(svc.KeepsImage && slices.Contains(drift, "image")) {
		log.Printf("deployment service %s: declared parameters moved (%s) — making it again on the storage its data is in; it sets its configuration at every start", cname, strings.Join(drift, ", "))
		// Stopped first, so it shuts down as it would on a host's restart:
		// a store that writes its data at shutdown (Redis's snapshot) keeps
		// what it held, where a kill would lose what came after its last
		// snapshot. A stop that fails is left to the removal below.
		_, _ = dk.Run(ctx, "stop", cname)
		if out, err := dk.Run(ctx, "rm", "-f", cname); err != nil {
			return fmt.Errorf("removing the standing %s service to remake it: %v: %s", svc.Name, err, strs.Truncate(strings.TrimSpace(string(out)), 200))
		}
		return nil
	}
	// Only data declared synthetic is replaced; any other class, or
	// none known, is someone's data.
	if dataClass != "synthetic" {
		class := dataClass
		if class == "" {
			class = "data of an unknown class"
		} else {
			class += " data"
		}
		return fmt.Errorf("the standing %s service was created with other parameters than the contract declares now (%s); this deployment holds %s, so AppTown will not replace it — retire the deployment's services under Deployments to recreate them from the contract",
			svc.Name, strings.Join(drift, ", "), class)
	}
	vol := Current.NeedVolume(stackID, svc.Name)
	_, isNeed := own.Needs[svc.Name]
	if isNeed && !own.recorded("volume", vol) {
		// Only a volume the deployment recorded creating is its to
		// reset: one that was there before (reused, never recorded)
		// is someone's data, whatever the data class says.
		if there, err := dk.Exists(ctx, "volume", "inspect", vol); err != nil || there {
			if err == nil {
				err = errors.New("it was there before this deployment and is not on its records")
			}
			return fmt.Errorf("the standing %s service was created with other parameters than the contract declares now (%s); its data volume %s is not one this deployment recorded making (%v), so AppTown will not reset it — move or delete that volume yourself, or change the contract back",
				svc.Name, strings.Join(drift, ", "), vol, err)
		}
	}
	log.Printf("deployment service %s: declared parameters moved (%s) — recreating from the contract; its data was synthetic and is not kept", cname, strings.Join(drift, ", "))
	// The service is made again only once both are gone: a container or
	// volume left would carry the old parameters into the new service.
	if err := dk.Remove(ctx, cname); err != nil {
		return fmt.Errorf("removing the standing %s service to recreate it: %w", svc.Name, err)
	}
	if isNeed && own.recorded("volume", vol) {
		// Its recorded data volume goes too: a database made with the
		// old parameters would keep them.
		if err := dk.RemoveVolume(ctx, vol); err != nil {
			return fmt.Errorf("removing the %s service's data volume %s to recreate it: %w", svc.Name, vol, err)
		}
		if own.Storage != nil {
			if err := own.Storage.Retire(ctx, svc.Name, vol); err != nil {
				return fmt.Errorf("retiring the deleted %s volume's engine evidence: %w", svc.Name, err)
			}
		}
	}
	return nil
}
