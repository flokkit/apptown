package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/poll"
)

// holderWorld is a repository-backed change whose chats run on a fake agent,
// with a writing workflow ("implement") the project can run.
type holderWorld struct {
	a      *App
	change *workflow.Change
	agent  *consoletest.Agent
	folder string
}

func newHolderWorld(t *testing.T) holderWorld {
	t.Helper()
	agent := consoletest.NewAgent()
	a := authzActors(t, Config{DockerClient: newFakeDocker(), ConsoleCLI: fixtureCLI(t), ConsoleAgent: agent,
		InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
	authzWorkflow(t, a, workflow.ApprovalConfig{})
	revokeReadingForAll(t, a)
	configureConsoleModel(t, a)
	rebuildWork(t, a, func(deps *workflow.Dependencies) { deps.Git = workflow.GitWorkspace{DataDir: a.cfg.dataDir} })
	if w := authzCall(a.Handler(), "owner", "POST", "/api/v1/projects/game/repo", `{}`); w.Code != 200 {
		t.Fatalf("initialize repository: %d %s", w.Code, w.Body.String())
	}
	change, err := a.Work.Create(asPerson("owner"), workflow.ChangeDraft{ProjectID: idstest.N(1), Title: "One worker at a time"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Stop()
	return holderWorld{a: a, change: change, agent: agent, folder: filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", change.ID))}
}

// openChat opens a chat on the change and reports its id, or the refusal.
func (w holderWorld) openChat(t *testing.T) (string, int, string) {
	t.Helper()
	resp := authzCall(w.a.Handler(), "owner", "POST", "/api/v1/projects/game/console", fmt.Sprintf(`{"model":"fake/model","changeId":%q}`, w.change.ID))
	var out struct{ Session struct{ ID string } }
	_ = json.Unmarshal(resp.Body.Bytes(), &out)
	return out.Session.ID, resp.Code, resp.Body.String()
}

func (w holderWorld) holder(t *testing.T) string {
	t.Helper()
	authority, err := scm.ReadAuthorityIn(context.Background(), w.a.DB, w.change.ProjectID, w.change.Branch)
	if err != nil {
		t.Fatal(err)
	}
	return authority.Holder
}

// A change has one worker at a time. The first chat takes it; opening a
// chat again returns that chat; a writing workflow and a desktop checkout
// are refused while it holds the change. Once the chat has ended (its
// runtime removed) a workflow takes the change, and a chat is refused while
// the workflow works it.
func TestOneWorkerHoldsAChangeAtATime(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	first, code, body := w.openChat(t)
	if code != 201 || w.holder(t) != scm.ChatHolder(first) {
		t.Fatalf("the first chat: %d %s holder=%q", code, body, w.holder(t))
	}
	if again, code, _ := w.openChat(t); code != 201 || again != first {
		t.Fatalf("a second chat on the change is %q (%d), not the change's chat %q", again, code, first)
	}
	var held *workflow.ChangeHeld
	if _, err := w.a.Work.StartRun(asPerson("owner"), workflow.StartChangeRun{ChangeID: w.change.ID, Workflow: "implement"}); !errors.As(err, &held) || !strings.Contains(err.Error(), "a chat is working") {
		t.Fatalf("a workflow beside the chat: %v", err)
	}
	if _, err := contributionsFor(w.a).Checkout(asPerson("owner"), w.change.ID); err == nil || !strings.Contains(err.Error(), "a chat is working") {
		t.Fatalf("a checkout beside the chat: %v", err)
	}

	if err := w.a.changeConsole.End(context.Background(), first, "owner"); err != nil {
		t.Fatal(err)
	}
	run, err := w.a.Work.StartRun(asPerson("owner"), workflow.StartChangeRun{ChangeID: w.change.ID, Workflow: "implement"})
	if err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatalf("a workflow once the chat ended: %v", err)
	}
	if w.holder(t) != scm.RunHolder(run.ID) {
		t.Fatalf("the workflow did not take the change: %q", w.holder(t))
	}
	if _, code, body := w.openChat(t); code != 409 || !strings.Contains(body, "a workflow (run") {
		t.Fatalf("a chat beside the workflow: %d %s", code, body)
	}
}

// A landing stopped on a failed gate is taken to chat the way the change
// page offers it: opening the chat on the stopped step ends the landing,
// the change goes back to work, and the chat holds it (or the change's
// chat, already open beside the landing, is returned). A plain chat on a
// landing change is refused, as is one that names a step outside the
// landing, which would leave the change landing under the chat.
func TestTakingAStoppedLandingToChatEndsTheLanding(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	defineWorkflows(t, w.a,
		workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "gates", Steps: []schema.Step{{Name: "scan", Uses: "script"}}}}}},
		workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "note", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "apply", Steps: []schema.Step{{Name: "gate", Uses: "script"}}}}}},
	)
	w.a.Work.Stop() // the test runs each step itself
	owner := asPerson("owner")
	stopped := func(workflowName string) (*workflow.Run, ids.ID) {
		t.Helper()
		run, err := w.a.Work.StartRun(owner, workflow.StartChangeRun{ChangeID: w.change.ID, Workflow: workflowName})
		if !errors.Is(err, workflow.ErrDispatchStopped) {
			t.Fatalf("start %s: %v", workflowName, err)
		}
		snapshot, err := w.a.Work.Snapshot(owner, run.ID, workflow.DetailView)
		if err != nil || len(snapshot.Steps) != 1 {
			t.Fatalf("steps: %+v %v", snapshot, err)
		}
		gate := snapshot.Steps[0].ID
		// The project's script runner fails every step: the gate stops the run.
		if err := w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), gate); err != nil {
			t.Fatal(err)
		}
		if step := readStep(t, w.a, gate); step.Status != workflow.StatusFail {
			t.Fatalf("gate: %s", step.Status)
		}
		return run, gate
	}
	open := func(continueStep ids.ID) (string, int, string) {
		resp := authzCall(w.a.Handler(), "owner", "POST", "/api/v1/projects/game/console", fmt.Sprintf(`{"model":"fake/model","changeId":%q,"continueStep":%q}`, w.change.ID, continueStep))
		var out struct{ Session struct{ ID string } }
		_ = json.Unmarshal(resp.Body.Bytes(), &out)
		return out.Session.ID, resp.Code, resp.Body.String()
	}

	// A step of an earlier, finished run: the change stays landing.
	working, workingGate := stopped("note")
	if _, err := w.a.Work.Abandon(owner, workflow.StopWork{RunID: working.ID, Reason: "stopped"}); err != nil {
		t.Fatal(err)
	}
	landing, gate := stopped("land")
	requireChangeState(t, w.a, w.change.ID, workflow.ChangeLanding)
	if _, code, body := w.openChat(t); code != 409 || !strings.Contains(body, "is landing") {
		t.Fatalf("a plain chat on a landing change: %d %s", code, body)
	}
	if _, code, body := open(workingGate); code != 409 || !strings.Contains(body, "is landing") {
		t.Fatalf("a chat on a step outside the landing: %d %s", code, body)
	}
	requireChangeState(t, w.a, w.change.ID, workflow.ChangeLanding)

	chat, code, body := open(gate)
	if code != 201 {
		t.Fatalf("taking the stopped landing to chat: %d %s", code, body)
	}
	requireRunStatus(t, w.a, workflow.RunAbandoned, landing.ID)
	requireChangeState(t, w.a, w.change.ID, workflow.ChangeInProgress)
	if w.holder(t) != scm.ChatHolder(chat) {
		t.Fatalf("the chat does not hold the change: %q", w.holder(t))
	}

	// A landing runs beside the change's chat: taking it to chat ends the
	// landing too, and returns the chat the change has.
	again, gate := stopped("land")
	requireChangeState(t, w.a, w.change.ID, workflow.ChangeLanding)
	if same, code, body := open(gate); code != 201 || same != chat {
		t.Fatalf("taking a landing beside the chat to chat: %d %s, want the chat %q", code, body, chat)
	}
	requireRunStatus(t, w.a, workflow.RunAbandoned, again.ID)
	requireChangeState(t, w.a, w.change.ID, workflow.ChangeInProgress)
}

