// Package executionfake is an in-memory TaskDriver for tests of callers. It
// keeps the contract's identity, lifecycle and refusal rules; it runs no
// program. A task's output and exit come from its spec's environment:
// FAKE_OUTPUT, FAKE_EXIT, and FAKE_BLOCK=1 for a task that runs until stopped.
package executionfake

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/execution"
)

type Tasks struct {
	// Now is the clock an admission's expiry is judged by, as a real driver
	// is given its caller's (AppTown's) clock: a test that moves that clock
	// gives it here. Nil is the system clock.
	Now func() time.Time

	mu       sync.Mutex
	next     int
	tasks    map[string]*task // by key
	admitted map[string]execution.Admission
	starts   map[string]int // by key, kept after release
}

type task struct {
	starts int
	id     string
	spec   execution.TaskSpec
	state  execution.TaskState
	exit   int
	oom    bool
	output []byte
	done   chan struct{}
}

var _ execution.TaskDriver = (*Tasks)(nil)

func (f *Tasks) Capabilities() execution.Capabilities { return execution.Capabilities{} }

func (f *Tasks) Ensure(_ context.Context, spec execution.TaskSpec) (execution.TaskObservation, error) {
	if err := spec.Validate(clock.Or(f.Now)()); err != nil {
		return execution.TaskObservation{}, err
	}
	if spec.SharedWorkspace != "" {
		return execution.TaskObservation{}, fmt.Errorf("%w: the fake shares no workspace", execution.ErrUnsupported)
	}
	if err := f.Capabilities().Unclaimed(spec); err != nil {
		return execution.TaskObservation{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tasks == nil {
		f.tasks = map[string]*task{}
	}
	if existing, ok := f.tasks[spec.Key]; ok {
		if existing.spec.Digest() != spec.Digest() {
			return execution.TaskObservation{}, fmt.Errorf("%w: task %s", execution.ErrTaskConflict, spec.Key)
		}
		return existing.observe(), nil
	}
	if f.admitted == nil {
		f.admitted = map[string]execution.Admission{}
	}
	f.admitted[spec.Key] = spec.Admission
	f.next++
	t := &task{id: fmt.Sprintf("fake-%d", f.next), spec: spec, state: execution.TaskCreated, exit: -1, done: make(chan struct{})}
	f.tasks[spec.Key] = t
	return t.observe(), nil
}

func (f *Tasks) find(ref execution.TaskRef) *task {
	for key, t := range f.tasks {
		if key == ref.Key || (ref.ProviderID != "" && t.id == ref.ProviderID) {
			return t
		}
	}
	return nil
}

func (f *Tasks) Start(_ context.Context, ref execution.TaskRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.find(ref)
	if t == nil {
		return fmt.Errorf("no task %s", ref.Key)
	}
	if t.state != execution.TaskCreated {
		return nil
	}
	t.starts++
	if f.starts == nil {
		f.starts = map[string]int{}
	}
	f.starts[t.spec.Key]++
	env := map[string]string{}
	for _, kv := range t.spec.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	t.output = []byte(env["FAKE_OUTPUT"])
	if env["FAKE_BLOCK"] == "1" {
		t.state = execution.TaskRunning
		return nil
	}
	t.exit, _ = strconv.Atoi(env["FAKE_EXIT"])
	t.state = execution.TaskExited
	close(t.done)
	return nil
}

func (f *Tasks) Observe(_ context.Context, ref execution.TaskRef) (execution.TaskObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.find(ref)
	if t == nil {
		return execution.TaskObservation{State: execution.TaskAbsent, ExitCode: -1}, nil
	}
	return t.observe(), nil
}

func (f *Tasks) Wait(ctx context.Context, ref execution.TaskRef) (int, error) {
	f.mu.Lock()
	t := f.find(ref)
	f.mu.Unlock()
	if t == nil {
		return -1, fmt.Errorf("no task %s", ref.Key)
	}
	select {
	case <-t.done:
	case <-ctx.Done():
		return -1, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return t.exit, nil
}

func (f *Tasks) Output(_ context.Context, ref execution.TaskRef, read execution.OutputRead) (execution.OutputPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.find(ref)
	if t == nil {
		return execution.OutputPage{}, fmt.Errorf("no task %s", ref.Key)
	}
	if read.Cursor < 0 || read.Cursor > int64(len(t.output)) {
		return execution.OutputPage{}, fmt.Errorf("cursor %d is outside %d bytes", read.Cursor, len(t.output))
	}
	max := read.Max
	if max <= 0 {
		max = 1 << 20
	}
	end := min(int64(len(t.output)), read.Cursor+int64(max))
	return execution.OutputPage{Data: append([]byte{}, t.output[read.Cursor:end]...), Next: end}, nil
}

func (f *Tasks) Stop(_ context.Context, ref execution.TaskRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.find(ref)
	if t == nil || t.state != execution.TaskRunning {
		return nil
	}
	t.state, t.exit = execution.TaskExited, 137
	close(t.done)
	return nil
}

func (f *Tasks) Release(_ context.Context, ref execution.TaskRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, t := range f.tasks {
		if key == ref.Key || (ref.ProviderID != "" && t.id == ref.ProviderID) {
			delete(f.tasks, key)
		}
	}
	return nil
}

// ReleaseWorkspace: the fake has no workspaces. Behind a worker, the worker
// keeps them.
func (f *Tasks) ReleaseWorkspace(context.Context, string) error {
	return fmt.Errorf("%w: the fake shares no workspace", execution.ErrUnsupported)
}

func (t *task) observe() execution.TaskObservation {
	return execution.TaskObservation{ID: t.id, State: t.state, ExitCode: t.exit, OOMKilled: t.oom, Request: t.spec.Digest()}
}

// Exit ends a running task with code, as its program would.
func (f *Tasks) Exit(key string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t := f.tasks[key]; t != nil && t.state == execution.TaskRunning {
		t.state, t.exit = execution.TaskExited, code
		close(t.done)
	}
}

// ExitOutOfMemory ends a running task as Docker does at its memory limit:
// killed, with code (which a killed child in a pipeline can leave at 0).
func (f *Tasks) ExitOutOfMemory(key string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t := f.tasks[key]; t != nil && t.state == execution.TaskRunning {
		t.state, t.exit, t.oom = execution.TaskExited, code, true
		close(t.done)
	}
}

// Starts is how many times a task under key was started, counting tasks
// since released: a test counts executions with it.
func (f *Tasks) Starts(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts[key]
}

// Present reports whether a task under key exists (it was ensured and not
// released).
func (f *Tasks) Present(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tasks[key] != nil
}

// AdmissionOf is the admission the task under key was ensured with, kept
// after it is released.
func (f *Tasks) AdmissionOf(key string) execution.Admission {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.admitted[key]
}
