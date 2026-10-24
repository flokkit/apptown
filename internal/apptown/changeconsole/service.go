// Package changeconsole is a chat that works a change: how its work reaches
// the change, how it ends, and what AppTown tells it. It owns the rules
// over a change's chats that are not the chat's own runtime (console) or its
// opening and authority (conversation): the boot rehearsal of a chat's work,
// saving a chat on the worker after each turn and at its end, ending a chat
// and publishing what it committed, ending the chats a restart finds without
// their change, opening chats on the worker, and the change's view of its
// chats on the worker.
//
// It keeps no records of its own beyond each chat's save record, kept in the
// chat's folder (repositoryedit.RecordChatSave), and its rehearsal mark
// (console.RehearsedFile). It writes changes' branches only through
// repositoryedit.SaveChat and the folder publisher it is given.
package changeconsole

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/ids"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/preview"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// Narratives queues a refresh of a change's generated narrative.
type Narratives interface {
	Request(ctx context.Context, changeID ids.ID) error
}

// FolderPublisher publishes a change folder's committed work under a
// holder's grant (workcommands.Preparation.PublishChangeFolder).
type FolderPublisher interface {
	PublishChangeFolder(ctx context.Context, p *project.Project, ch *workflow.Change, holder string) ([]string, error)
}

// Dependencies are what a change's chats are worked with.
type Dependencies struct {
	DB         *database.DB
	Sessions   *console.Service
	Chats      conversation.Chats
	Previews   *preview.Service
	Narratives Narratives
	Folders    FolderPublisher
	DataDir    string
	// Stop is closed when AppTown stops: rehearsal loops end.
	Stop <-chan struct{}
	// Engine is AppTown acting for itself, for the narrative refresh a
	// rehearsal starts.
	Engine context.Context
}

// Service works a change's chats. It uses the existing session registry and
// shutdown signal; it does not own or extend shutdown waiting, credentials
// or session lifetime.
type Service struct {
	db         *database.DB
	sessions   *console.Service
	chats      conversation.Chats
	previews   *preview.Service
	narratives Narratives
	folders    FolderPublisher
	dataDir    string
	stop       <-chan struct{}
	engine     context.Context
	saves      *Saves
}

// New builds the change console over saves, the half built before the
// landing preparation it publishes through. With a worker, the chats there
// are sourced, saved after each turn and saved last through it.
func New(saves *Saves, d Dependencies) *Service {
	s := &Service{db: d.DB, sessions: d.Sessions, chats: d.Chats, previews: d.Previews,
		narratives: d.Narratives, folders: d.Folders, dataDir: d.DataDir, stop: d.Stop, engine: d.Engine, saves: saves}
	if r := saves.remote; r != nil {
		r.Source, r.HandedBack, r.Ending = s.workerChatSource, s.SaveAfterTurn, s.lastSave
	}
	return s
}