// A holder is released only when its owner has ended, as its own records
// show: an abandoned run with a step still running, or a chat whose runtime
// could not be removed, keeps its change.
func TestAHolderIsReleasedOnlyOnProof(t *testing.T) {
	t.Parallel()
	t.Run("a chat whose runtime is still there", func(t *testing.T) {
		t.Parallel()
		w := newHolderWorld(t)
		chat, code, body := w.openChat(t)
		if code != 201 {
			t.Fatalf("open: %d %s", code, body)
		}
		if err := w.a.Console.Send(context.Background(), chat, "start"); err != nil {
			t.Fatal(err)
		}
		// Ending the chat cannot remove its runtime (a daemon that did not
		// answer): the chat stays live and holds its change until an end
		// that removes it.
		w.agent.FailRemove(chat, 1)
		if err := w.a.changeConsole.End(context.Background(), chat, "owner"); err == nil {
			t.Fatal("an end that could not remove the runtime reported success")
		}
		gone, err := holderPresenceFor(w.a).Gone(context.Background(), chat)
		if err != nil || gone || !w.agent.Exists(chat) {
			t.Fatalf("a chat whose runtime is still there was released: gone=%v exists=%v %v", gone, w.agent.Exists(chat), err)
		}
		if err := w.a.changeConsole.End(context.Background(), chat, "owner"); err != nil {
			t.Fatal(err)
		}
		gone, err = holderPresenceFor(w.a).Gone(context.Background(), chat)
		if err != nil || !gone || w.agent.Exists(chat) {
			t.Fatalf("an ended chat was not released: gone=%v exists=%v %v", gone, w.agent.Exists(chat), err)
		}
	})
	t.Run("an abandoned run with a step still running", func(t *testing.T) {
		t.Parallel()
		w := newHolderWorld(t)
		run, err := w.a.Work.StartRun(asPerson("owner"), workflow.StartChangeRun{ChangeID: w.change.ID, Workflow: "implement"})
		if err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
			t.Fatal(err)
		}
		snapshot, err := w.a.Work.Snapshot(asPerson("owner"), run.ID, workflow.DetailView)
		if err != nil || len(snapshot.Steps) == 0 {
			t.Fatalf("steps: %+v %v", snapshot, err)
		}
		// sql-guarantee: a step whose execution has not returned yet; no operation leaves one running without executing it.
		completionExec(t, w.a.DB, `UPDATE steps SET status='running' WHERE id=$1`, snapshot.Steps[0].ID)
		if _, err := w.a.Work.Abandon(asPerson("owner"), workflow.StopWork{RunID: run.ID, Reason: "test"}); err != nil {
			t.Fatal(err)
		}
		if _, code, body := w.openChat(t); code != 409 || !strings.Contains(body, "a workflow (run") {
			t.Fatalf("a chat while the abandoned run's step still runs: %d %s", code, body)
		}
	})
}

