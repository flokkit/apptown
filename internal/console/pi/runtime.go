// Package pi runs a console's agent for real: pi's interactive TUI inside a
// Docker container, under tmux, with forged holding a disposable tmux client
// over a host PTY. It implements console.Agent.
//
// Each session is a container created WITHOUT --rm whose PID1 is `sleep
// infinity`; pi runs under `tmux new -A` via /console/run-pi.sh with a pinned
// --session-id, so the same command creates the conversation on first launch
// and resumes it on every relaunch. forged's terminal connections are views,
// not the terminal itself: a client dying (restart, crash) leaves the tmux
// server and pi untouched inside the container.
//
// The ladder: attached → detached (PTY held, no viewers) → stopped (docker
// stop: filesystem intact, zero runtime cost) → removed.
package pi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flokkit.io/forge/internal/dockerlabel"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/dind"
	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/internal/strs"
)

// Config is what the runtime needs from the forge it runs in.
type Config struct {
	// Run executes one docker command against the forge's own daemon.
	Run func(ctx context.Context, args ...string) ([]byte, error)
	// Image is the agent image every console is created from; a moved
	// image makes existing containers stale. A func: the forge learns it
	// after construction.
	Image func() string
	// Dind configures the private Docker daemon a change console gets.
	Dind DindConfig
	// Now is the runtime's clock: it stamps container labels and session
	// headers and times the recheck of a running container. Nil is the
	// real clock.
	Now func() time.Time
	// Binary is the docker binary for what runs outside Run: the terminal's
	// client and Script. Empty means "docker".
	Binary string
	// CachePerSession gives each session its own cache volume, labelled
	// with it and removed with its runtime (a worker's chats, which share
	// no writable cache but the worker's tool cache, Mounts). False: every
	// session shares forge-cache.
	CachePerSession bool
	// Limits, when set, are each of a session's container's, its private
	// daemon's and its script's (a worker's chats); what the daemon runs is
	// inside the daemon's. A zero is refused, never "unlimited". Nil: none.
	Limits *Limits
	// Mounts are every session's container's besides its own (a worker's
	// shared tool cache, worker/tool_cache.go). Its daemon and its script
	// get none of them.
	Mounts []string
	// LocalTLS, when set, is the forge's own local HTTPS (forged
	// -local-tls): each chat gets the authority's certificate and the
	// prelude that trusts it and reaches the forge's addresses.
	LocalTLS *console.LocalTLS
}

// CLI is a Config.Run for a program with no daemon scope of its own
// (forge-console, forge-worker): the docker binary, run directly against
// the daemon its environment names, with stdout and stderr together.
func CLI(binary string) func(ctx context.Context, args ...string) ([]byte, error) {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, binary, args...).CombinedOutput()
	}
}

// Limits are one container's CPU, memory and PID limits.
type Limits struct {
	CPUs        float64
	MemoryBytes int64
	PIDs        int64
}

// Runtime is the pi-in-Docker agent runtime, built once.
type Runtime struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	sessions map[string]*state
}

// state is what the runtime holds for one session between calls: the PTY
// and its viewers.
type state struct {
	attachMu      sync.Mutex // single-flight for attaching
	mu            sync.Mutex
	ptmx          *os.File
	attachCmd     *exec.Cmd
	ring          []byte
	clients       map[*view]chan []byte
	dockerChecked time.Time
}

const ringSize = 256 * 1024

func New(cfg Config) *Runtime {
	ctx, cancel := context.WithCancel(context.Background())
	if cfg.Binary == "" {
		cfg.Binary = "docker"
	}
	cfg.Now = clock.Or(cfg.Now)
	return &Runtime{cfg: cfg, ctx: ctx, cancel: cancel, sessions: map[string]*state{}}
}

// created is a container's forge.created label: the runtime's clock, UTC.
func (r *Runtime) created() string { return r.cfg.Now().UTC().Format(time.RFC3339) }

// Close cancels every control operation for a process stop. Containers and
// session files are untouched; the next process resumes them.
func (r *Runtime) Close() { r.cancel() }

func (r *Runtime) state(id string) *state {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.sessions[id]
	if st == nil {
		st = &state{clients: map[*view]chan []byte{}}
		r.sessions[id] = st
	}
	return st
}

