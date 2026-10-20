package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/consoletest"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/httpapi"
	"flokkit.io/forge/internal/forge/operations"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// workerChatWorld is an AppTown with a worker for chats: AppTown's own chat
// service, opening and saving, a real worker server over mutual TLS, and
// on the worker the fake chat runtime, whose work tree is a real folder a
// test can commit in.
type workerChatWorld struct {
	holderWorld
	remote  *worker.RemoteAgent
	onWork  *consoletest.Agent // the runtime on the worker
	srv     interface{ Close() }
	workers worker.SessionClient
	// failSaves makes the worker's answers to saves fail on the way back.
	failSaves *atomic.Bool
}

// failing fails requests for saves while fail is set: a worker that
// answers everything but its chat's commits.
type failing struct {
	base http.RoundTripper
	fail *atomic.Bool
}

func (f failing) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.fail.Load() && strings.HasSuffix(r.URL.Path, "/save") {
		return nil, errors.New("the connection dropped")
	}
	return f.base.RoundTrip(r)
}

func newWorkerChatWorld(t *testing.T) workerChatWorld {
	t.Helper()
	return newWorkerChatWorldConfigured(t, nil)
}

// newWorkerChatWorldConfigured is newWorkerChatWorld with AppTown's
// configuration adjusted by configure before it is built.
func newWorkerChatWorldConfigured(t *testing.T, configure func(*Config) Config) workerChatWorld {
	t.Helper()
	ca := workertest.NewCA(t)
	onWork := consoletest.NewAgent()
	srv := workertest.StartServer(t, ca, &worker.Server{State: t.TempDir(), Sessions: onWork})
	client := worker.SessionClient{URL: srv.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	failSaves := &atomic.Bool{}
	client.Client.Transport = failing{base: client.Client.Transport, fail: failSaves}
	remote := &worker.RemoteAgent{Sessions: client, Image: "apptown-agent:test", CopyEvery: 50 * time.Millisecond}
	t.Cleanup(remote.Close)
	local := consoletest.NewAgent()
	cfg := Config{DockerClient: newFakeDocker(), ConsoleCLI: fixtureCLI(t), ConsoleAgent: local, ConsoleWorker: remote,
		InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}}
	if configure != nil {
		cfg = configure(&cfg)
	}
	a := authzActors(t, cfg)
	authzWorkflow(t, a, workflow.ApprovalConfig{})
	revokeReadingForAll(t, a)
	remote.Root = filepath.Join(a.cfg.dataDir, "console")
	t.Cleanup(remote.Close) // before AppTown's own cleanup: no copy or save outlives it
	configureConsoleModel(t, a)
	rebuildWork(t, a, func(deps *workflow.Dependencies) { deps.Git = workflow.GitWorkspace{DataDir: a.cfg.dataDir} })
	if w := authzCall(a.Handler(), "owner", "POST", "/api/v1/projects/game/repo", `{}`); w.Code != 200 {
		t.Fatalf("initialize repository: %d %s", w.Code, w.Body.String())
	}
	change, err := a.Work.Create(asPerson("owner"), workflow.ChangeDraft{ProjectID: idstest.N(1), Title: "Work on the worker"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Stop()
	return workerChatWorld{holderWorld: holderWorld{a: a, change: change, agent: local,
		folder: filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", change.ID))},
		remote: remote, onWork: onWork, srv: srv, workers: client, failSaves: failSaves}
}

func (w workerChatWorld) openOnWorker(t *testing.T, in conversation.ProjectOpen) (*console.Session, error) {
	t.Helper()
	p, err := w.a.Projects.BySlug(context.Background(), "game")
	if err != nil {
		t.Fatal(err)
	}
	in.Placement = conversation.PlaceOnWorker
	if in.Model == "" {
		in.Model = "fake/model"
	}
	return w.a.consoleAdmission.Open(asPerson("owner"), &p, in, "owner")
}

// commitOnWorker commits a file in the chat's work tree on the worker.
func (w workerChatWorld) commitOnWorker(t *testing.T, id, name, body string) string {
	t.Helper()
	w.writeOnWorker(t, id, name, body)
	work := w.onWork.Started(id).Workspace.Path
	gittest.Run(t, work, "add", "-f", name)
	gittest.Run(t, work, "-c", "user.name=chat", "-c", "user.email=chat@example.org", "commit", "-qm", "chat: "+name)
	return gittest.Run(t, work, "rev-parse", "HEAD")
}

// commitIn commits a file in a work tree and answers the commit.
func commitIn(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, dir, "add", "-f", name)
	gittest.Run(t, dir, "-c", "user.name=chat", "-c", "user.email=chat@example.org", "commit", "-qm", "chat: "+name)
	return gittest.Run(t, dir, "rev-parse", "HEAD")
}

func (w workerChatWorld) writeOnWorker(t *testing.T, id, name, body string) {
	t.Helper()
	work := w.onWork.Started(id).Workspace.Path
	if work == "" {
		t.Fatalf("chat %s has not started on the worker", id)
	}
	path := filepath.Join(work, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A chat opened on the worker runs there, not here: its work tree is made
// from the change's folder, and its record places it, so AppTown asks
// the worker about it.
func TestAChatOpenedOnTheWorkerRunsThere(t *testing.T) {
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
	if !w.onWork.Running(s.ID) || w.agent.Exists(s.ID) {
		t.Fatalf("on the worker %v, on AppTown %v", w.onWork.Running(s.ID), w.agent.Exists(s.ID))
	}
	folderHead := gittest.Run(t, w.folder, "rev-parse", "HEAD")
	if head := gittest.Run(t, w.onWork.Started(s.ID).Workspace.Path, "rev-parse", "HEAD"); head != folderHead {
		t.Fatalf("the worker's work tree is at %s, the change's folder at %s", head, folderHead)
	}
	if w.holder(t) != scm.ChatHolder(s.ID) {
		t.Fatalf("the chat does not hold its change: %s", w.holder(t))
	}
	if st := w.a.Console.State(ctx, s); st == console.Dead || st == console.Unavailable {
		t.Fatalf("state: %s", st)
	}
}

// Opening on the worker is refused, with the reason, for what a worker
// chat cannot do: a project's chat, and continuing a step's agent; and on a
// AppTown with no worker.
func TestOpeningAChatOnTheWorkerIsRefusedWhatItCannotDo(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	if _, err := w.openOnWorker(t, conversation.ProjectOpen{}); err == nil || !strings.Contains(err.Error(), "only a change's chat") {
		t.Fatalf("a project chat on the worker: %v", err)
	}
	if _, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID, ContinueStep: idstest.N(1)}); err == nil || !strings.Contains(err.Error(), "continuing") {
		t.Fatalf("continuing a step's agent on the worker: %v", err)
	}
	h := newHolderWorld(t)
	p, _ := h.a.Projects.BySlug(context.Background(), "game")
	if _, err := h.a.consoleAdmission.Open(asPerson("owner"), &p, conversation.ProjectOpen{ChangeID: h.change.ID, Placement: conversation.PlaceOnWorker, Model: "fake/model"}, "owner"); err == nil ||
		!strings.Contains(err.Error(), "no worker") {
		t.Fatalf("a worker chat on an AppTown without one: %v", err)
	}
}

// savedTo reports whether the chat's last recorded save left want on the
// branch. The record is written after the branch is published and the
// folder follows, and reading it starts no git, so a test polls it rather
// than the branch.
func savedTo(dir, want string) func() bool {
	return func() bool {
		save, ok, _ := repositoryedit.LastChatSave(dir)
		return ok && save.Outcome == repositoryedit.ChatSaved && save.Saved == want
	}
}

// eventually polls cond until it holds, failing after ten seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	if !poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, cond) {
		t.Fatalf("never: %s", what)
	}
}