// A worker that takes the change finds its folder brought to the branch,
// with what the previous worker left uncommitted committed as its work, so
// nothing is discarded or mistaken for the new worker's; a run's restart
// point comes after that commit.
func TestTakingAChangeKeepsThePreviousWorkersLeftovers(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	chat, code, body := w.openChat(t)
	if code != 201 {
		t.Fatalf("open: %d %s", code, body)
	}
	if err := os.WriteFile(filepath.Join(w.folder, "left.txt"), []byte("the chat's unfinished edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.a.changeConsole.End(context.Background(), chat, "owner"); err != nil {
		t.Fatal(err)
	}
	run, err := w.a.Work.StartRun(asPerson("owner"), workflow.StartChangeRun{ChangeID: w.change.ID, Workflow: "implement"})
	if err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	if got := gittest.Run(t, w.folder, "show", "HEAD:left.txt"); got != "the chat's unfinished edit" {
		t.Fatalf("the leftover was not committed: %q", got)
	}
	if got := gittest.Run(t, w.folder, "log", "-1", "--format=%s"); !strings.Contains(got, "previous worker") {
		t.Fatalf("the leftover commit says %q", got)
	}
	source := workflowSourceForTest(t, w.a, run)
	if source.RunBaseSHA != gittest.Run(t, w.folder, "rev-parse", "HEAD") {
		t.Fatalf("the run's restart point %s is not after the leftover commit %s", source.RunBaseSHA, gittest.Run(t, w.folder, "rev-parse", "HEAD"))
	}
}

// A landing takes no holder: started while the change's chat holds it (the
// chat's own `apptown land`), it publishes the chat's committed work under the
// chat's grant and leaves the chat holding the change. Nothing waits on the
// chat's turn.
func TestALandingLeavesTheChatHoldingTheChange(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	chat, code, body := w.openChat(t)
	if code != 201 {
		t.Fatalf("open: %d %s", code, body)
	}
	if err := w.a.Console.Send(context.Background(), chat, "make it"); err != nil {
		t.Fatal(err) // the chat is mid-turn from here on
	}
	if err := os.WriteFile(filepath.Join(w.folder, "made.txt"), []byte("made in the chat\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, w.folder, "add", "made.txt")
	gittest.Run(t, w.folder, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "made in the chat")
	tip := gittest.Run(t, w.folder, "rev-parse", "HEAD")
	defineWorkflows(t, w.a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing",
		Stages: []schema.Stage{{Name: "done", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}}}}})
	w.a.Work.Stop()
	if _, err := w.a.Work.RequestLanding(asPerson("owner"), w.change.ID); err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatalf("landing from the chat: %v", err)
	}
	if w.holder(t) != scm.ChatHolder(chat) {
		t.Fatalf("the landing took the change from the chat: %q", w.holder(t))
	}
	if published := scm.Ref(t.Context(), w.a.projectRepoPath("game"), "refs/heads/"+w.change.Branch); published != tip {
		t.Fatalf("the chat's commit was not published: %s, want %s", published, tip)
	}
	// The first landing has not merged yet. The chat's real code must stay
	// visible while that landing is pending, including its missing-branch error.
	assertCode := func(missing bool) {
		t.Helper()
		out := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d", w.change.ID), "")
		var view struct {
			CommittedDiff struct{ Status, Message string }
		}
		if err := json.Unmarshal(out.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		want := "changed"
		if missing {
			want = "unavailable"
		}
		if out.Code != 200 || view.CommittedDiff.Status != want {
			t.Errorf("chat code (missing=%v): %d %+v, want %s", missing, out.Code, view.CommittedDiff, want)
		}
		patch := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/diff", w.change.ID), "")
		if !missing && (patch.Code != 200 || !strings.Contains(patch.Body.String(), "made in the chat")) {
			t.Errorf("chat diff: %d %s", patch.Code, patch.Body.String())
		}
		if missing && (patch.Code == 200 || strings.Contains(patch.Body.String(), "no code of its own")) {
			t.Errorf("missing chat branch: %d %s", patch.Code, patch.Body.String())
		}
	}
	assertCode(false)
	gittest.Run(t, w.a.projectRepoPath("game"), "branch", "-D", w.change.Branch)
	if err := os.RemoveAll(w.folder); err != nil {
		t.Fatal(err)
	}
	assertCode(true)
}

