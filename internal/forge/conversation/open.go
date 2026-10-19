package conversation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/database"
	piharness "flokkit.io/forge/internal/forge/agentharness/pi"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/gitexec"

	"flokkit.io/forge/internal/forge/model"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
)

// Admission owns preparation and opening against the existing session
// reservation. Briefing, model selection and worktree effects have their own owners.
type Admission struct {
	work     *workflow.Service
	sessions *console.Service
	models   model.Selection
	steering *Briefing
	harness  piharness.Preparer
	effects  interface {
		Rehearse(context.Context, *console.Session)
	}
	db       *database.DB
	dataDir  string
	presence workflow.Presence
	workers  WorkerPlacement
	noLocal  bool
	changes  changeLocks
	clock    func() time.Time
}

type AdmissionDependencies struct {
	Work     *workflow.Service
	Sessions *console.Service
	Models   model.Selection
	Briefing *Briefing
	Harness  piharness.Preparer
	Effects  interface {
		Rehearse(context.Context, *console.Session)
	}
	// Presence tells whether the change's current holder has ended.
	Presence workflow.Presence
	// Workers places chats on the worker; nil when there is no worker.
	Workers WorkerPlacement
	// NoLocal is a forge that runs no chats itself (the no-local-Docker mode).
	NoLocal bool
	// Now is the forge's clock, which stamps a chat's start (the real clock
	// when nil).
	Now func() time.Time
}

// Where a chat runs, as ProjectOpen.Placement names it.
const (
	PlaceOnForge  = "forge"
	PlaceOnWorker = "worker"
)

// WorkerPlacement is what opening a chat on the worker needs
// (design/operations/workers.md, "Chats on the worker").
type WorkerPlacement interface {
	// Ready refuses, with the reason, a chat the worker cannot serve now:
	// a registry the chat's builds use that the worker cannot reach.
	Ready(ctx context.Context) error
	// Publish publishes the change folder's committed work, with the usual
	// checks, under the chat's grant, before the worker is sent a bundle of it.
	Publish(ctx context.Context, p *project.Project, ch *workflow.Change, holder string) error
}