// Rehearse is the chat's boot gate: whenever the helper hands back the
// turn and the worktree holds an unrehearsed state with a contract, the
// AppTown boots it exactly like the try-it button. Failure is fed back to the
// HELPER as a [apptown check] message and it reworks; success leaves the
// preview container warm.
//
// Runs until the session ends or the app stops. A change that is not in
// progress is skipped, not an exit: a failed landing puts the change back in
// progress and the chat carries on.
func (c *Service) Rehearse(ctx context.Context, s *console.Session) {
	if conversation.OnWorkerRecord(s.Meta) {
		return // its saved work is rehearsed after each save (RehearseSaved)
	}
	t := time.NewTicker(7 * time.Second)
	defer t.Stop()
	var rehearsed string
	var lastSessionMod time.Time
	for {
		select {
		case <-s.Done():
			return
		case <-c.stop:
			return
		case <-t.C:
		}
		ch, err := workflow.ReadChangeIn(c.engine, c.db, conversation.Of(s).ChangeID)
		if err != nil || ch.State != workflow.ChangeInProgress {
			if err == nil && (ch.State == workflow.ChangeShipped || ch.State == workflow.ChangeAbandoned) {
				return // the change closed; nothing left to rehearse, ever
			}
			continue // a landing may come back to in_progress
		}
		ws := filepath.Join(c.dataDir, "workspaces", fmt.Sprintf("change-%d", conversation.Of(s).ChangeID))
		if _, err := docker.ReadWorkspaceContract(ws); err == docker.ErrNoContract {
			continue // nothing claims to be runnable; the steering owns that nudge
		}
		if !s.HandedBack() {
			continue
		}
		// Only walk the worktree when the session file moved since the last
		// look: the walk is the expensive part.
		if latest, _ := s.LatestRecord(); latest != "" {
			if fi, err := os.Stat(latest); err == nil {
				unchanged := fi.ModTime().Equal(lastSessionMod)
				lastSessionMod = fi.ModTime()
				if unchanged && rehearsed != "" {
					continue
				}
			}
		}
		fp := docker.WorktreeFingerprint(ws)
		if rehearsed == "" {
			// Rehearsal memory survives restarts on disk: without it, every
			// restart re-booted every open chat's unchanged worktree.
			if b, err := os.ReadFile(filepath.Join(s.Dir, console.RehearsedFile)); err == nil {
				rehearsed = strings.TrimSpace(string(b))
			}
		}
		if rehearsed == fp {
			continue
		}
		// The turn just handed back with a NEW worktree state: refresh the
		// change's narrative too, once per state.
		if err := c.narratives.Request(c.engine, conversation.Of(s).ChangeID); err != nil {
			log.Printf("console %s: asking for the narrative: %v", s.ID, err)
		}
		_, bootErr := c.previews.RehearseChange(c.engine, previewOf(ch), false)
		if bootErr != nil && !preview.IsAppBootFailure(bootErr) {
			// Not the app's failure: nothing to tell the helper, and the
			// state is rehearsed again next time.
			log.Printf("console %s: rehearsing: %v", s.ID, bootErr)
			continue
		}
		delivered := true
		if bootErr != nil {
			msg := "[apptown check] I cold-booted the app from this worktree exactly like the try-it button and it FAILED: " +
				bootErr.Error() + " — fix it in /work and finish your turn; I re-check automatically. " +
				"The operator already sees this, so just get to work."
			if derr := c.sessions.Send(c.engine, s.ID, msg); derr != nil {
				// The helper never heard: do not mark this state rehearsed.
				log.Printf("console %s: delivering the boot check failed: %v", s.ID, derr)
				delivered = false
			}
			audit.At(c.saves.now()).BestEffort(ctx, c.db, "apptown-system", conversation.Of(s).ProjectID, "change.boot-check", fmt.Sprintf("%d", conversation.Of(s).ChangeID), map[string]any{"ok": false, "error": bootErr.Error(), "delivered": delivered})
		} else {
			audit.At(c.saves.now()).BestEffort(ctx, c.db, "apptown-system", conversation.Of(s).ProjectID, "change.boot-check", fmt.Sprintf("%d", conversation.Of(s).ChangeID), map[string]any{"ok": true})
		}
		if delivered {
			rehearsed = fp
			_ = os.WriteFile(filepath.Join(s.Dir, console.RehearsedFile), []byte(fp+"\n"), 0o644)
		}
	}
}

// previewOf is the preview of a change as it stands.
func previewOf(ch *workflow.Change) preview.Change {
	return preview.Change{ID: ch.ID, ProjectID: ch.ProjectID, SourceRevision: ch.LandedSHA}
}

