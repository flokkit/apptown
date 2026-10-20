// Package consoletest is the fake agent runtime: it stands in for pi in a
// container, remembers what it was started with, and writes the session
// record the way pi does so the console reads replies out of it.
package consoletest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/gitexec"
)

type agent struct {
	launch      console.Launch
	exists      bool
	running     bool
	everStarted bool
	stale       bool
	interrupted bool
	attached    int
	screen      bytes.Buffer
	screenEnded bool
	// scriptsRunning counts scripts run while the agent ran.
	scriptsRunning int
}

// Agent records every session it is asked to run.
type Agent struct {
	mu     sync.Mutex
	agents map[string]*agent
	// Fail makes the next Start fail, for tests of a runtime that will not come up.
	Fail error
	// failRemoves counts removals to fail per agent (FailRemove).
	failRemoves map[string]int
	// screens wakes a terminal's Read when a screen changes: bytes, an
	// end, a close. Guarded by mu; made on first use.
	screens *sync.Cond
}

// screenChanged is screens, made on first use; the caller holds mu.
func (f *Agent) screenChanged() *sync.Cond {
	if f.screens == nil {
		f.screens = sync.NewCond(&f.mu)
	}
	return f.screens
}

func NewAgent() *Agent { return &Agent{agents: map[string]*agent{}} }

func (f *Agent) get(id string) *agent {
	a := f.agents[id]
	if a == nil {
		a = &agent{}
		f.agents[id] = a
	}
	return a
}

// --- what the console calls -----------------------------------------------------

func (f *Agent) Start(ctx context.Context, in console.Launch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		err := f.Fail
		f.Fail = nil
		return err
	}
	a := f.get(in.ID)
	a.launch, a.exists, a.running, a.stale, a.everStarted, a.screenEnded = in, true, true, false, true, false
	f.screenChanged().Broadcast()
	return nil
}

func (f *Agent) Stale(ctx context.Context, in console.Launch) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.agents[in.ID]
	if a == nil || !a.exists {
		return false, nil
	}
	return a.stale || a.launch.Model != in.Model || a.launch.Docker != in.Docker, nil
}

func (f *Agent) Status(ctx context.Context, id string) (console.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.agents[id]
	if a == nil {
		return console.Status{}, nil
	}
	return console.Status{Exists: a.exists, Running: a.running, EverStarted: a.everStarted && a.exists, Attached: a.attached > 0}, nil
}

// Deliver appends the user's words to the session record, as pi does when
// the words are submitted.
func (f *Agent) Deliver(ctx context.Context, in console.Launch, text string) error {
	f.mu.Lock()
	a := f.get(in.ID)
	running := a.running
	f.mu.Unlock()
	if !running {
		return fmt.Errorf("agent %s is not running", in.ID)
	}
	return appendEntry(in.Dir, in.AgentSession, map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": text}}}, time.Now())
}

func (f *Agent) Interrupt(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.get(id).interrupted = true
	return nil
}

func (f *Agent) Attach(ctx context.Context, id string, size console.Size) (console.Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.get(id)
	if !a.running {
		return nil, fmt.Errorf("agent %s is not running", id)
	}
	a.attached++
	return &terminal{f: f, id: id, size: size}, nil
}

// Stop stops the agent; its terminals end, as a real runtime's do.
func (f *Agent) Stop(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.get(id)
	a.running, a.screenEnded = false, true
	f.screenChanged().Broadcast()
	return nil
}

func (f *Agent) Remove(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRemoves[id] > 0 {
		f.failRemoves[id]--
		return errors.New("the runtime could not be removed")
	}
	a := f.get(id)
	a.exists, a.running, a.everStarted, a.screenEnded = false, false, false, true
	f.screenChanged().Broadcast()
	return nil
}

// Script runs a shell script in the launch's work tree, standing in for a
// script run in a container beside the agent's.
func (f *Agent) Script(ctx context.Context, in console.Launch, script string, env []string, stdout io.Writer) error {
	f.mu.Lock()
	if a := f.get(in.ID); a.running {
		a.scriptsRunning++
	}
	f.mu.Unlock()
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	// AppTown's environment without its GIT_* variables (gitexec.Environ),
	// as a container would not have them: a script's git names the work tree.
	cmd.Dir, cmd.Env, cmd.Stdout = in.Workspace.Path, gitexec.Environ(env...), stdout
	return cmd.Run()
}

// End is Remove, as for any runtime on AppTown's machine.
func (f *Agent) End(ctx context.Context, id string) error { return f.Remove(ctx, id) }

// FailRemove makes the next n removals of an agent's runtime fail, leaving
// it in place: a daemon that did not answer.
func (f *Agent) FailRemove(id string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRemoves == nil {
		f.failRemoves = map[string]int{}
	}
	f.failRemoves[id] = n
}

func (f *Agent) Reap(ctx context.Context, alive map[string]bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, a := range f.agents {
		if !alive[id] {
			a.exists, a.running = false, false
		}
	}
	return nil
}

// --- what a test asks and does --------------------------------------------------

// ScriptsWhileRunning counts the scripts run on an agent's work tree while
// the agent itself ran.
func (f *Agent) ScriptsWhileRunning(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.get(id).scriptsRunning
}

// Started is what the agent was last started with.
func (f *Agent) Started(id string) console.Launch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.get(id).launch
}