func NewAdmission(db *database.DB, deps AdmissionDependencies, dataDir string) *Admission {
	return &Admission{work: deps.Work, sessions: deps.Sessions, models: deps.Models, steering: deps.Briefing, harness: deps.Harness, effects: deps.Effects, db: db, dataDir: dataDir, presence: deps.Presence, workers: deps.Workers, noLocal: deps.NoLocal, clock: deps.Now}
}
func (c *Admission) Open(ctx context.Context, p *project.Project, in ProjectOpen, who string) (*console.Session, error) {
	model, pinned := in.Model, in.Model != ""
	if model == "" {
		// Chat gets its own slot: quality-per-word matters most here, while
		// mechanical lanes stay on the cheap slot.
		model, _, _ = c.models.Resolve(ctx, p.ID, "chat")
		if model == "" {
			model, _, _ = c.models.Resolve(ctx, ids.Nil, "")
		}
	}
	onWorker, err := c.onWorker(in)
	if err != nil {
		return nil, err
	}
	var change *workflow.Change
	if !in.ChangeID.IsZero() {
		var err error
		change, err = c.work.Change(ctx, in.ChangeID)
		if err != nil || change.ProjectID != p.ID {
			return nil, ErrNoChange
		}
		// A landing change is admitted only to take its stopped landing to
		// chat: the continuation below stops the landing, which returns the
		// change to work, the same as sending it back does.
		if change.State != workflow.ChangeInProgress && (change.State != workflow.ChangeLanding || in.ContinueStep.IsZero()) {
			return nil, notInProgress(change)
		}
		if change.Branch == "" {
			return nil, &AdmissionConflict{Err: fmt.Errorf("change #%d has no branch (project is not repo-backed)", change.ID)}
		}
	}

	// Admission: a purge admitted meanwhile refuses the open, and a stale
	// request after a completed purge finds no project.
	release, err := ReserveProject(ctx, c.sessions, c.db, p.ID)
	if err != nil {
		return nil, &AdmissionConflict{Err: fmt.Errorf("opening console: %w", err)}
	}
	defer release()

	var cont *laneContinuation
	if !in.ContinueStep.IsZero() && change == nil {
		return nil, &AdmissionConflict{Err: fmt.Errorf("continuing a lane needs a change")}
	}
	// One chat per change: opening a change's chat again returns the one it
	// has. Opens of one change are serialised, so a second waits for the first
	// and then finds its chat.
	if change != nil {
		defer c.changes.lock(change.ID)()
		// Continuing a stuck lane in chat stops its run first, so the chat
		// can take the change once the run has ended, and a stopped landing
		// ends even when the change's chat is already open (a landing runs
		// beside it).
		if !in.ContinueStep.IsZero() {
			if cont, err = continueLane(c.dataDir, ctx, c.work, change, in.ContinueStep, who); err != nil {
				return nil, &AdmissionConflict{Err: err}
			}
			// A landing is ended by stopping its own run; a step of any
			// other run leaves the change landing, and the chat is refused.
			if change.State != workflow.ChangeInProgress {
				if change, err = c.work.Change(ctx, change.ID); err != nil {
					return nil, err
				}
				if change.State != workflow.ChangeInProgress {
					return nil, notInProgress(change)
				}
			}
		}
		authority, err := scm.ReadAuthorityIn(ctx, c.db, p.ID, change.Branch)
		if err != nil {
			return nil, err
		}
		for _, live := range c.sessions.List(ctx, console.Match{Scope: Key(p.ID, change.ID)}) {
			if authority.Holder == scm.ChatHolder(live.ID) {
				return live, nil
			}
		}
	}
	// Only a new chat on the worker needs the worker ready for it; the
	// change's existing chat is returned wherever it runs.
	if onWorker {
		if err := c.workers.Ready(ctx); err != nil {
			return nil, c.workerRefusal(fmt.Errorf("the worker cannot take this chat: %w", err), "open it on AppTown")
		}
	}
	// The chat takes the change before anything touches its folder, under
	// the id its session will have; another holder that has not ended
	// refuses it.
	id := c.sessions.NewSessionID()
	defer c.sessions.Hold(id)()
	if change != nil {
		if err := c.db.InTx(ctx, func(tx *database.Tx) error {
			_, err := workflow.TakeChangeIn(ctx, tx, p.ID, change.Branch, scm.ChatHolder(id), c.presence, c.work.Now())
			return err
		}); err != nil {
			var held *workflow.ChangeHeld
			if errors.As(err, &held) {
				return nil, &AdmissionConflict{Err: err}
			}
			return nil, err
		}
	}

	// The checkout: a change console mounts the change's worktree itself;
	// a project console gets a read-only snapshot clone of the default
	// branch, so the mount never races live runs.
	req := console.OpenRequest{ID: id, Scope: Key(p.ID, in.ChangeID), Labels: LabelsFor(p, change), Actor: who, Model: model, Pinned: pinned}
	if onWorker {
		req.Labels[PlacementLabel] = OnWorker
	}
	if opener := access.ActorFrom(ctx, who).Principal; !opener.IsZero() {
		req.Labels[OpenerLabel] = opener.String()
	}
	src := filepath.Join(c.dataDir, "workspaces", "project-"+p.Slug)
	diverged := false
	if change != nil {
		if err := c.work.PrepareWorkspace(ctx, change.ID); errors.Is(err, workflow.ErrFolderDiverged) {
			diverged = true
		} else if err != nil {
			return nil, err
		}
		if diverged && onWorker {
			// Its saves build on the published branch, which this folder
			// has left: a chat on the forge merges it first.
			return nil, c.workerRefusal(fmt.Errorf("change #%d's folder and its published branch have both moved", change.ID), "open a chat on AppTown to merge them first")
		}
		if onWorker {
			// The worker is sent the folder's branch, and its saves build
			// on the published one: the folder's committed work is published
			// first, with the checks; work they refuse stays in the folder.
			if err := c.workers.Publish(ctx, p, change, scm.ChatHolder(id)); err != nil {
				return nil, c.workerRefusal(fmt.Errorf("change #%d's folder has committed work that could not be published (%w)", change.ID, err), "open a chat on AppTown to sort it out")
			}
		}
		// The change's own folder, borrowed: it stays when the chat ends.
		req.Workspace = console.Workspace{Path: filepath.Join(c.dataDir, "workspaces", fmt.Sprintf("change-%d", change.ID)), Writable: true}
		req.Docker = true
	} else {
		root := filepath.Join(c.dataDir, "console")
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, fmt.Errorf("creating console directory: %w", err)
		}
		snapshot, err := os.MkdirTemp(root, "checkout-")
		if err != nil {
			return nil, err
		}
		if p.DefaultBranch != "" {
			_ = os.Remove(snapshot)
			if out, err := gitexec.Run(ctx, "", "clone", "--depth", "1", "--branch", p.DefaultBranch, src, snapshot); err != nil {
				return nil, fmt.Errorf("cloning checkout: %v: %s", err, out)
			}
		} else if _, err := os.Stat(src); err == nil {
			if err := copyTree(src, snapshot, map[string]bool{".git": true, "node_modules": true}); err != nil {
				_ = os.RemoveAll(snapshot)
				return nil, fmt.Errorf("copying workspace: %s", err.Error())
			}
		}
		req.Workspace = console.Workspace{Path: snapshot, Owned: true}
	}

	req.Steering = c.steering.Console(ctx, p, c.harness.Delegation(ctx))
	if change != nil {
		req.Steering = c.steering.Workshop(ctx, p, c.harness.Delegation(ctx)) + c.steering.Change(change)
	}
	if diverged {
		req.Steering += "\n\nThis change's folder and its branch as published have both moved (someone published to the branch while this folder had its own commits). Merge the published branch before anything else: `git merge " + workflow.PublishedRef + "`, resolve any conflict, and commit.\n"
	}
	if cont != nil {
		req.Steering += cont.steer
		req.Resume = &cont.Resumption
		for k, v := range cont.labels {
			req.Labels[k] = v
		}
	}

	s, err := c.sessions.Open(ctx, req)
	if err != nil {
		if change == nil {
			_ = os.RemoveAll(req.Workspace.Path)
		}
		return nil, err
	}
	if !Of(s).ChangeID.IsZero() {
		go c.effects.Rehearse(context.WithoutCancel(ctx), s) // outlives the request that opened the chat
	}
	audit.At(clock.Or(c.clock)()).BestEffort(ctx, c.db, s.Actor, p.ID, "console.start", s.ID, map[string]any{"model": s.Model})
	return s, nil
}

