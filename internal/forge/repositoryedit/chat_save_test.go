package repositoryedit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/worker"
)

// chatSaveWorld is a project's repository and one change in progress whose
// branch a chat holds, set up through their owners: what a chat's save
// reads and writes.
type chatSaveWorld struct {
	db       *database.DB
	dataDir  string
	branches *scm.Branches
	change   *workflow.Change
	hub      string
	folder   string
	chat     string
}

// noMirror publishes to no mirror: the project has no origin.
type noMirror struct{}

func (noMirror) PushBranch(context.Context, ids.ID, string, string) {}

func newChatSaveWorld(t *testing.T) chatSaveWorld {
	t.Helper()
	db := databaseSuite.New(t)
	dataDir := t.TempDir()
	accounts := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := accounts.SeedAdministrator(context.Background(), "owner"); err != nil {
		t.Fatal(err)
	}
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "game", Name: "Game"})
	})
	if err != nil {
		t.Fatal(err)
	}
	projects := project.New(db, identity.Scoped(accounts, "project"))
	hub := filepath.Join(dataDir, "workspaces", "project-game")
	if err := scm.InitializeRepository(t.Context(), hub, "main"); err != nil {
		t.Fatal(err)
	}
	if err := projects.SetDefaultBranch(context.Background(), id, "main"); err != nil {
		t.Fatal(err)
	}
	locks := &scm.RepositoryLocks{}
	work := workflow.New(context.Background(), db, workflow.Dependencies{
		Authority:    access.Checked{Authority: identity.Scoped(accounts, "workflow"), Projects: projects},
		Repositories: locks,
		Git:          workflow.GitWorkspace{DataDir: dataDir},
	})
	t.Cleanup(work.Stop)
	ctx := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{Kind: "user", Name: "owner", Via: "session"}, Receipt: "owner", Verified: true})
	change, err := work.Create(ctx, workflow.ChangeDraft{ProjectID: id, Title: "Save a chat"})
	if err != nil {
		t.Fatal(err)
	}
	if err := work.PrepareWorkspace(ctx, change.ID); err != nil {
		t.Fatal(err)
	}
	chat := "thechat"
	if err := db.InTx(ctx, func(tx *database.Tx) error {
		_, err := workflow.TakeChangeIn(ctx, tx, id, change.Branch, scm.ChatHolder(chat), nil, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return chatSaveWorld{db: db, dataDir: dataDir, branches: scm.NewBranches(db, locks), change: change, hub: hub,
		folder: filepath.Join(dataDir, "workspaces", fmt.Sprintf("change-%d", change.ID)), chat: chat}
}

func (w chatSaveWorld) save(t *testing.T, chat string, fetch SaveFetch) ChatSave {
	t.Helper()
	return SaveChat(context.Background(), w.db, w.dataDir, w.branches, noMirror{},
		ChatRef{Session: chat, ProjectID: w.change.ProjectID, ChangeID: w.change.ID}, fetch, worker.SaveRef, time.Now())
}

// branchTip is the change's branch in the project's repository; "" before
// anything is published.
func (w chatSaveWorld) branchTip() string {
	return scm.Ref(context.Background(), w.hub, "refs/heads/"+w.change.Branch)
}

// chatClone stands in for a worker chat's work tree: a clone of the
// change's branch the chat commits in, and the save its container makes (a
// bundle of its commits since the branch's tip, naming worker.SaveRef).
type chatClone struct {
	t   *testing.T
	dir string
}

func newChatClone(t *testing.T, w chatSaveWorld) *chatClone {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "work")
	// The worker's work tree is made from the change's folder's branch.
	gittest.Run(t, t.TempDir(), "clone", "-q", "--branch", w.change.Branch, w.folder, dir)
	return &chatClone{t: t, dir: dir}
}

func (c *chatClone) commit(name, body string) string {
	c.t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(c.dir, name)), 0o755); err != nil {
		c.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.dir, name), []byte(body), 0o644); err != nil {
		c.t.Fatal(err)
	}
	gittest.Run(c.t, c.dir, "add", "-f", name)
	gittest.Run(c.t, c.dir, "-c", "user.name=chat", "-c", "user.email=chat@example.org", "commit", "-qm", "chat: "+name)
	return gittest.Run(c.t, c.dir, "rev-parse", "HEAD")
}

func (c *chatClone) fetch(ctx context.Context, base, dest string) (string, error) {
	head := gittest.Run(c.t, c.dir, "rev-parse", "HEAD")
	if head == base {
		return "", nil
	}
	gittest.Run(c.t, c.dir, "update-ref", worker.SaveRef, head)
	gittest.Run(c.t, c.dir, "bundle", "create", "-q", dest, worker.SaveRef, "^"+base)
	return head, nil
}

