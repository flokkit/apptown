package execution

import (
	"context"
	"errors"
	"flokkit.io/forge/internal/dockerlabel"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/diskspace"
	"flokkit.io/forge/internal/strs"
)

// DockerTasks is the local Docker implementation of TaskDriver: a task is a
// container created under a name derived from its key, started separately,
// observed by container ID, and removed only when released. A caller that
// restarts finds it again by key or ID instead of starting another.
//
// A container carries its request's digest as a label; one found under the
// key for a different request is refused, never adopted. Start starts only a
// container that has never run, so a repeated start cannot run a task again.
//
// Admission limits map to --cpus, --memory and --pids-limit, the network
// policy to --network (none, or Docker's default bridge), and the expiry to a
// label plus a refusal to create an expired task. The wall-clock limit is the
// caller's to enforce while waiting.
type DockerTasks struct {
	Binary string
	// Now is the clock a task's admission expiry is judged on: the one that
	// wrote it. Nil is the real clock.
	Now func() time.Time
	// Daemon pins the Docker daemon every call uses (SelectDocker). Its
	// address is passed on each call, so nothing the CLI would look up again
	// can move a call to another daemon. A zero Daemon follows the CLI's own
	// selection (the worker's own daemon, tests).
	Daemon DockerSelection
	// Namespace keeps one installation's containers apart from another's on
	// a shared Docker daemon: it is part of every container name.
	Namespace string
	// Files is the folder tasks' generated files are kept in, one folder per
	// task until it is released. "" refuses a task with generated files.
	Files string
	// CallerAdmitsDisk says the caller reads this daemon's data disk before
	// its own first effect of a task (a worker, before the task's workspace
	// or record), so Ensure does not read it again. Otherwise Ensure admits
	// it before anything of the task is written (admitDisk).
	CallerAdmitsDisk bool
}

var _ TaskDriver = DockerTasks{}

// TaskState is what a backend reports about a task.
type TaskState string

const (
	TaskAbsent  TaskState = "absent" // the backend answered that there is no such task
	TaskCreated TaskState = "created"
	TaskRunning TaskState = "running"
	TaskExited  TaskState = "exited"
)

type TaskObservation struct {
	ID       string
	State    TaskState
	ExitCode int
	// OOMKilled: Docker killed the task at its memory limit. Its exit code
	// is then not its answer (a killed child in a pipeline can leave 0).
	OOMKilled bool `json:"oomKilled,omitempty"`
	// Request is the request digest the task was created for, where the
	// backend keeps it: a caller acting on a task found by its key alone
	// checks it is its own.
	Request string
}

// ErrTaskUnobservable means the backend could not be asked. It is never absence.
var ErrTaskUnobservable = errors.New("the task could not be observed")

// taskName is the container name for a task key.
func (d DockerTasks) taskName(key string) string {
	if d.Namespace == "" {
		return "forge-task-" + key
	}
	return "forge-task-" + d.Namespace + "-" + key
}

const requestLabel = dockerlabel.Request

// dockerFailure classifies a failed Docker command. The daemon's own answer
// ("Error response from daemon: ...") is authoritative. Anything else (a
// client that could not start, reach or authenticate to a daemon, or a
// cancelled call) says nothing about the task: ErrTaskUnobservable, which
// callers ask again about.
func dockerFailure(what string, err error, out string) error {
	if strings.Contains(out, "Error response from daemon") && !strings.Contains(out, "Cannot connect") {
		return fmt.Errorf("%s: %w: %s", what, err, out)
	}
	return fmt.Errorf("%w: %s: %v: %s", ErrTaskUnobservable, what, err, out)
}

// noSuchContainer reports Docker's own answer that there is no such
// container. Anything else, including a client that never reached a daemon
// ("no such file or directory" for a missing certificate), is not absence.
func noSuchContainer(out string) bool {
	return strings.Contains(out, "No such container") || strings.Contains(out, "No such object")
}

// starts serializes starting one task within a process, keyed by its
// container ID whichever way the caller names it (key or ID): the
// observation that a task never ran and the start that follows are one step,
// so two overlapping starts through any driver here cannot both run it.
// Waiting for the lock honours the caller's context.
var starts sync.Map // container ID -> chan struct{} (a one-slot lock)

func startLock(ctx context.Context, id string) (func(), error) {
	slot, _ := starts.LoadOrStore(id, make(chan struct{}, 1))
	lock := slot.(chan struct{})
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: waiting to start task %s: %v", ErrTaskUnobservable, id, ctx.Err())
	}
}

func (d DockerTasks) Capabilities() Capabilities { return Capabilities{} }