// A landing asked for in the middle of a turn
// lands the chat's latest commits: the chat is saved before the landing
// chooses its candidate; a save refused refuses the landing.
func TestALandingSavesTheWorkerChatFirst(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	defineWorkflows(t, w.a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "accept", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}}}}})
	w.a.Work.Stop()
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	w.commitOnWorker(t, s.ID, "saved.txt", "earlier turn\n")
	w.a.changeConsole.SaveAfterTurn(s.ID)
	w.commitOnWorker(t, s.ID, ".apptown/not-source", "x\n")
	if _, err := w.a.Work.RequestLanding(conversation.Context(context.Background(), s), w.change.ID); err == nil || !strings.Contains(err.Error(), "not saved") {
		t.Fatalf("a landing whose save was refused: %v", err)
	}
	work := w.onWork.Started(s.ID).Workspace.Path
	gittest.Run(t, work, "reset", "-q", "--hard", "HEAD~1")
	want := w.commitOnWorker(t, s.ID, "latest.txt", "committed before asking to land\n")
	run, err := w.a.Work.RequestLanding(conversation.Context(context.Background(), s), w.change.ID)
	if err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	candidate, _, err := workflow.LandingCandidateIn(context.Background(), w.a.DB, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Candidate != want {
		t.Fatalf("the landing chose %s, not the chat's latest commit %s", candidate.Candidate, want)
	}
}

