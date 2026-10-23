// Package dind runs private Docker daemons: one privileged docker:dind
// container per owner, reached as dind on the owner's own network, with the
// owner's work tree at the same path (/work) as in the owner's container.
// A chat on a worker keeps one for its session (internal/console/pi); an
// agent task on a worker has one from its start to its exit
// (internal/worker; design/operations/workers.md, "Private Docker on the
// worker").
//
// Every daemon has the same DNS and mirror policy (DaemonOptions,
// BuildkitConfig) and its owner's limits (LimitArgs).
//
// Every container here carries its owner in a label, and nothing with
// another owner is stopped or removed. A stop or removal is confirmed by
// asking Docker again; one it cannot confirm is an error.
package dind

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/trust"
)

// Run runs one docker command and answers its combined output; a failed
// command is an error, with its output.
type Run func(ctx context.Context, args ...string) ([]byte, error)

// Missing reports Docker's own answer that there is no such object: the
// one classifier of "gone" for every Docker command the apptown runs. Only an
// answer from the daemon or the CLI counts. Anything else, a daemon that
// could not be asked included, is not absence: "no such file or directory"
// from a missing socket also says "no such", and once let a cleanup that
// never reached Docker record its containers removed.
func Missing(out []byte) bool {
	s := strings.ToLower(string(out))
	// `docker inspect` prints its "[]" before the CLI's own answer, so the
	// answer is looked for on any line.
	cliAnswer := false
	for _, l := range strings.Split(s, "\n") {
		cliAnswer = cliAnswer || strings.HasPrefix(strings.TrimSpace(l), "error: no such")
	}
	if !strings.Contains(s, "error response from daemon") && !cliAnswer {
		return false
	}
	for _, missing := range []string{"no such container", "no such network", "no such volume", "no such object", "no such image"} {
		if strings.Contains(s, missing) {
			return true
		}
	}
	return strings.Contains(s, "network") && strings.Contains(s, "not found")
}

// LimitArgs are a container's CPU, memory and PID limits. Memory includes
// swap: the limit is the memory there is. A caller refuses a zero first:
// zero is never "unlimited".
func LimitArgs(cpus float64, memoryBytes, pids int64) []string {
	memory := strconv.FormatInt(memoryBytes, 10)
	return []string{"--cpus", strconv.FormatFloat(cpus, 'f', -1, 64), "--memory", memory, "--memory-swap", memory,
		"--pids-limit", strconv.FormatInt(pids, 10)}
}

// Daemon is one private daemon's container.
type Daemon struct {
	Name, Network, Image string
	// OwnerLabel=Owner is on the container; nothing is changed that
	// carries another owner.
	OwnerLabel, Owner string
	// Args are the owner's own docker run flags: its labels, mounts and
	// limits.
	Args []string
	// Options are the daemon's own flags: DNS, search domains, the mirror.
	Options []string
}

// Create runs the daemon's container. Its plain-TCP listener is reached
// only on the owner's network.
func (d Daemon) Create(ctx context.Context, run Run) error {
	args := append([]string{"run", "-d", "--privileged", "--name", d.Name, "--network", d.Network, "--network-alias", "dind",
		"--label", d.OwnerLabel + "=" + d.Owner,
		// ZFS_MODULE_TIMEOUT=0: containerd's zfs probe would otherwise stall
		// the daemon's start on a ZFS host; --tls=false: dockerd pauses
		// fifteen seconds for a plain-TCP listener unless told.
		"-e", "DOCKER_TLS_CERTDIR=", "-e", "ZFS_MODULE_TIMEOUT=0",
		// The daemon shares its memory budget with the containers it runs.
		// When they reach it, the kernel kills by size, and may kill a
		// second process before the first's memory is back: at 0 that could
		// be dockerd, and the owner's Docker is gone with its work. At -500
		// the daemon (and containerd, its child) is the last choice; the
		// containers it starts run at Docker's default, 0.
		"--oom-score-adj", "-500",
		// The daemon's run state (its pid files, containerd's, sockets) is
		// a tmpfs, gone when the container stops. A daemon stopped before it
		// finished (docker stop's grace ran out) otherwise left its pid
		// files, and on the next start a new process with the same pid in
		// the fresh namespace read as the old one still running: containerd
		// refused to start and the owner's Docker never came back.
		"--tmpfs", "/run"}, d.Args...)
	_, err := run(ctx, append(args, Start(d.Image, append([]string{"--tls=false"}, d.Options...)...)...)...)
	return err
}

// Start is the end of a private daemon's docker run arguments: the image,
// started so that it first trusts the extra certificates this machine's
// containers are given (internal/trust), then runs the image's own
// entrypoint with the daemon's flags. It runs as root, so they join its
// system store, which dockerd reads for its pulls.
func Start(image string, daemonFlags ...string) []string {
	args := append(trust.ContainerArgs(true), "--entrypoint", "sh", image, "-c", trust.Run+`; exec dockerd-entrypoint.sh "$@"`, "sh")
	return append(args, daemonFlags...)
}