func (d DockerTasks) binary() string {
	if d.Binary != "" {
		return d.Binary
	}
	return "docker"
}

func (d DockerTasks) run(ctx context.Context, args ...string) (string, error) {
	cmd, err := d.command(ctx, args...)
	if err != nil {
		return err.Error(), err
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// command is one docker call on the pinned daemon: its address is an
// argument, and the environment names no context or host of its own, nor
// TLS settings the CLI would not have applied to that daemon. A refused
// selection makes no call; what it would have seen is unknown.
func (d DockerTasks) command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	if d.Daemon.Refused != nil {
		return nil, fmt.Errorf("%w: %v", ErrTaskUnobservable, d.Daemon.Refused)
	}
	if d.Daemon.Host == "" {
		return exec.CommandContext(ctx, d.binary(), args...), nil
	}
	cmd := exec.CommandContext(ctx, d.binary(), append([]string{"--host", d.Daemon.Host}, args...)...)
	dropped := []string{"DOCKER_CONTEXT=", "DOCKER_HOST="}
	if !d.Daemon.TLSFromEnvironment {
		dropped = append(dropped, "DOCKER_TLS_VERIFY=", "DOCKER_TLS=", "DOCKER_CERT_PATH=")
	}
	for _, v := range os.Environ() {
		if !slices.ContainsFunc(dropped, func(prefix string) bool { return strings.HasPrefix(v, prefix) }) {
			cmd.Env = append(cmd.Env, v)
		}
	}
	return cmd, nil
}

func (d DockerTasks) target(ref TaskRef) string {
	if ref.ProviderID != "" {
		return ref.ProviderID
	}
	return d.taskName(ref.Key)
}

// Ensure creates the task's container without starting it. If one already
// exists under the key's name (a retried create after a lost reply), it is
// returned instead: one key, one container.
func (d DockerTasks) Ensure(ctx context.Context, spec TaskSpec) (TaskObservation, error) {
	if err := spec.Validate(clock.Or(d.Now)()); err != nil {
		return TaskObservation{}, err
	}
	if spec.Source != nil || spec.SharedWorkspace != "" {
		return TaskObservation{}, fmt.Errorf("%w: a local task mounts its workspace; it does not take a source commit or share a backend workspace", ErrUnsupported)
	}
	if err := d.Capabilities().Unclaimed(spec); err != nil {
		return TaskObservation{}, err
	}
	if d.Daemon.Refused != nil {
		// Nothing is created: the refusal is known, not an unanswered call.
		return TaskObservation{}, fmt.Errorf("%w: %v", ErrUnsupported, d.Daemon.Refused)
	}
	// Before anything of the task is written here: a refusal leaves nothing.
	if !d.CallerAdmitsDisk {
		if err := d.admitDisk(ctx, spec.Key); err != nil {
			return TaskObservation{}, err
		}
	}
	name := d.taskName(spec.Key)
	digest := spec.Digest()
	mounts := spec.Mounts
	if len(spec.Files) > 0 {
		if d.Files == "" {
			return TaskObservation{}, fmt.Errorf("%w: this backend keeps no generated files", ErrUnsupported)
		}
		// Written before the task is created, and kept as they are for as
		// long as it exists: an ensure repeated with other files is refused.
		staged, err := StageFiles(d.filesPath(spec.Key), spec.Files)
		if err != nil {
			return TaskObservation{}, fmt.Errorf("the task's generated files: %w", err)
		}
		mounts = append(append([]string{}, mounts...), staged...)
	}
	req := Request{Workspace: spec.Workspace, WorkspaceRO: spec.WorkspaceRO, Mounts: mounts, Argv: spec.Argv, Env: spec.Env,
		Image: spec.Image, Name: name, ExtraHosts: spec.ExtraHosts,
		Labels: append(append([]string{}, spec.Labels...), dockerlabel.Task+"="+spec.Key, dockerlabel.Profile+"="+spec.Admission.Profile, requestLabel+"="+digest)}
	if !spec.Admission.Expires.IsZero() {
		req.Labels = append(req.Labels, dockerlabel.Expires+"="+spec.Admission.Expires.UTC().Format(time.RFC3339))
	}
	if spec.Admission.Network == NetworkNone {
		req.Network = "none"
	}
	if spec.Network != "" {
		req.Network = spec.Network
	}
	args := removeFlag(dockerArgs(req), "--rm")
	args[0] = "create" // dockerArgs builds a `run`; a task is created, then started
	args = append(args[:1:1], append(LimitFlags(spec.Admission.Limits), args[1:]...)...)
	out, err := d.run(ctx, args...)
	if err == nil {
		return TaskObservation{ID: lastLine(out), State: TaskCreated, ExitCode: -1}, nil
	}
	if strings.Contains(out, "is already in use") || strings.Contains(out, "Conflict") {
		// The daemon says the name is held, not by what: an observation that
		// fails, or finds nothing (removed meanwhile), leaves that unknown,
		// and the caller asks again.
		existing, held, observeErr := d.inspect(ctx, TaskRef{Key: spec.Key})
		switch {
		case observeErr != nil:
			return TaskObservation{}, fmt.Errorf("creating task %s: its name is held: %w", spec.Key, observeErr)
		case existing.State == TaskAbsent:
			return TaskObservation{}, fmt.Errorf("%w: creating task %s: its name was held and is now free", ErrTaskUnobservable, spec.Key)
		case held != digest:
			return TaskObservation{}, fmt.Errorf("%w: task %s", ErrTaskConflict, spec.Key)
		}
		existing.Request = held
		return existing, nil
	}
	return TaskObservation{}, dockerFailure("creating task "+spec.Key, err, out)
}

// admitDisk refuses a new task while the daemon's data folder has too
// little free, or cannot be read (diskspace.Admit): a task's image, layers
// and output land there. A task is admitted once, before anything of it is
// written, so a refusal leaves nothing of the key. A key with anything here
// already (its staged files, or its container) was admitted by an earlier
// ensure, and goes on as a repeated ensure; one whose container Docker
// cannot say is absent is unknown, never refused.
func (d DockerTasks) admitDisk(ctx context.Context, key string) error {
	if d.Files != "" {
		if _, err := os.Stat(d.filesPath(key)); !errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	root, space, err := d.DataDisk(ctx)
	what := "the task Docker's data folder"
	if root != "" {
		what += " (" + root + ")"
	}
	refusal := diskspace.Admit(what, space, err)
	if refusal == nil {
		return nil
	}
	// Only a container Docker says is absent supports the refusal: one that
	// exists goes on as a repeated ensure, and one that cannot be asked
	// about is unknown, never "nothing made".
	existing, _, err := d.inspect(ctx, TaskRef{Key: key})
	switch {
	case err != nil:
		return fmt.Errorf("%w (and %v)", err, refusal)
	case existing.State != TaskAbsent:
		return nil
	}
	return refusal
}

// DataDisk reads the filesystem holding the daemon's data folder (its
// root, as docker info names it), through a throwaway container
// (diskspace.DockerDFArgs).
func (d DockerTasks) DataDisk(ctx context.Context) (root string, space diskspace.Space, err error) {
	root, err = d.run(ctx, "info", "--format", "{{.DockerRootDir}}")
	if err != nil || root == "" || strings.ContainsAny(root, "\n:") {
		return "", diskspace.Space{}, fmt.Errorf("where Docker keeps its data: %v: %s", err, strs.Truncate(root, 200))
	}
	out, err := d.run(ctx, diskspace.DockerDFArgs(root)...)
	if err != nil {
		return root, diskspace.Space{}, fmt.Errorf("reading its disk: %v: %s", err, strs.Truncate(out, 200))
	}
	space, err = diskspace.ParseDF([]byte(out))
	return root, space, err
}

// Start starts a task that has never run. Starting one that runs or has run
// does nothing: Docker would run an exited container's command again. (A
// start racing another start of the same task is serialized within this
// process; a start issued by another process that outlived its caller is
// not, and callers that cannot rule that out guard the command itself.)
func (d DockerTasks) Start(ctx context.Context, ref TaskRef) error {
	found, err := d.Observe(ctx, ref)
	if err != nil {
		return fmt.Errorf("starting task %s: %w", d.target(ref), err)
	}
	if found.State == TaskAbsent {
		return fmt.Errorf("starting task %s: there is no such task", d.target(ref))
	}
	unlock, err := startLock(ctx, found.ID)
	if err != nil {
		return err
	}
	defer unlock()
	ref = TaskRef{Key: ref.Key, ProviderID: found.ID}
	obs, err := d.Observe(ctx, ref)
	if err != nil {
		return fmt.Errorf("starting task %s: %w", d.target(ref), err)
	}
	switch obs.State {
	case TaskAbsent:
		return fmt.Errorf("starting task %s: there is no such task", d.target(ref))
	case TaskCreated:
	default:
		return nil
	}
	out, err := d.run(ctx, "start", d.target(ref))
	if err != nil {
		return dockerFailure("starting task "+d.target(ref), err, out)
	}
	return nil
}

// Observe reports a task by ID or key. Absence is reported only when Docker
// answered that there is no such container.
func (d DockerTasks) Observe(ctx context.Context, ref TaskRef) (TaskObservation, error) {
	obs, held, err := d.inspect(ctx, ref)
	obs.Request = held
	return obs, err
}

// inspect observes a task and returns the request digest it was created for.
func (d DockerTasks) inspect(ctx context.Context, ref TaskRef) (TaskObservation, string, error) {
	out, err := d.run(ctx, "inspect", "--type", "container", "--format", `{{.Id}} {{.State.Status}} {{.State.ExitCode}} {{.State.OOMKilled}} {{index .Config.Labels "`+requestLabel+`"}}`, d.target(ref))
	if err != nil {
		if noSuchContainer(out) {
			return TaskObservation{State: TaskAbsent, ExitCode: -1}, "", nil
		}
		return TaskObservation{}, "", fmt.Errorf("%w: %v: %s", ErrTaskUnobservable, err, out)
	}
	fields := strings.Fields(out)
	if len(fields) < 4 || len(fields) > 5 || (fields[3] != "true" && fields[3] != "false") {
		return TaskObservation{}, "", fmt.Errorf("%w: unexpected inspect output %q", ErrTaskUnobservable, out)
	}
	held := ""
	if len(fields) == 5 {
		held = fields[4]
	}
	code, _ := strconv.Atoi(fields[2])
	obs := TaskObservation{ID: fields[0], ExitCode: code, OOMKilled: fields[3] == "true"}
	switch fields[1] {
	case "created":
		obs.State = TaskCreated
	case "exited", "dead":
		obs.State = TaskExited
	default: // running, restarting, paused, removing: still there; observe again
		obs.State = TaskRunning
	}
	return obs, held, nil
}

func (d DockerTasks) Wait(ctx context.Context, ref TaskRef) (int, error) {
	out, err := d.run(ctx, "wait", d.target(ref))
	if err != nil {
		return -1, dockerFailure("waiting for task "+d.target(ref), err, out)
	}
	code, err := strconv.Atoi(lastLine(out))
	if err != nil {
		return -1, fmt.Errorf("waiting for task %s: unexpected exit %q", d.target(ref), out)
	}
	return code, nil
}

// Output reads the task's combined output from a byte cursor. Docker keeps
// the whole log until the container is removed, so nothing is truncated.
func (d DockerTasks) Output(ctx context.Context, ref TaskRef, read OutputRead) (OutputPage, error) {
	cmd, err := d.command(ctx, "logs", d.target(ref))
	if err != nil {
		return OutputPage{}, err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return OutputPage{}, dockerFailure("reading task "+d.target(ref)+"'s output", err, string(out))
	}
	if read.Cursor < 0 || read.Cursor > int64(len(out)) {
		return OutputPage{}, fmt.Errorf("reading task %s's output: cursor %d is outside its %d bytes", d.target(ref), read.Cursor, len(out))
	}
	max := read.Max
	if max <= 0 {
		max = 1 << 20
	}
	end := min(int64(len(out)), read.Cursor+int64(max))
	return OutputPage{Data: out[read.Cursor:end], Next: end}, nil
}

// Stop kills a running task. Stopping one that already exited or is gone is
// not an error.
func (d DockerTasks) Stop(ctx context.Context, ref TaskRef) error {
	out, err := d.run(ctx, "kill", d.target(ref))
	if err != nil && !strings.Contains(out, "is not running") && !noSuchContainer(out) {
		return dockerFailure("stopping task "+d.target(ref), err, out)
	}
	return nil
}

func (d DockerTasks) Release(ctx context.Context, ref TaskRef) error {
	out, err := d.run(ctx, "rm", "-f", d.target(ref))
	if err != nil && !noSuchContainer(out) {
		return dockerFailure("removing task "+d.target(ref), err, out)
	}
	// Its generated files go with it, never before.
	if d.Files != "" && ref.Key != "" {
		if err := os.RemoveAll(d.filesPath(ref.Key)); err != nil {
			return fmt.Errorf("removing task %s's generated files: %w", ref.Key, err)
		}
	}
	return nil
}

// filesPath is the folder a task's generated files are kept in.
// ReleaseWorkspace: local tasks mount AppTown's own folder; they share no
// workspace on the backend.
func (d DockerTasks) ReleaseWorkspace(context.Context, string) error {
	return fmt.Errorf("%w: local tasks share no workspace", ErrUnsupported)
}

func (d DockerTasks) filesPath(key string) string { return filepath.Join(d.Files, d.taskName(key)) }

func removeFlag(args []string, flag string) []string {
	out := args[:0:0]
	for _, a := range args {
		if a != flag {
			out = append(out, a)
		}
	}
	return out
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