// legacyChat is a chat as the upgrade leaves it: opened through the console
// while holding its change, then released from it as the migration does.
func (w holderWorld) legacyChat(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	p := w.a.projectByID(ctx, w.change.ProjectID)
	id := w.a.Console.NewSessionID()
	if err := w.a.DB.InTx(ctx, func(tx *database.Tx) error {
		_, err := workflow.TakeChangeIn(ctx, tx, p.ID, w.change.Branch, scm.ChatHolder(id), holderPresenceFor(w.a), time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.a.Console.Open(ctx, console.OpenRequest{ID: id, Scope: conversation.Key(p.ID, w.change.ID), Labels: conversation.LabelsFor(p, w.change),
		Workspace: console.Workspace{Path: w.folder, Writable: true}, Actor: "owner", Model: "fake/model"}); err != nil {
		t.Fatal(err)
	}
	if err := w.a.Console.Warm(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := w.a.DB.InTx(ctx, func(tx *database.Tx) error {
		return workflow.ReleaseChangeIn(ctx, tx, p.ID, w.change.Branch, scm.ChatHolder(id))
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// A live chat always holds its change, so at startup a chat that does not
// (every chat after the upgrade releases every change, or one left on a
// closed change) is ended, its transcript kept; the holding chat stays. When
// one cannot be ended and another worker holds its change, startup refuses
// rather than let two writers share the folder (a free change:
// TestAStuckChatOnAFreeChangeKeepsItAndStartupGoesOn).
func TestAChatThatDoesNotHoldItsChangeIsEndedAtStartup(t *testing.T) {
	t.Parallel()
	live := func(w holderWorld, id string) bool {
		_, err := w.a.Console.Get(context.Background(), id)
		return err == nil
	}
	recover := func(w holderWorld) error {
		return w.a.changeConsole.EndChatsWithoutTheirChange(context.Background())
	}
	t.Run("a stray beside the holding chat", func(t *testing.T) {
		t.Parallel()
		w := newHolderWorld(t)
		stray := w.legacyChat(t)
		holding, code, body := w.openChat(t)
		if code != 201 {
			t.Fatalf("open: %d %s", code, body)
		}
		if err := recover(w); err != nil {
			t.Fatal(err)
		}
		if !live(w, holding) || w.holder(t) != scm.ChatHolder(holding) {
			t.Fatalf("the holding chat was ended or lost its change: %s", w.holder(t))
		}
		if live(w, stray) || w.agent.Exists(stray) {
			t.Fatal("a chat that does not hold its change was left running")
		}
	})
	t.Run("a stray that cannot be ended, beside another holder", func(t *testing.T) {
		t.Parallel()
		w := newHolderWorld(t)
		stuck := w.legacyChat(t)
		w.agent.FailRemove(stuck, 1)
		if _, code, body := w.openChat(t); code != 201 {
			t.Fatalf("open: %d %s", code, body)
		}
		if err := recover(w); err == nil || !strings.Contains(err.Error(), stuck) {
			t.Fatalf("startup went ahead beside a chat that may still write: %v", err)
		}
	})
}

// A chat whose change record cannot be read does not stop startup: the chat
// is ended, so it cannot write the change's folder. Only one that cannot be
// ended refuses the start, since another worker may hold the change. One
// chat shows both: its first end fails and refuses the start, and the next
// startup ends it.
func TestAChatWhoseChangeCannotBeReadIsEndedAtStartup(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	id, code, body := w.openChat(t)
	if code != 201 {
		t.Fatalf("open: %d %s", code, body)
	}
	// sql-guarantee: a change state no operation writes is the malformed record under test; no owner operation can produce it.
	completionExec(t, w.a.DB, `UPDATE changes SET state='garbled' WHERE id=$1`, w.change.ID)
	w.agent.FailRemove(id, 1)
	if err := w.a.changeConsole.EndChatsWithoutTheirChange(context.Background()); err == nil || !strings.Contains(err.Error(), id) {
		t.Fatalf("startup went ahead beside a chat that may still write: %v", err)
	}
	err := w.a.changeConsole.EndChatsWithoutTheirChange(context.Background())
	if _, getErr := w.a.Console.Get(context.Background(), id); err != nil || getErr == nil {
		t.Fatalf("startup over a chat whose change cannot be read: %v; chat still live: %v", err, getErr == nil)
	}
}

// A chat opening while a checkout is removing leftovers from the change's
// folder. The checkout removes only under the branch's lock, so the chat
// cannot take the change in the middle of that: the two are serialized, one
// of them holds the change afterwards, and no worker that took it is
// removed. The chat opens between the checkout's unlocked look at the
// holder and its removal.
type interleavedDocker struct {
	*survivingContainers
	beforeList func()
}

func (d *interleavedDocker) Run(ctx context.Context, host string, req docker.Command) ([]byte, error) {
	if len(req.Args) > 0 && req.Args[0] == "ps" && d.beforeList != nil {
		f := d.beforeList
		d.beforeList = nil
		f()
	}
	return d.survivingContainers.Run(ctx, host, req)
}

func TestACheckoutDoesNotRemoveAWorkerThatTookTheChangeSince(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	d := &interleavedDocker{survivingContainers: newSurvivingContainers()}
	d.allowRemoval()
	w.a.docker = docker.Scope{Client: d}
	rebuildRoutes(w.a)
	type opened struct {
		id   string
		code int
	}
	chat := make(chan opened, 1)
	d.beforeList = func() {
		go func() {
			id, code, _ := w.openChat(t)
			if code == 201 {
				// The fake console starts no container: model the runtime with
				// the label console/pi gives it.
				d.survive(execRequest{Name: "apptown-console-" + id, Labels: []string{docker.WritesLabel + "=" + w.folder}})
			}
			chat <- opened{id, code}
		}()
		select {
		case c := <-chat:
			chat <- c // the chat was not held back by the checkout's lock
		case <-time.After(500 * time.Millisecond):
		}
	}
	_, checkoutErr := contributionsFor(w.a).Checkout(asPerson("owner"), w.change.ID)
	c := <-chat
	holder := w.holder(t)
	switch {
	case c.code == 201:
		if !d.has("apptown-console-"+c.id) || holder != scm.ChatHolder(c.id) || checkoutErr == nil {
			t.Fatalf("the chat opened, yet holder=%s runtime kept=%v checkout=%v", holder, d.has("apptown-console-"+c.id), checkoutErr)
		}
	default:
		if checkoutErr != nil || !strings.HasPrefix(holder, "ticket:") {
			t.Fatalf("the chat was refused, yet the checkout did not take the change: holder=%s checkout=%v", holder, checkoutErr)
		}
	}
}

// A chat that cannot be ended at startup, on a change nobody holds, does not
// stop AppTown starting: it never runs again, and its runtime, labelled
// with the folder, keeps anyone from taking the change until it is gone.
func TestAStuckChatOnAFreeChangeKeepsItAndStartupGoesOn(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	id := w.legacyChat(t)
	d := newSurvivingContainers()
	runtime := "apptown-console-" + id
	d.survive(execRequest{Name: runtime, Labels: []string{docker.WritesLabel + "=" + w.folder}})
	w.a.docker = docker.Scope{Client: d}
	rebuildRoutes(w.a)
	w.agent.FailRemove(id, 1)
	if err := w.a.changeConsole.EndChatsWithoutTheirChange(context.Background()); err != nil {
		t.Fatalf("startup refused over a stuck chat on a free change: %v", err)
	}
	_, err := w.a.Work.StartRun(asPerson("owner"), workflow.StartChangeRun{ChangeID: w.change.ID, Workflow: "implement"})
	var held *workflow.ChangeHeld
	if !errors.As(err, &held) || !strings.Contains(held.Error(), "still running in this change's folder") {
		t.Fatalf("a run was admitted, or refused for the wrong reason, beside the stuck chat's runtime: %v", err)
	}
	d.allowRemoval()
	if _, err := w.a.Work.StartRun(asPerson("owner"), workflow.StartChangeRun{ChangeID: w.change.ID, Workflow: "implement"}); err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatalf("the change was not free once the runtime could be removed: %v", err)
	}
	if d.has(runtime) {
		t.Fatal("the stuck runtime was not removed when the change was taken")
	}
}

// A landing takes no holder. Even when it contains an unsupported agent gate,
// executing that gate must not remove the holding chat's runtime.
func TestALandingsAgentGateDoesNotRemoveTheHoldingChat(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	id, code, body := w.openChat(t)
	if code != 201 {
		t.Fatalf("open: %d %s", code, body)
	}
	if err := os.WriteFile(filepath.Join(w.folder, "made.txt"), []byte("chat work\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, w.folder, "add", "made.txt")
	gittest.Run(t, w.folder, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "chat work")
	d := newSurvivingContainers()
	d.allowRemoval()
	name := "apptown-console-" + id
	d.survive(execRequest{Name: name, Labels: []string{docker.WritesLabel + "=" + w.folder}})
	w.a.docker = docker.Scope{Client: d}
	defineWorkflows(t, w.a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "check", Steps: []schema.Step{{Name: "review", Uses: "agent/review"}}}}}})
	f := installFakePi(t, w.a, func(_ int, req execRequest) execOutcome {
		writeVerdict(t, req.Workspace, `{"verdict":"pass","summary":"checked","findings":[]}`)
		return execOutcome{Output: piSays("checked")}
	})
	// holderWorld replaces the runner registry; restore production runners.
	rebuildWork(t, w.a, func(deps *workflow.Dependencies) {
		deps.Runners = w.a.workDependencies().Runners
		deps.Automatic = autoStepTypes()
	})
	w.a.Work.Stop()
	run, err := w.a.Work.RequestLanding(asPerson("owner"), w.change.ID)
	if err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	if !d.has(name) {
		t.Fatal("fixture: admission already removed chat")
	}
	step := reviewExecute(t, w.a, run, "review")
	t.Logf("landing gate: %s %s, calls=%d", step.Status, step.Evidence, len(f.requests()))
	if w.holder(t) != scm.ChatHolder(id) {
		t.Fatal("fixture: chat lost authority")
	}
	if !d.has(name) {
		t.Fatalf("landing removed the holding chat's runtime and invoked %d reviewer(s)", len(f.requests()))
	}
}

// Two End requests overlap while the first publishes. The second must not
// release the first request's Hold merely because the session is archived.
func TestOverlappingEndsKeepTheChatHeldUntilItsPublication(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	chat, code, body := w.openChat(t)
	if code != 201 {
		t.Fatalf("open: %d %s", code, body)
	}
	if err := os.WriteFile(filepath.Join(w.folder, "new.txt"), []byte("committed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, w.folder, "add", "new.txt")
	gittest.Run(t, w.folder, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "chat work")
	release, err := w.a.repositories.Acquire(context.Background(), w.change.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	done := make(chan error, 1)
	go func() { done <- w.a.changeConsole.End(context.Background(), chat, "owner") }()
	archived := poll.Until(context.Background(), 30*time.Second, time.Millisecond, func() bool {
		_, err := w.a.Console.Get(context.Background(), chat)
		return err != nil
	})
	if !archived {
		release()
		<-done
		t.Fatal("end did not archive")
	}
	if err := w.a.changeConsole.End(context.Background(), chat, "owner"); err == nil {
		release()
		<-done
		t.Fatal("second end unexpectedly succeeded")
	}
	ended, err := workflow.HolderEndedIn(context.Background(), w.a.DB, w.change.ProjectID, w.change.Branch, scm.ChatHolder(chat), holderPresenceFor(w.a), time.Now())
	release()
	if endErr := <-done; endErr != nil {
		t.Fatal(endErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if ended {
		t.Fatal("second End removed first End's reservation while publication was still blocked")
	}
}

// runHolderEnded asks the holder rule whether run no longer holds its change.
func runHolderEnded(t *testing.T, a *App, runID ids.ID) bool {
	t.Helper()
	run, err := workflow.ReadRunIn(context.Background(), a.DB, runID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := workflow.ReadSourceIn(context.Background(), a.DB, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	ended, err := workflow.HolderEndedIn(context.Background(), a.DB, run.ProjectID, source.Branch, scm.RunHolder(runID), holderPresenceFor(a), time.Now())
	if err != nil && !strings.Contains(err.Error(), "daemon did not answer") {
		t.Fatal(err)
	}
	return ended
}

// The rule these tests hold: every container that mounts a change's folder
// writable is labelled with that folder, and a change is taken only once
// Docker lists none.

type failedReaper struct{ *consoletest.Agent }

func (failedReaper) Reap(context.Context, map[string]bool) error {
	return errors.New("Docker orphan removal unavailable")
}

// A chat whose record cannot be restored after a restart, and whose runtime
// the reaper cannot remove, still keeps its change: no other worker takes it
// while Docker lists the chat's container as writing the folder.
func TestAChatLostAtRestartStillKeepsItsChange(t *testing.T) {
	t.Parallel()
	w := newHolderWorld(t)
	id := w.legacyChat(t)
	sess, err := w.a.Console.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	daemon := newSurvivingContainers()
	daemon.live["apptown-console-"+id] = []string{docker.WritesLabel + "=" + w.folder}
	w.a.docker = docker.Scope{Client: daemon}
	rebuildRoutes(w.a)
	// Unreadable metadata after a restart; the runtime is still there.
	if err := os.WriteFile(filepath.Join(sess.Dir, console.MetaFile), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	w.a.Console = console.New(w.a.consoleDependencies(failedReaper{w.agent}, nil))
	rebuildConsoleOwners(w.a)
	if err := w.a.RecoverConsoles(context.Background()); err != nil {
		return
	}
	run, err := w.a.Work.StartRun(asPerson("owner"), workflow.StartChangeRun{ChangeID: w.change.ID, Workflow: "implement"})
	if !errors.As(err, new(*workflow.ChangeHeld)) {
		t.Fatalf("run %d admitted while the lost chat %s may still write the folder: %v", run.ID, id, err)
	}
}

// A review's wrap-up is another invocation of the step: it does not start
// while the reviewer before it is still there.
func TestAReviewWrapUpWaitsForTheReviewerBeforeIt(t *testing.T) {
	t.Parallel()
	a := agentTestApp(t)
	daemon := newSurvivingContainers()
	a.docker = docker.Scope{Client: daemon}
	_, stepID := admittedReviewStep(t, a, "review-wrapup")
	calls := 0
	installFakePi(t, a, func(_ int, req execRequest) execOutcome {
		calls++
		daemon.survive(req)
		return execOutcome{TimedOut: true, RunErr: context.DeadlineExceeded, CleanupErr: errors.New("old reviewer still runs")}
	})
	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
	if calls != 1 {
		t.Fatalf("started %d reviewers while the first is still there", calls)
	}
}

// A container that survives keeps its run's change whatever an earlier
// container of the same name did: here a review's wrap-up, whose name is the
// same on every attempt, was removed on the first attempt and survives on
// the second.
func TestASurvivingWrapUpKeepsTheChangeWhateverAnEarlierOneDid(t *testing.T) {
	t.Parallel()
	a := agentTestApp(t)
	daemon := newSurvivingContainers()
	a.docker = docker.Scope{Client: daemon}
	runID, stepID := admittedReviewStep(t, a, "review-name")
	calls := 0
	var firstResume, secondResume string
	installFakePi(t, a, func(_ int, req execRequest) execOutcome {
		calls++
		switch calls {
		case 2:
			firstResume = req.Name
			writeVerdict(t, req.Workspace, `{"verdict":"pass","summary":"checked","findings":[]}`)
		case 4:
			secondResume = req.Name
			daemon.survive(req)
			return execOutcome{TimedOut: true, RunErr: context.DeadlineExceeded, CleanupErr: errors.New("second wrap-up survives")}
		}
		return execOutcome{Output: piSays("reviewing")}
	})
	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
	if status, _ := stepStatus(t, a, stepID); status != workflow.StatusPass {
		t.Fatalf("first review: %s", status)
	}
	if _, err := a.Work.RetryStep(asPerson("alice"), workflow.RetryCommand{StepID: stepID}); err != nil {
		t.Fatal(err)
	}
	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
	if calls != 4 || firstResume != secondResume {
		t.Fatalf("fixture: calls=%d names=%s/%s", calls, firstResume, secondResume)
	}
	if _, err := a.Work.Abandon(asPerson("alice"), workflow.StopWork{RunID: runID, Reason: "stop"}); err != nil {
		t.Fatal(err)
	}
	if runHolderEnded(t, a, runID) {
		t.Fatal("the change was released while the second wrap-up is still there")
	}
}

// An implementation agent's private Docker daemon mounts the folder writable
// (and so can the containers the agent starts in it), so it keeps its run's
// change until Docker confirms it gone.
func TestAnAgentsPrivateDockerKeepsItsRunsChange(t *testing.T) {
	t.Parallel()
	a := agentTestApp(t, Config{PrivateDocker: true})
	if _, err := a.Ways.CreateBlock(t.Context(), "skill/implementation", "Implement the requested change.", "", "alice"); err != nil {
		t.Fatal(err)
	}
	daemon := newSurvivingContainers()
	a.docker = docker.Scope{Client: daemon}
	runID, stepID := admittedAgentStep(t, a, `{"prompt":"do it","skills":["skill/implementation"],"docker":true}`)
	called := false
	installFakePi(t, a, func(_ int, req execRequest) execOutcome {
		called = true
		if envValue(req, "DOCKER_HOST") == "" {
			t.Error("the private daemon was not provisioned")
		}
		return execOutcome{TimedOut: true, RunErr: context.DeadlineExceeded}
	})
	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
	private := ""
	for name := range daemon.live {
		private = name
	}
	if !called || private == "" {
		t.Fatalf("fixture did not leave the private daemon: called=%v live=%v", called, daemon.live)
	}
	if _, err := a.Work.Abandon(asPerson("alice"), workflow.StopWork{RunID: runID, Reason: "stop"}); err != nil {
		t.Fatal(err)
	}
	if runHolderEnded(t, a, runID) {
		t.Fatalf("the change was released while the private daemon %s is still there", private)
	}
	daemon.allowRemoval()
	if !runHolderEnded(t, a, runID) {
		t.Fatal("the change was not released once the private daemon was removed")
	}
}

// branchTip is the change's branch in the project's repository; "" before
// anything is published.
func (w holderWorld) branchTip(t *testing.T) string {
	t.Helper()
	return scm.Ref(t.Context(), w.a.projectRepoPath("game"), "refs/heads/"+w.change.Branch)
}
