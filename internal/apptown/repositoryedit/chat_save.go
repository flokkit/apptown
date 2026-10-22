package repositoryedit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/console"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/gitexec"
)

// A chat on a worker is saved when AppTown has put its commits on the
// change's branch (design/operations/workers.md, "Chats on the worker").
// AppTown asks the worker for a bundle of the chat's commits
// since the branch's tip, imports it through the checked import, and
// publishes it under the chat's hold, with the checks every publication
// gets. Nothing the worker sends is trusted before those checks.

// Save outcomes.
const (
	ChatSaved        = "saved"         // the branch has the chat's commit
	ChatSaveRefused  = "refused"       // the commit was not published; see Reason
	ChatSaveUnknown  = "not-confirmed" // the worker could not answer; nothing was published
	ChatSaveNoChange = "nothing-new"   // the chat has no commits since the branch's tip
	ChatSaveSkipped  = "skipped"       // the change is closed, or the chat no longer holds it
	// ChatSaveDiscarded: the chat's project was deleted, and its change with
	// it; there is nowhere to save to, and what the chat had not saved is
	// discarded, with a receipt.
	ChatSaveDiscarded = "discarded"
)

// ChatSave is one save's result: what the change page and the chat are told.
type ChatSave struct {
	At      time.Time `json:"at"`
	Outcome string    `json:"outcome"`
	// Commit is the chat's commit the save was about; Saved is the branch's
	// tip after it (the last saved commit).
	Commit string `json:"commit,omitempty"`
	Saved  string `json:"saved,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Kept is the ref a refused commit is kept under, for its download.
	Kept string `json:"kept,omitempty"`
}

// Settled: the save leaves nothing on the worker AppTown needs: its
// commits are on the branch, it had none, a refused commit is kept here for
// its download, or there was nothing to save for (a closed change). An
// ending chat is removed only after a settled save; a refusal that could not
// be kept, or an outcome not confirmed, keeps it.
func (s ChatSave) Settled() bool {
	switch s.Outcome {
	case ChatSaved, ChatSaveNoChange, ChatSaveSkipped, ChatSaveDiscarded:
		return true
	case ChatSaveRefused:
		return s.Kept != ""
	}
	return false
}

// ChatRef names the chat being saved and its change.
type ChatRef struct {
	Session   string
	ProjectID ids.ID
	ChangeID  ids.ID
}

// SaveFetch writes a bundle of the chat's commits since base to dest and
// answers the commit it holds; "" when there is nothing new
// (worker.RemoteAgent.Save). The bundle names that commit by the saveRef
// given to SaveChat.
type SaveFetch func(ctx context.Context, base, dest string) (tip string, err error)

// RefusedChatRef is where a chat's refused commit is kept in the project's
// repository, for its download.
func RefusedChatRef(session string) string { return "refs/apptown/refused/chat-" + session }

// SaveChat saves a worker chat's committed work. The result is always an
// outcome, never a silent failure: a worker that cannot answer is "not
// confirmed", and a check that refuses is "refused" with its reason (and,
// when the commit was imported, the ref it is kept under). A chat that no
// longer holds its change, or whose change is closed, saves nothing. A chat
// whose project was deleted (its change went with it) can never be saved:
// its unsaved work is discarded, and the ledger says so. at is AppTown's
// time of the save.
func SaveChat(ctx context.Context, db *database.DB, dataDir string, branches *scm.Branches, publisher Publisher, chat ChatRef, fetch SaveFetch, saveRef string, at time.Time) ChatSave {
	now := at.UTC()
	refuse := func(reason string) ChatSave { return ChatSave{At: now, Outcome: ChatSaveRefused, Reason: reason} }
	unknown := func(err error) ChatSave {
		return ChatSave{At: now, Outcome: ChatSaveUnknown, Reason: err.Error()}
	}
	ch, err := workflow.ReadChangeIn(ctx, db, chat.ChangeID)
	if errors.Is(err, sql.ErrNoRows) {
		return discardIfDeleted(ctx, db, chat, now, err)
	}
	if err != nil {
		return unknown(err)
	}
	// A change being landed still takes its chat's work: a recheck collects it.
	if ch.State.Closed() {
		return ChatSave{At: now, Outcome: ChatSaveSkipped, Reason: fmt.Sprintf("change %d is %s and takes no more work", ch.Number, ch.State)}
	}
	p, err := project.ByIDIn(ctx, db, chat.ProjectID)
	if err != nil {
		return unknown(err)
	}
	holder := scm.ChatHolder(chat.Session)
	authority, err := scm.ReadAuthorityIn(ctx, db, p.ID, ch.Branch)
	if err != nil {
		return unknown(err)
	}
	if authority.Holder != holder {
		return ChatSave{At: now, Outcome: ChatSaveSkipped, Reason: fmt.Sprintf("this chat no longer holds change %s#%d", p.Slug, ch.Number)}
	}
	hub := filepath.Join(dataDir, "workspaces", "project-"+p.Slug)
	// The chat builds on the branch's tip; before anything is published the
	// branch is not in the repository yet, and the chat builds on the
	// change's fork point, where the branch then begins.
	base := scm.Ref(ctx, hub, "refs/heads/"+ch.Branch)
	expected := base
	if base == "" {
		base = ch.BaseSHA
	}
	if base == "" {
		return unknown(fmt.Errorf("change %s#%d has no branch and no recorded fork point", p.Slug, ch.Number))
	}
	scratch, err := os.MkdirTemp("", "apptown-chat-save-*")
	if err != nil {
		return unknown(err)
	}
	defer os.RemoveAll(scratch)
	bundle := filepath.Join(scratch, "save.bundle")
	tip, err := fetch(ctx, base, bundle)
	switch {
	case errors.Is(err, gitexec.ErrBundleRefused):
		return refuse(err.Error())
	case err != nil:
		return unknown(err)
	case tip == "":
		// Everything the chat has committed is on the branch: an earlier
		// refusal kept for it is settled (it discarded that work).
		clearRefused(ctx, hub, chat.Session)
		return ChatSave{At: now, Outcome: ChatSaveNoChange, Saved: base}
	}
	// Imported under the change's branch name: the publication reads that
	// branch from the quarantine.
	dir, imported, cleanup, err := gitexec.Quarantine(ctx, hub, bundle, saveRef, ch.Branch, base)
	if err != nil {
		if errors.Is(err, gitexec.ErrBundleRefused) {
			return ChatSave{At: now, Outcome: ChatSaveRefused, Commit: tip, Reason: err.Error() + recipe(base)}
		}
		return unknown(err)
	}
	defer cleanup()
	if imported != tip {
		return unknown(fmt.Errorf("the worker named %s but its bundle holds %s", tip, imported))
	}
	// The change's views read the branch while the folder lacks this save
	// (scm.Behind): recorded before publishing, so a save the folder cannot
	// take is never shown as the folder's older work.
	folder := filepath.Join(dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	// The folder's lock is held from the record to the fast-forward, as a
	// step holds it: neither writes the folder while the other checks it.
	held := gitexec.LockFolder(folder)
	defer held.Release()
	if err := scm.MarkSaved(ctx, folder, hub, ch.Branch, tip); err != nil {
		return unknown(fmt.Errorf("recording the save for change %s#%d's folder: %w", p.Slug, ch.Number, err))
	}
	_, err = branches.Publish(ctx, scm.Publication{ProjectID: p.ID, Repository: hub, Branch: ch.Branch, From: dir,
		Expected: expected, Grant: scm.Grant{Holder: holder}})
	if err != nil {
		save := ChatSave{At: now, Outcome: ChatSaveRefused, Commit: tip, Reason: err.Error(), Saved: base}
		if errors.Is(err, scm.ErrNotPublished) {
			save.Reason += recipe(base)
		}
		// The refused commit passed the import's checks: keep it for its
		// download before the quarantine goes.
		kept := RefusedChatRef(chat.Session)
		if out, kerr := gitexec.HubRun(ctx, hub, "fetch", "--quiet", "--no-tags", "--end-of-options", dir, "+refs/heads/"+ch.Branch+":"+kept); kerr != nil {
			log.Printf("chat %s: keeping its refused save: %v: %s", chat.Session, kerr, out)
		} else {
			save.Kept = kept
		}
		audit.At(now).BestEffort(ctx, db, "chat/"+chat.Session, p.ID, "change.save-refused", fmt.Sprintf("change %d", ch.ID),
			map[string]any{"commit": tip, "reason": err.Error()})
		return save
	}
	publisher.PushBranch(ctx, p.ID, hub, ch.Branch)
	// An earlier refusal kept for this chat is settled by this save: its
	// commit is either in what was saved or no longer the chat's.
	clearRefused(ctx, hub, chat.Session)
	// The change's folder follows the saved branch, as it follows a worker
	// task's published commits: nothing else writes it while the chat on
	// the worker holds the change. One that cannot follow is behind.
	_ = fastForward(ctx, fmt.Sprintf("chat %s", chat.Session), held, hub, ch.Branch) // logged; a folder left behind reads the branch
	audit.At(now).BestEffort(ctx, db, "chat/"+chat.Session, p.ID, "change.saved", fmt.Sprintf("change %d", ch.ID),
		map[string]any{"from": base, "to": tip})
	return ChatSave{At: now, Outcome: ChatSaved, Commit: tip, Saved: tip}
}

// discardIfDeleted answers the save of a chat whose change cannot be found.
// Only the project's recorded deletion makes the save moot: a change
// missing for any other reason stays "not confirmed", and the chat keeps
// its work. The receipt is written before the save counts as settled; one
// that cannot be written keeps the chat, and its end is asked again.
func discardIfDeleted(ctx context.Context, db *database.DB, chat ChatRef, now time.Time, missing error) ChatSave {
	deleted, err := project.DeletedIn(ctx, db, chat.ProjectID)
	if err != nil {
		return ChatSave{At: now, Outcome: ChatSaveUnknown, Reason: fmt.Sprintf("%v; and whether its project was deleted could not be read: %v", missing, err)}
	}
	if !deleted {
		return ChatSave{At: now, Outcome: ChatSaveUnknown, Reason: missing.Error()}
	}
	reason := fmt.Sprintf("project %s was deleted, and change %s with it: the chat's unsaved work was discarded", chat.ProjectID, chat.ChangeID)
	payload, _ := json.Marshal(map[string]any{"projectId": chat.ProjectID, "change": chat.ChangeID, "reason": reason})
	// Project 0: the deleted project's own lines are no longer read.
	if err := audit.Append(ctx, db, now.Format(time.RFC3339), "chat/"+chat.Session, ids.Nil, "chat.save-discarded", fmt.Sprintf("change %d", chat.ChangeID), string(payload)); err != nil {
		return ChatSave{At: now, Outcome: ChatSaveUnknown, Reason: fmt.Sprintf("%s, but its receipt could not be written: %v", reason, err)}
	}
	return ChatSave{At: now, Outcome: ChatSaveDiscarded, Reason: reason}
}

// clearRefused removes a chat's kept refusal, best effort: what is shown as
// refused is decided by the chat's last save record, not by the ref.
func clearRefused(ctx context.Context, hub, session string) {
	if scm.Ref(ctx, hub, RefusedChatRef(session)) == "" {
		return
	}
	if out, err := gitexec.Run(ctx, hub, "update-ref", "-d", RefusedChatRef(session)); err != nil {
		log.Printf("chat %s: clearing its earlier refused save: %v: %s", session, err, out)
	}
}

// recipe is how a chat takes a refused file out of commits AppTown never
// accepted: the checks read every new commit, so a later commit deleting
// the file does not help.
func recipe(saved string) string {
	return fmt.Sprintf(". To take a file out of commits that were never saved: git reset --soft %s, "+
		"then git rm --cached <file> (or add it to .gitignore), and commit again. Only unsaved commits are rewritten.", saved)
}

// ChatSaveFile, in a chat's session directory, holds its last save.
const ChatSaveFile = console.SavedFile

// RecordChatSave keeps a save's result in the chat's session directory,
// "nothing new" included: it says all the chat's committed work is on the
// branch, which settles an earlier refusal.
func RecordChatSave(dir string, save ChatSave) error {
	raw, err := json.Marshal(save)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ChatSaveFile+".tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, ChatSaveFile))
}

// LastChatSave reads a chat's last recorded save; false when it has none.
func LastChatSave(dir string) (ChatSave, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ChatSaveFile))
	if errors.Is(err, os.ErrNotExist) {
		return ChatSave{}, false, nil
	}
	if err != nil {
		return ChatSave{}, false, err
	}
	var save ChatSave
	return save, true, json.Unmarshal(raw, &save)
}

// BundleRefusedChatSave writes a bundle of a chat's kept refused commit,
// since the change's branch, to w: the download of a refused last save.
// Anyone who clones or fetches it gets the commits AppTown refused and
// can repair them; nothing about it is published.
func BundleRefusedChatSave(ctx context.Context, dataDir, projectSlug, branch, chat string, w io.Writer) error {
	return bundleKept(ctx, hubOf(dataDir, projectSlug), branch, RefusedChatRef(chat), errNoRefusedSave, w)
}

var errNoRefusedSave = fault.New(fault.NotFound, "no refused save is kept for this chat")