// A chat's commits are saved when the forge has put them on the change's
// branch: two saves in a row under the same chat each move the branch, the
// change's folder follows it, and a save with nothing new changes nothing.
func TestAChatsCommitsAreSavedOntoTheChangesBranch(t *testing.T) {
	t.Parallel()
	w := newChatSaveWorld(t)
	clone := newChatClone(t, w)
	for _, name := range []string{"one.txt", "two.txt"} {
		want := clone.commit(name, name+"\n")
		save := w.save(t, w.chat, clone.fetch)
		if save.Outcome != ChatSaved || save.Saved != want || w.branchTip() != want {
			t.Fatalf("saving %s: %+v, branch at %s", name, save, w.branchTip())
		}
		if head := gittest.Run(t, w.folder, "rev-parse", "HEAD"); head != want {
			t.Fatalf("the change's folder is at %s, not the saved %s", head, want)
		}
	}
	if save := w.save(t, w.chat, clone.fetch); save.Outcome != ChatSaveNoChange {
		t.Fatalf("a save with nothing new: %+v", save)
	}
}

// Only a publication is saved. A worker that cannot answer is "not
// confirmed"; a bundle the checked import refuses, and a commit publication
// refuses, are "refused" with the reason; none moves the branch. A commit
// refused by publication is kept for its download, and the reason says how
// to take the file out of unsaved commits.
func TestOnlyAPublicationIsASave(t *testing.T) {
	t.Parallel()
	w := newChatSaveWorld(t)
	before := w.branchTip()
	clone := newChatClone(t, w)

	unreachable := func(context.Context, string, string) (string, error) {
		return "", execution.ErrWorkerUnavailable
	}
	if save := w.save(t, w.chat, unreachable); save.Outcome != ChatSaveUnknown || w.branchTip() != before {
		t.Fatalf("an unreachable worker: %+v", save)
	}

	unrelated := filepath.Join(t.TempDir(), "other")
	gittest.Run(t, t.TempDir(), "init", "-q", unrelated)
	other := &chatClone{t: t, dir: unrelated}
	other.commit("elsewhere.txt", "x\n")
	stranger := func(ctx context.Context, base, dest string) (string, error) {
		head := gittest.Run(t, unrelated, "rev-parse", "HEAD")
		gittest.Run(t, unrelated, "update-ref", worker.SaveRef, head)
		gittest.Run(t, unrelated, "bundle", "create", "-q", dest, worker.SaveRef)
		return head, nil
	}
	if save := w.save(t, w.chat, stranger); save.Outcome != ChatSaveRefused || save.Kept != "" || w.branchTip() != before {
		t.Fatalf("a bundle the import refuses: %+v", save)
	}

	refused := clone.commit(".forge/not-source", "x\n")
	save := w.save(t, w.chat, clone.fetch)
	if save.Outcome != ChatSaveRefused || w.branchTip() != before {
		t.Fatalf("a commit publication refuses: %+v", save)
	}
	if !strings.Contains(save.Reason, "git reset --soft "+before) {
		t.Fatalf("the refusal does not say how to fix it: %s", save.Reason)
	}
	if kept := scm.Ref(t.Context(), w.hub, save.Kept); save.Kept == "" || kept != refused {
		t.Fatalf("the refused commit is not kept: %q holds %q", save.Kept, kept)
	}
}

// A chat saves only while it holds its change: for one that does not, the
// save is skipped before its worker is even asked for its commits.
func TestAChatThatNoLongerHoldsItsChangeSavesNothing(t *testing.T) {
	t.Parallel()
	w := newChatSaveWorld(t)
	before := w.branchTip()
	asked := func(context.Context, string, string) (string, error) {
		t.Fatal("the worker was asked for the commits of a chat that does not hold its change")
		return "", nil
	}
	if save := w.save(t, "notthechat", asked); save.Outcome != ChatSaveSkipped || w.branchTip() != before {
		t.Fatalf("a save by a chat without the change: %+v", save)
	}
}

// A save's result is kept with the chat, "nothing new" included: it says
// all the chat's committed work is on the branch.
func TestAChatsLastSaveIsKept(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, ok, err := LastChatSave(dir); ok || err != nil {
		t.Fatalf("no save yet: %v %v", ok, err)
	}
	for _, s := range []ChatSave{{Outcome: ChatSaveRefused, Kept: "refs/x"}, {Outcome: ChatSaveNoChange, Saved: "abc"}} {
		if err := RecordChatSave(dir, s); err != nil {
			t.Fatal(err)
		}
	}
	if got, ok, err := LastChatSave(dir); !ok || err != nil || got.Outcome != ChatSaveNoChange || got.Saved != "abc" {
		t.Fatalf("last save: %+v %v %v", got, ok, err)
	}
}

// A chat whose change cannot be found, while its project still stands, is
// not saved and not discarded: only the project's recorded deletion makes
// the save moot, so a chat on a live project never loses its work quietly.
func TestAChatsMissingChangeIsNotADeletedProject(t *testing.T) {
	t.Parallel()
	w := newChatSaveWorld(t)
	asked := func(context.Context, string, string) (string, error) {
		t.Fatal("the worker was asked for the commits of a chat without a change")
		return "", nil
	}
	save := SaveChat(context.Background(), w.db, w.dataDir, w.branches, noMirror{},
		ChatRef{Session: "lostchange", ProjectID: w.change.ProjectID, ChangeID: idstest.N(999999)}, asked, worker.SaveRef, time.Now())
	if save.Outcome != ChatSaveUnknown || save.Settled() {
		t.Fatalf("the save of a chat whose change is missing on a live project: %+v", save)
	}
}