// ContainerName is the session's container.
func ContainerName(id string) string { return "forge-console-" + id }

// --- docker ----------------------------------------------------------------------

func (r *Runtime) command(ctx context.Context, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out, err := r.cfg.Run(ctx, args...)
	if err != nil {
		return out, fmt.Errorf("docker %s: %w: %s", args[0], err, strs.Truncate(strings.TrimSpace(string(out)), 500))
	}
	return out, nil
}

// bounded control commands share process cancellation with sidecar readiness.
func (r *Runtime) docker(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()
	return r.command(ctx, args...)
}

var missingDockerObject = dind.Missing

// inspection is what the runtime reads off a session's container.
type inspection struct {
	exists, running bool
	everStarted     bool
	image           string
	dind            string // the forge.console.dind label: "0" | "1"
	model           string // the forge.console.model label
	mounts          string // the forge.console.mounts label
}

func (r *Runtime) inspect(id string) (inspection, error) {
	// The labels come as JSON: an empty label (a launch with no model) must
	// not shift the fields after it.
	out, err := r.docker("inspect", "-f", `{{.State.Running}} {{.State.StartedAt}} {{.Image}} {{json .Config.Labels}}`, ContainerName(id))
	if err != nil {
		if missingDockerObject(out) {
			return inspection{}, nil
		}
		return inspection{}, err
	}
	f := strings.SplitN(strings.TrimSpace(string(out)), " ", 4)
	in := inspection{exists: true}
	if len(f) > 0 {
		in.running = f[0] == "true"
	}
	if len(f) > 1 {
		in.everStarted = !strings.HasPrefix(f[1], "0001-")
	}
	if len(f) > 2 {
		in.image = f[2]
	}
	if len(f) > 3 {
		var labels map[string]string
		if err := json.Unmarshal([]byte(f[3]), &labels); err != nil {
			return inspection{}, fmt.Errorf("reading %s's labels: %w", ContainerName(id), err)
		}
		in.dind, in.model, in.mounts = labels[dockerlabel.ConsoleDind], labels[dockerlabel.ConsoleModel], labels[dockerlabel.ConsoleMounts]
	}
	return in, nil
}

// image is what a launch's container is created from.
func (r *Runtime) image(in console.Launch) string {
	if in.Image != "" {
		return in.Image
	}
	return r.cfg.Image()
}

// mounts are the extra bind mounts a launch's container is created with.
func (r *Runtime) mounts(in console.Launch) []string {
	return append(slices.Clone(r.cfg.Mounts), in.Mounts...)
}

// mountsDigest names a launch's extra mounts in a label: mount specs carry
// host paths, too long and too revealing for a label.
func (r *Runtime) mountsDigest(in console.Launch) string {
	sum := sha256.Sum256([]byte(strings.Join(r.mounts(in), "\n")))
	return hex.EncodeToString(sum[:8])
}

func dindMode(in console.Launch) string {
	if in.Docker {
		return "1"
	}
	return "0"
}

// --- console.Agent -----------------------------------------------------------------

// Stale: the container pins the image it was created from, the model its
// run script names, the Docker mode its network was built for and its bind
// mounts; if any moved, it should be rebuilt when safe. A worker sends the
// forge CLI as a content-named blob, so a new CLI is a new mount that only a
// rebuilt container sees.
func (r *Runtime) Stale(ctx context.Context, in console.Launch) (bool, error) {
	cur, err := r.inspect(in.ID)
	if err != nil || !cur.exists {
		return false, err
	}
	if cur.dind != dindMode(in) {
		return true, nil
	}
	if cur.model != in.Model {
		return true, nil
	}
	if cur.mounts != r.mountsDigest(in) {
		return true, nil
	}
	if img, err := r.docker("image", "inspect", "-f", "{{.Id}}", r.image(in)); err == nil && strings.TrimSpace(string(img)) != cur.image {
		return true, nil
	}
	return false, nil
}

