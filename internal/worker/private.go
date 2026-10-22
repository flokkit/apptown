package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/dockerlabel"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/dind"
	"github.com/flokkit/apptown/internal/execution"
)

// A task's private Docker, from the agent's start to its exit
// (design/operations/workers.md, "Private Docker on the worker"): a network, the services
// its request froze, and a private daemon, all under names recorded with
// the task before any exists.
//
//   - The network is made when the task is ensured, so its container can
//     join it; the services and the daemon when it first starts, and only
//     then: a task that has started is never given them again. What a
//     failed start made is removed, and the start refused with the reason.
//   - When the task's container has exited, the daemon and the services are
//     stopped, and Docker asked again that they have. Only then is the task
//     reported exited, and its result files captured; until then it reads
//     as running.
//   - A running task whose daemon or services are gone is reported absent:
//     lost, never made again.
//   - Release removes them all, and confirms; the sweep stops the task and
//     removes them all once its admission has expired, whether or not the
//     AppTown is there.

// serviceBudget is every private service's budget, beside the task's own:
// the worker's -service-* limits, by default execution.DefaultServiceLimits,
// the same as a check's services get.
func (s *Server) serviceBudget() execution.Limits {
	if s.ServiceLimits == (execution.Limits{}) {
		return execution.DefaultServiceLimits
	}
	return s.ServiceLimits
}

const (
	privateClass = dockerlabel.Class + "=private-docker"
	taskLabel    = dockerlabel.Task
	// dockerClient is where a task with a daemon keeps its Docker client
	// state and the builder's (DOCKER_CONFIG), in its workspace's state.
	dockerClient = execution.StateRoot + "/docker"
)

// privateSet is a task's private Docker as its record keeps it, from its
// ensure: what to make, and the names to make it under.
type privateSet struct {
	Spec     execution.PrivateDocker `json:"spec"`
	Limits   execution.Limits        `json:"limits"`
	Expires  time.Time               `json:"expires"`
	Network  string                  `json:"network"`
	Daemon   string                  `json:"daemon,omitempty"`
	Services []string                `json:"services,omitempty"`
	// Quiet: after the task exited, everything beside it was confirmed
	// stopped; its results may be captured.
	Quiet bool `json:"quiet,omitempty"`
}

// privateSetFor names a task's private resources: its worker's and its
// key's, so no other worker or task shares a name.
func (s *Server) privateSetFor(key string, req TaskRequest) *privateSet {
	prefix := "apptown-private-" + strings.TrimPrefix(s.CheckOwnerLabel(), dockerlabel.Worker+"=") + "-" + key
	p := &privateSet{Spec: *req.Private, Limits: req.Admission.Limits, Expires: req.Admission.Expires, Network: prefix}
	if p.Spec.Daemon {
		p.Daemon = prefix + "-dind"
	}
	for _, svc := range p.Spec.Services {
		p.Services = append(p.Services, prefix+"-"+svc.Name)
	}
	return p
}

// labels are on every private resource of a task.
func (s *Server) privateLabels(key string, p *privateSet) []string {
	labels := []string{"--label", privateClass, "--label", s.CheckOwnerLabel(), "--label", taskLabel + "=" + key}
	if !p.Expires.IsZero() {
		labels = append(labels, "--label", dockerlabel.Expires+"="+p.Expires.UTC().Format(time.RFC3339))
	}
	return labels
}

func (s *Server) privateDir(key string) string { return filepath.Join(s.State, "task-docker", key) }