// publishOnEnd publishes what an ending chat committed in its change's
// folder, under its own grant while it still holds the change; what it did
// not commit stays in the folder. A refusal (a secret, a moved branch) is
// logged and the work stays where it is. It says what became of the work.
func (c *Service) publishOnEnd(ctx context.Context, s *console.Session) string {
	scope := conversation.Of(s)
	p, err := project.ByIDIn(ctx, c.db, scope.ProjectID)
	if err != nil {
		log.Printf("console %s: publishing its work: %v", s.ID, err)
		return "not saved: " + err.Error()
	}
	ch, err := workflow.ReadChangeIn(ctx, c.db, scope.ChangeID)
	if err != nil {
		log.Printf("console %s: publishing its work: %v", s.ID, err)
		return "not saved: " + err.Error()
	}
	if ch.State != workflow.ChangeInProgress {
		return "not saved: the change is " + string(ch.State) // a closed change takes no more work
	}
	authority, err := scm.ReadAuthorityIn(ctx, c.db, p.ID, ch.Branch)
	if err != nil || authority.Holder != scm.ChatHolder(s.ID) {
		return "not saved: the chat no longer holds the change"
	}
	if _, err := c.folders.PublishChangeFolder(ctx, &p, ch, authority.Holder); err != nil {
		log.Printf("console %s: its committed work was not published and stays in the change's folder: %v", s.ID, err)
		return "not saved, kept in the change's folder: " + err.Error()
	}
	return repositoryedit.ChatSaved
}

// EndBy ends a chat for the person asking, who must be allowed to act on it
// (conversation.Chats.Act). The chat's work is saved as for any end; the
// record names who ended it, whose chat it was, and what became of its work.
func (c *Service) EndBy(ctx context.Context, id string) error {
	s, err := c.sessions.Get(ctx, id)
	if err != nil {
		return err
	}
	actor, err := c.chats.Act(ctx, s, "ending")
	if err != nil {
		return err
	}
	return c.End(ctx, id, actor)
}

// End settles the runtime before optional worktree/narrative effects, preserving
// the existing receipt and asynchronous publication policy. It is AppTown's
// own end, for the person EndBy admitted or for AppTown itself (a closed
// change, a deleted project): actor is who is answerable for it.
//
// The chat holds its change until its commits are published: nobody takes
// the change between its runtime's removal and that publication.
func (c *Service) End(ctx context.Context, id, actor string) error {
	defer c.sessions.Hold(id)()
	s, err := c.sessions.Get(ctx, id)
	if err != nil {
		return err
	}
	// The end itself records the chat's spend, after its runtime's last
	// record is in (console.Dependencies.Settle).
	_, err = c.sessions.End(ctx, id)
	if err != nil {
		return err
	}
	scope := conversation.Of(s)
	detail := map[string]any{}
	if !scope.ChangeID.IsZero() {
		if conversation.OnWorkerRecord(s.Meta) {
			// A worker chat's last save ran inside the end; its record says how it went.
			if save, ok, err := repositoryedit.LastChatSave(s.Dir); err == nil && ok {
				detail["saved"] = save.Outcome
			} else {
				detail["saved"] = "unknown"
			}
		} else {
			detail["saved"] = c.publishOnEnd(ctx, s)
		}
		if err := c.narratives.Request(ctx, scope.ChangeID); err != nil {
			log.Printf("console %s: asking for the narrative: %v", s.ID, err)
		}
	}
	c.chats.Record(ctx, actor, s, "console.end", detail)
	return nil
}

// TellChangeChats sends the change's chats a message from AppTown, waking
// any that sleep; every chat is tried, and those that could not be told are
// named in the error.
func (c *Service) TellChangeChats(ctx context.Context, projectID, changeID ids.ID, msg string) error {
	var errs []error
	for _, s := range c.sessions.List(ctx, console.Match{Scope: conversation.Key(projectID, changeID)}) {
		if err := c.sessions.Send(ctx, s.ID, msg); err != nil {
			errs = append(errs, fmt.Errorf("telling chat %s: %w", s.ID, err))
		}
	}
	return errors.Join(errs...)
}

