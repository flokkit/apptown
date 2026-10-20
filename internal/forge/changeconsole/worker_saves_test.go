package changeconsole

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/gitexec/gittest"
)

// A turn handed back is saved: the chat's commit is on the change's branch,
// the change's folder follows it, and the save is recorded with the chat.
func TestAWorkerChatsTurnIsSavedWhenItHandsBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	s := w.openOnWorker(t)
	if err := w.sessions.Send(ctx, s.ID, "add one.txt"); err != nil {
		t.Fatal(err)
	}
	want := w.commitOnWorker(t, s.ID, "one.txt", "1\n")
	w.onWork.Reply(s.ID, "added it")
	// The save is recorded after the branch is published and the folder
	// follows it: wait for the record (no git), then read both once.
	eventually(t, "the save recorded", func() bool {
		save, ok, _ := repositoryedit.LastChatSave(s.Dir)
		return ok && save.Outcome == repositoryedit.ChatSaved && save.Saved == want
	})
	if got := w.branchTip(); got != want {
		t.Fatalf("the change's branch is at %s, want the turn's commit %s", got, want)
	}
	if got := gittest.Run(t, w.folder, "rev-parse", "HEAD"); got != want {
		t.Fatalf("the change's folder is at %s, want the saved commit %s", got, want)
	}
}

// A refused save is recorded and told to the chat, with how to fix it.
func TestARefusedSaveIsToldToTheChat(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	s := w.warmOnWorker(t)
	before := w.branchTip()
	w.commitOnWorker(t, s.ID, ".apptown/not-source", "x\n")
	w.chats.SaveAfterTurn(s.ID)
	if w.branchTip() != before {
		t.Fatal("a refused commit reached the branch")
	}
	save, ok, _ := repositoryedit.LastChatSave(s.Dir)
	if !ok || save.Outcome != repositoryedit.ChatSaveRefused {
		t.Fatalf("recorded: %+v", save)
	}
	eventually(t, "the chat told of the refusal", func() bool {
		for _, m := range s.Messages() {
			if strings.Contains(m.Text, "[apptown save]") && strings.Contains(m.Text, "git reset --soft") {
				return true
			}
		}
		return false
	})
}

// Ending a chat on the worker, in the middle of a turn: its runtime stops
// before its last save, what it left uncommitted is saved as its leftovers,
// the session is removed on the worker, and only then is the change free.
func TestEndingAWorkerChatStopsItThenSavesItsLeftovers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	s := w.openOnWorker(t)
	if err := w.sessions.Send(ctx, s.ID, "work on it"); err != nil {
		t.Fatal(err)
	}
	w.onWork.Working(s.ID)
	committed := w.commitOnWorker(t, s.ID, "done.txt", "done\n")
	w.writeOnWorker(t, s.ID, "left.txt", "left over\n")
	if err := w.chats.End(ctx, s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if n := w.onWork.ScriptsWhileRunning(s.ID); n != 0 {
		t.Fatalf("%d saves ran while the chat was still running", n)
	}
	tip := w.branchTip()
	if tip == committed || !strings.Contains(gittest.Run(t, w.hub, "log", "-1", "--format=%s", tip), "Leftovers of chat "+s.ID) {
		t.Fatalf("the branch's tip %s is not the leftovers commit", tip)
	}
	if got := gittest.Run(t, w.hub, "show", tip+":left.txt"); got != "left over" {
		t.Fatalf("left.txt on the branch: %q", got)
	}
	st, err := w.workers.Status(ctx, s.ID)
	if err != nil || !st.Removed || !st.Gone() {
		t.Fatalf("the chat on the worker: %+v %v", st, err)
	}
	if gone, err := w.sessions.Gone(ctx, s.ID); err != nil || !gone {
		t.Fatalf("the ended chat is not gone: %v %v", gone, err)
	}
	// The change's narrative is asked for, to tell what the chat did.
	if asked := w.narratives.asked(); len(asked) != 1 || asked[0] != w.change.ID {
		t.Fatalf("narratives asked for: %v", asked)
	}
}