// BuilderContainer is the container the buildx builder apptown runs in,
// inside the private daemon (driver docker-container, its one node).
const BuilderContainer = "buildx_buildkit_apptown0"

// TrustBuilder gives the builder apptown, just created in the private daemon
// name, the extra certificates: buildkit pulls base images itself, with its
// own container's roots. It starts the builder, runs the script in its
// container (as root, into its system store) and restarts it, so buildkit
// reads them, before any build. Nothing when there is no script.
func TrustBuilder(ctx context.Context, run Run, name, clientDir string) error {
	if len(trust.ContainerArgs(true)) == 0 {
		return nil
	}
	inner := []string{"exec", "-e", "DOCKER_HOST=tcp://dind:2375", "-e", "DOCKER_CONFIG=" + clientDir, name, "docker"}
	for _, step := range [][]string{
		{"buildx", "inspect", "--bootstrap", "apptown"},
		{"exec", "-e", trust.EnvVar, BuilderContainer, "sh", "-c", trust.Run},
		{"restart", BuilderContainer},
	} {
		if out, err := run(ctx, append(append([]string{}, inner...), step...)...); err != nil {
			return fmt.Errorf("giving the private Docker builder the extra certificates (%s): %w: %s", strings.Join(step[:2], " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// Ready waits, at most 40 seconds, until the daemon answers.
func Ready(ctx context.Context, run Run, name string) error {
	// Every probe is bounded too: a stuck docker exec must not consume the
	// whole wait, or outlive its cancellation.
	ready, stop := context.WithTimeout(ctx, 40*time.Second)
	defer stop()
	for {
		probe, end := context.WithTimeout(ready, 3*time.Second)
		_, err := run(probe, "exec", "-e", "DOCKER_HOST=tcp://localhost:2375", name, "docker", "info")
		end()
		if err == nil {
			return nil
		}
		select {
		case <-ready.Done():
			return fmt.Errorf("private Docker did not become ready: %w (%v)", ready.Err(), err)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// Builder makes the buildx builder apptown, the default, in the client state
// at clientDir (a path in the daemon's container, shared with the owner's
// container), with the buildkit configuration at config. network=host puts
// buildkit in the daemon's own network namespace, where its DNS answers;
// builds load by default, so `docker build -t app .; docker run app` works.
// One already there is kept.
func Builder(ctx context.Context, run Run, name, clientDir, config string) error {
	prefix := []string{"exec", "-e", "DOCKER_HOST=tcp://dind:2375", "-e", "DOCKER_CONFIG=" + clientDir, name, "docker", "buildx"}
	if _, err := run(ctx, append(append([]string{}, prefix...), "inspect", "apptown")...); err == nil {
		return nil
	}
	if _, err := run(ctx, append(prefix, "create", "--name", "apptown", "--driver", "docker-container",
		"--driver-opt", "network=host", "--driver-opt", "default-load=true",
		"--config", config, "--use", "tcp://dind:2375")...); err != nil {
		return fmt.Errorf("preparing private Docker builder: %w", err)
	}
	return TrustBuilder(ctx, run, name, clientDir)
}

// State reads whether an owner's container exists and runs. One with
// another owner is an error.
func State(ctx context.Context, run Run, name, ownerLabel, owner string) (exists, running bool, err error) {
	out, err := run(ctx, "inspect", "--type", "container", "--format", `{{index .Config.Labels "`+ownerLabel+`"}} {{.State.Running}}`, name)
	if err != nil && Missing(out) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	f := strings.Fields(string(out))
	if len(f) != 2 || f[0] != owner {
		return false, false, fmt.Errorf("private Docker resource %s has a different owner", name)
	}
	return true, f[1] == "true", nil
}

// Stop stops an owner's container and confirms it has stopped. One that
// does not exist is stopped.
func Stop(ctx context.Context, run Run, name, ownerLabel, owner string) error {
	return settle(ctx, run, name, ownerLabel, owner, []string{"stop", "--time", "3", name}, false)
}

// Remove removes an owner's container with its anonymous volumes (a
// daemon's images and containers), and confirms it is gone.
func Remove(ctx context.Context, run Run, name, ownerLabel, owner string) error {
	return settle(ctx, run, name, ownerLabel, owner, []string{"rm", "-f", "-v", name}, true)
}

func settle(ctx context.Context, run Run, name, ownerLabel, owner string, command []string, gone bool) error {
	exists, _, err := State(ctx, run, name, ownerLabel, owner)
	if err != nil || !exists {
		return err
	}
	if _, err := run(ctx, command...); err != nil {
		return err
	}
	exists, running, err := State(ctx, run, name, ownerLabel, owner)
	switch {
	case err != nil:
		return fmt.Errorf("confirming %s %s: %w", name, command[0], err)
	case gone && exists, running:
		return fmt.Errorf("docker did not confirm %s %s", name, map[bool]string{true: "removed", false: "stopped"}[gone])
	}
	return nil
}
