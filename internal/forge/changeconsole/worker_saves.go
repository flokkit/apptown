package changeconsole

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"flokkit.io/forge/internal/forge/preview"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/strs"
	"flokkit.io/forge/internal/worker"
)

// Saving a chat on the worker (design/operations/workers.md, "Chats on the
// worker"): after each turn it hands back, and once more when
// it ends, AppTown collects the chat's commits and publishes them under
// the chat's hold (repositoryedit.SaveChat). The worker answers; it never
// calls AppTown.

// SavesDependencies are what saving a chat on the worker needs.
type SavesDependencies struct {
	DB       *database.DB
	Sessions *console.Service
	// Remote runs the chats on the worker; nil without one, and then
	// nothing is saved from a worker.
	Remote    *worker.RemoteAgent
	Branches  *scm.Branches
	Publisher repositoryedit.Publisher
	DataDir   string
	// Now is AppTown's clock; nil is the real one.
	Now func() time.Time
}

// Saves saves chats on the worker. It is the half of the change console a
// landing, a recheck, a check and a preview ask before they choose what
// they read, so it is built before them; New builds the rest over it.
//
// One chat's saves are serialized: a turn's save still under way when the
// chat ends must not race its last save.
type Saves struct {
	db        *database.DB
	sessions  *console.Service
	remote    *worker.RemoteAgent
	branches  *scm.Branches
	publisher repositoryedit.Publisher
	dataDir   string
	locks     sync.Map
	// rehearsals holds each chat's rehearsal while it runs: one at a time.
	rehearsals sync.Map
	clock      func() time.Time // AppTown's clock; nil is the real one
}

func (w *Saves) now() time.Time { return clock.Or(w.clock)() }

func NewSaves(d SavesDependencies) *Saves {
	return &Saves{db: d.DB, sessions: d.Sessions, remote: d.Remote, branches: d.Branches, publisher: d.Publisher, dataDir: d.DataDir, clock: d.Now}
}

func (w *Saves) lock(id string) func() {
	m, _ := w.locks.LoadOrStore(id, &sync.Mutex{})
	m.(*sync.Mutex).Lock()
	return m.(*sync.Mutex).Unlock
}

// save saves and records the save; the error is the record's.
func (w *Saves) save(ctx context.Context, s *console.Session, fetch func(ctx context.Context, id, base, dest string) (string, error)) (repositoryedit.ChatSave, error) {
	scope := conversation.Of(s)
	save := repositoryedit.SaveChat(ctx, w.db, w.dataDir, w.branches, w.publisher,
		repositoryedit.ChatRef{Session: s.ID, ProjectID: scope.ProjectID, ChangeID: scope.ChangeID},
		func(ctx context.Context, base, dest string) (string, error) { return fetch(ctx, s.ID, base, dest) }, worker.SaveRef, w.now())
	err := repositoryedit.RecordChatSave(s.Dir, save)
	if err != nil {
		log.Printf("console %s: recording its save: %v", s.ID, err)
	}
	return save, err
}

// SaveFirst is the preparation's WorkerChats: it saves a chat on the
// worker before a landing, recheck or `apptown check` chooses what it reads.
func (w *Saves) SaveFirst(ctx context.Context, holder string) (bool, error) {
	saved, err := w.SaveAndReport(ctx, holder)
	return saved != "" || err != nil, err
}

// SaveAndReport saves the chat on the worker a holder names, and answers the
// commit that save left on the branch; "" when the holder is no such chat.
// It queues no rehearsal: what reads the save chose to.
func (w *Saves) SaveAndReport(ctx context.Context, holder string) (string, error) {
	id, ok := strings.CutPrefix(holder, "chat:")
	if !ok || w.remote == nil {
		return "", nil
	}
	meta, err := w.sessions.Recorded(id)
	if errors.Is(err, console.ErrNoSession) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading where chat %s runs: %w", id, err)
	}
	if !conversation.OnWorkerRecord(meta) {
		return "", nil
	}
	s, err := w.sessions.Get(ctx, id)
	if err != nil {
		return "", err
	}
	defer w.lock(id)()
	switch save, _ := w.save(ctx, s, w.remote.Save); save.Outcome {
	case repositoryedit.ChatSaved, repositoryedit.ChatSaveNoChange:
		return save.Saved, nil
	case repositoryedit.ChatSaveUnknown:
		return "", fmt.Errorf("the chat's latest commits could not be saved from its worker (not confirmed): %s", save.Reason)
	default:
		return "", fmt.Errorf("the chat's latest commits were not saved: %s", save.Reason)
	}
}