// With worker chats on, a change's chat opens on the worker unless the
// person chooses AppTown; a project's chat opens on AppTown.
func TestAChangesChatOpensOnTheWorkerByDefault(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	p, _ := w.a.Projects.BySlug(context.Background(), "game")
	s, err := w.a.consoleAdmission.Open(asPerson("owner"), &p, conversation.ProjectOpen{ChangeID: w.change.ID, Model: "fake/model"}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if !conversation.OnWorkerRecord(s.Meta) {
		t.Fatal("a change's chat opened on AppTown by default")
	}
	project, err := w.a.consoleAdmission.Open(asPerson("owner"), &p, conversation.ProjectOpen{Model: "fake/model"}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.OnWorkerRecord(project.Meta) {
		t.Fatal("a project's chat opened on the worker")
	}
	if _, err := w.a.consoleAdmission.Open(asPerson("owner"), &p, conversation.ProjectOpen{ChangeID: w.change.ID, Placement: "elsewhere", Model: "fake/model"}, "owner"); err == nil {
		t.Fatal("an unknown placement was accepted")
	}
}

// The person can open a change's chat on AppTown while worker chats are on.
func TestAChangesChatCanBeOpenedOnTheForge(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	p, _ := w.a.Projects.BySlug(context.Background(), "game")
	s, err := w.a.consoleAdmission.Open(asPerson("owner"), &p, conversation.ProjectOpen{ChangeID: w.change.ID, Placement: conversation.PlaceOnForge, Model: "fake/model"}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.OnWorkerRecord(s.Meta) {
		t.Fatal("the chat chosen for AppTown opened on the worker")
	}
	if err := w.a.Console.Warm(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	if !w.agent.Running(s.ID) || w.onWork.Exists(s.ID) {
		t.Fatal("the chat did not run on AppTown")
	}
}

// In the no-local-Docker mode a change's chat still opens on the worker,
// and every chat the worker cannot take is refused with its reason: a
// project's chat, continuing a step's agent, and a chat chosen for AppTown.
func TestInTheNoLocalDockerModeChatsTheWorkerCannotTakeAreRefused(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorldConfigured(t, func(cfg *Config) Config {
		cfg.NoLocalDocker = true
		cfg.Worker = workerClientConfig(t, workertest.NewCA(t), "https://worker.invalid")
		return *cfg
	})
	p, _ := w.a.Projects.BySlug(context.Background(), "game")
	open := func(in conversation.ProjectOpen) (*console.Session, error) {
		in.Model = "fake/model"
		return w.a.consoleAdmission.Open(asPerson("owner"), &p, in, "owner")
	}
	s, err := open(conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil || !conversation.OnWorkerRecord(s.Meta) {
		t.Fatalf("a change's chat in the mode: %v", err)
	}
	for _, c := range []struct {
		in  conversation.ProjectOpen
		why string
	}{
		{conversation.ProjectOpen{}, "only a change's chat"},
		{conversation.ProjectOpen{ChangeID: w.change.ID, ContinueStep: idstest.N(1)}, "continuing a step's agent"},
		{conversation.ProjectOpen{ChangeID: w.change.ID, Placement: conversation.PlaceOnForge}, "open the chat on the worker"},
	} {
		_, err := open(c.in)
		if err == nil || !strings.Contains(err.Error(), c.why) || !strings.Contains(err.Error(), "runs no chats itself") {
			t.Fatalf("%+v in the mode: %v", c.in, err)
		}
		// Nothing offers AppTown to an AppTown that runs no chats.
		if fault.CodeOf(err) != "" {
			t.Fatalf("%+v in the mode is coded %q", c.in, fault.CodeOf(err))
		}
	}
	if w.agent.Running(s.ID) {
		t.Fatal("a chat ran on AppTown in the mode")
	}
}

// The folder's committed work is published
// with the checks before the worker is sent it; work they refuse keeps the
// chat off the worker, with the reason, and the work stays in the folder.
func TestOpeningOnTheWorkerPublishesTheFolderFirst(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	if err := w.a.Work.PrepareWorkspace(asPerson("owner"), w.change.ID); err != nil {
		t.Fatal(err)
	}
	good := commitIn(t, w.folder, "good.txt", "published first\n")
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	if w.branchTip(t) != good {
		t.Fatalf("the folder's commit was not published before the worker got it: %s", w.branchTip(t))
	}
	if _, err := w.a.Console.End(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}

	v := newWorkerChatWorld(t)
	if err := v.a.Work.PrepareWorkspace(asPerson("owner"), v.change.ID); err != nil {
		t.Fatal(err)
	}
	refused := commitIn(t, v.folder, ".apptown/not-source", "refused\n")
	_, err = v.openOnWorker(t, conversation.ProjectOpen{ChangeID: v.change.ID})
	if err == nil || !strings.Contains(err.Error(), "could not be published") {
		t.Fatalf("opening on the worker from a folder whose work is refused: %v", err)
	}
	if fault.CodeOf(err) != conversation.CodeChatOnForge {
		t.Fatalf("the refusal does not say the chat can open on AppTown: %q", fault.CodeOf(err))
	}
	if head := gittest.Run(t, v.folder, "rev-parse", "HEAD"); head != refused {
		t.Fatalf("the refused work left the folder: %s", head)
	}
}

// A registry the chat's builds use that the worker cannot reach keeps the
// chat off the worker, naming the registry. The refusal offers AppTown
// instead, in its code and its sentence, only when the apptown runs chats
// itself.
func TestAChatIsNotOpenedOnAWorkerThatCannotReachItsRegistry(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		noLocal bool
		code    string
	}{
		{"on an AppTown that runs chats", false, conversation.CodeChatOnForge},
		{"on an AppTown that runs none", true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newWorkerChatWorldConfigured(t, func(cfg *Config) Config {
				if c.noLocal {
					cfg.NoLocalDocker = true
					cfg.Worker = workerClientConfig(t, workertest.NewCA(t), "https://worker.invalid")
				}
				return *cfg
			})
			closed, down := listenLocalForge(t)
			closed.Close()
			if _, err := w.a.Integrations.Bindings.Upsert(t.Context(), "npm-mirror", "registry", "registry", `{"format":"npm","url":"`+down+`"}`, false); err != nil {
				t.Fatal(err)
			}
			_, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
			if err == nil || !strings.Contains(err.Error(), "npm-mirror") {
				t.Fatalf("opening on a worker that cannot reach the registry: %v", err)
			}
			// The page offers AppTown on the code, never on the sentence.
			if fault.CodeOf(err) != c.code {
				t.Fatalf("the refusal is coded %q, want %q", fault.CodeOf(err), c.code)
			}
			if says := strings.Contains(err.Error(), "on AppTown"); says != (c.code != "") {
				t.Fatalf("the refusal's sentence names AppTown as the way round = %v on this AppTown: %v", says, err)
			}
		})
	}
}

func listenLocalForge(t *testing.T) (net.Listener, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return l, "http://" + l.Addr().String()
}

// A change a chat on the worker holds has no preview yet, refused with the
// reason; the chat is told so in its instructions.
// A preview of a change a chat on the worker holds saves the chat first, and
// boots the saved commit, never the folder or an older tree.
func TestAWorkerChatsChangePreviewsItsSavedWork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// This world's Docker runs no app: every container it starts has
	// already stopped, so the boot fails at once instead of waiting out
	// the preview's probe for an answer that never comes.
	w := newWorkerChatWorldConfigured(t, func(cfg *Config) Config {
		exited := newFakeDocker()
		exited.reply = func(args []string) (string, error, bool) {
			if len(args) == 4 && args[0] == "inspect" && args[2] == "{{.State.Running}}" {
				return "false\n", nil, true
			}
			return "", nil, false
		}
		cfg.DockerClient = exited
		return *cfg
	})
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.a.Console.Send(ctx, s.ID, "go"); err != nil {
		t.Fatal(err)
	}
	saved := w.commitOnWorker(t, s.ID, ".apptown/app.yaml", "version: 1\napp:\n  web:\n    static: .\n    public: true\n")
	// Asked for before the turn's save: the preview saves first.
	resp := authzCall(w.a.Handler(), "owner", "POST", fmt.Sprintf("/api/v1/changes/%d/preview", w.change.ID), `{}`)
	if resp.Code == 409 || resp.Code == 422 || resp.Code == 403 {
		t.Fatalf("preview refused: %d %s", resp.Code, resp.Body.String())
	}
	last, ok, err := repositoryedit.LastChatSave(s.Dir)
	if err != nil || !ok || last.Saved != saved {
		t.Fatalf("the chat was not saved first: %+v %v %v", last, ok, err)
	}
	if _, err := os.Stat(filepath.Join(w.a.cfg.dataDir, "workspaces", fmt.Sprintf("asof-change-%d", w.change.ID), saved, ".apptown", "app.yaml")); err != nil {
		t.Fatalf("the preview did not boot the saved commit: %v", err)
	}
	if err := w.a.Console.Warm(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.Steering(), "runs on a separate machine, the worker") {
		t.Fatal("the chat's instructions do not say it runs on the worker")
	}
}

// The chat's page reads where it runs, its last save and how fresh the
// forge's copy is; a refused last save is listed on its change and
// downloads as a bundle holding the refused commit.
func TestAWorkerChatsSavesAreShownAndARefusedOneDownloads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorkerChatWorld(t)
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.a.Console.Send(ctx, s.ID, "go"); err != nil {
		t.Fatal(err)
	}
	want := w.commitOnWorker(t, s.ID, "one.txt", "1\n")
	w.a.changeConsole.SaveAfterTurn(s.ID)
	eventually(t, "a copy of the record", func() bool { return !worker.RecordAsOf(s.Dir).IsZero() })
	resp := authzCall(w.a.Handler(), "owner", "GET", "/api/v1/projects/game/console", "")
	var list httpapi.ConsoleSessionList
	if err := json.Unmarshal(resp.Body.Bytes(), &list); err != nil || len(list.Sessions) != 1 {
		t.Fatalf("chat list: %d %s", resp.Code, resp.Body.String())
	}
	v := list.Sessions[0]
	if v.Placement != conversation.PlaceOnWorker || v.Saved == nil || v.Saved.Outcome != repositoryedit.ChatSaved || v.Saved.Commit != want || v.RecordAsOf == nil {
		t.Fatalf("the chat as listed: %+v %+v", v, v.Saved)
	}

	refused := w.commitOnWorker(t, s.ID, ".apptown/not-source", "x\n")
	if err := w.a.changeConsole.End(ctx, s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	chats := workerChatList(t, w, w.change.ID)
	if len(chats.Chats) != 1 || chats.Chats[0].Chat != s.ID || chats.Chats[0].Live || chats.Chats[0].Refused == nil ||
		chats.Chats[0].Refused.Commit != refused || chats.Chats[0].Refused.Reason == "" {
		t.Fatalf("the change's worker chats: %+v", chats)
	}
	resp = authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/worker-chats/%s/refused", w.change.ID, s.ID), "")
	if resp.Code != 200 {
		t.Fatalf("download: %d %s", resp.Code, resp.Body.String())
	}
	bundle := filepath.Join(t.TempDir(), "refused.bundle")
	if err := os.WriteFile(bundle, resp.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if heads := gittest.Run(t, w.a.projectRepoPath("game"), "bundle", "list-heads", bundle); !strings.Contains(heads, refused) {
		t.Fatalf("the bundle does not hold the refused commit: %s", heads)
	}
	if resp := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/worker-chats/%s/refused", w.change.ID, "abcdef012345"), ""); resp.Code != 404 {
		t.Fatalf("a chat with no refused save: %d", resp.Code)
	}
	other, err := w.a.Work.Create(asPerson("owner"), workflow.ChangeDraft{ProjectID: idstest.N(1), Title: "Another change"})
	if err != nil {
		t.Fatal(err)
	}
	if list := workerChatList(t, w, other.ID); len(list.Chats) != 0 {
		t.Fatalf("another change lists this change's chat: %+v", list)
	}
}

// workerChatList reads a change's chats on the worker as its page does.
func workerChatList(t *testing.T, w workerChatWorld, changeID ids.ID) httpapi.WorkerChatList {
	t.Helper()
	resp := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/worker-chats", changeID), "")
	var list httpapi.WorkerChatList
	if err := json.Unmarshal(resp.Body.Bytes(), &list); err != nil || resp.Code != 200 {
		t.Fatalf("worker chats of change %d: %d %s", changeID, resp.Code, resp.Body.String())
	}
	return list
}

// `apptown check` on a change a chat on the worker holds saves the chat first
// and checks what was saved, naming the commit; while the folder is behind
// its saved work, the saved commit is the one checked.
func TestForgeCheckChecksAWorkerChatsSavedWork(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T) string {
		t.Helper()
		out := authzCall(w.a.Handler(), "owner", "POST", fmt.Sprintf("/api/v1/changes/%d/check", w.change.ID), `{}`)
		var got struct{ Commit string }
		if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &got) != nil {
			t.Fatalf("check: %d %s", out.Code, out.Body.String())
		}
		return got.Commit
	}
	first := w.commitOnWorker(t, s.ID, "one.txt", "1\n")
	if got := check(t); got != first || w.branchTip(t) != first {
		t.Fatalf("checked %s with the branch at %s; want the unsaved commit %s saved and checked", got, w.branchTip(t), first)
	}
	if err := os.WriteFile(filepath.Join(w.folder, ".git", "index.lock"), []byte("busy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := w.commitOnWorker(t, s.ID, "two.txt", "2\n")
	if got := check(t); got != second || gittest.Run(t, w.folder, "rev-parse", "HEAD") == second {
		t.Fatalf("checked %s; want the saved %s, which the folder has not caught up with", got, second)
	}
	// With no chat open, the saved work is still what is checked.
	if err := w.a.changeConsole.End(context.Background(), s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if got := check(t); got != second {
		t.Fatalf("with the chat ended, checked %s; want the saved %s", got, second)
	}
	// And with no folder at all.
	if err := os.RemoveAll(w.folder); err != nil {
		t.Fatal(err)
	}
	if got := check(t); got != second {
		t.Fatalf("with no folder, checked %s; want the saved %s", got, second)
	}
}

// A damaged change folder is an error for `apptown check`, never a fall-back
// to the saved branch: only an absent folder reads the branch.
func TestForgeCheckOfADamagedFolderIsAnError(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	// Saved work on the branch, which a fall-back would wrongly check.
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	w.commitOnWorker(t, s.ID, "one.txt", "1\n")
	if err := w.a.changeConsole.End(context.Background(), s.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(w.folder, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git", filepath.Join(w.folder, ".git")); err != nil { // a loop: unreadable
		t.Fatal(err)
	}
	out := authzCall(w.a.Handler(), "owner", "POST", fmt.Sprintf("/api/v1/changes/%d/check", w.change.ID), `{}`)
	if out.Code == 200 {
		t.Fatalf("a damaged folder was checked: %s", out.Body.String())
	}
}

// A save refused or not confirmed refuses the check, over HTTP and MCP: what
// the chat committed was not saved, so it is not what would be checked.
func TestForgeCheckIsRefusedWhenTheSaveIs(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	w.commitOnWorker(t, s.ID, ".apptown/not-source", "refused\n")
	out := authzCall(w.a.Handler(), "owner", "POST", fmt.Sprintf("/api/v1/changes/%d/check", w.change.ID), `{}`)
	if out.Code != 409 || !strings.Contains(out.Body.String(), "was not checked") {
		t.Fatalf("HTTP: a refused save did not refuse the check: %d %s", out.Code, out.Body.String())
	}
	token := mcp_token(t, w.a, "refused-check", "owner", "operator")
	if result := mcp_call(t, w.a, token, "run_change_checks", map[string]any{"id": w.change.ID}); result["isError"] != true || !strings.Contains(mustJSON(result), "was not checked") {
		t.Fatalf("MCP: a refused save did not refuse the check: %s", mustJSON(result))
	}
}

// `apptown check --published` on a change a chat on the worker holds checks
// the branch's published tip as it is: the chat's unsaved commit is not
// saved, the branch does not move, and the chat's work is not what is
// checked. An empty request keeps its meaning: the chat saves first and its
// saved work is checked.
func TestForgeCheckPublishedSavesNothingFromAWorkerChat(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T, body string) string {
		t.Helper()
		out := authzCall(w.a.Handler(), "owner", "POST", fmt.Sprintf("/api/v1/changes/%d/check", w.change.ID), body)
		var got struct{ Commit string }
		if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &got) != nil {
			t.Fatalf("check %s: %d %s", body, out.Code, out.Body.String())
		}
		return got.Commit
	}
	published := w.branchTip(t)
	onWorker := w.commitOnWorker(t, s.ID, "unsaved.txt", "work\n")
	if published == "" || published == onWorker {
		t.Fatalf("published %q, on the worker %q: want two different commits", published, onWorker)
	}
	if got := check(t, `{"published":true}`); got != published || w.branchTip(t) != published {
		t.Fatalf("--published checked %s with the branch at %s; want the published %s, the chat's %s not saved", got, w.branchTip(t), published, onWorker)
	}
	if got := check(t, `{}`); got != onWorker || w.branchTip(t) != onWorker {
		t.Fatalf("an empty request checked %s with the branch at %s; want the chat's %s saved and checked", got, w.branchTip(t), onWorker)
	}
}

// A project deleted while a chat on the worker holds one of its changes:
// the chat's last save has nowhere to go, so its cleanup ends the chat,
// discards what it had not saved, and the ledger says so. The cleanup
// finishes; it is not stuck retrying a save that can never succeed.
func TestDeletingAProjectEndsItsWorkerChatAndDiscardsItsUnsavedWork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w, s := deletedUnderAWorkerChat(t)
	runCleanup(t, w.a, w.change.ProjectID, 0)
	if d, err := w.a.Projects.Deletion(ctx, w.change.ProjectID); err != nil || d.State != project.CleanupDone {
		t.Fatalf("the cleanup did not finish: %+v %v", d, err)
	}
	if gone, err := w.a.Console.Gone(ctx, s.ID); err != nil || !gone {
		t.Fatalf("the chat was not removed: %v %v", gone, err)
	}
	if st, err := w.workers.Status(ctx, s.ID); err != nil || !st.Removed {
		t.Fatalf("the chat on the worker: %+v %v", st, err)
	}
	receipts, err := audit.Recent(ctx, w.a.DB, audit.Filter{KindPrefix: "chat.save-discarded", Subject: fmt.Sprintf("change %d", w.change.ID)})
	if err != nil || len(receipts) != 1 || receipts[0].Actor != "chat/"+s.ID || !strings.Contains(string(receipts[0].Payload), "was deleted") {
		t.Fatalf("no receipt says the chat's unsaved work was discarded: %+v %v", receipts, err)
	}
}

// deletedUnderAWorkerChat is a project deleted and purged, its cleanup not yet run,
// while an idle chat on the worker holds one of its changes with a commit
// it has not saved.
func deletedUnderAWorkerChat(t *testing.T) (workerChatWorld, *console.Session) {
	t.Helper()
	ctx := context.Background()
	w := newWorkerChatWorld(t)
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.a.Console.Warm(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	w.commitOnWorker(t, s.ID, "unsaved.txt", "never saved\n")
	s.SetLastUsed(time.Now().Add(-time.Hour)) // idle: a deletion refuses a chat mid-conversation
	if st := deleteProject(t, w.a, "game"); st.State != project.Deleted {
		t.Fatalf("delete: %+v", st)
	}
	if op := purgeProject(t, w.a, "game"); op.State != operations.Succeeded {
		t.Fatalf("purge: %s %q", op.State, op.Detail)
	}
	return w, s
}

// An AppTown restarted after a project was deleted, before its cleanup ended
// a chat on one of its changes, starts: startup's recovery ends the chat
// whose change no longer exists rather than refusing to start, so one such
// chat cannot make AppTown crash-loop.
func TestStartupEndsAChatWhoseProjectWasDeleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w, s := deletedUnderAWorkerChat(t)
	if err := w.a.changeConsole.EndChatsWithoutTheirChange(ctx); err != nil {
		t.Fatalf("startup refused a chat whose project was deleted: %v", err)
	}
	if gone, err := w.a.Console.Gone(ctx, s.ID); err != nil || !gone {
		t.Fatalf("the chat was not ended: %v %v", gone, err)
	}
}

// Fail only the first inventory request; every later worker call succeeds.
// A transient startup connection failure must not disable automatic saves.
type failFirstWorkerListing struct {
	base   http.RoundTripper
	failed *atomic.Bool
}

func (f failFirstWorkerListing) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodGet && r.URL.Path == "/v1/sessions" && f.failed.CompareAndSwap(false, true) {
		return nil, errors.New("transient connection failure during recovery")
	}
	return f.base.RoundTrip(r)
}