// A last save refused by the checks does not keep the chat: its commit is
// kept for its download, and the chat ends.
func TestARefusedLastSaveIsKeptAndTheChatEnds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	s := w.warmOnWorker(t)
	refused := w.commitOnWorker(t, s.ID, ".apptown/not-source", "x\n")
	if err := w.chats.End(ctx, s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if kept := scm.Ref(ctx, w.hub, repositoryedit.RefusedChatRef(s.ID)); kept != refused {
		t.Fatalf("the refused last save is not kept: %q", kept)
	}
	if gone, err := w.sessions.Gone(ctx, s.ID); err != nil || !gone {
		t.Fatalf("the chat did not end: %v %v", gone, err)
	}
}

// A chat whose worker cannot be reached cannot end: it stays, and so does
// its hold on the change; nothing says it is gone.
func TestAWorkerChatThatCannotBeReachedKeepsItsChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	s := w.warmOnWorker(t)
	w.srv.Close()
	if err := w.chats.End(ctx, s.ID, "owner"); err == nil {
		t.Fatal("a chat ended while its worker could not be reached")
	}
	if _, err := w.sessions.Get(ctx, s.ID); err != nil {
		t.Fatalf("the chat is gone from AppTown: %v", err)
	}
	if w.holder(t) != scm.ChatHolder(s.ID) {
		t.Fatalf("the change was released: %s", w.holder(t))
	}
	if st := w.sessions.State(ctx, s); st != console.Unavailable {
		t.Fatalf("state: %s", st)
	}
}

// A last save that is not confirmed keeps the chat and its change: its end
// is asked again, and once the save is confirmed the chat ends.
func TestALastSaveNotConfirmedKeepsTheChat(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	s := w.warmOnWorker(t)
	want := w.commitOnWorker(t, s.ID, "keep.txt", "keep\n")
	w.failSaves.Store(true)
	if err := w.chats.End(ctx, s.ID, "owner"); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("an end whose last save was not confirmed: %v", err)
	}
	if _, err := w.sessions.Get(ctx, s.ID); err != nil || w.holder(t) != scm.ChatHolder(s.ID) {
		t.Fatalf("the chat or its hold is gone: %v %s", err, w.holder(t))
	}
	if st, err := w.workers.Status(ctx, s.ID); err != nil || st.Removed {
		t.Fatalf("the chat was removed on the worker: %+v %v", st, err)
	}
	w.failSaves.Store(false)
	if err := w.chats.End(ctx, s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if w.branchTip() != want {
		t.Fatalf("the chat's commit was not saved as it ended: %s", w.branchTip())
	}
}