// Status is the runtime's view of the session's agent.
func (r *Runtime) Status(ctx context.Context, id string) (console.Status, error) {
	cur, err := r.inspect(id)
	if err != nil {
		return console.Status{}, err
	}
	sidecar, sidecarRunning, err := r.sidecar(ctx, id)
	if err != nil {
		return console.Status{}, err
	}
	script, err := r.scriptExists(id)
	if err != nil {
		return console.Status{}, err
	}
	st := r.state(id)
	st.mu.Lock()
	attached := st.ptmx != nil
	st.mu.Unlock()
	return console.Status{Exists: cur.exists, SidecarExists: sidecar, SidecarRunning: sidecarRunning, ScriptExists: script, Running: cur.running, EverStarted: cur.everStarted, Attached: attached}, nil
}

// Start makes the agent run: provision the container if it is gone, ensure
// the private daemon if the session has one, start the container if it is
// stopped, and hold the tmux client.
func (r *Runtime) Start(ctx context.Context, in console.Launch) error {
	st := r.state(in.ID)
	st.attachMu.Lock()
	defer st.attachMu.Unlock()
	if err := r.ctx.Err(); err != nil {
		return err
	}
	st.mu.Lock()
	attached := st.ptmx != nil
	st.mu.Unlock()
	if attached && r.cfg.Now().Sub(st.dockerChecked) < 15*time.Second {
		return nil
	}
	if err := r.run(in); err != nil {
		return err
	}
	st.dockerChecked = r.cfg.Now()
	return r.attachLocked(st, in.ID)
}

// run is called under the session's attachMu.
func (r *Runtime) run(in console.Launch) error {
	cur, err := r.inspect(in.ID)
	if err != nil {
		return err
	}
	if !cur.exists {
		// Removed out of band: the conversation is the /console mount, not
		// the container; provision again from the session dir.
		if err := r.provision(in); err != nil {
			return fmt.Errorf("container %s is gone and reprovision failed: %v", ContainerName(in.ID), err)
		}
		cur = inspection{exists: true, dind: dindMode(in)}
	} else if in.Docker && cur.dind == "1" {
		if err := r.ensureDind(in); err != nil {
			return fmt.Errorf("preparing console Docker: %w", err)
		}
		if err := r.connectClient(in.ID); err != nil {
			return err
		}
	}
	if !cur.running {
		if _, err := r.docker("start", ContainerName(in.ID)); err != nil {
			return fmt.Errorf("starting container: %w", err)
		}
	}
	return nil
}

// Deliver types text into the session's pi and presses Enter, through
// tmux send-keys (raw PTY writes raced the TUI's init and doubled
// characters). The session record is the truth: keep pressing Enter until
// a user message lands past where the record stood before we typed.
func (r *Runtime) Deliver(ctx context.Context, in console.Launch, text string) error {
	rec := &console.Transcript{Dir: in.Dir}
	preFile, preSize := rec.LatestSessionFile()
	// A freshly woken container needs a beat before its tmux server answers.
	var out []byte
	var err error
	poll.Until(ctx, 7500*time.Millisecond, 1500*time.Millisecond, func() bool {
		out, err = r.command(r.ctx, "exec", ContainerName(in.ID), "tmux", "send-keys", "-t", console.TmuxSession, "-l", text)
		return err == nil
	})
	if err != nil {
		return fmt.Errorf("delivering your words: %s", strs.Truncate(string(out), 120))
	}
	if out, err := r.command(r.ctx, "exec", ContainerName(in.ID), "tmux", "send-keys", "-t", console.TmuxSession, "Enter"); err != nil {
		return fmt.Errorf("pressing enter: %s", strs.Truncate(string(out), 120))
	}
	for attempt := 0; attempt < 12; attempt++ {
		beat := 1500 * time.Millisecond
		if attempt == 0 {
			beat = 400 * time.Millisecond
		}
		if err := poll.Wait(ctx, beat); err != nil {
			return fmt.Errorf("waiting for the helper's screen to accept the message: %w", err)
		}
		if rec.UserMessageAppended(preFile, preSize) {
			return nil
		}
		_, _ = r.command(r.ctx, "exec", ContainerName(in.ID), "tmux", "send-keys", "-t", console.TmuxSession, "Enter")
	}
	return fmt.Errorf("the helper's screen never accepted the message — wait a few seconds and try again")
}

// Interrupt presses Escape in the helper's terminal: pi aborts the current
// turn and hands control back.
func (r *Runtime) Interrupt(ctx context.Context, id string) error {
	if out, err := r.command(r.ctx, "exec", ContainerName(id), "tmux", "send-keys", "-t", console.TmuxSession, "Escape"); err != nil {
		return fmt.Errorf("sending the stop: %s", strs.Truncate(string(out), 120))
	}
	return nil
}