// changeLocks serialises opening chats on one change. An entry is kept per
// change opened in this process; there are few, and each is small.
type changeLocks struct {
	mu   sync.Mutex
	held map[ids.ID]*sync.Mutex
}

func (l *changeLocks) lock(id ids.ID) func() {
	l.mu.Lock()
	if l.held == nil {
		l.held = map[ids.ID]*sync.Mutex{}
	}
	m := l.held[id]
	if m == nil {
		m = &sync.Mutex{}
		l.held[id] = m
	}
	l.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// onWorker decides where a chat runs. A chat on the worker works a change,
// starts from the change's folder as it stands, and runs nothing the worker
// cannot give it; left to the forge, a chat goes to the worker only when it
// can. A forge that runs no chats itself (noLocal) refuses, with the reason,
// every chat the worker cannot take.
// notInProgress refuses a chat on a change that is not in progress. A
// landing one names the way in: taking its stopped landing step to chat.
func notInProgress(change *workflow.Change) error {
	if change.State == workflow.ChangeLanding {
		return &AdmissionConflict{Err: fmt.Errorf("change #%d is landing — a chat opens on it only by taking the landing's stopped step to chat, which ends the landing", change.ID)}
	}
	return &AdmissionConflict{Err: fmt.Errorf("change #%d is %s — consoles attach to in-progress changes", change.ID, change.State)}
}

func (c *Admission) onWorker(in ProjectOpen) (bool, error) {
	var cannot error
	switch {
	case c.workers == nil:
		cannot = fmt.Errorf("this AppTown has no worker for chats")
	case in.ChangeID.IsZero():
		cannot = fmt.Errorf("only a change's chat can run on the worker")
	case !in.ContinueStep.IsZero():
		cannot = fmt.Errorf("continuing a step's agent is not yet possible on the worker")
	}
	switch in.Placement {
	case "", PlaceOnWorker:
		if cannot == nil {
			return true, nil
		}
		if c.noLocal {
			return false, &AdmissionConflict{Err: fmt.Errorf("%w, and this AppTown runs no chats itself", cannot)}
		}
		if in.Placement == PlaceOnWorker {
			return false, &AdmissionConflict{Err: cannot}
		}
		return false, nil
	case PlaceOnForge:
		if c.noLocal {
			return false, &AdmissionConflict{Err: fmt.Errorf("this AppTown runs no chats itself; open the chat on the worker")}
		}
		return false, nil
	}
	return false, &AdmissionConflict{Err: fmt.Errorf("unknown placement %q: %s or %s", in.Placement, PlaceOnForge, PlaceOnWorker)}
}
