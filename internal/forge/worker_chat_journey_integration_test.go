//go:build integration

package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/consoletest"
	"flokkit.io/forge/internal/deploy/docker/dockertest"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// The plan's step 4 journey, with a real worker in its own container and
// the real agent image: a change's chat opens on the worker (the default
// with worker chats on), a turn's commit is saved to the change's branch,
// AppTown restarts and finds the chat on the worker, the worker restarts
// and the chat carries on, a landing saves the chat's latest commit first
// and lands it, and ending the chat leaves nothing on the worker and frees
// the change. The model is not called; the chat's commits are made in its
// container as its agent would.
func TestIntegrationAChatOnAWorkerFromOpeningToItsEnd(t *testing.T) {
	if dockertest.Run("image", "inspect", "apptown-agent") != nil {
		t.Skip("needs AppTown-agent image on this machine's Docker")
	}
	ctx := context.Background()
	ca := workertest.NewCA(t)
	w := workertest.StartInContainer(t, ca)
	image, err := worker.AgentImage("../../deploy/apptown-agent")
	if err != nil {
		t.Fatal(err)
	}
	w.Load("apptown-agent")
	w.Load("docker:27-dind")
	w.Docker("tag", "apptown-agent", image)
	remote := func() *worker.RemoteAgent {
		return &worker.RemoteAgent{Sessions: worker.SessionClient{URL: w.URL, Client: workertest.Client(t, ca, workertest.Installation)},
			Image: image, CopyEvery: 200 * time.Millisecond}
	}
	first := remote()
	cfg := Config{DataDir: t.TempDir(), DockerClient: newFakeDocker(), ConsoleCLI: fixtureCLI(t), ConsoleAgent: consoletest.NewAgent(),
		ConsoleWorker: first, InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}}
	a := workAuthorityFixture(t, cfg)
	first.Root = filepath.Join(a.cfg.dataDir, "console")
	configureConsoleModel(t, a)
	rebuildWork(t, a, func(deps *workflow.Dependencies) { deps.Git = workflow.GitWorkspace{DataDir: a.cfg.dataDir} })
	if resp := authzCall(a.Handler(), "owner", "POST", "/api/v1/projects/game/repo", `{}`); resp.Code != 200 {
		t.Fatalf("initialize repository: %d %s", resp.Code, resp.Body.String())
	}
	change, err := a.Work.Create(asPerson("owner"), workflow.ChangeDraft{ProjectID: idstest.N(1), Title: "Work on the worker"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Stop()
	hub := a.projectRepoPath("game")
	tip := func() string { return scm.Ref(ctx, hub, "refs/heads/"+change.Branch) }

	// Opening: the default places a change's chat on the worker.
	resp := authzCall(a.Handler(), "owner", "POST", "/api/v1/projects/game/console", fmt.Sprintf(`{"model":"fake/model","changeId":%q}`, change.ID))
	var opened struct{ Session struct{ ID string } }
	if err := json.Unmarshal(resp.Body.Bytes(), &opened); err != nil || resp.Code != 201 {
		t.Fatalf("open: %d %s", resp.Code, resp.Body.String())
	}
	id := opened.Session.ID
	if meta, err := a.Console.Recorded(id); err != nil || !conversation.OnWorkerRecord(meta) {
		t.Fatalf("the chat did not open on the worker: %v", err)
	}
	if err := a.Console.Warm(ctx, id); err != nil {
		t.Fatal(err)
	}
	commit := func(name string) string {
		t.Helper()
		w.Docker("exec", "apptown-console-"+id, "sh", "-c",
			"cd /work && echo "+name+" > "+name+" && git add "+name+" && git -c user.name=chat -c user.email=chat@x commit -qm "+name)
		return strings.TrimSpace(w.Docker("exec", "apptown-console-"+id, "git", "-C", "/work", "rev-parse", "HEAD"))
	}

	// A turn's commit is saved.
	one := commit("one.txt")
	a.changeConsole.SaveAfterTurn(id)
	if tip() != one {
		t.Fatalf("the first save: branch at %s, want %s", tip(), one)
	}

	// AppTown restarts: the chat is found on the worker and saves again.
	a.StopConsoles(ctx)
	if err := a.ShutdownBackground(ctx); err != nil {
		t.Fatal(err)
	}
	second := remote()
	cfg.ConsoleWorker = second
	cfg.DataDir = a.cfg.dataDir
	b := newApp(t, a.DB, cfg)
	second.Root = filepath.Join(b.cfg.dataDir, "console")
	if err := b.RecoverConsoles(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := b.Console.Get(ctx, id)
	if err != nil {
		t.Fatalf("the restarted AppTown lost the chat: %v", err)
	}
	if st := b.Console.State(ctx, s); st == console.Unavailable || st == console.Dead {
		t.Fatalf("the chat after an AppTown restart: %s", st)
	}
	if err := b.Console.Warm(ctx, id); err != nil {
		t.Fatal(err)
	}
	two := commit("two.txt")
	b.changeConsole.SaveAfterTurn(id)
	if tip() != two {
		t.Fatalf("a save after AppTown restarted: branch at %s, want %s", tip(), two)
	}

	// The worker restarts: the chat carries on.
	w.Restart()
	if st := b.Console.State(ctx, s); st == console.Unavailable || st == console.Dead {
		t.Fatalf("the chat after a worker restart: %s", st)
	}
	three := commit("three.txt")
	b.changeConsole.SaveAfterTurn(id)
	if tip() != three {
		t.Fatalf("a save after the worker restarted: branch at %s, want %s", tip(), three)
	}

	// A landing saves the chat's latest commit first and lands it.
	defineWorkflows(t, b, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "accept", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}}}}})
	b.Work.Stop()
	four := commit("four.txt")
	run, err := b.Work.RequestLanding(conversation.Context(ctx, s), change.ID)
	if err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	candidate, _, err := workflow.LandingCandidateIn(ctx, b.DB, run.ID)
	if err != nil || candidate.Candidate != four {
		t.Fatalf("the landing's candidate: %s %v, want %s", candidate.Candidate, err, four)
	}

	// The end: nothing of the chat is left on the worker, and the change is free.
	if err := b.changeConsole.End(ctx, id, "owner"); err != nil {
		t.Fatal(err)
	}
	if left := w.Docker("ps", "-a", "--filter", "label=forge.owner="+id, "--format", "{{.Names}}"); left != "" {
		t.Fatalf("left on the worker's Docker: %s", left)
	}
	if gone, err := b.Console.Gone(ctx, id); err != nil || !gone {
		t.Fatalf("the ended chat is not gone: %v %v", gone, err)
	}
}