func (f *Agent) Running(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.get(id).running
}

func (f *Agent) Exists(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.get(id).exists
}

func (f *Agent) Interrupted(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.get(id).interrupted
}

// MarkStale makes the runtime report that what runs no longer matches.
func (f *Agent) MarkStale(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.get(id).stale = true
}

// Reply is the agent answering: an assistant entry with text and no tool
// calls, and the record left quiet long enough to count as handed back.
func (f *Agent) Reply(id, text string) {
	l := f.Started(id)
	_ = appendEntry(l.Dir, l.AgentSession, map[string]any{"role": "assistant", "provider": "fake", "model": l.Model,
		"content": []map[string]any{{"type": "text", "text": text}}, "usage": map[string]any{"input": 10, "output": 5, "totalTokens": 15}}, time.Now())
	settle(l.Dir)
}

// Working is the agent mid-turn: its newest entry is a tool call, just now.
func (f *Agent) Working(id string) {
	l := f.Started(id)
	_ = appendEntry(l.Dir, l.AgentSession, map[string]any{"role": "assistant", "provider": "fake", "model": l.Model,
		"content": []map[string]any{{"type": "toolCall", "id": "c1", "name": "bash", "arguments": map[string]any{"command": "ls"}}}}, time.Now())
}

// EndScreen ends the agent's screen, as a terminal whose process exits:
// its viewers read what is left, then the end.
func (f *Agent) EndScreen(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.get(id).screenEnded = true
	f.screenChanged().Broadcast()
}

// Screen puts bytes on the agent's screen, for terminal tests.
func (f *Agent) Screen(id string, out []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.get(id).screen.Write(out)
	f.screenChanged().Broadcast()
}

// --- the record -------------------------------------------------------------------

func recordPath(dir, piSession string) string {
	files, _ := filepath.Glob(filepath.Join(dir, "sessions", "*_"+piSession+".jsonl"))
	if len(files) > 0 {
		return files[len(files)-1]
	}
	return filepath.Join(dir, "sessions", "2026-01-01T00-00-00-000Z_"+piSession+".jsonl")
}

func appendEntry(dir, piSession string, message map[string]any, at time.Time) error {
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(map[string]any{"type": "message", "timestamp": at.UTC().Format("2006-01-02T15:04:05.000Z"), "message": message})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(recordPath(dir, piSession), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// settle backdates the record so the transcript's quiet period has passed.
func settle(dir string) {
	files, _ := filepath.Glob(filepath.Join(dir, "sessions", "*.jsonl"))
	for _, f := range files {
		old := time.Now().Add(-10 * time.Second)
		_ = os.Chtimes(f, old, old)
	}
}

// --- a terminal onto the fake screen ---------------------------------------------

type terminal struct {
	f      *Agent
	id     string
	size   console.Size
	read   int
	closed bool
}

// Read waits for the screen, as a real terminal does: it answers new bytes,
// or the end once the terminal is closed or the screen has ended.
func (t *terminal) Read(p []byte) (int, error) {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	for {
		a := t.f.get(t.id)
		screen := a.screen.Bytes()
		switch {
		case t.closed:
			return 0, io.EOF
		case t.read < len(screen):
			n := copy(p, screen[t.read:])
			t.read += n
			return n, nil
		case a.screenEnded:
			return 0, io.EOF
		}
		t.f.screenChanged().Wait()
	}
}

func (t *terminal) Write(p []byte) (int, error) {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	t.f.get(t.id).screen.Write(p)
	t.f.screenChanged().Broadcast()
	return len(p), nil
}

func (t *terminal) Resize(size console.Size) error { t.size = size; return nil }

func (t *terminal) Close() error {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	if !t.closed {
		t.closed = true
		t.f.get(t.id).attached--
		t.f.screenChanged().Broadcast()
	}
	return nil
}