// command runs docker for private resources; a failure carries its output.
func (s *Server) command(ctx context.Context, args ...string) ([]byte, error) {
	dk := s.Docker
	if dk == nil {
		dk = docker.CLI{}
	}
	out, err := dk.Run(ctx, "", docker.Command{Args: args})
	if err != nil {
		return out, fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// lockTask serializes a task's start, stop before capture, and release.
// It is taken before s.mu, never while holding it.
func (s *Server) lockTask(key string) func() {
	s.mu.Lock()
	if s.taskLocks == nil {
		s.taskLocks = map[string]*sync.Mutex{}
	}
	m := s.taskLocks[key]
	if m == nil {
		m = &sync.Mutex{}
		s.taskLocks[key] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// makeNetwork makes a task's network, or finds it made. The caller holds
// s.mu.
func (s *Server) makeNetwork(ctx context.Context, key string, p *privateSet) error {
	out, err := s.command(ctx, "network", "inspect", "--format", `{{index .Labels "`+taskLabel+`"}}`, p.Network)
	switch {
	case err == nil && strings.TrimSpace(string(out)) == key:
		return nil
	case err == nil:
		return fmt.Errorf("the network %s belongs to another task", p.Network)
	case !dind.Missing(out):
		return err
	}
	_, err = s.command(ctx, append(append([]string{"network", "create"}, s.privateLabels(key, p)...), p.Network)...)
	return err
}

// startPrivate makes the services and the daemon of a task that has never
// started. One that runs is left as it is (a repeated start); one that has
// exited, or whose stop was confirmed, is refused. Anything a failed start
// made is removed, and the start refused with the reason.
func (s *Server) startPrivate(ctx context.Context, rec record) error {
	key, p := rec.Key, rec.Private
	obs, err := s.Tasks.Observe(ctx, execution.TaskRef{Key: key})
	switch {
	case err != nil:
		return err
	case obs.State == execution.TaskRunning && !p.Quiet:
		return nil
	case obs.State != execution.TaskCreated || p.Quiet:
		return fmt.Errorf("%w: the task has %s, and its private Docker is never made again", execution.ErrStartRefused, obs.State)
	case !p.Expires.IsZero() && !s.now().Before(p.Expires):
		return fmt.Errorf("%w: %w", execution.ErrStartRefused, execution.ErrExpired)
	}
	// Made only on the profile it was admitted on: a Docker upgraded since
	// its ensure refuses it.
	if err := s.sameProfile(ctx, p.Spec.Profile, p.Spec.Daemon); errors.Is(err, errProfileChanged) {
		return fmt.Errorf("%w: %w", execution.ErrStartRefused, err)
	} else if err != nil {
		return err
	}
	// What an earlier start made before it failed, or before the worker
	// stopped, goes first: the task has never run, so nothing of it is lost.
	err = s.removeContainers(ctx, key, p)
	if err == nil {
		err = s.makePrivate(ctx, rec)
	}
	if err != nil {
		if cleanup := s.removeContainers(ctx, key, p); cleanup != nil {
			err = fmt.Errorf("%w; removing what was made: %v", err, cleanup)
		}
		return fmt.Errorf("%w: its private Docker: %w", execution.ErrStartRefused, err)
	}
	return nil
}

func (s *Server) makePrivate(ctx context.Context, rec record) error {
	key, p := rec.Key, rec.Private
	abandonAt("network", key)
	labels := s.privateLabels(key, p)
	for i, svc := range p.Spec.Services {
		args := append([]string{"run", "-d", "--init", "--name", p.Services[i], "--network", p.Network, "--network-alias", svc.Name}, labels...)
		args = append(args, limitArgs(s.serviceBudget())...)
		args = append(args, execution.LogFlags()...)
		env := make([]string, 0, len(svc.Env))
		for k, v := range svc.Env {
			env = append(env, k+"="+v)
		}
		slices.Sort(env)
		for _, kv := range env {
			args = append(args, "-e", kv)
		}
		if svc.Health != "" {
			args = append(args, "--health-cmd", svc.Health, "--health-interval", "2s", "--health-timeout", "3s", "--health-retries", "30")
		}
		// A need's disposable data folder: an anonymous volume, which the
		// removal (dind.Remove: rm -f -v) takes with the container.
		if svc.Data != "" {
			args = append(args, "-v", svc.Data)
		}
		args = append(args, svc.EntrypointFlag()...)
		if _, err := s.command(ctx, append(append(args, svc.Image), svc.RunArgs()...)...); err != nil {
			return fmt.Errorf("the service %s: %w", svc.Name, err)
		}
	}
	for i, svc := range p.Spec.Services {
		if err := s.healthy(ctx, p.Services[i]); err != nil {
			return fmt.Errorf("the service %s: %w", svc.Name, err)
		}
	}
	if !p.Spec.Daemon {
		return nil
	}
	abandonAt("services", key)
	client := filepath.Join(s.statePath(rec.Workspace), "docker")
	for _, dir := range []string{s.privateDir(key), client} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(s.privateDir(key), "buildkitd.toml"), []byte(dind.BuildkitConfig(p.Spec.Mirror)), 0o644); err != nil {
		return err
	}
	// The daemon has the task's limits, and sees its tree at the same path
	// (/work), its client state, and its builder's configuration: no cache,
	// socket or other folder of the worker's.
	daemon := dind.Daemon{Name: p.Daemon, Network: p.Network, Image: s.DindImage, OwnerLabel: taskLabel, Owner: key,
		Options: dind.DaemonOptions(p.Spec.Mirror),
		Args: append(append(append(slices.Clone(labels), limitArgs(p.Limits)...), execution.LogFlags()...),
			"-v", s.workspacePath(rec.Workspace)+":/work", "-v", client+":"+dockerClient, "-v", s.privateDir(key)+":/forge-dind:ro")}
	if err := daemon.Create(ctx, s.command); err != nil {
		return err
	}
	if err := dind.Ready(ctx, s.command, p.Daemon); err != nil {
		return err
	}
	return dind.Builder(ctx, s.command, p.Daemon, dockerClient, "/forge-dind/buildkitd.toml")
}

// limitArgs are a container's limits (dind.LimitArgs).
func limitArgs(l execution.Limits) []string { return dind.LimitArgs(l.CPUs, l.MemoryBytes, l.PIDs) }

// healthy waits, at most a minute, until a service runs, and is healthy if
// it declares a health check.
func (s *Server) healthy(ctx context.Context, name string) error {
	deadline := s.now().Add(time.Minute)
	for {
		out, err := s.command(ctx, "inspect", "--type", "container", "--format", `{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}`, name)
		if err != nil {
			return err
		}
		switch state := strings.TrimSpace(string(out)); state {
		case "running healthy", "running none":
			return nil
		case "running starting":
		default:
			return fmt.Errorf("it is %s", state)
		}
		if s.now().After(deadline) {
			return errors.New("it did not become healthy within a minute")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// containers are a task's private containers: its daemon, then its
// services.
func (p *privateSet) containers() []string {
	if p.Daemon == "" {
		return p.Services
	}
	return append([]string{p.Daemon}, p.Services...)
}

// removeContainers removes a task's daemon (with its storage) and services,
// and confirms they are gone.
func (s *Server) removeContainers(ctx context.Context, key string, p *privateSet) error {
	var errs []error
	for _, name := range p.containers() {
		errs = append(errs, dind.Remove(ctx, s.command, name, taskLabel, key))
	}
	return errors.Join(errs...)
}

// removePrivate removes everything of a task's private Docker, its network
// last, and its builder's configuration.
func (s *Server) removePrivate(ctx context.Context, key string, p *privateSet) error {
	if err := s.removeContainers(ctx, key, p); err != nil {
		return err
	}
	out, err := s.command(ctx, "network", "inspect", "--format", `{{index .Labels "`+taskLabel+`"}}`, p.Network)
	switch {
	case err != nil && !dind.Missing(out):
		return err
	case err == nil && strings.TrimSpace(string(out)) != key:
		return fmt.Errorf("the network %s belongs to another task", p.Network)
	case err == nil:
		if out, err := s.command(ctx, "network", "rm", p.Network); err != nil && !dind.Missing(out) {
			return err
		}
	}
	return os.RemoveAll(s.privateDir(key))
}

// quiet stops everything beside a task that has exited, confirms it, and
// records that it has: from then on its files change no more.
func (s *Server) quiet(ctx context.Context, key string) error {
	unlock := s.lockTask(key)
	defer unlock()
	return s.quietLocked(ctx, key)
}

// quietLocked is quiet for a caller holding the task's lock.
func (s *Server) quietLocked(ctx context.Context, key string) error {
	rec, found, err := s.readRecord(key)
	if err != nil || !found || rec.Private == nil || rec.Private.Quiet {
		return err
	}
	abandonAt("exited", key)
	var errs []error
	for _, name := range rec.Private.containers() {
		errs = append(errs, dind.Stop(ctx, s.command, name, taskLabel, key))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec.Private.Quiet = true
	return s.writeRecord(rec)
}

// privateWriters are the tasks in a workspace whose private Docker is not
// confirmed stopped. The caller holds s.mu.
func (s *Server) privateWriters(workspace string) ([]string, error) {
	keys, err := s.tasksIn(workspace)
	if err != nil {
		return nil, err
	}
	var writers []string
	for _, key := range keys {
		rec, found, err := s.readRecord(key)
		if err != nil {
			return nil, err
		}
		if found && rec.Private != nil && !rec.Private.Quiet {
			writers = append(writers, key)
		}
	}
	return writers, nil
}

// observeTask is a task as AppTown is told of it. A task with private
// Docker has exited only once everything beside it is confirmed stopped;
// until then it is running. One running whose private containers are gone
// has lost them: it is reported absent, and they are not made again.
func (s *Server) observeTask(ctx context.Context, ref execution.TaskRef) (execution.TaskObservation, error) {
	obs, err := s.Tasks.Observe(ctx, ref)
	if err != nil || (obs.State != execution.TaskExited && obs.State != execution.TaskRunning) {
		return obs, err
	}
	rec, found, err := s.readRecord(ref.Key)
	if err != nil || !found || rec.Private == nil {
		return obs, err
	}
	if obs.State == execution.TaskExited {
		if err := s.quiet(ctx, ref.Key); err != nil {
			log.Printf("worker: task %s exited; stopping its private Docker: %v (it reads as running until confirmed)", ref.Key, err)
			obs.State = execution.TaskRunning
		}
		return obs, nil
	}
	for _, name := range rec.Private.containers() {
		exists, _, err := dind.State(ctx, s.command, name, taskLabel, ref.Key)
		if err != nil {
			return execution.TaskObservation{}, fmt.Errorf("%w: its private Docker: %w", execution.ErrTaskUnobservable, err)
		}
		if !exists {
			log.Printf("worker: task %s runs without its private container %s: it is lost", ref.Key, name)
			return execution.TaskObservation{State: execution.TaskAbsent, ExitCode: -1}, nil
		}
	}
	return obs, nil
}

// SweepPrivate stops the tasks whose private Docker's admission has
// expired, and removes their private resources, whether or not AppTown
// is there. Their records stay, for AppTown to collect and release. A
// removal that fails is logged and tried again by the next sweep.
func (s *Server) SweepPrivate(ctx context.Context) {
	expired := map[string]bool{}
	for _, list := range [][]string{{"ps", "-a"}, {"network", "ls"}} {
		out, err := s.command(ctx, append(list, "--filter", "label="+privateClass, "--filter", "label="+s.CheckOwnerLabel(),
			"--format", `{{.Label "`+taskLabel+`"}} {{.Label "`+dockerlabel.Expires+`"}}`)...)
		if err != nil {
			log.Printf("private Docker sweep: %v", err)
			return
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			key, at, _ := strings.Cut(line, " ")
			if t, err := time.Parse(time.RFC3339, at); err == nil && keyPattern.MatchString(key) && s.now().After(t) {
				expired[key] = true
			}
		}
	}
	for key := range expired {
		unlock := s.lockTask(key)
		rec, found, err := s.readRecord(key)
		if err == nil && found && rec.Private != nil {
			err = errors.Join(s.Tasks.Stop(ctx, execution.TaskRef{Key: key}), s.removePrivate(ctx, key, rec.Private))
		}
		unlock()
		if err != nil {
			log.Printf("private Docker sweep: task %s: %v", key, err)
		}
	}
}