func TestSavesResumeWhenTheWorkerFirstFailsToAnswer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorkerChatWorld(t)
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.a.Console.Send(ctx, s.ID, "finish this turn"); err != nil {
		t.Fatal(err)
	}
	w.a.StopConsoles(ctx)
	if err := w.a.ShutdownBackground(ctx); err != nil {
		t.Fatal(err)
	}
	client := *w.workers.Client
	failed := &atomic.Bool{}
	client.Transport = failFirstWorkerListing{base: client.Transport, failed: failed}
	sessions := w.workers
	sessions.Client = &client
	remote := &worker.RemoteAgent{Sessions: sessions, Image: "apptown-agent:test", Root: filepath.Join(w.a.cfg.dataDir, "console"), CopyEvery: 50 * time.Millisecond}
	cfg := Config{DataDir: w.a.cfg.dataDir, DockerClient: newFakeDocker(), ConsoleCLI: fixtureCLI(t), ConsoleAgent: consoletest.NewAgent(), ConsoleWorker: remote, InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}}
	b := newApp(t, w.a.DB, cfg)
	t.Cleanup(remote.Close)
	if err := b.RecoverConsoles(ctx); err != nil {
		t.Fatal(err)
	}
	if !failed.Load() {
		t.Fatal("the recovery query was not failed")
	}
	if st, err := sessions.Status(ctx, s.ID); err != nil || !st.Agent.Running {
		t.Fatalf("the surviving worker chat is not reachable: %+v %v", st, err)
	}
	if _, err := b.Console.Get(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	want := w.commitOnWorker(t, s.ID, "finished.txt", "finished after restart\n")
	w.onWork.Reply(s.ID, "finished after restart")
	resumed := poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, savedTo(s.Dir, want)) && w.branchTip(t) == want
	// Positive control: the same pending turn does save once a viewer warms
	// the chat, so this is recovery wiring, not an invalid record or commit.
	if err := b.Console.Warm(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "save after explicit Warm", savedTo(s.Dir, want))
	if got := w.branchTip(t); got != want {
		t.Fatalf("the save after Warm was recorded, but the branch is at %s, want %s", got, want)
	}
	if !resumed {
		t.Error("automatic saving did not resume after recovery; explicit Warm repaired it")
	}
}

