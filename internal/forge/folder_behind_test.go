package forge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
)

// A folder that could not catch up with
// a save (here a left-behind index lock) does not make the change's diff
// contradict its saved branch.
func TestAChangesDiffShowsItsSavedWorkWhenTheFolderLags(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.a.Console.Warm(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	lock := gittest.Run(t, w.folder, "rev-parse", "--git-path", "index.lock")
	if !filepath.IsAbs(lock) {
		lock = filepath.Join(w.folder, lock)
	}
	if err := os.WriteFile(lock, []byte("interrupted Git operation\n"), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(lock)
	want := w.commitOnWorker(t, s.ID, "saved-by-worker.txt", "saved work\n")
	w.a.changeConsole.SaveAfterTurn(s.ID)
	if w.branchTip(t) != want {
		t.Fatal("save did not publish")
	}
	save, ok, err := repositoryedit.LastChatSave(s.Dir)
	if err != nil || !ok || save.Outcome != repositoryedit.ChatSaved {
		t.Fatalf("save: %+v %v", save, err)
	}
	out := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/diff", w.change.ID), "")
	if out.Code != 200 || !strings.Contains(out.Body.String(), "saved-by-worker.txt") {
		t.Fatalf("published %s, but diff is %d %q", want, out.Code, out.Body.String())
	}
}

// A change whose work lives on its saved
// branch shows its files from the folder only while the folder is that
// branch; a folder that did not catch up is said to be behind, never shown
// as the work. After the chat ends, the change's page still has its last save.
func TestAWorkerChangesFilesAreNeverAStaleFolder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorkerChatWorld(t)
	run := abandonedRunOn(t, w)
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.a.Console.Warm(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	w.commitOnWorker(t, s.ID, "first.txt", "first\n")
	w.a.changeConsole.SaveAfterTurn(s.ID)
	files := func() (int, string) {
		out := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/runs/%d/workspace/", run), "")
		return out.Code, out.Body.String()
	}
	if code, body := files(); code != 200 || !strings.Contains(body, "first.txt") {
		t.Fatalf("files with the folder caught up: %d %s", code, body)
	}
	lock := gittest.Run(t, w.folder, "rev-parse", "--git-path", "index.lock")
	if !filepath.IsAbs(lock) {
		lock = filepath.Join(w.folder, lock)
	}
	if err := os.WriteFile(lock, []byte("left behind\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.commitOnWorker(t, s.ID, "second.txt", "second\n")
	w.a.changeConsole.SaveAfterTurn(s.ID)
	if code, body := files(); code != 409 || !strings.Contains(body, "not caught up") {
		t.Fatalf("files with the folder behind: %d %s", code, body)
	}
	if err := w.a.changeConsole.End(ctx, s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if code, _ := files(); code != 409 {
		t.Fatalf("files after the end, with the folder behind: %d", code)
	}
	if list := workerChatList(t, w, w.change.ID); len(list.Chats) != 1 || list.Chats[0].Saved == nil || list.Chats[0].Saved.Outcome == "" {
		t.Fatalf("the ended chat's last save on its change's page: %+v", list)
	}
}

// abandonedRunOn starts a writing run on the change and abandons it, keeping
// its work: a run whose workspace is the change's folder, for its files view.
func abandonedRunOn(t *testing.T, w workerChatWorld) ids.ID {
	t.Helper()
	run, err := w.a.Work.StartRun(asPerson("owner"), workflow.StartChangeRun{ChangeID: w.change.ID, Workflow: "implement"})
	if err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	if _, err := w.a.Work.Abandon(asPerson("owner"), workflow.StopWork{RunID: run.ID, Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

// Which history a change's views read is
// not decided by Git ancestry. A local chat's folder is read, even when the
// folder was reset behind what a worker chat saved earlier.
func TestALocalChatsOwnHistoryIsShownAfterAWorkerChat(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorkerChatWorld(t)
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.a.Console.Warm(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	w.commitOnWorker(t, s.ID, "removed-locally.txt", "saved on the worker\n")
	w.a.changeConsole.SaveAfterTurn(s.ID)
	if err := w.a.changeConsole.End(ctx, s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	p, _ := w.a.Projects.BySlug(ctx, "game")
	if _, err := w.a.consoleAdmission.Open(asPerson("owner"), &p, conversation.ProjectOpen{ChangeID: w.change.ID, Model: "fake/model", Placement: conversation.PlaceOnForge}, "owner"); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, w.folder, "reset", "-q", "--hard", "HEAD~1")
	out := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/diff", w.change.ID), "")
	if out.Code != 200 || strings.Contains(out.Body.String(), "removed-locally.txt") {
		t.Fatalf("the local chat's reset history is not what the diff shows: %d %s", out.Code, out.Body.String())
	}
}

// A local chat's reset folder is its history even when the published branch
// is ahead of it.
func TestALocalChatsResetIsNotALaggingFolder(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	w.commitOnWorker(t, s.ID, "removed-locally.txt", "old work\n")
	w.a.changeConsole.SaveAfterTurn(s.ID)
	if err := w.a.changeConsole.End(context.Background(), s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	p, _ := w.a.Projects.BySlug(context.Background(), "game")
	if _, err := w.a.consoleAdmission.Open(asPerson("owner"), &p, conversation.ProjectOpen{ChangeID: w.change.ID, Model: "fake/model", Placement: conversation.PlaceOnForge}, "owner"); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, w.folder, "reset", "--hard", "HEAD~1")
	if _, err := os.Stat(filepath.Join(w.folder, "removed-locally.txt")); !os.IsNotExist(err) {
		t.Fatalf("reset did not remove file: %v", err)
	}
	out := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/diff", w.change.ID), "")
	if out.Code != 200 || strings.Contains(out.Body.String(), "removed-locally.txt") {
		t.Fatalf("local chat reset hidden by published history: %d %s", out.Code, out.Body.String())
	}
}

// A landing's own copy is its
// candidate, so its diff and files stay its own after the change's worker
// chat saves more.
func TestALandingsOwnCopyIsAlwaysItsCandidate(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	defineWorkflows(t, w.a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "accept", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}}}}})
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	first := w.commitOnWorker(t, s.ID, "candidate.txt", "candidate\n")
	run, err := w.a.Work.RequestLanding(conversation.Context(context.Background(), s), w.change.ID)
	if err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	source := workflowSourceForTest(t, w.a, run)
	if got := gittest.Run(t, source.Workspace(w.a.cfg.dataDir), "rev-parse", "HEAD"); got != first {
		t.Fatalf("landing clone %s != %s", got, first)
	}
	filePath := fmt.Sprintf("/api/v1/runs/%d/workspace/candidate.txt", run.ID)
	initialFiles := authzCall(w.a.Handler(), "owner", "GET", filePath, "")
	if initialFiles.Code != 200 || initialFiles.Body.String() != "candidate\n" {
		t.Fatalf("initial candidate file: %d %s", initialFiles.Code, initialFiles.Body.String())
	}
	path := fmt.Sprintf("/api/v1/runs/%d/diff", run.ID)
	before := authzCall(w.a.Handler(), "owner", "GET", path, "")
	if before.Code != 200 || strings.TrimSpace(before.Body.String()) != "" {
		t.Fatalf("initial diff: %d %s", before.Code, before.Body.String())
	}
	if _, err := w.a.Work.Abandon(asPerson("owner"), workflow.StopWork{RunID: run.ID, Reason: "revise candidate"}); err != nil {
		t.Fatal(err)
	}
	second := w.commitOnWorker(t, s.ID, "after-candidate.txt", "later work\n")
	w.a.changeConsole.SaveAfterTurn(s.ID)
	save, _, _ := repositoryedit.LastChatSave(s.Dir)
	published := w.branchTip(t)
	t.Logf("source branch=%s change branch=%s save=%+v published=%s second=%s", source.Branch, w.change.Branch, save, published, second)
	if published != second {
		t.Fatalf("second save not published: %+v", save)
	}
	out := authzCall(w.a.Handler(), "owner", "GET", path, "")
	if out.Code != 200 || strings.Contains(out.Body.String(), "after-candidate.txt") {
		t.Fatalf("landing diff includes work outside its unchanged clone: %d %s", out.Code, out.Body.String())
	}
	files := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/runs/%d/workspace/candidate.txt", run.ID), "")
	if files.Code != 200 || files.Body.String() != "candidate\n" {
		t.Fatalf("landing's own candidate file unavailable: %d %s", files.Code, files.Body.String())
	}
}

// An opening that fails does not
// bring the folder to the branch, so a folder that did not catch up with the
// ended worker chat's saves is still not shown as the change's work.
func TestAFailedOpeningDoesNotHideSavedWork(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	lock := gittest.Run(t, w.folder, "rev-parse", "--git-path", "index.lock")
	if !filepath.IsAbs(lock) {
		lock = filepath.Join(w.folder, lock)
	}
	if err := os.WriteFile(lock, []byte("interrupted git\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tip := w.commitOnWorker(t, s.ID, "saved-by-worker.txt", "saved work\n")
	w.a.changeConsole.SaveAfterTurn(s.ID)
	if w.branchTip(t) != tip {
		t.Fatal("save not published")
	}
	if err := w.a.changeConsole.End(context.Background(), s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/changes/%d/diff", w.change.ID)
	before := authzCall(w.a.Handler(), "owner", "GET", path, "")
	if before.Code != 200 || !strings.Contains(before.Body.String(), "saved-by-worker.txt") {
		t.Fatalf("precondition: %d %s", before.Code, before.Body.String())
	}
	p, _ := w.a.Projects.BySlug(context.Background(), "game")
	_, err = w.a.consoleAdmission.Open(asPerson("owner"), &p, conversation.ProjectOpen{ChangeID: w.change.ID, Model: "fake/model", Placement: conversation.PlaceOnForge}, "owner")
	if err == nil {
		t.Fatal("expected opening with locked folder to fail")
	}
	t.Logf("opening failed: %v", err)
	after := authzCall(w.a.Handler(), "owner", "GET", path, "")
	if after.Code != 200 || !strings.Contains(after.Body.String(), "saved-by-worker.txt") {
		t.Errorf("failed opening hid saved work: %d %s", after.Code, after.Body.String())
	}
	evidence, err := narrativeDiff(context.Background(), w.a.cfg.dataDir, workflow.NarrativeSource{Change: w.change, Slug: "game", DefaultBranch: "main"})
	if err == nil && evidence.Empty {
		t.Error("failed opening also made saved narrative empty")
	}
	detail := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d", w.change.ID), "")
	if detail.Code == 200 && strings.Contains(detail.Body.String(), `"committedDiff":{"status":"empty"`) {
		t.Error("failed opening also made detail's committed comparison empty")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if _, err := w.a.consoleAdmission.Open(asPerson("owner"), &p, conversation.ProjectOpen{ChangeID: w.change.ID, Model: "fake/model", Placement: conversation.PlaceOnForge}, "owner"); err != nil {
		t.Fatal(err)
	}
	repaired := authzCall(w.a.Handler(), "owner", "GET", path, "")
	if repaired.Code != 200 || !strings.Contains(repaired.Body.String(), "saved-by-worker.txt") {
		t.Fatalf("successful local handoff: %d %s", repaired.Code, repaired.Body.String())
	}
}

// A landing requested after a worker chat ended takes the change for a
// moment and releases it, without touching the folder, so a folder that did
// not catch up with the chat's saves is still not shown as the work.
func TestAReleasedChangeShowsSavedWorkWhenTheFolderLags(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	defineWorkflows(t, w.a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "accept", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}}}}})
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(w.folder, ".git", "index.lock")
	if err := os.WriteFile(lock, []byte("interrupted git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tip := w.commitOnWorker(t, s.ID, "saved-by-worker.txt", "saved work\n")
	w.a.changeConsole.SaveAfterTurn(s.ID)
	if err := w.a.changeConsole.End(context.Background(), s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.a.Work.RequestLanding(asPerson("owner"), w.change.ID); err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	if w.branchTip(t) != tip || gittest.Run(t, w.folder, "rev-parse", "HEAD") == tip {
		t.Fatal("precondition: the branch has the save and the folder lags")
	}
	if held, err := scm.ReadAuthorityIn(context.Background(), w.a.DB, w.change.ProjectID, w.change.Branch); err != nil || !held.Free() {
		t.Fatalf("precondition: the landing's request released the change: %+v %v", held, err)
	}
	out := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/diff", w.change.ID), "")
	if out.Code != 200 || !strings.Contains(out.Body.String(), "saved-by-worker.txt") {
		t.Fatalf("a released change hid its saved work: %d %s", out.Code, out.Body.String())
	}
}

// A folder marked behind is read again once it is brought to its branch:
// by a later save that can reach it, or by a chat on the forge opening on
// it. After that the folder is the work again, a local reset included.
func TestACaughtUpFolderIsReadAgain(t *testing.T) {
	t.Parallel()
	behind := func(t *testing.T, w workerChatWorld, id string) string {
		t.Helper()
		lock := filepath.Join(w.folder, ".git", "index.lock")
		if err := os.WriteFile(lock, []byte("interrupted git\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		w.commitOnWorker(t, id, "first.txt", "first\n")
		w.a.changeConsole.SaveAfterTurn(id)
		return lock
	}
	t.Run("by a later save", func(t *testing.T) {
		t.Parallel()
		w := newWorkerChatWorld(t)
		run := abandonedRunOn(t, w)
		s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
		if err != nil {
			t.Fatal(err)
		}
		files := fmt.Sprintf("/api/v1/runs/%d/workspace/", run)
		lock := behind(t, w, s.ID)
		if out := authzCall(w.a.Handler(), "owner", "GET", files, ""); out.Code != 409 {
			t.Fatalf("precondition: a folder behind its branch: %d %s", out.Code, out.Body.String())
		}
		if err := os.Remove(lock); err != nil {
			t.Fatal(err)
		}
		w.commitOnWorker(t, s.ID, "second.txt", "second\n")
		w.a.changeConsole.SaveAfterTurn(s.ID)
		if out := authzCall(w.a.Handler(), "owner", "GET", files, ""); out.Code != 200 {
			t.Fatalf("a folder the save brought along is still not shown: %d %s", out.Code, out.Body.String())
		}
	})
	t.Run("by a chat on AppTown opening", func(t *testing.T) {
		t.Parallel()
		w := newWorkerChatWorld(t)
		s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
		if err != nil {
			t.Fatal(err)
		}
		lock := behind(t, w, s.ID)
		if err := w.a.changeConsole.End(context.Background(), s.ID, "owner"); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(lock); err != nil {
			t.Fatal(err)
		}
		p, _ := w.a.Projects.BySlug(context.Background(), "game")
		if _, err := w.a.consoleAdmission.Open(asPerson("owner"), &p, conversation.ProjectOpen{ChangeID: w.change.ID, Model: "fake/model", Placement: conversation.PlaceOnForge}, "owner"); err != nil {
			t.Fatal(err)
		}
		gittest.Run(t, w.folder, "reset", "--hard", "HEAD~1")
		out := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/diff", w.change.ID), "")
		if out.Code != 200 || strings.Contains(out.Body.String(), "first.txt") {
			t.Fatalf("the local chat's reset folder is not read: %d %s", out.Code, out.Body.String())
		}
	})
}

// A change's holder being
// replaced (an opening that failed, a landing request's release) says
// nothing about its folder, so an ended local chat's reset stays its work.
func TestALocalResetSurvivesTheHolderBeingReplaced(t *testing.T) {
	t.Parallel()
	for _, next := range []string{"failed-opening", "released-landing-request"} {
		t.Run(next, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			w := newWorkerChatWorld(t)
			run := abandonedRunOn(t, w)
			defineWorkflows(t, w.a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "accept", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}}}}})
			worker, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
			if err != nil {
				t.Fatal(err)
			}
			published := w.commitOnWorker(t, worker.ID, "removed-locally.txt", "old worker work\n")
			w.a.changeConsole.SaveAfterTurn(worker.ID)
			if err := w.a.changeConsole.End(ctx, worker.ID, "owner"); err != nil {
				t.Fatal(err)
			}
			p, err := w.a.Projects.BySlug(ctx, "game")
			if err != nil {
				t.Fatal(err)
			}
			local, err := w.a.consoleAdmission.Open(asPerson("owner"), &p, conversation.ProjectOpen{ChangeID: w.change.ID, Model: "fake/model", Placement: conversation.PlaceOnForge}, "owner")
			if err != nil {
				t.Fatal(err)
			}
			gittest.Run(t, w.folder, "reset", "--hard", "HEAD~1")
			reset := gittest.Run(t, w.folder, "rev-parse", "HEAD")
			if err := w.a.changeConsole.End(ctx, local.ID, "owner"); err != nil {
				t.Fatal(err)
			}
			diffPath := fmt.Sprintf("/api/v1/changes/%d/diff", w.change.ID)
			before := authzCall(w.a.Handler(), "owner", "GET", diffPath, "")
			if before.Code != 200 || strings.TrimSpace(before.Body.String()) != "" {
				t.Fatalf("ended local chat must show its reset history: %d %s", before.Code, before.Body.String())
			}
			filesPath := fmt.Sprintf("/api/v1/runs/%d/workspace/", run)
			if files := authzCall(w.a.Handler(), "owner", "GET", filesPath, ""); files.Code != 200 {
				t.Fatalf("local folder before replacement: %d %s", files.Code, files.Body.String())
			}
			switch next {
			case "failed-opening":
				lock := gittest.Run(t, w.folder, "rev-parse", "--git-path", "index.lock")
				if !filepath.IsAbs(lock) {
					lock = filepath.Join(w.folder, lock)
				}
				if err := os.WriteFile(lock, []byte("interrupted git\n"), 0600); err != nil {
					t.Fatal(err)
				}
				_, err := w.a.consoleAdmission.Open(asPerson("owner"), &p, conversation.ProjectOpen{ChangeID: w.change.ID, Model: "fake/model", Placement: conversation.PlaceOnForge}, "owner")
				if err == nil {
					t.Fatal("expected locked catch-up to fail")
				}
				t.Logf("opening failed: %v", err)
			case "released-landing-request":
				landing, err := w.a.Work.RequestLanding(asPerson("owner"), w.change.ID)
				if err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
					t.Fatal(err)
				}
				if _, err := w.a.Work.Abandon(asPerson("owner"), workflow.StopWork{RunID: landing.ID, Reason: "continue local work"}); err != nil {
					t.Fatal(err)
				}
			}
			authority, err := scm.ReadAuthorityIn(ctx, w.a.DB, w.change.ProjectID, w.change.Branch)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("holder after replacement: %s", authority.Holder)
			if next == "released-landing-request" && !authority.Free() {
				t.Fatal("request did not release")
			}
			if got := gittest.Run(t, w.folder, "rev-parse", "HEAD"); got != reset {
				t.Fatal("folder was changed")
			}
			if w.branchTip(t) != published {
				t.Fatal("published branch was changed")
			}
			if _, err := os.Stat(filepath.Join(w.folder, "removed-locally.txt")); !os.IsNotExist(err) {
				t.Fatalf("reset file reappeared in folder: %v", err)
			}
			after := authzCall(w.a.Handler(), "owner", "GET", diffPath, "")
			if after.Code != 200 || strings.TrimSpace(after.Body.String()) != "" {
				t.Errorf("unchanged local history replaced by old published work: %d %s", after.Code, after.Body.String())
			}
			evidence, err := narrativeDiff(ctx, w.a.cfg.dataDir, workflow.NarrativeSource{Change: w.change, Slug: "game", DefaultBranch: "main"})
			if err != nil || !evidence.Empty {
				t.Errorf("reset narrative changed: %+v %v", evidence, err)
			}
			detail := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d", w.change.ID), "")
			if detail.Code != 200 || !strings.Contains(detail.Body.String(), `"committedDiff":{"status":"empty"`) {
				t.Errorf("detail no longer reports the empty reset comparison: %d %s", detail.Code, detail.Body.String())
			}
			files := authzCall(w.a.Handler(), "owner", "GET", filesPath, "")
			if files.Code != 200 {
				t.Errorf("unchanged local folder is no longer browsable: %d %s", files.Code, files.Body.String())
			}
		})
	}
}

// A writing run that takes a folder behind its branch shows it once
// preparation catches it up, including the run's subsequent unpublished committed work.
func TestARunPreparingABehindFolderReadsItsOwnWork(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	chat, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(w.folder, ".git", "index.lock")
	if err := os.WriteFile(lock, []byte("interrupted git\n"), 0600); err != nil {
		t.Fatal(err)
	}
	published := w.commitOnWorker(t, chat.ID, "saved.txt", "saved work\n")
	w.a.changeConsole.SaveAfterTurn(chat.ID)
	if w.branchTip(t) != published {
		t.Fatal("save did not publish")
	}
	if behind, err := scm.Behind(context.Background(), w.folder, w.a.projectRepoPath("game")); err != nil || !behind {
		t.Fatalf("not marked: %v %v", behind, err)
	}
	if err := w.a.changeConsole.End(context.Background(), chat.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	run, err := w.a.Work.StartRun(asPerson("owner"), workflow.StartChangeRun{ChangeID: w.change.ID, Workflow: "implement"})
	if err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	if head := gittest.Run(t, w.folder, "rev-parse", "HEAD"); head != published {
		t.Fatalf("preparation did not catch up: %s != %s", head, published)
	}
	files := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/runs/%d/workspace/", run.ID), "")
	if files.Code != 200 {
		t.Errorf("caught-up run folder unavailable: %d %s", files.Code, files.Body.String())
	}
	if err := os.WriteFile(filepath.Join(w.folder, "run-work.txt"), []byte("new run work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, w.folder, "add", "run-work.txt")
	gittest.Run(t, w.folder, "commit", "-qm", "work by the run")
	if w.branchTip(t) != published {
		t.Fatal("test unexpectedly published run work")
	}
	for _, path := range []string{fmt.Sprintf("/api/v1/changes/%d/diff", w.change.ID), fmt.Sprintf("/api/v1/runs/%d/diff", run.ID)} {
		out := authzCall(w.a.Handler(), "owner", "GET", path, "")
		if out.Code != 200 || !strings.Contains(out.Body.String(), "run-work.txt") {
			t.Errorf("run's work missing from %s: %d %s", path, out.Code, out.Body.String())
		}
	}
	evidence, err := narrativeDiff(context.Background(), w.a.cfg.dataDir, workflow.NarrativeSource{Change: w.change, Slug: "game", DefaultBranch: "main"})
	if err != nil || !strings.Contains(fmt.Sprint(evidence), "run-work.txt") {
		t.Errorf("run work missing from narrative: %+v %v", evidence, err)
	}
	// The run's own history is its work, even reset behind the saved commit.
	gittest.Run(t, w.folder, "reset", "--hard", "HEAD~2")
	out := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/diff", w.change.ID), "")
	if out.Code != 200 || strings.Contains(out.Body.String(), "saved.txt") {
		t.Errorf("the run's reset folder is not read: %d %s", out.Code, out.Body.String())
	}
}

// Work left uncommitted in the folder (a hand edit) while a worker chat
// saves to the branch the folder cannot follow (a left-behind index lock,
// taken after the chat opened): local preparation later commits those
// leftovers and detects divergence; repairing and publishing that
// divergence must not leave an equal folder permanently marked behind.
func TestARepairedDivergenceIsNotBehind(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	run := abandonedRunOn(t, w)
	chat, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.folder, "leftover.txt"), []byte("previous local work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(w.folder, ".git", "index.lock")
	if err := os.WriteFile(lock, []byte("interrupted git\n"), 0600); err != nil {
		t.Fatal(err)
	}
	saved := w.commitOnWorker(t, chat.ID, "saved.txt", "worker work\n")
	w.a.changeConsole.SaveAfterTurn(chat.ID)
	if w.branchTip(t) != saved {
		t.Fatal("save did not publish")
	}
	if err := w.a.changeConsole.End(context.Background(), chat.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if status := gittest.Run(t, w.folder, "status", "--porcelain"); !strings.Contains(status, "leftover.txt") {
		t.Fatalf("leftover unexpectedly committed: %s", status)
	}
	p, err := w.a.Projects.BySlug(context.Background(), "game")
	if err != nil {
		t.Fatal(err)
	}
	local, err := w.a.consoleAdmission.Open(asPerson("owner"), &p, conversation.ProjectOpen{ChangeID: w.change.ID, Model: "fake/model", Placement: conversation.PlaceOnForge}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if ref := gittest.Run(t, w.folder, "rev-parse", workflow.PublishedRef); ref != saved {
		t.Fatalf("no recorded divergence: %s", ref)
	}
	if behind, err := scm.Behind(context.Background(), w.folder, w.a.projectRepoPath("game")); err != nil || !behind {
		t.Fatalf("expected documented marked divergence: %v %v", behind, err)
	}
	gittest.Run(t, w.folder, "merge", "--no-edit", workflow.PublishedRef)
	if err := w.a.changeConsole.End(context.Background(), local.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	head := gittest.Run(t, w.folder, "rev-parse", "HEAD")
	if w.branchTip(t) != head {
		t.Fatal("repaired work was not published on local end")
	}
	out := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/runs/%d/workspace/", run), "")
	if out.Code != 200 {
		t.Errorf("repaired and published folder still unavailable: %d %s", out.Code, out.Body.String())
	}
	if behind, err := scm.Behind(context.Background(), w.folder, w.a.projectRepoPath("game")); err != nil || behind {
		t.Errorf("repaired equal folder remains marked: %v %v", behind, err)
	}
	savedBeside(t, w.folder, saved)
	diff := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/diff", w.change.ID), "")
	if diff.Code != 200 || !strings.Contains(diff.Body.String(), "saved.txt") || !strings.Contains(diff.Body.String(), "leftover.txt") {
		t.Errorf("the repaired folder's history is not read: %d %s", diff.Code, diff.Body.String())
	}
}

// savedBeside checks a folder's saved record is kept beside it, naming
// commit, and nothing is written in its .git.
func savedBeside(t *testing.T, folder, commit string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(folder, ".git", "forge-saved")); !os.IsNotExist(err) {
		t.Fatalf("a record was written in the folder: %v", err)
	}
	record, err := os.ReadFile(filepath.Join(filepath.Dir(folder), ".saved", filepath.Base(folder)))
	if err != nil || !strings.HasPrefix(string(record), commit+" ") {
		t.Fatalf("the record beside the folder: %q %v, want %s", record, err, commit)
	}
}

// Desktop sync is another supported handoff which brings the folder to
// the published branch without opening a local chat or calling PrepareChange.
func TestADesktopSyncBringsABehindFolderBack(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	run := abandonedRunOn(t, w)
	chat, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	before := gittest.Run(t, w.folder, "rev-parse", "HEAD")
	lock := filepath.Join(w.folder, ".git", "index.lock")
	if err := os.WriteFile(lock, []byte("interrupted git\n"), 0600); err != nil {
		t.Fatal(err)
	}
	saved := w.commitOnWorker(t, chat.ID, "saved.txt", "worker work\n")
	w.a.changeConsole.SaveAfterTurn(chat.ID)
	if w.branchTip(t) != saved {
		t.Fatal("save did not publish")
	}
	if err := w.a.changeConsole.End(context.Background(), chat.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	c := contributionsFor(w.a)
	checkout, err := c.Checkout(asPerson("owner"), w.change.ID)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Tip != saved {
		t.Fatal("desktop was not given saved work")
	}
	if _, err := c.Sync(asPerson("owner"), w.change.ID, checkout.Tip, before); err != nil {
		t.Fatal(err)
	}
	if head := gittest.Run(t, w.folder, "rev-parse", "HEAD"); head != saved {
		t.Fatalf("sync did not catch up: %s", head)
	}
	out := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/runs/%d/workspace/", run), "")
	if out.Code != 200 {
		t.Errorf("synced folder still unavailable: %d %s", out.Code, out.Body.String())
	}
	savedBeside(t, w.folder, saved)
	if behind, err := scm.Behind(context.Background(), w.folder, w.a.projectRepoPath("game")); err != nil || behind {
		t.Errorf("a synced folder is behind: %v %v", behind, err)
	}
	diff := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/diff", w.change.ID), "")
	if diff.Code != 200 || !strings.Contains(diff.Body.String(), "saved.txt") {
		t.Errorf("the synced folder's history is not read: %d %s", diff.Code, diff.Body.String())
	}
}

// A save refused at publication names a commit the branch never gets, so the
// folder, still at the branch, is not behind and is shown.
func TestARefusedSaveLeavesTheFolderShown(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	run := abandonedRunOn(t, w)
	chat, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	w.commitOnWorker(t, chat.ID, ".forge/not-source", "refused\n")
	w.a.changeConsole.SaveAfterTurn(chat.ID)
	if save, _, err := repositoryedit.LastChatSave(chat.Dir); err != nil || save.Outcome != repositoryedit.ChatSaveRefused || save.Kept == "" {
		t.Fatalf("precondition: a save refused at publication: %+v %v", save, err)
	}
	files := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/runs/%d/workspace/", run), "")
	if files.Code != 200 {
		t.Fatalf("a folder at its branch is shown as behind after a refused save: %d %s", files.Code, files.Body.String())
	}
	save, _, _ := repositoryedit.LastChatSave(chat.Dir)
	savedBeside(t, w.folder, save.Commit)
	diff := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/diff", w.change.ID), "")
	if diff.Code != 200 || strings.Contains(diff.Body.String(), "not-source") {
		t.Fatalf("the change's history shows a refused save: %d %s", diff.Code, diff.Body.String())
	}
}

// Failure to read the
// project's history is not evidence that the folder is current.
func TestAnUnreadableHistoryIsNotACurrentFolder(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	run := abandonedRunOn(t, w)
	chat, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.folder, ".git", "index.lock"), []byte("busy\n"), 0600); err != nil {
		t.Fatal(err)
	}
	saved := w.commitOnWorker(t, chat.ID, "saved.txt", "published work\n")
	w.a.changeConsole.SaveAfterTurn(chat.ID)
	if w.branchTip(t) != saved {
		t.Fatal("save did not publish")
	}
	hub := w.a.projectRepoPath("game")
	if behind, err := scm.Behind(context.Background(), w.folder, hub); err != nil || !behind {
		t.Fatalf("not behind: %v %v", behind, err)
	}
	config := filepath.Join(hub, ".git", "config")
	original, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.WriteFile(config, original, 0644); err != nil {
			t.Error(err)
		}
	}()
	if err := os.WriteFile(config, []byte("[broken\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := gitexec.Run(t.Context(), hub, "merge-base", "--is-ancestor", saved, "refs/heads/"+w.change.Branch); err == nil || !strings.Contains(out, "bad config") {
		t.Fatalf("no Git read failure: %q %v", out, err)
	}
	if behind, err := scm.Behind(context.Background(), w.folder, hub); err == nil {
		t.Errorf("Git failure became behind=%v with no error", behind)
	}
	diff := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/diff", w.change.ID), "")
	if diff.Code == 200 && !strings.Contains(diff.Body.String(), "saved.txt") {
		t.Errorf("Git failure became stale successful diff: %d %q", diff.Code, diff.Body.String())
	}
	files := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/runs/%d/workspace/", run), "")
	if files.Code == 200 {
		t.Errorf("Git failure allowed stale folder: %d", files.Code)
	}
}

// A change whose folder and published branch have both moved keeps a new
// chat off the worker, and the refusal offers the chat on the forge (which
// merges them) by its code.
func TestADivergedFolderOffersTheChatOnTheForge(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	// An interrupted git in the folder keeps the save from bringing it along,
	// so the branch moves without it.
	lock := filepath.Join(w.folder, ".git", "index.lock")
	if err := os.WriteFile(lock, []byte("interrupted git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.commitOnWorker(t, s.ID, "on-worker.txt", "saved from the worker\n")
	w.a.changeConsole.SaveAfterTurn(s.ID)
	if err := w.a.changeConsole.End(context.Background(), s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	commitIn(t, w.folder, "in-folder.txt", "committed in the folder\n")
	_, err = w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err == nil || !strings.Contains(err.Error(), "both moved") {
		t.Fatalf("opening on the worker from a diverged folder: %v", err)
	}
	if fault.CodeOf(err) != conversation.CodeChatOnForge {
		t.Fatalf("the refusal does not say the chat can open on AppTown: %q", fault.CodeOf(err))
	}
}