// HeldBy names the chat on the worker that holds a change ("" when none
// does): the branch's authority names it, and it has not ended (or whether
// it has cannot be known).
func (w *Saves) HeldBy(ctx context.Context, ch *workflow.Change) (string, error) {
	if ch.Branch == "" {
		return "", nil
	}
	authority, err := scm.ReadAuthorityIn(ctx, w.db, ch.ProjectID, ch.Branch)
	if err != nil {
		return "", err
	}
	id, ok := strings.CutPrefix(authority.Holder, "chat:")
	if !ok {
		return "", nil
	}
	meta, err := w.sessions.Recorded(id)
	if errors.Is(err, console.ErrNoSession) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !conversation.OnWorkerRecord(meta) {
		return "", nil
	}
	// The branch's authority keeps naming the chat until the change is next
	// taken: only a chat that has not ended, and whose worker shows nothing
	// of it, has let go. An answer the worker cannot give is a hold.
	if ended, err := w.sessions.Ended(ctx, id); err == nil && ended {
		return "", nil
	}
	return authority.Holder, nil
}

// SaveAfterTurn saves a chat on the worker after a turn it handed back. A
// refusal is recorded for the change page and told to the chat once per
// refused commit, with how to fix it; work saved is rehearsed.
func (c *Service) SaveAfterTurn(id string) {
	ctx, cancel := context.WithTimeout(c.engine, 10*time.Minute)
	defer cancel()
	s, err := c.sessions.Get(ctx, id)
	if err != nil {
		return
	}
	c.saveTurn(ctx, s)
}

func (c *Service) saveTurn(ctx context.Context, s *console.Session) repositoryedit.ChatSave {
	defer c.saves.lock(s.ID)()
	prev, _, _ := repositoryedit.LastChatSave(s.Dir)
	save, _ := c.saves.save(ctx, s, c.saves.remote.Save) // a turn's record failing is logged; the next save records again
	// The boot rehearsal reads what was saved, queued in save order while
	// the save's lock is held.
	if save.Outcome == repositoryedit.ChatSaved || save.Outcome == repositoryedit.ChatSaveNoChange {
		c.queueRehearsal(s, save.Saved)
	}
	if save.Outcome == repositoryedit.ChatSaveRefused && (prev.Outcome != repositoryedit.ChatSaveRefused || prev.Commit != save.Commit) {
		msg := "[apptown save] Your latest commits were not saved to the change: " + save.Reason
		if err := c.sessions.Send(ctx, s.ID, msg); err != nil {
			log.Printf("console %s: telling the chat its save was refused: %v", s.ID, err)
		}
	}
	return save
}

// rehearsalQueue is a chat's rehearsals: the latest saved commit waiting,
// and whether one runs. One rehearsal of a chat runs at a time; a save made
// meanwhile replaces the commit waiting, so the latest saved work is always
// rehearsed, without waiting for another turn.
type rehearsalQueue struct {
	mu      sync.Mutex
	pending string
	running bool
}

func (c *Service) queueRehearsal(s *console.Session, commit string) {
	if commit == "" {
		return
	}
	v, _ := c.saves.rehearsals.LoadOrStore(s.ID, &rehearsalQueue{})
	q := v.(*rehearsalQueue)
	q.mu.Lock()
	q.pending = commit
	if q.running {
		q.mu.Unlock()
		return
	}
	q.running = true
	q.mu.Unlock()
	go func() {
		for {
			q.mu.Lock()
			next := q.pending
			q.pending = ""
			if next == "" {
				q.running = false
				q.mu.Unlock()
				return
			}
			q.mu.Unlock()
			ctx, cancel := context.WithTimeout(c.engine, docker.BootLifetime+time.Minute)
			c.RehearseSaved(ctx, s, next)
			cancel()
		}
	}()
}