// Stop stops the container and its private daemon; the filesystem stays.
func (r *Runtime) Stop(ctx context.Context, id string) error {
	st := r.state(id)
	st.attachMu.Lock()
	defer st.attachMu.Unlock()
	r.detach(st)
	out, err := r.docker("stop", "--time", "3", ContainerName(id))
	if err != nil && !missingDockerObject(out) {
		return err
	}
	if err := r.removeScript(id); err != nil {
		return err
	}
	return r.disposeDind(id, false)
}

// Remove takes the container, its private daemon and its network away.
func (r *Runtime) Remove(ctx context.Context, id string) error {
	st := r.state(id)
	st.attachMu.Lock()
	defer st.attachMu.Unlock()
	r.detach(st)
	if out, err := r.docker("rm", "-f", ContainerName(id)); err != nil && !missingDockerObject(out) {
		return err
	}
	if err := r.removeScript(id); err != nil {
		return err
	}
	return r.disposeDind(id, true)
}

// Script runs a shell script against the session's work tree in a new,
// short-lived container from the session's image: the work tree mounted at
// the work mount, no network, nothing else of the session. What it runs is
// the chat's own tools on the chat's own files, inside the chat's boundary,
// whether or not the chat is running. The script's standard output goes to
// stdout; its exit status is the error.
//
// A session has at most one script at a time, named for it: a script left
// running by an earlier call that was cancelled (which ends the docker
// client, not the container) is removed before the next begins, and by
// Stop and Remove, and Status reports one that exists.
func (r *Runtime) Script(ctx context.Context, in console.Launch, script string, env []string, stdout io.Writer) error {
	if err := r.removeScript(in.ID); err != nil {
		return err
	}
	limits, err := r.limitArgs()
	if err != nil {
		return err
	}
	args := []string{"run", "--rm", "-i", "--name", scriptName(in.ID), "--network", "none", "-w", console.WorkMount,
		"--label", dockerlabel.Class + "=console-script", "--label", dockerlabel.Owner + "=" + in.ID}
	args = append(append(args, limits...), writesLabel(in.Workspace)...)
	args = append(args, "-v", in.Workspace.Path+":"+console.WorkMount,
		// The work tree's owner on this machine need not be the image's user.
		"-e", "GIT_CONFIG_COUNT=1", "-e", "GIT_CONFIG_KEY_0=safe.directory", "-e", "GIT_CONFIG_VALUE_0=*")
	for _, kv := range env {
		args = append(args, "-e", kv)
	}
	args = append(args, "--", r.image(in), "sh", "-c", script)
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, r.cfg.Binary, args...)
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			// Cancelling ended the client; the container is removed here.
			if rerr := r.removeScript(in.ID); rerr != nil {
				return errors.Join(err, rerr)
			}
		}
		return &ExecError{Err: err, Stderr: strs.Truncate(strings.TrimSpace(stderr.String()), 2000)}
	}
	return nil
}

func scriptName(id string) string { return "forge-console-script-" + id }

// removeScript removes the session's script container, if there is one.
func (r *Runtime) removeScript(id string) error {
	if out, err := r.docker("rm", "-f", scriptName(id)); err != nil && !missingDockerObject(out) {
		return fmt.Errorf("removing the chat's script: %w", err)
	}
	return nil
}

// scriptExists reports whether the session's script container exists.
func (r *Runtime) scriptExists(id string) (bool, error) {
	out, err := r.docker("inspect", "--type", "container", "--format", "{{.Name}}", scriptName(id))
	if err != nil {
		if missingDockerObject(out) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ExecError is a script that failed in the container.
type ExecError struct {
	Err    error
	Stderr string
}

func (e *ExecError) Error() string { return fmt.Sprintf("%v: %s", e.Err, e.Stderr) }
func (e *ExecError) Unwrap() error { return e.Err }

// End is Remove: a runtime on this machine has nothing else to fence.
func (r *Runtime) End(ctx context.Context, id string) error { return r.Remove(ctx, id) }

func logf(format string, args ...any) { log.Printf("console: "+format, args...) }