// EndChangeChats ends the chats of a change that has closed (landed or
// abandoned), keeping their transcripts, so none goes on writing a folder
// that is about to be removed. A runtime whose removal is not confirmed
// still counts as present until it is (console.Service.Gone).
func (c *Service) EndChangeChats(ctx context.Context, projectID, changeID ids.ID) error {
	var errs []error
	for _, s := range c.sessions.List(ctx, console.Match{Scope: conversation.Key(projectID, changeID)}) {
		if err := c.End(ctx, s.ID, "apptown: the change closed"); err != nil {
			errs = append(errs, fmt.Errorf("ending chat %s: %w", s.ID, err))
		}
	}
	return errors.Join(errs...)
}

// EndChatsWithoutTheirChange ends every restored change chat that does not
// hold its change: a live chat always holds it, from its opening until it
// ends, so one that does not is left from before one holder per change
// (whose upgrade releases every change) or from a change that closed while
// its chats could not be ended. Their transcripts are kept.
//
// A chat whose runtime cannot be removed may still write its change's
// folder. It never runs again (only the holding chat launches), and its
// runtime, labelled with the folder, keeps anyone from taking the change
// until it is gone (workflow.HolderEndedIn). If another worker already
// holds the change, the two would share the folder, and startup refuses.
// A chat whose record could not be restored is not seen here at all; its
// runtime's label protects its change the same way.
func (c *Service) EndChatsWithoutTheirChange(ctx context.Context) error {
	var stuck []error
	for _, s := range c.sessions.List(ctx, console.Match{}) {
		scope := conversation.Of(s)
		if scope.ChangeID.IsZero() {
			continue
		}
		ch, err := workflow.ReadChangeIn(ctx, c.db, scope.ChangeID)
		if errors.Is(err, sql.ErrNoRows) {
			// Its change is gone (its project was deleted): the chat holds
			// nothing and has nothing to save. It is ended; a chat that
			// cannot be ended cannot run and blocks nobody, so it never
			// stops AppTown from starting.
			if endErr := c.End(ctx, s.ID, "apptown: the chat's change no longer exists"); endErr != nil {
				log.Printf("console %s: its change %d no longer exists, and it could not be ended (%v)", s.ID, scope.ChangeID, endErr)
			}
			continue
		}
		if err != nil {
			// A change that cannot be read (its record malformed) cannot say
			// whether this chat holds it. The chat is ended so it cannot
			// write the folder; only one that cannot be ended refuses the
			// start, since another worker may hold the change.
			if endErr := c.End(ctx, s.ID, "apptown: the chat's change cannot be read"); endErr != nil {
				stuck = append(stuck, fmt.Errorf("chat %s could not be ended, and its change %d cannot be read (%v): %w", s.ID, scope.ChangeID, err, endErr))
			} else {
				log.Printf("console %s: its change %d cannot be read (%v); the chat was ended", s.ID, scope.ChangeID, err)
			}
			continue
		}
		open := !ch.State.Closed() && ch.Branch != ""
		holder := ""
		if open {
			authority, err := scm.ReadAuthorityIn(ctx, c.db, ch.ProjectID, ch.Branch)
			if err != nil {
				return fmt.Errorf("chat %s: reading who holds change %d: %w", s.ID, ch.ID, err)
			}
			if authority.Holder == scm.ChatHolder(s.ID) {
				continue
			}
			if !authority.Free() {
				holder = authority.Holder
			}
		}
		endErr := c.End(ctx, s.ID, "apptown: the chat does not hold its change")
		switch {
		case endErr == nil:
		case holder == "":
			log.Printf("console %s: could not be ended (%v); it cannot run, and change %d cannot be taken while its runtime is there", s.ID, endErr, ch.ID)
		default:
			stuck = append(stuck, fmt.Errorf("chat %s could not be ended, and change %d is held by %s: %w", s.ID, ch.ID, holder, endErr))
		}
	}
	if len(stuck) > 0 {
		return fmt.Errorf("refusing to start beside chats that may still write a change's folder another worker holds (remove their containers, then start again): %w", errors.Join(stuck...))
	}
	return nil
}
