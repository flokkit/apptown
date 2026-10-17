package console

import (
	"context"
	"io"
	"os"
)

// Agent is the runtime a session's agent runs in: pi in a container under
// tmux for real, a recorder in tests. The console never shells out; every
// process, terminal and sidecar is behind this interface.
type Agent interface {
	// Start makes the agent run for the launch: create it if absent, start
	// it if stopped, leave it if running.
	Start(ctx context.Context, in Launch) error
	// Stale reports that what runs no longer matches the launch (the image,
	// the model or the Docker mode moved), so it should be rebuilt when safe.
	Stale(ctx context.Context, in Launch) (bool, error)
	// Status is the runtime's view: does it exist, is it running, is a
	// terminal held on it.
	Status(ctx context.Context, id string) (Status, error)
	// Deliver types text at the agent and presses enter, verifying against
	// the session record that the words entered the conversation.
	Deliver(ctx context.Context, in Launch, text string) error
	// Interrupt stops the agent's current turn; the conversation stays.
	Interrupt(ctx context.Context, id string) error
	// Attach opens a terminal on the agent's screen, recent output replayed.
	Attach(ctx context.Context, id string, size Size) (Terminal, error)
	// Stop stops the agent's process; its filesystem and the session dir stay.
	Stop(ctx context.Context, id string) error
	// Remove takes the agent's runtime away entirely: process, sidecar,
	// networks. The session stays; the next Start builds a new runtime.
	Remove(ctx context.Context, id string) error
	// End takes the runtime away for good: the session has ended and its
	// runtime is never started again. For a runtime on this machine it is
	// Remove; a runtime on a worker also fences the session there.
	End(ctx context.Context, id string) error
	// Reap removes runtime left behind by sessions that no longer exist.
	Reap(ctx context.Context, alive map[string]bool) error
}

// Launch is everything the runtime needs to run a session's agent. The
// console fills the session facts; the environment (a dependency) supplies
// the provision: the model, the env, the mounts, and the staged home.
type Launch struct {
	ID           string
	Dir          string // the session dir, mounted at Mount
	Actor        string
	Token        string
	Workspace    Workspace // mounted at WorkMount
	AgentSession string    // the agent's session id to resume
	Model        string
	Docker       bool // a private Docker daemon for the agent's own builds
	// Mirror is the Docker Hub mirror that daemon and its builder pull
	// through ("" none): data from the forge, on its machine or a worker's.
	Mirror string
	Env    []string
	Mounts []string
	// Image is the agent image to create from; empty means the runtime's
	// own. A worker runs the image its forge names.
	Image string
}

// Status is the runtime's view of a session's agent.
type Status struct {
	Exists bool
	// SidecarExists: the agent's private Docker daemon exists, which also
	// mounts the workspace.
	SidecarExists bool
	// SidecarRunning: the private daemon runs, so work in it may be writing.
	SidecarRunning bool
	// ScriptExists: a script runs on the session's work tree beside the
	// agent (a worker chat's save), and may be writing it.
	ScriptExists bool
	Running      bool
	EverStarted  bool // it has run before: starting it again is a resume
	Attached     bool // the runtime holds a terminal on it
}

// Size is a terminal's dimensions.
type Size struct{ Cols, Rows uint16 }

// Terminal is one viewer's window onto the agent's screen: reads deliver the
// recent output then live bytes, writes are keystrokes.
type Terminal interface {
	io.ReadWriteCloser
	Resize(Size) error
}

// Provision is what the world outside supplies for a launch: the model,
// the container's environment and mounts, files staged into the session
// dir (the agent's settings, keys, a binary), and words about what was
// provisioned, which the console puts into the prompt. At the bottom an
// agent gets a model name, environment variables, mounted files and one
// block of text; this is that, and nothing more.
type Provision struct {
	Model  string
	Env    []string
	Mounts []string
	Files  map[string]Staged // path under the session dir → what to put there
	Notes  string            // prose about the provision, refreshed each wake
}

// Staged is one file the provision places: bytes, or a symbolic link.
type Staged struct {
	Data []byte
	Link string      // a symbolic link's target, when set
	Mode os.FileMode // 0 means 0644
}

// Environment answers what a session's launch gets, at every wake, because
// the answer moves (a model follows a slot, a binding is renamed). The
// system that opened the console implements it; Static answers the same
// thing every time.
type Environment interface {
	Prepare(ctx context.Context, s *Session) (Provision, error)
}

// Static is an Environment with one fixed answer: a console with nothing
// but a model, a key and a workspace.
type Static struct{ Provision Provision }

func (e Static) Prepare(context.Context, *Session) (Provision, error) { return e.Provision, nil }
