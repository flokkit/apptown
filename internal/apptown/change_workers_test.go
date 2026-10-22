package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execfile"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/poll"
)

// Each test here is a way a second worker could otherwise write a change's
// folder, or a check could read something other than what lands.

// Two opens of one change's chat, the second arriving while the first is
// still preparing the folder, end with one chat: the second waits and
// returns the first, rather than taking the change from a chat that does
// not exist yet.
func TestConcurrentOpensOfAChangeShareOneChat(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	release, err := w.a.repositories.Acquire(context.Background(), w.change.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	type result struct {
		id   string
		code int
		body string
	}
	done := make(chan result, 2)
	open := func() { id, code, body := w.openChat(t); done <- result{id, code, body} }
	go open()
	var first string
	poll.Until(context.Background(), 5*time.Second, time.Millisecond, func() bool {
		first = w.holder(t)
		return strings.HasPrefix(first, "chat:")
	})
	if !strings.HasPrefix(first, "chat:") {
		t.Fatal("first request did not reserve")
	}
	go open()
	// Give the second request time to take the change from the first, as
	// it would if the opening chat did not count as live: a bounded
	// negative observation, ending early only if the holder does change.
	poll.Until(context.Background(), time.Second, time.Millisecond, func() bool { return w.holder(t) != first })
	release()
	a, b := <-done, <-done
	if a.code != 201 || b.code != 201 || a.id != b.id {
		t.Fatalf("concurrent opens: %+v %+v; live chats=%d holder=%s", a, b, len(w.a.Console.List(context.Background(), console.Match{})), w.holder(t))
	}
}

// A chat on a closed change is kept to read and never runs again, so
// nothing writes a folder the closure removes; closing the change ends it.
func TestAChatOnAClosedChangeCannotRunAgain(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	chat, code, body := w.openChat(t)
	if code != 201 {
		t.Fatalf("open: %d %s", code, body)
	}
	if _, err := w.a.Work.Abandon(asPerson("owner"), workflow.StopWork{ChangeID: w.change.ID, Reason: "closed"}); err != nil {
		t.Fatal(err)
	}
	if err := w.a.Console.Evict(context.Background(), chat); err != nil {
		t.Fatal(err)
	}
	err := w.a.Console.Send(context.Background(), chat, "keep editing")
	sess, getErr := w.a.Console.Get(context.Background(), chat)
	if err == nil && getErr == nil && sess.Writable && w.agent.Exists(chat) {
		t.Fatalf("closed change woke a writable chat; holder=%s", w.holder(t))
	}
	if _, err := workflowFollowUps(w.a).Abandon(context.Background(), fmt.Sprintf("change/%d/cleanup", w.change.ID), map[string]any{"runIds": []int64{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.a.Console.Get(context.Background(), chat); err == nil || w.agent.Exists(chat) {
		t.Fatal("closing the change left its chat live")
	}
}

// A desktop push Git accepted before its ticket was revoked is not
// published after the revocation.
func TestARevokedTicketsPushIsNotPublished(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	checkout, err := contributionsFor(w.a).Checkout(asPerson("owner"), w.change.ID)
	if err != nil {
		t.Fatal(err)
	}
	hub := w.a.projectRepoPath("game")
	writer := filepath.Join(t.TempDir(), "desktop")
	gittest.Run(t, t.TempDir(), "clone", "--quiet", "--no-hardlinks", hub, writer)
	gittest.Run(t, writer, "checkout", "-qb", w.change.Branch)
	if err := os.WriteFile(filepath.Join(writer, "late.txt"), []byte("late\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, writer, "add", "late.txt")
	gittest.Run(t, writer, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "late push")
	tip := gittest.Run(t, writer, "rev-parse", "HEAD")
	gittest.Run(t, hub, "fetch", "--quiet", writer, "HEAD:"+scm.IncomingRef(checkout.Ticket.ID))
	if err := w.a.Access.RevokeChangeTickets(context.Background(), w.change.ID); err != nil {
		t.Fatal(err)
	}
	if err := contributionsFor(w.a).PublishPush(context.Background(), checkout.Ticket); err == nil {
		t.Fatalf("revoked ticket published %s; hub=%s", tip, scm.Ref(t.Context(), hub, "refs/heads/"+w.change.Branch))
	}
}

// A desktop push Git accepted after its ticket expired, by AppTown's
// clock, is not published: AppTown's clock two days on is past any
// ticket's life, whatever the real clock says.
//
// Kills: the ticket judged at publication by the real clock (it still
// holds, and the push is published).
func TestAnExpiredTicketsPushIsNotPublishedByAppTownsClock(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	checkout, err := contributionsFor(w.a).Checkout(asPerson("owner"), w.change.ID)
	if err != nil {
		t.Fatal(err)
	}
	hub := w.a.projectRepoPath("game")
	writer := filepath.Join(t.TempDir(), "desktop")
	gittest.Run(t, t.TempDir(), "clone", "--quiet", "--no-hardlinks", hub, writer)
	gittest.Run(t, writer, "checkout", "-qb", w.change.Branch)
	if err := os.WriteFile(filepath.Join(writer, "late.txt"), []byte("late\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, writer, "add", "late.txt")
	gittest.Run(t, writer, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "late push")
	tip := gittest.Run(t, writer, "rev-parse", "HEAD")
	gittest.Run(t, hub, "fetch", "--quiet", writer, "HEAD:"+scm.IncomingRef(checkout.Ticket.ID))
	moveClock(w.a, 48*time.Hour)
	if err := contributionsFor(w.a).PublishPush(context.Background(), checkout.Ticket); err == nil {
		t.Fatalf("an expired ticket published %s; hub=%s", tip, scm.Ref(t.Context(), hub, "refs/heads/"+w.change.Branch))
	}
}

// A recheck after the chat commits its fix checks that commit: the chat's
// work is published before the landing picks its candidate again.
func TestARecheckChecksTheChatsNewCommit(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	_, code, body := w.openChat(t)
	if code != 201 {
		t.Fatalf("open: %d %s", code, body)
	}
	write := func(value string) {
		if err := os.WriteFile(filepath.Join(w.folder, "value.txt"), []byte(value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		gittest.Run(t, w.folder, "add", "value.txt")
		gittest.Run(t, w.folder, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", value)
	}
	write("bad")
	defineWorkflows(t, w.a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "checks", OnEnter: []schema.Step{{Name: "catch-up", Uses: "reconcile"}}, Steps: []schema.Step{{Name: "verify", Uses: "script", With: map[string]any{"run": "test \"$(cat value.txt)\" = good"}}, {Name: "hold", Uses: "approval"}}}}}})
	rebuildWork(t, w.a, func(deps *workflow.Dependencies) { deps.Runners = w.a.workDependencies().Runners })
	w.a.Work.Stop()
	run, err := w.a.Work.RequestLanding(asPerson("owner"), w.change.ID)
	if err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	if step := reviewExecute(t, w.a, run, "catch-up"); step.Status != workflow.StatusPass {
		t.Fatalf("reconcile: %s %s", step.Status, step.Evidence)
	}
	if step := reviewExecute(t, w.a, run, "verify"); step.Status != workflow.StatusFail {
		t.Fatalf("initial check: %s %s", step.Status, step.Evidence)
	}
	write("good")
	wanted := gittest.Run(t, w.folder, "rev-parse", "HEAD")
	result, err := w.a.Work.RecheckLanding(asPerson("owner"), workflow.LandingRecheck{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Names) == 0 {
		t.Fatal("no check selected")
	}
	reviewExecute(t, w.a, run, "catch-up")
	check := reviewExecute(t, w.a, run, "verify")
	candidate, _, err := workflow.LandingCandidateIn(context.Background(), w.a.DB, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if check.Status != workflow.StatusPass || candidate.Candidate != wanted {
		t.Fatalf("recheck still checks old work: status=%s candidate=%s new chat commit=%s", check.Status, candidate.Candidate, wanted)
	}
}

// Whatever is still writing the change's folder keeps its change held after
// its run is stopped: here an agent whose container outlived a timeout, or
// a lost Docker connection. The change is released once Docker confirms
// nothing labelled as writing the folder is left; asking removes it.
func TestSomethingStillWritingTheFolderKeepsTheChangeHeld(t *testing.T) {
	t.Parallel()
	for name, outcome := range map[string]ExecOutcome{
		"a timeout whose removal failed": {TimedOut: true, RunErr: context.DeadlineExceeded, CleanupErr: errors.New("Docker removal not confirmed")},
		"a lost Docker connection":       {RunErr: errors.New("Docker attach connection lost"), ExitCode: 125},
	} {
		t.Run(name, func(t *testing.T) {
			a := agentTestApp(t)
			a.cfg.agentExecutor = ExecutorDocker
			daemon := newSurvivingContainers()
			a.docker = docker.Scope{Client: daemon}
			runID, stepID := admittedAgentStep(t, a, `{"prompt":"do it"}`)
			var container string
			installFakePi(t, a, func(_ int, req ExecRequest) ExecOutcome {
				container = req.Name
				daemon.survive(req)
				return outcome
			})
			a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
			if _, err := a.Work.Abandon(asPerson("alice"), workflow.StopWork{RunID: runID, Reason: "stop the writer"}); err != nil {
				t.Fatal(err)
			}
			if container == "" || runHolderEnded(t, a, runID) {
				t.Fatalf("the change was released while agent %q may still write its folder", container)
			}
			daemon.allowRemoval()
			if !runHolderEnded(t, a, runID) || daemon.has(container) {
				t.Fatal("the change was not released once the agent's container was removed")
			}
		})
	}
}

// A workflow a landing calls checks the landing's pinned candidate, never
// the landing's scratch copy.
func TestAWorkflowALandingCallsChecksItsCandidate(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	run, source := reviewLanding(t, a, nil, map[string]string{"value.txt": "candidate\n"}, func(ids.ID) {
		defineWorkflows(t, a,
			workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "checks", Steps: []schema.Step{{Name: "call", Uses: "workflow/call", With: map[string]any{"workflow": "child"}}}}}}},
			workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "child", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{{Name: "verify", Uses: "script", With: map[string]any{"run": "test \"$(cat value.txt)\" = candidate"}}}}}}})
	})
	if err := os.WriteFile(filepath.Join(source.Workspace(a.cfg.dataDir), "value.txt"), []byte("previous scratch write\n"), 0600); err != nil {
		t.Fatal(err)
	}
	call := reviewExecute(t, a, run, "call")
	var ev struct{ ChildRunID ids.ID }
	if err := json.Unmarshal(call.Evidence, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.ChildRunID.IsZero() {
		t.Fatalf("call: %s %s", call.Status, call.Evidence)
	}
	child, err := workflow.ReadRunIn(context.Background(), a.DB, ev.ChildRunID)
	if err != nil {
		t.Fatal(err)
	}
	check := reviewExecute(t, a, child, "verify")
	if check.Status != workflow.StatusPass {
		t.Fatalf("landing child checked shared scratch instead of C: %s %s", check.Status, check.Evidence)
	}
}

// A desktop sync refuses to reset a folder holding source nobody committed,
// including source git could not stage, but AppTown's own untracked
// runtime files under .apptown are not such work, even where the project's
// .gitignore does not hide them: the reset keeps them.
func TestASyncRefusesUncommittedSourceButNotRuntimeFiles(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	chat, code, body := w.openChat(t)
	if code != 201 {
		t.Fatalf("open: %d %s", code, body)
	}
	if err := os.WriteFile(filepath.Join(w.folder, ".gitignore"), []byte("/bin/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, w.folder, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qam", "a .gitignore without .apptown")
	if err := w.a.changeConsole.End(context.Background(), chat, "owner"); err != nil {
		t.Fatal(err)
	}
	c := contributionsFor(w.a)
	checkout, err := c.Checkout(asPerson("owner"), w.change.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(w.folder, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.folder, ".apptown", "steering.md"), []byte("runtime\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// An edit git cannot stage (unreadable) is refused, not reset away.
	ignore := filepath.Join(w.folder, ".gitignore")
	if err := os.WriteFile(ignore, []byte("/bin/\nunstageable edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ignore, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sync(asPerson("owner"), w.change.ID, checkout.Tip, checkout.Tip); err == nil {
		t.Fatal("a sync over source git could not stage went ahead")
	}
	if err := os.Chmod(ignore, 0600); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(ignore); err != nil || string(body) != "/bin/\nunstageable edit\n" {
		t.Fatalf("the unstageable edit was not kept: %q %v", body, err)
	}
	gittest.Run(t, w.folder, "checkout", "--", ".gitignore")
	uncommitted := filepath.Join(w.folder, "uncommitted.txt")
	if err := os.WriteFile(uncommitted, []byte("hand edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sync(asPerson("owner"), w.change.ID, checkout.Tip, checkout.Tip); err == nil || !strings.Contains(err.Error(), "uncommitted work") {
		t.Fatalf("a sync over uncommitted source: %v", err)
	}
	gittest.Run(t, w.folder, "rm", "-q", "--cached", "uncommitted.txt")
	if err := os.Remove(uncommitted); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sync(asPerson("owner"), w.change.ID, checkout.Tip, checkout.Tip); err != nil {
		t.Fatalf("a sync over runtime files alone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.folder, ".apptown", "steering.md")); err != nil {
		t.Fatalf("the sync removed the runtime file: %v", err)
	}
}

// A desktop sync resets the change's folder; the next worker cannot start
// on the folder until the sync has finished, even when the ticket is
// revoked mid-sync. The sync is paused at its first fetch, the ticket
// revoked and the next chat opened before the sync may go on.
func TestASyncKeepsTheFolderUntilItFinishes(t *testing.T) {
	// Not parallel: t.Setenv puts a git wrapper first on the process's PATH.
	w := newHolderWorld(t)
	chat, code, body := w.openChat(t)
	if code != 201 {
		t.Fatalf("open: %d %s", code, body)
	}
	file := filepath.Join(w.folder, "value.txt")
	if err := os.WriteFile(file, []byte("published\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, w.folder, "add", "value.txt")
	gittest.Run(t, w.folder, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "published")
	if err := w.a.changeConsole.End(context.Background(), chat, "owner"); err != nil {
		t.Fatal(err)
	}
	checkout, err := contributionsFor(w.a).Checkout(asPerson("owner"), w.change.ID)
	if err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	gate := t.TempDir()
	// AppTown's git starts with -c settings of its own (gitexec); the
	// subcommand follows them.
	shim := `#!/bin/sh
subcommand() { while [ "$1" = -c ]; do shift 2; done; echo "$1"; }
sub=$(subcommand "$@")
if [ "$PWD" = "$REVIEW_FOLDER" ] && [ "$sub" = fetch ] && mkdir "$REVIEW_GATE/entered" 2>/dev/null; then
  while [ ! -f "$REVIEW_GATE/release" ]; do sleep 0.01; done
fi
exec "$REVIEW_REAL_GIT" "$@"
`
	if err := execfile.Write(filepath.Join(gate, "git"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REVIEW_FOLDER", w.folder)
	t.Setenv("REVIEW_GATE", gate)
	t.Setenv("REVIEW_REAL_GIT", realGit)
	t.Setenv("PATH", gate+string(os.PathListSeparator)+os.Getenv("PATH"))
	release := func() { _ = os.WriteFile(filepath.Join(gate, "release"), nil, 0600) }
	defer release()
	done := make(chan error, 1)
	go func() {
		_, err := contributionsFor(w.a).Sync(asPerson("owner"), w.change.ID, checkout.Tip, checkout.Tip)
		done <- err
	}()
	entered := poll.Until(context.Background(), 30*time.Second, time.Millisecond, func() bool {
		_, err := os.Stat(filepath.Join(gate, "entered"))
		return err == nil
	})
	if !entered {
		release()
		t.Fatal("sync did not reach the effect boundary")
	}
	if err := w.a.Access.RevokeChangeTickets(context.Background(), w.change.ID); err != nil {
		t.Fatal(err)
	}
	opened := make(chan int, 1)
	go func() { _, code, _ := w.openChat(t); opened <- code }()
	select {
	case code = <-opened:
		if code != 201 {
			release()
			<-done
			return
		}
	case <-time.After(time.Second):
		// The chat waits for the sync to finish, then opens on the folder
		// as the sync left it.
		release()
		if err := <-done; err != nil {
			t.Fatalf("the sync: %v", err)
		}
		if code := <-opened; code != 201 {
			t.Fatalf("the next chat after the sync: %d", code)
		}
		return
	}
	if err := os.WriteFile(file, []byte("new chat's unfinished edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	release()
	err = <-done
	got, readErr := os.ReadFile(file)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "new chat's unfinished edit\n" {
		t.Fatalf("old sync overwrote new holder's edit: sync=%v file=%q holder=%s", err, got, w.holder(t))
	}
}

// The app's native contract is source: left uncommitted as the only work,
// the landing refuses with nothing to land, rather than landing empty.
func TestAnUncommittedAppContractDoesNotLandEmpty(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	_, code, body := w.openChat(t)
	if code != 201 {
		t.Fatalf("open: %d %s", code, body)
	}
	if err := os.MkdirAll(filepath.Join(w.folder, ".apptown"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.folder, ".apptown", "app.yaml"), []byte("app:\n  web: {image: nginx:alpine, ports: [\"80\"], public: true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, w.a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "done", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}}}}})
	w.a.Work.Stop()
	run, err := w.a.Work.RequestLanding(asPerson("owner"), w.change.ID)
	if err == nil || errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatalf("empty landing admitted despite uncommitted source contract: run=%d error=%v", run.ID, err)
	}
	if !strings.Contains(err.Error(), "nothing to land") {
		t.Fatalf("unexpected refusal: %v", err)
	}
}
