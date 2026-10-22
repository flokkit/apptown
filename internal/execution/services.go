package execution

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/dind"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/strs"
)

// serviceReadiness bounds the wait for a run's services to become healthy.
const serviceReadiness = time.Minute

// DefaultServiceLimits are each service's CPU, memory (swap included) and
// process limits when its runner names none: a project's test database
// shares the machine with every other run on it, and must not starve them.
var DefaultServiceLimits = Limits{CPUs: 1, MemoryBytes: 1 << 30, PIDs: 256}

// DefaultTaskLimits bound a step's, check's or task's container when the
// operator names none (apptownd -task-cpus, -task-memory-mib, -task-pids):
// room for an ordinary build, never the whole host.
var DefaultTaskLimits = Limits{CPUs: 4, MemoryBytes: 8 << 30, PIDs: 4096}

// DefaultChatLimits bound a chat's container on AppTown's own host, as a
// worker's -chat-* flags do on a worker.
var DefaultChatLimits = Limits{CPUs: 2, MemoryBytes: 4 << 30, PIDs: 4096}

// LimitFlags are a container's docker flags for l: --cpus, --memory with
// equal swap, --pids-limit; none for a zero field.
func LimitFlags(l Limits) []string {
	var out []string
	if l.CPUs > 0 {
		out = append(out, "--cpus", strconv.FormatFloat(l.CPUs, 'f', -1, 64))
	}
	if l.MemoryBytes > 0 {
		m := strconv.FormatInt(l.MemoryBytes, 10)
		out = append(out, "--memory", m, "--memory-swap", m)
	}
	if l.PIDs > 0 {
		out = append(out, "--pids-limit", strconv.FormatInt(l.PIDs, 10))
	}
	return out
}

// LogFlags bound a long-lived container's log on its host's disk: Docker's
// json-file driver, rotated at 10 MiB with three files kept, so at most
// about 30 MiB a container. Without them a container keeps its whole output
// for as long as it exists, which for a standing deployment is until the
// disk is full. json-file is named, not left to the daemon's default,
// because docker logs reads it and a daemon whose default driver takes
// other options would refuse these. Rotation discards a container's oldest
// output, so a task's own container, whose whole output is read by byte
// cursor (DockerTasks.Output), does not get them: it lives only until it
// is released, and new tasks are refused on a disk short of space.
func LogFlags() []string {
	return []string{"--log-driver", "json-file", "--log-opt", "max-size=10m", "--log-opt", "max-file=3"}
}

// serviceLimits are the runner's limits for each service, or the defaults.
// A limit with a zero is refused: zero is never "unlimited".
func (r Runner) serviceLimits() (Limits, error) {
	l := r.ServiceLimits
	if l == (Limits{}) {
		return DefaultServiceLimits, nil
	}
	if l.CPUs <= 0 || l.MemoryBytes <= 0 || l.PIDs <= 0 {
		return Limits{}, fmt.Errorf("a service's CPU, memory and process limits must each be above zero (%+v)", l)
	}
	return l, nil
}