// An admitted landing is still open work. Ending its worker chat must keep
// everything written since the candidate was selected, or refuse to end.
func TestEndingWhileLandingKeepsTheChatsWork(t *testing.T) {
	t.Parallel()
	for _, duringRecovery := range []bool{false, true} {
		t.Run(fmt.Sprintf("recovery=%t", duringRecovery), func(t *testing.T) {
			t.Parallel()
			w := newWorkerChatWorld(t)
			defineWorkflows(t, w.a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "accept", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}}}}})
			s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
			if err != nil {
				t.Fatal(err)
			}
			w.commitOnWorker(t, s.ID, "candidate.txt", "candidate\n")
			_, err = w.a.Work.RequestLanding(conversation.Context(context.Background(), s), w.change.ID)
			if err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
				t.Fatal(err)
			}
			ch, err := workflow.ReadChangeIn(context.Background(), w.a.DB, w.change.ID)
			if err != nil || ch.State != workflow.ChangeLanding {
				t.Fatalf("not landing: %+v %v", ch, err)
			}
			tip := w.commitOnWorker(t, s.ID, "after.txt", "unsaved after candidate\n")
			w.writeOnWorker(t, s.ID, "left.txt", "uncommitted leftovers\n")
			work := w.onWork.Started(s.ID).Workspace.Path
			var endErr error
			if duringRecovery {
				// This is the production recovery settlement called after Resume. The
				// record still names this chat as the holder; landing is not closed.
				endErr = w.a.changeConsole.EndChatsWithoutTheirChange(context.Background())
			} else {
				endErr = w.a.changeConsole.End(context.Background(), s.ID, "owner")
			}
			if endErr != nil {
				if _, err := os.Stat(filepath.Join(work, "left.txt")); err != nil {
					t.Fatalf("failed end lost work: %v", err)
				}
				return
			}
			if duringRecovery {
				// A chat holding a change being landed is live: recovery keeps it.
				if _, err := w.a.Console.Get(context.Background(), s.ID); err != nil {
					t.Fatalf("recovery ended the chat holding a change being landed: %v", err)
				}
				if _, err := os.Stat(filepath.Join(work, "left.txt")); err != nil {
					t.Fatal(err)
				}
				if w.holder(t) != scm.ChatHolder(s.ID) {
					t.Fatal("recovery replaced the live holder")
				}
				return
			}
			save, _, err := repositoryedit.LastChatSave(s.Dir)
			if err != nil {
				t.Fatal(err)
			}
			hub := w.a.projectRepoPath("game")
			kept := scm.Ref(t.Context(), hub, repositoryedit.RefusedChatRef(s.ID))
			if !scm.Contains(t.Context(), hub, w.change.Branch, tip) && kept == "" {
				_, statErr := os.Stat(work)
				t.Fatalf("ending during landing lost unsaved work: outcome=%s reason=%q worker worktree error=%v", save.Outcome, save.Reason, statErr)
			}
			copy := w.branchTip(t)
			if kept != "" {
				copy = kept
			}
			if got := gittest.Run(t, hub, "show", copy+":left.txt"); got != "uncommitted leftovers" {
				t.Fatalf("leftovers missing from kept copy: %q", got)
			}
		})
	}
}

