package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// TaskDriver runs bounded tasks for one backend (design/operations/workers.md,
// "How work travels" and "Recovery"). It is
// the smallest contract the first consumer needs; terminals, image builds,
// captures and snapshot reads arrive with the consumers that need them.
//
// A task is named by its caller's operation key. Ensure is idempotent: the
// same key finds the task already created instead of making another, so a
// retry after a lost reply cannot run the work twice. Ensure does not start
// the task; the caller records the returned identity first, then starts it.
// A driver keeps no records of its own beyond what the backend holds.
type TaskDriver interface {
	Capabilities() Capabilities
	Ensure(context.Context, TaskSpec) (TaskObservation, error)
	Start(context.Context, TaskRef) error
	Observe(context.Context, TaskRef) (TaskObservation, error)
	// Wait blocks until the task exits and returns its exit code. Cancelling
	// ctx stops waiting, never the task.
	Wait(context.Context, TaskRef) (int, error)
	Stop(context.Context, TaskRef) error
	Output(context.Context, TaskRef, OutputRead) (OutputPage, error)
	// Release removes the task once its result is collected. A task in a
	// shared workspace (TaskSpec.SharedWorkspace) leaves the workspace.
	Release(context.Context, TaskRef) error
	// ReleaseWorkspace removes a shared workspace. It is refused
	// (ErrWorkspaceInUse) while any task in it is not released, and a
	// workspace already gone is released. A driver without shared
	// workspaces returns ErrUnsupported.
	ReleaseWorkspace(ctx context.Context, key string) error
}

// Capabilities are what a backend claims beyond the lifecycle every driver
// provides. Admission refuses work that needs a capability not claimed;
// conformance tests run the capability groups a driver claims.
type Capabilities struct {
	PrivateDocker bool // the task can run its own containers
	Terminal      bool // an interactive terminal can attach
	Persistence   bool // the task's files survive it for capture
	Inputs        bool // TaskSpec.Inputs are presented under /apptown/in (inputs.go)
	Results       bool // TaskSpec.Results are captured when the task exits
	Images        bool // TaskSpec.Images are captured when the task exits, and served by file
}

// Admission is carried by every request, whatever runs it: the profile that
// admitted it, its resource limits, its network policy and its expiry. A
// driver enforces what it can and refuses what it cannot.
type Admission struct {
	Profile string
	Limits  Limits
	Network NetworkPolicy
	// Expires is when the task must no longer run. The caller stops it then;
	// a driver refuses to create a task already expired.
	Expires time.Time
}

// Limits bound a task. Zero means the profile imposes no limit of that kind;
// it is recorded as such, never as "unknown".
type Limits struct {
	CPUs        float64
	MemoryBytes int64
	PIDs        int64
	WallClock   time.Duration
}

// NetworkPolicy names what a task may reach. "none" has no network;
// "default" is the backend's ordinary egress, which a profile qualifies.
type NetworkPolicy string

const (
	NetworkNone    NetworkPolicy = "none"
	NetworkDefault NetworkPolicy = "default"
)

// TaskSpec is one task: its operation key (the caller's identity for it),
// the admission it runs under, and what it runs.
type TaskSpec struct {
	Key       string
	Admission Admission
	Image     string
	Argv      []string
	Env       []string
	// Workspace is a host directory mounted at /work. Mounts and ExtraHosts
	// are for the local Docker profile's caches and host routing. A remote
	// driver shares no files with the forge and refuses all three.
	Workspace   string
	WorkspaceRO bool
	Mounts      []string
	ExtraHosts  []string
	Labels      []string
	// Source is the committed tree a remote task works on at /work, sent to
	// the backend as a Git bundle. A local driver refuses it.
	Source *TaskSource
	// SharedWorkspace names a workspace on the backend the task shares with
	// other tasks (the invocations of one agent step attempt) instead of
	// owning one. The workspace is made from Source the first time and must
	// hold that commit after. Releasing the task leaves the workspace;
	// ReleaseWorkspace removes it. Empty: the task owns its workspace, which
	// goes with it. A driver without shared workspaces refuses it.
	SharedWorkspace string `json:",omitempty"`
	// AgentGit, with Source, gives the task a Git repository of its own at
	// /work/.git when its workspace is made (an agent or review step's).
	// Nil: /work has no .git.
	AgentGit *AgentGit `json:",omitempty"`
	// Files are generated files presented read-only in the task's container
	// (files.go). The driver keeps them in a folder the task owns, from
	// before it is created until it is released, and never rewrites them.
	Files []File
	// Inputs are files the task reads under /apptown/in, sent to the backend
	// as content-addressed blobs (inputs.go). Results are files it declares
	// it will leave, captured by the backend when it exits. Both need a
	// backend claiming them (Capabilities).
	// Left out of a request's identity when there are none, so a request
	// without them keeps the identity it had before they existed.
	Inputs  []Input  `json:",omitempty"`
	Results []Result `json:",omitempty"`
	// Images are OCI image layouts a release build declares it will leave
	// in its workspace's state (image_results.go), captured when it exits.
	// A request declaring them carries no environment, inputs or services.
	Images []ImageResult `json:",omitempty"`
	// Private is the task's own network, services and Docker daemon
	// (private.go), on a backend claiming PrivateDocker. Network is the
	// Docker network such a backend joins the task to, which it names
	// itself: WorkerTasks refuses it, as it refuses Mounts.
	Private *PrivateDocker `json:",omitempty"`
	Network string         `json:",omitempty"`
	// Request is the caller's identity for what the task runs, when paths in
	// the spec vary between identical requests (a freshly staged copy).
	// Empty: Digest computes it from the spec.
	Request string
}