// RehearseSaved is a chat on the worker's boot gate: AppTown boots its
// saved commit on the worker exactly like the try-it button, once per
// incarnation (the commit and the values it is prepared with). The app's
// own failure (a boot phase, a contract or values that cannot be prepared)
// goes to the chat, as for a chat here; a worker that could not be asked,
// or an AppTown that cannot run previews there, is not the app's failure, is
// only logged, and leaves it to rehearse again. A successful preview is
// left warm, unless the change closed meanwhile.
func (c *Service) RehearseSaved(ctx context.Context, s *console.Session, commit string) {
	if commit == "" || !c.previews.PreviewsOnWorker() {
		return
	}
	scope := conversation.Of(s)
	ch, err := workflow.ReadChangeIn(ctx, c.db, scope.ChangeID)
	if err != nil || ch.State != workflow.ChangeInProgress {
		return
	}
	p, err := project.ByIDIn(ctx, c.db, scope.ProjectID)
	if err != nil {
		log.Printf("console %s: rehearsing its saved work: %v", s.ID, err)
		return
	}
	if _, err := docker.ReadContractAt(ctx, filepath.Join(c.dataDir, "workspaces", "project-"+p.Slug), commit); errors.Is(err, docker.ErrNoContract) {
		return // nothing claims to be runnable; the steering owns that nudge
	}
	pinned := preview.Change{ID: ch.ID, ProjectID: ch.ProjectID, Pinned: commit}
	// What this rehearsal would boot: its incarnation, or, when the commit
	// cannot even be prepared, the commit itself.
	key, bootErr := c.previews.ChangePreviewIdentity(ctx, pinned)
	if bootErr != nil && !preview.IsAppBootFailure(bootErr) {
		log.Printf("console %s: preparing saved commit %s: %v", s.ID, commit, bootErr)
		return
	}
	if bootErr != nil {
		key = commit
	}
	marker := filepath.Join(s.Dir, console.RehearsedFile)
	if b, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(b)) == key {
		return
	}
	if bootErr == nil {
		_, bootErr = c.previews.RehearseChange(ctx, pinned, false)
		if bootErr != nil && !preview.IsAppBootFailure(bootErr) {
			log.Printf("console %s: rehearsing saved commit %s: %v", s.ID, commit, bootErr)
			return
		}
		// A change that closed while it booted keeps no preview.
		if now, err := workflow.ReadChangeIn(ctx, c.db, scope.ChangeID); err == nil && now.State != workflow.ChangeInProgress {
			if err := c.previews.DiscardChangePreview(ctx, ch.ID); err != nil {
				log.Printf("console %s: removing the preview of a change that closed: %v", s.ID, err)
			}
			return
		}
	}
	delivered := true
	if bootErr != nil {
		msg := "[apptown check] I cold-booted the app from your saved commit " + strs.ShortSHA(commit) +
			" exactly like the try-it button and it FAILED: " + bootErr.Error() +
			" — fix it, commit, and finish your turn; I re-check automatically. The operator already sees this, so just get to work."
		if derr := c.sessions.Send(ctx, s.ID, msg); derr != nil {
			log.Printf("console %s: delivering the boot check failed: %v", s.ID, derr)
			delivered = false
		}
		audit.At(c.saves.now()).BestEffort(ctx, c.db, "apptown-system", scope.ProjectID, "change.boot-check", fmt.Sprintf("%d", scope.ChangeID), map[string]any{"ok": false, "error": bootErr.Error(), "delivered": delivered, "commit": commit})
	} else {
		audit.At(c.saves.now()).BestEffort(ctx, c.db, "apptown-system", scope.ProjectID, "change.boot-check", fmt.Sprintf("%d", scope.ChangeID), map[string]any{"ok": true, "commit": commit})
	}
	if delivered {
		_ = os.WriteFile(marker, []byte(key+"\n"), 0o644)
	}
}

// lastSave is an ending worker chat's last save, once its runtime has
// stopped: what it left uncommitted is committed as its leftovers and
// saved. The chat ends only when the save is settled (ChatSave.Settled);
// otherwise it stays, with its work on the worker, and its end is asked
// again: a save not confirmed, or a refused commit that could not be kept
// here (import refused it, or keeping it failed).
func (c *Service) lastSave(ctx context.Context, id string) error {
	s, err := c.sessions.Get(ctx, id)
	if err != nil {
		return err
	}
	defer c.saves.lock(id)()
	save, err := c.saves.save(ctx, s, c.saves.remote.SaveLast)
	switch {
	case err != nil:
		// The change page reads the chat's last save from its record: an
		// end it cannot record would show an earlier save as the last.
		return fmt.Errorf("its last save (%s) could not be recorded: %w", save.Outcome, err)
	case save.Settled():
		return nil
	case save.Outcome == repositoryedit.ChatSaveUnknown:
		return fmt.Errorf("not confirmed: %s", save.Reason)
	}
	return fmt.Errorf("refused, and not kept on AppTown, so the chat's work stays on the worker: %s", save.Reason)
}

// workerChatSource bundles the change's folder for a worker chat's work
// tree: its branch as it stands, which AppTown prepared (and published)
// when the chat opened.
func (c *Service) workerChatSource(ctx context.Context, in console.Launch, dest string) (string, string, error) {
	// The first start comes while the chat is opening, before it is
	// registered: its record, on disk by then, names its change.
	meta, err := c.sessions.Recorded(in.ID)
	if err != nil {
		return "", "", err
	}
	ch, err := workflow.ReadChangeIn(ctx, c.db, conversation.FromLabels(meta.Labels).ChangeID)
	if err != nil {
		return "", "", err
	}
	head, err := gitexec.Output(ctx, in.Workspace.Path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("reading the change's folder: %w", err)
	}
	head = strings.TrimSpace(head)
	return ch.Branch, head, gitexec.BundleCommit(ctx, in.Workspace.Path, head, ch.Branch, dest)
}