// The public recheck command must collect the chat's fix before selecting
// the next candidate, including while the change is in the landing state.
func TestARecheckSavesTheChatWhileLanding(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	defineWorkflows(t, w.a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "accept", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}}}}})
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	w.commitOnWorker(t, s.ID, "candidate.txt", "candidate\n")
	_, err = w.a.Work.RequestLanding(conversation.Context(context.Background(), s), w.change.ID)
	if err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	fix := w.commitOnWorker(t, s.ID, "fix.txt", "fix\n")
	_, err = w.a.Work.RecheckLanding(conversation.Context(context.Background(), s), workflow.LandingRecheck{ChangeID: w.change.ID})
	if w.branchTip(t) != fix {
		t.Fatalf("recheck did not save the fix: %v", err)
	}
}

// An AppTown restart must resume transcript collection and automatic saves
// without a browser warming the chat or an explicit save call.
func TestSavesResumeAfterTheForgeRestarts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorkerChatWorld(t)
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.a.Console.Send(ctx, s.ID, "finish this turn"); err != nil {
		t.Fatal(err)
	}
	w.a.StopConsoles(ctx)
	if err := w.a.ShutdownBackground(ctx); err != nil {
		t.Fatal(err)
	}
	remote := &worker.RemoteAgent{Sessions: w.workers, Image: "apptown-agent:test", Root: filepath.Join(w.a.cfg.dataDir, "console"), CopyEvery: 50 * time.Millisecond}
	cfg := Config{DataDir: w.a.cfg.dataDir, DockerClient: newFakeDocker(), ConsoleCLI: fixtureCLI(t), ConsoleAgent: consoletest.NewAgent(), ConsoleWorker: remote, InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}}
	b := newApp(t, w.a.DB, cfg)
	t.Cleanup(remote.Close)
	if err := b.RecoverConsoles(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Console.Get(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	want := w.commitOnWorker(t, s.ID, "finished.txt", "finished after restart\n")
	w.onWork.Reply(s.ID, "finished after restart")
	resumed := poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, savedTo(s.Dir, want)) && w.branchTip(t) == want
	// Positive control: the same pending turn does save once a viewer warms
	// the chat, so this is recovery wiring, not an invalid record or commit.
	if err := b.Console.Warm(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "save after explicit Warm", savedTo(s.Dir, want))
	if got := w.branchTip(t); got != want {
		t.Fatalf("the save after Warm was recorded, but the branch is at %s, want %s", got, want)
	}
	if !resumed {
		t.Error("automatic saving did not resume after recovery; explicit Warm repaired it")
	}
}