// startServices makes a run's network and its services on it, each reachable
// by its name, and waits until every one runs, healthy where it declares a
// health check. The network is internal when the run has no egress
// (Network "none"). remove takes all of it away again, and is never nil: a
// start that fails is removed by it too.
func (r Runner) startServices(ctx context.Context, req Request) (string, func() error, error) {
	network := req.Name + "-net"
	var made []string
	madeNetwork := false
	// Removal runs after a cancelled start too: it has its own bound.
	remove := func() error {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		var errs []error
		for _, name := range made {
			// -v: a database image's anonymous volume goes with it.
			if out, err := r.docker(cctx, "rm", "-f", "-v", name); err != nil && !dind.Missing(out) {
				errs = append(errs, fmt.Errorf("remove the service container %s: %w", name, err))
			}
		}
		if madeNetwork {
			if out, err := r.docker(cctx, "network", "rm", network); err != nil && !dind.Missing(out) {
				errs = append(errs, fmt.Errorf("remove the network %s: %w", network, err))
			}
		}
		return errors.Join(errs...)
	}
	if req.Network != "" && req.Network != "none" {
		return "", remove, fmt.Errorf("a run with services has a network of its own, not %q", req.Network)
	}
	limits, err := r.serviceLimits()
	if err != nil {
		return "", remove, err
	}
	seen := map[string]bool{}
	for _, s := range req.Services {
		if !serviceName.MatchString(s.Name) || s.Image == "" || !ValidImage(s.Image) || seen[s.Name] {
			return "", remove, fmt.Errorf("the service %q needs a unique name of lower-case letters, digits and dashes, and an image", s.Name)
		}
		seen[s.Name] = true
	}
	labels := make([]string, 0, 2*len(req.Labels))
	for _, l := range req.Labels {
		labels = append(labels, "--label", l)
	}
	args := append([]string{"network", "create"}, labels...)
	if req.Network == "none" {
		args = append(args, "--internal")
	}
	if _, err := r.docker(ctx, append(args, network)...); err != nil {
		return "", remove, err
	}
	madeNetwork = true
	for _, s := range req.Services {
		name := req.Name + "-" + s.Name
		args := append([]string{"run", "-d", "--init", "--name", name, "--network", network, "--network-alias", s.Name}, labels...)
		args = append(args, dind.LimitArgs(limits.CPUs, limits.MemoryBytes, limits.PIDs)...)
		args = append(args, LogFlags()...)
		env := make([]string, 0, len(s.Env))
		for k, v := range s.Env {
			env = append(env, k+"="+v)
		}
		slices.Sort(env)
		for _, kv := range env {
			args = append(args, "-e", kv)
		}
		if s.Health != "" {
			args = append(args, "--health-cmd", s.Health, "--health-interval", "1s", "--health-timeout", "3s", "--health-retries", "60")
		}
		// A disposable folder where the emulator keeps its data: an
		// anonymous volume, which the removal's -v takes with the container.
		if s.Data != "" {
			args = append(args, "-v", s.Data)
		}
		made = append(made, name)
		args = append(args, s.EntrypointFlag()...)
		if _, err := r.docker(ctx, append(append(args, s.Image), s.RunArgs()...)...); err != nil {
			return "", remove, fmt.Errorf("the service %s: %w", s.Name, err)
		}
	}
	for _, s := range req.Services {
		if err := r.serviceReady(ctx, req.Name+"-"+s.Name); err != nil {
			return "", remove, fmt.Errorf("the service %s: %w", s.Name, err)
		}
	}
	return network, remove, nil
}

// serviceReady waits until a service container runs, and is healthy if it
// declares a health check.
func (r Runner) serviceReady(ctx context.Context, name string) error {
	var failed error
	ready := poll.Until(ctx, serviceReadiness, time.Second, func() bool {
		out, err := r.docker(ctx, "inspect", "--type", "container", "--format", `{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}`, name)
		if err != nil {
			failed = err
			return true
		}
		switch state := strings.TrimSpace(string(out)); state {
		case "running healthy", "running none":
			return true
		case "running starting", "created none", "created starting":
			return false
		default:
			if strings.HasPrefix(state, "exited ") || strings.HasPrefix(state, "dead ") {
				failed = r.serviceExited(ctx, name)
				return true
			}
			failed = fmt.Errorf("it is %s", state)
			return true
		}
	})
	switch {
	case failed != nil:
		return failed
	case ready:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	}
	return fmt.Errorf("it did not become ready within %s", serviceReadiness)
}

// serviceExitLog bounds what an exited service's log adds to its error.
const serviceExitLog = 400

// serviceExited says why a service stopped during its start: its exit code
// and the last lines of its log, bounded ("exited 1: failed to init posix
// backend: chdir /data: …"). What could not be read is said, not guessed.
func (r Runner) serviceExited(ctx context.Context, name string) error {
	code, err := r.docker(ctx, "inspect", "--type", "container", "--format", "{{.State.ExitCode}}", name)
	if err != nil {
		return fmt.Errorf("it exited, and its exit code could not be read: %w", err)
	}
	what := "it exited " + strings.TrimSpace(string(code))
	logs, err := r.docker(ctx, "logs", "--tail", "5", name)
	if err != nil {
		return fmt.Errorf("%s, and its log could not be read: %w", what, err)
	}
	var lines []string
	for _, line := range strings.Split(string(logs), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return fmt.Errorf("%s with nothing in its log", what)
	}
	return fmt.Errorf("%s: %s", what, strs.Truncate(strings.Join(lines, " / "), serviceExitLog))
}

// docker runs the Docker CLI; a failure carries its output.
func (r Runner) docker(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.dockerBinary(), args...)
	configureCancellation(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