// A refused last save whose commit could not be kept on AppTown is not
// settled: the chat is not ended and its work stays on the worker.
func TestARefusedLastSaveThatCouldNotBeKeptKeepsTheChat(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	s := w.warmOnWorker(t)
	refused := w.commitOnWorker(t, s.ID, ".apptown/not-source", "refused work\n")
	// A held ref lock makes keeping the refused commit fail.
	lock := filepath.Join(w.hub, ".git", repositoryedit.RefusedChatRef(s.ID)+".lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, []byte("busy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.chats.End(ctx, s.ID, "owner"); err == nil || !strings.Contains(err.Error(), "not kept") {
		t.Fatalf("an end whose refused commit could not be kept: %v", err)
	}
	if st, err := w.workers.Status(ctx, s.ID); err != nil || st.Removed {
		t.Fatalf("the chat was removed on the worker: %+v %v", st, err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := w.chats.End(ctx, s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if kept := scm.Ref(ctx, w.hub, repositoryedit.RefusedChatRef(s.ID)); kept != refused {
		t.Fatalf("the refused commit is not kept: %q", kept)
	}
}

// Deleting the tracked app contract is part of what a chat leaves, and its
// last save carries it, as AppTown's own staging does.
func TestALastSaveCarriesTheContractsDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	s := w.warmOnWorker(t)
	w.commitOnWorker(t, s.ID, ".apptown/app.yaml", "name: review\n")
	w.chats.SaveAfterTurn(s.ID)
	if names := gittest.Run(t, w.hub, "ls-tree", "--name-only", w.branchTip(), "--", ".apptown/app.yaml"); names == "" {
		t.Fatal("the contract was not saved")
	}
	if err := os.Remove(filepath.Join(w.onWork.Started(s.ID).Workspace.Path, ".apptown/app.yaml")); err != nil {
		t.Fatal(err)
	}
	w.writeOnWorker(t, s.ID, ".apptown/runtime-state", "not source\n")
	if err := w.chats.End(ctx, s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	tree := gittest.Run(t, w.hub, "ls-tree", "-r", "--name-only", w.branchTip())
	if strings.Contains(tree, ".apptown/") {
		t.Fatalf("the saved tree has .apptown files: %s", tree)
	}
}

// Staging the contract that fails (here a required clean filter that
// fails) fails the last save, and the chat and its work stay; only an
// absent, untracked contract is nothing to stage.
func TestAContractThatCannotBeStagedKeepsTheChat(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	s := w.warmOnWorker(t)
	work := w.onWork.Started(s.ID).Workspace.Path
	w.writeOnWorker(t, s.ID, "pending.txt", "ordinary leftovers\n")
	w.writeOnWorker(t, s.ID, ".apptown/app.yaml", "name: keep-this-contract\n")
	gittest.Run(t, work, "config", "filter.failing.clean", "false")
	gittest.Run(t, work, "config", "filter.failing.required", "true")
	w.writeOnWorker(t, s.ID, ".git/info/attributes", ".apptown/app.yaml filter=failing\n")
	if err := w.chats.End(ctx, s.ID, "owner"); err == nil {
		t.Fatal("the chat ended though its contract could not be staged")
	}
	if st, err := w.workers.Status(ctx, s.ID); err != nil || st.Removed {
		t.Fatalf("the chat was removed on the worker: %+v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(work, ".apptown", "app.yaml")); err != nil {
		t.Fatalf("the unsaved contract is gone: %v", err)
	}
}

// With no contract at all, the leftovers are saved as usual.
func TestLeftoversWithoutAContractAreSaved(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	s := w.warmOnWorker(t)
	w.writeOnWorker(t, s.ID, "pending.txt", "ordinary leftovers\n")
	if err := w.chats.End(context.Background(), s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if got := gittest.Run(t, w.hub, "show", w.branchTip()+":pending.txt"); got != "ordinary leftovers" {
		t.Fatalf("pending.txt: %q", got)
	}
}

// A refused ending needs a durable result as well as a kept ref: what the
// change offers for download is decided by the chat's save record, not by
// the ref alone. An end whose record cannot be written keeps the chat and
// its hold; one that ends offers its refused work.
func TestAnEndThatCannotBeRecordedKeepsTheChat(t *testing.T) {
	w := newWorld(t)
	s := w.openOnWorker(t)
	if err := w.sessions.Warm(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	good := w.commitOnWorker(t, s.ID, "good.txt", "saved work\n")
	w.chats.SaveAfterTurn(s.ID)
	if w.branchTip() != good {
		t.Fatal("first save failed")
	}
	refused := w.commitOnWorker(t, s.ID, ".apptown/not-source", "refused work\n")
	// Simulate a failed atomic record replacement without changing the old
	// readable saved result (e.g. a directory obstructing its temporary file).
	if err := os.Mkdir(filepath.Join(s.Dir, repositoryedit.ChatSaveFile+".tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	endErr := w.chats.End(context.Background(), s.ID, "owner")
	if endErr != nil {
		st, err := w.workers.Status(context.Background(), s.ID)
		if err != nil || st.Removed {
			t.Fatalf("unrecorded ending removed worker session: %+v %v", st, err)
		}
		if w.holder(t) != scm.ChatHolder(s.ID) {
			t.Fatal("unrecorded ending lost the hold")
		}
		return
	}
	if scm.Ref(t.Context(), w.hub, repositoryedit.RefusedChatRef(s.ID)) != refused {
		t.Fatal("refused work not retained")
	}
	if w.workerChat(t).Refused == "" {
		t.Error("ended refusal has no download and shows stale result")
	}
	var bundle bytes.Buffer
	if err := w.chats.RefusedBundle(context.Background(), w.change, s.ID, &bundle); err != nil || bundle.Len() == 0 {
		t.Errorf("retained work is inaccessible through its download: %v", err)
	}
}

// Whether a chat on the worker holds its change: while it runs, and while
// whether it has ended cannot be known because its worker cannot be asked;
// once it has ended, it no longer does.
func TestAnEndedWorkerChatNoLongerHoldsItsChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	s := w.warmOnWorker(t)
	if holder, err := w.saves.HeldBy(ctx, w.change); err != nil || holder != scm.ChatHolder(s.ID) {
		t.Fatalf("a running chat on the worker: held by %q, %v", holder, err)
	}
	if err := w.chats.End(ctx, s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if gone, err := w.sessions.Gone(ctx, s.ID); err != nil || !gone {
		t.Fatalf("gone: %v %v", gone, err)
	}
	if holder, err := w.saves.HeldBy(ctx, w.change); err != nil || holder != "" {
		t.Fatalf("an ended chat still holds its change: %q %v", holder, err)
	}
	// Whether it is gone cannot be known while its worker cannot be asked:
	// that is a hold, not a release.
	w.srv.Close()
	if holder, err := w.saves.HeldBy(ctx, w.change); err != nil || holder != scm.ChatHolder(s.ID) {
		t.Fatalf("an ended chat whose worker cannot be asked is treated as released: %q %v", holder, err)
	}

	held := newWorld(t)
	hs := held.warmOnWorker(t)
	held.srv.Close()
	if holder, err := held.saves.HeldBy(ctx, held.change); err != nil || holder != scm.ChatHolder(hs.ID) {
		t.Fatalf("a chat whose worker cannot be reached no longer holds: %q %v", holder, err)
	}
	if _, err := held.saves.SaveFirst(ctx, held.holder(t)); err == nil {
		t.Fatal("a chat whose worker cannot be reached was saved")
	}
}

// A refused turn the chat then fixes is settled by the next save: when the
// chat ends, its change lists no refused save for it.
func TestARefusalFixedBeforeTheEndIsNotListed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	s := w.warmOnWorker(t)
	w.commitOnWorker(t, s.ID, ".apptown/not-source", "refused\n")
	w.chats.SaveAfterTurn(s.ID)
	gittest.Run(t, w.onWork.Started(s.ID).Workspace.Path, "reset", "-q", "--hard", "HEAD~1")
	w.commitOnWorker(t, s.ID, "fixed.txt", "fixed\n")
	w.chats.SaveAfterTurn(s.ID)
	if err := w.chats.End(ctx, s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if c := w.workerChat(t); c.Refused != "" || c.Live {
		t.Fatalf("a settled refusal is listed: %+v", c)
	}
}

// Discarding a refused commit leaves nothing to save; that settles the
// refusal, and the ended chat offers no refused save.
func TestADiscardedRefusalIsNotARefusedLastSave(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	s := w.warmOnWorker(t)
	w.commitOnWorker(t, s.ID, ".apptown/not-source", "refused\n")
	w.chats.SaveAfterTurn(s.ID)
	save, ok, err := repositoryedit.LastChatSave(s.Dir)
	if err != nil || !ok || save.Outcome != repositoryedit.ChatSaveRefused || save.Saved == "" {
		t.Fatalf("refusal: %+v %v", save, err)
	}
	gittest.Run(t, w.onWork.Started(s.ID).Workspace.Path, "reset", "-q", "--hard", save.Saved)
	w.chats.SaveAfterTurn(s.ID)
	if err := w.chats.End(ctx, s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	c := w.workerChat(t)
	if c.Refused != "" {
		t.Fatalf("a discarded refusal is offered as a refused last save: %+v", c)
	}
	if !c.HasSave || c.LastSave.Outcome != repositoryedit.ChatSaveNoChange {
		t.Fatalf("the ended chat's last save: %+v", c.LastSave)
	}
}

// A refusal settled by "nothing new" is not offered even when its kept ref
// could not be removed.
func TestASettledRefusalIsNotOfferedWhenItsRefStays(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	s := w.openOnWorker(t)
	if err := w.sessions.Warm(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	w.commitOnWorker(t, s.ID, ".apptown/not-source", "refused\n")
	w.chats.SaveAfterTurn(s.ID)
	save, ok, err := repositoryedit.LastChatSave(s.Dir)
	if err != nil || !ok || save.Kept == "" {
		t.Fatalf("refusal: %+v %v", save, err)
	}
	lock := gittest.Run(t, w.hub, "rev-parse", "--git-path", save.Kept+".lock")
	if !filepath.IsAbs(lock) {
		lock = filepath.Join(w.hub, lock)
	}
	if err := os.WriteFile(lock, []byte("locked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, w.onWork.Started(s.ID).Workspace.Path, "reset", "--hard", save.Saved)
	if err := w.chats.End(context.Background(), s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if got := gittest.Run(t, w.hub, "rev-parse", save.Kept); got != save.Commit {
		t.Fatal("ref not retained")
	}
	if c := w.workerChat(t); c.Refused != "" || c.LastSave.Outcome != repositoryedit.ChatSaveNoChange {
		t.Fatalf("settled refusal listed: %+v", c)
	}
}

// An unrecordable last save keeps its leftovers and hold across repeated
// ending attempts; repairing the folder permits the same work to settle.
func TestAnUnrecordableLastSaveKeepsTheChatUntilRepaired(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	chat := w.openOnWorker(t)
	if err := w.sessions.Warm(context.Background(), chat.ID); err != nil {
		t.Fatal(err)
	}
	before := w.branchTip()
	w.writeOnWorker(t, chat.ID, "left.txt", "left over\n")
	// The records beside the folders are read-only: the save cannot be
	// recorded for the change's folder.
	metadata := filepath.Join(filepath.Dir(w.folder), ".saved")
	if err := os.MkdirAll(metadata, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(metadata, 0555); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chmod(metadata, 0755); err != nil {
			t.Error(err)
		}
	}()
	for i := 0; i < 2; i++ {
		if err := w.chats.End(context.Background(), chat.ID, "owner"); err == nil || !strings.Contains(err.Error(), "not confirmed") {
			t.Fatalf("unrecordable end: %v", err)
		}
		save, ok, err := repositoryedit.LastChatSave(chat.Dir)
		if err != nil || !ok || save.Outcome != repositoryedit.ChatSaveUnknown || !strings.Contains(save.Reason, "recording the save") {
			t.Fatalf("save: %+v %v", save, err)
		}
		if w.branchTip() != before || w.holder(t) != scm.ChatHolder(chat.ID) {
			t.Fatal("unconfirmed end published or released")
		}
		if st, err := w.workers.Status(context.Background(), chat.ID); err != nil || st.Removed {
			t.Fatalf("worker removed: %+v %v", st, err)
		}
	}
	if err := os.Chmod(metadata, 0755); err != nil {
		t.Fatal(err)
	}
	if err := w.chats.End(context.Background(), chat.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	tip := w.branchTip()
	if got := gittest.Run(t, w.hub, "show", tip+":left.txt"); got != "left over" {
		t.Fatalf("leftovers lost: %q", got)
	}
	if subject := gittest.Run(t, w.hub, "log", "-1", "--format=%s", tip); !strings.Contains(subject, "Leftovers of chat "+chat.ID) {
		t.Fatalf("wrong attribution: %s", subject)
	}
	if gone, err := w.sessions.Gone(context.Background(), chat.ID); err != nil || !gone {
		t.Fatalf("repaired end: %v %v", gone, err)
	}
}

type emptyReachAnswers struct{ base http.RoundTripper }

func (r emptyReachAnswers) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path == "/v1/reach" {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`[]`)), Request: req}, nil
	}
	return r.base.RoundTrip(req)
}

// A registry the worker's answer leaves out is unreachable.
func TestEveryRegistryNeedsTheWorkersAnswer(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if _, err := integration.New(w.db).Upsert(t.Context(), "npm-mirror", "registry", "registry", `{"format":"npm","url":"https://registry.invalid"}`, false); err != nil {
		t.Fatal(err)
	}
	w.remote.Sessions.Client.Transport = emptyReachAnswers{w.remote.Sessions.Client.Transport}
	if err := w.chats.Placement().Ready(context.Background()); err == nil {
		t.Fatal("registry readiness accepted a reply with no answer for the bound registry")
	}
}