// The change's existing chat is returned wherever it
// runs; only a new chat on the worker needs the worker ready.
func TestAnExistingChatIsReturnedWhateverTheWorkerCanReach(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	p, _ := w.a.Projects.BySlug(context.Background(), "game")
	s, err := w.a.consoleAdmission.Open(asPerson("owner"), &p, conversation.ProjectOpen{ChangeID: w.change.ID, Model: "fake/model", Placement: conversation.PlaceOnForge}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	l, down := listenLocalForge(t)
	l.Close()
	if _, err := w.a.Integrations.Bindings.Upsert(t.Context(), "npm-mirror", "registry", "registry", `{"format":"npm","url":"`+down+`"}`, false); err != nil {
		t.Fatal(err)
	}
	again, err := w.a.consoleAdmission.Open(asPerson("owner"), &p, conversation.ProjectOpen{ChangeID: w.change.ID, Model: "fake/model"}, "owner")
	if err != nil {
		t.Fatalf("existing local chat %s was refused: %v", s.ID, err)
	}
	if again.ID != s.ID {
		t.Fatalf("changed chat: %s", again.ID)
	}
}

// A live chat's refused turn is not listed as an
// ended chat's refused save; the list and download need read access and
// stay within their change.
func TestALiveChatsRefusedTurnIsNotAnEndedChatsRefusedSave(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.a.Console.Warm(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	w.commitOnWorker(t, s.ID, ".apptown/not-source", "refused\n")
	w.a.changeConsole.SaveAfterTurn(s.ID)
	save, ok, err := repositoryedit.LastChatSave(s.Dir)
	if err != nil || !ok || save.Outcome != repositoryedit.ChatSaveRefused || save.Kept == "" {
		t.Fatalf("refusal: %+v %v", save, err)
	}
	prefix := fmt.Sprintf("/api/v1/changes/%d/worker-chats", w.change.ID)
	for _, path := range []string{prefix, prefix + "/" + s.ID + "/refused"} {
		out := authzCall(w.a.Handler(), "outsider", "GET", path, "")
		if out.Code != 403 {
			t.Fatalf("outsider %s: %d", path, out.Code)
		}
	}
	other, err := w.a.Work.Create(asPerson("owner"), workflow.ChangeDraft{ProjectID: idstest.N(1), Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	out := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/worker-chats/%s/refused", other.ID, s.ID), "")
	if out.Code != 404 {
		t.Fatalf("wrong change download: %d", out.Code)
	}
	list := workerChatList(t, w, w.change.ID)
	if len(list.Chats) != 1 || !list.Chats[0].Live || list.Chats[0].Refused != nil {
		t.Fatalf("an open chat is listed as a refused last save: %+v", list)
	}
	if out := authzCall(w.a.Handler(), "owner", "GET", prefix+"/"+s.ID+"/refused", ""); out.Code != 404 {
		t.Fatalf("an open chat's refused turn downloads as a last save: %d", out.Code)
	}
}

// `apptown check` over MCP saves a worker chat first,
// as over HTTP.
func TestChecksOverMCPSaveAWorkerChatFirstToo(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	tip := w.commitOnWorker(t, s.ID, "one.txt", "1\n")
	token := mcp_token(t, w.a, "worker-check-review", "owner", "operator")
	result := mcp_call(t, w.a, token, "run_change_checks", map[string]any{"id": w.change.ID})
	if result["isError"] == true || !strings.Contains(mustJSON(result), tip) || w.branchTip(t) != tip {
		t.Fatalf("MCP check did not save and check the chat's work: %s", mustJSON(result))
	}
	// A change whose chat runs on AppTown is checked as before.
	local := newWorkerChatWorld(t)
	p, _ := local.a.Projects.BySlug(context.Background(), "game")
	if _, err := local.a.consoleAdmission.Open(asPerson("owner"), &p, conversation.ProjectOpen{ChangeID: local.change.ID, Model: "fake/model", Placement: conversation.PlaceOnForge}, "owner"); err != nil {
		t.Fatal(err)
	}
	token = mcp_token(t, local.a, "local-check", "owner", "operator")
	if result := mcp_call(t, local.a, token, "run_change_checks", map[string]any{"id": local.change.ID}); strings.Contains(mustJSON(result), "runs on the worker") {
		t.Fatalf("a local chat's change was refused: %s", mustJSON(result))
	}
}

// An unreadable save record is unknown, never no save.
func TestAnUnreadableSaveIsUnknownNotAbsent(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	s, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	// A damaged receipt is unknown, not proof that the chat has never saved.
	if err := os.WriteFile(filepath.Join(s.Dir, repositoryedit.ChatSaveFile), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	out := authzCall(w.a.Handler(), "owner", "GET", "/api/v1/projects/game/console", "")
	if out.Code != 200 {
		t.Fatalf("chat list: %d %s", out.Code, out.Body.String())
	}
	var list httpapi.ConsoleSessionList
	if err := json.Unmarshal(out.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, v := range list.Sessions {
		if v.ID == s.ID && (v.Saved == nil || v.Saved.Outcome != "unknown") {
			t.Fatalf("an unreadable save record: %+v", v.Saved)
		}
	}
}

// A save that cannot record itself for the change's folder (here the
// records beside the folders are read-only) is not published, so the folder
// is never shown in place of work it lacks. The save is not confirmed, and the chat keeps its commits for the next turn.
func TestASaveThatCannotBeRecordedIsNotConfirmed(t *testing.T) {
	t.Parallel()
	w := newWorkerChatWorld(t)
	chat, err := w.openOnWorker(t, conversation.ProjectOpen{ChangeID: w.change.ID})
	if err != nil {
		t.Fatal(err)
	}
	before := w.branchTip(t)
	w.commitOnWorker(t, chat.ID, "saved.txt", "worker work\n")
	metadata := filepath.Join(filepath.Dir(w.folder), ".saved")
	if err := os.MkdirAll(metadata, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(metadata, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(metadata, 0o755); err != nil {
			t.Error(err)
		}
	})
	w.a.changeConsole.SaveAfterTurn(chat.ID)
	save, ok, err := repositoryedit.LastChatSave(chat.Dir)
	if err != nil || !ok || save.Outcome != repositoryedit.ChatSaveUnknown {
		t.Fatalf("a save that could not be recorded: %+v %v %v", save, ok, err)
	}
	if w.branchTip(t) != before {
		t.Fatal("a save that could not be recorded was published")
	}
	out := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/diff", w.change.ID), "")
	if out.Code != 200 || strings.Contains(out.Body.String(), "saved.txt") {
		t.Fatalf("the diff shows work that was not saved: %d %s", out.Code, out.Body.String())
	}
}

// A refused later turn or last save cannot erase the source of an earlier
// successful save whose folder catch-up failed.
func TestARefusedSaveDoesNotHideAnEarlierSave(t *testing.T) {
	t.Parallel()
	for _, ending := range []bool{false, true} {
		t.Run(fmt.Sprintf("ending=%t", ending), func(t *testing.T) {
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
			saved := w.commitOnWorker(t, chat.ID, "saved.txt", "published work\n")
			w.a.changeConsole.SaveAfterTurn(chat.ID)
			if w.branchTip(t) != saved {
				t.Fatal("first save did not publish")
			}
			if behind, err := scm.Behind(context.Background(), w.folder, w.a.projectRepoPath("game")); err != nil || !behind {
				t.Fatalf("first save not behind: %v %v", behind, err)
			}
			if ending {
				// SaveLast must commit these leftovers before attempting publication.
				w.commitOnWorker(t, chat.ID, ".apptown/not-source", "refused commit\n")
				w.writeOnWorker(t, chat.ID, "left.txt", "last leftovers\n")
				if err := w.a.changeConsole.End(context.Background(), chat.ID, "owner"); err != nil {
					t.Fatal(err)
				}
				if gone, err := w.a.Console.Gone(context.Background(), chat.ID); err != nil || !gone {
					t.Fatalf("end failed: %v %v", gone, err)
				}
			} else {
				w.commitOnWorker(t, chat.ID, ".apptown/not-source", "refused turn\n")
				w.a.changeConsole.SaveAfterTurn(chat.ID)
			}
			save, ok, err := repositoryedit.LastChatSave(chat.Dir)
			if err != nil || !ok || save.Outcome != repositoryedit.ChatSaveRefused || save.Kept == "" {
				t.Fatalf("second save not refused and kept: %+v %v", save, err)
			}
			if w.branchTip(t) != saved || gittest.Run(t, w.folder, "rev-parse", "HEAD") != before {
				t.Fatal("branch or folder unexpectedly moved")
			}
			if scm.Ref(t.Context(), w.a.projectRepoPath("game"), save.Kept) != save.Commit {
				t.Fatal("refusal not retained")
			}
			if ending {
				if got := gittest.Run(t, w.a.projectRepoPath("game"), "show", save.Kept+":left.txt"); got != "last leftovers" {
					t.Fatalf("refused leftovers lost: %q", got)
				}
			}
			if behind, err := scm.Behind(context.Background(), w.folder, w.a.projectRepoPath("game")); err != nil || !behind {
				t.Errorf("refusal erased earlier lag: behind=%v err=%v", behind, err)
			}
			savedBeside(t, w.folder, saved)
			diff := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/changes/%d/diff", w.change.ID), "")
			if diff.Code != 200 || !strings.Contains(diff.Body.String(), "saved.txt") {
				t.Errorf("saved work hidden: %d %s", diff.Code, diff.Body.String())
			}
			evidence, err := narrativeDiff(context.Background(), w.a.cfg.dataDir, workflow.NarrativeSource{Change: w.change, Slug: "game", DefaultBranch: "main"})
			if err != nil || !strings.Contains(fmt.Sprint(evidence), "saved.txt") {
				t.Errorf("saved narrative hidden: %+v %v", evidence, err)
			}
			files := authzCall(w.a.Handler(), "owner", "GET", fmt.Sprintf("/api/v1/runs/%d/workspace/", run), "")
			if files.Code != 409 {
				t.Errorf("stale folder served: %d %s", files.Code, files.Body.String())
			}
		})
	}
}