// TaskSource is one commit in a repository on the forge's side.
type TaskSource struct {
	Repository string
	Commit     string
}

// AgentGit is the repository a task is given at /work/.git: an ordinary
// clone of its source commit's history, on Branch, with Base, when set, at
// BaseCommit (an ancestor of the source), no remote, and the forge as its
// committer. It is the task's own: the backend's collection reads the work
// tree with a Git directory of its own and never this one, so the task's
// commits reach the forge only as the tree they leave, in one commit.
type AgentGit struct {
	Branch     string
	Base       string `json:",omitempty"`
	BaseCommit string `json:",omitempty"`
}

// TaskRef names a task to its driver: the provider identity the caller
// recorded, or, before one was recorded, the task's key.
type TaskRef struct {
	Key        string
	ProviderID string
}

type OutputRead struct {
	Cursor int64 // byte offset already read
	Max    int   // at most this many bytes; 0 means a driver default
}

type OutputPage struct {
	Data []byte
	Next int64 // the cursor after Data
	// Truncated: bytes before Cursor are no longer available.
	Truncated bool
}

var (
	// ErrTaskConflict: a task already exists under the key for a different
	// request. Nothing was created, and the existing task was not touched.
	ErrTaskConflict = errors.New("a different task already exists under this key")
	// ErrUnsupported: the request needs something this driver does not
	// provide. Nothing was created.
	ErrUnsupported = errors.New("the task needs something this backend does not provide")
	// ErrExpired: the admission's expiry has passed. Nothing was created.
	ErrExpired = errors.New("the task's admission has expired")
	// ErrStartRefused: the backend refused to start the task, and says why
	// (its private Docker could not be made). Nothing of it runs.
	ErrStartRefused = errors.New("the backend refused to start the task")
	// ErrWorkspaceInUse: a shared workspace still has a task not released.
	// Nothing was removed.
	ErrWorkspaceInUse = errors.New("a task in this workspace is not released")
)

// Validate checks what every driver requires of a request.
func (s TaskSpec) Validate(now time.Time) error {
	if s.Key == "" || s.Image == "" || len(s.Argv) == 0 {
		return fmt.Errorf("a task needs its key, image and command")
	}
	if s.Admission.Profile == "" {
		return fmt.Errorf("a task needs the profile that admitted it")
	}
	l := s.Admission.Limits
	if l.CPUs < 0 || l.MemoryBytes < 0 || l.PIDs < 0 || l.WallClock < 0 {
		return fmt.Errorf("task limits cannot be negative")
	}
	switch s.Admission.Network {
	case NetworkNone, NetworkDefault:
	default:
		return fmt.Errorf("%w: network policy %q", ErrUnsupported, s.Admission.Network)
	}
	if s.AgentGit != nil && (s.Source == nil || s.AgentGit.Branch == "" || (s.AgentGit.Base == "") != (s.AgentGit.BaseCommit == "")) {
		return fmt.Errorf("a task's own repository needs its source, its branch, and its base with the base's commit")
	}
	if err := ValidInputs(s.Inputs); err != nil {
		return err
	}
	if s.Private != nil {
		if s.Admission.Network != NetworkDefault {
			return fmt.Errorf("private Docker needs the default network, not %q", s.Admission.Network)
		}
		if err := s.Private.Valid(l); err != nil {
			return err
		}
	}
	if err := ValidResults(s.Results); err != nil {
		return err
	}
	if err := s.validBuild(); err != nil {
		return err
	}
	if !s.Admission.Expires.IsZero() && !now.Before(s.Admission.Expires) {
		return ErrExpired
	}
	return nil
}

// Digest identifies the request a task was created for: everything the task
// runs with except its expiry, which a retry may carry later. A driver keeps
// it with the task and refuses to adopt a task under the same key for a
// different request (ErrTaskConflict).
func (s TaskSpec) Digest() string {
	if s.Request != "" {
		return s.Request
	}
	s.Admission.Expires = time.Time{}
	body, _ := json.Marshal(s)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
