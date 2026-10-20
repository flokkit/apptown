package console_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/consoletest"
)

// A console from open to end, with a fake agent and no database: the whole
// usage of the package in one place.
func TestAConsoleFromOpenToEnd(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()             // session records live at <data>/console/<id>/
	agent := consoletest.NewAgent() // stands in for pi in a container
	chats := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: data}})

	// Open: everything the session needs is handed in; it looks nothing up.
	s, err := chats.Open(ctx, console.OpenRequest{
		Scope:     "project:coin-flip",
		Labels:    map[string]string{"project": "1"},
		Workspace: console.Workspace{Path: "/tmp/checkout", Writable: false, Owned: true},
		Steering:  "You are helping with Coin Flip.",
		Actor:     "bls",
		Model:     "claude-sonnet-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := agent.Started(s.ID); got.Workspace.Path != "/tmp/checkout" || got.Workspace.Writable || got.Model != "claude-sonnet-5" || got.Docker || got.Dir != s.Dir {
		t.Fatalf("the agent was started with %+v", got)
	}
	if steering := readFile(t, s.Dir+"/steering.md"); steering != "You are helping with Coin Flip." {
		t.Fatalf("steering on disk: %q", steering)
	}

	// Talk: a message goes in; the reply comes back out of the record.
	if err := chats.Send(ctx, s.ID, "What does this project do?"); err != nil {
		t.Fatal(err)
	}
	agent.Reply(s.ID, "It flips a coin.")
	page, err := chats.Messages(ctx, s.ID, 0)
	if err != nil || len(page.Messages) != 2 || page.Messages[0].Role != "you" || page.Messages[1].Text != "It flips a coin." || page.Turn != console.HandedBack {
		t.Fatalf("messages: %+v %v", page, err)
	}

	// While the agent works, the page says so; a message still goes in, as at a terminal.
	agent.Working(s.ID)
	if page, _ := chats.Messages(ctx, s.ID, 0); page.Turn != console.Working {
		t.Fatalf("turn: %v", page.Turn)
	}
	if err := chats.Send(ctx, s.ID, "and?"); err != nil {
		t.Fatal(err)
	}
	if err := chats.Interrupt(ctx, s.ID); err != nil || !agent.Interrupted(s.ID) {
		t.Fatal("interrupt did not reach the agent")
	}

	// The token is the one credential the agent inside holds: its own session, its own project.
	if id, ok := chats.Identity(s.Token); !ok || id.Scope != "project:coin-flip" || id.Labels["project"] != "1" || id.SessionID != s.ID || id.Actor != "bls" {
		t.Fatalf("identity: %+v %v", id, ok)
	}
	if _, ok := chats.Identity("not-a-token"); ok {
		t.Fatal("a wrong token identifies nobody")
	}

	// A terminal is a viewer; while one is open the session is attached and never idle.
	term, err := chats.Attach(ctx, s.ID, console.Size{Cols: 120, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	if st := chats.State(ctx, s); st != console.Attached {
		t.Fatalf("state with a viewer: %v", st)
	}
	if s.Idle(time.Now().Add(time.Hour), 30*time.Minute) {
		t.Fatal("a watched session is never idle")
	}
	_ = term.Close()
	if st := chats.State(ctx, s); st != console.Detached {
		t.Fatalf("state after the viewer left: %v", st)
	}

	// Idle is a fact about the session; stopping it is an operation; who decides when is not the console's business.
	if s.Idle(time.Now(), 30*time.Minute) {
		t.Fatal("a session just used is not idle")
	}
	if !s.Idle(time.Now().Add(31*time.Minute), 30*time.Minute) {
		t.Fatal("thirty quiet minutes is idle")
	}
	if err := chats.Stop(ctx, s.ID); err != nil || agent.Running(s.ID) {
		t.Fatal("stop did not stop the agent")
	}
	if st := chats.State(ctx, s); st != console.Stopped {
		t.Fatalf("state after stop: %v", st)
	}
	if _, err := chats.Get(ctx, s.ID); err != nil {
		t.Fatal("a stopped session forgot its record")
	}
	if err := chats.Send(ctx, s.ID, "back again"); err != nil || !agent.Running(s.ID) {
		t.Fatalf("a message did not wake the agent: %v", err)
	}

	// What runs can go stale (the image moved); it is rebuilt at the next wake, never mid-turn.
	agent.MarkStale(s.ID)
	agent.Working(s.ID)
	if err := chats.Send(ctx, s.ID, "still here?"); err != nil || !agent.Exists(s.ID) {
		t.Fatalf("stale mid-turn: %v", err)
	}
	agent.Reply(s.ID, "yes")
	if err := chats.Send(ctx, s.ID, "good"); err != nil {
		t.Fatal(err)
	}
	if st, _ := agent.Status(ctx, s.ID); !st.Running {
		t.Fatal("the rebuilt agent is not running")
	}

	// Recover: a new service over the same directory finds the session.
	again := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: data}})
	if err := again.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	if found, err := again.Get(ctx, s.ID); err != nil || found.Scope != "project:coin-flip" || found.Token != s.Token {
		t.Fatalf("restart lost the session: %+v %v", found, err)
	}
	if id, ok := again.Identity(s.Token); !ok || id.SessionID != s.ID {
		t.Fatal("the token does not identify the recovered session")
	}

	// End: the agent is removed, the record is gone, the id means nothing.
	if _, err := again.End(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if agent.Exists(s.ID) {
		t.Fatal("ended session's agent still exists")
	}
	if _, err := again.Get(ctx, s.ID); !errors.Is(err, console.ErrNoSession) {
		t.Fatalf("ended session: %v", err)
	}
	if _, ok := again.Identity(s.Token); ok {
		t.Fatal("an ended session's token identifies nobody")
	}
	// The words survive the session: a change's transcript reads them off disk.
	convs, _, err := again.Transcript(ctx, "project:coin-flip")
	if err != nil || len(convs) != 1 {
		t.Fatalf("the words survive the session: %+v %v", convs, err)
	}
}

// A change console: the worktree writable, Docker for its own builds, and its
// conversation on the change's transcript after it ends.
func TestAChangeConsole(t *testing.T) {
	ctx := context.Background()
	agent := consoletest.NewAgent()
	chats := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: t.TempDir()}})
	s, err := chats.Open(ctx, console.OpenRequest{
		Scope:     "change:305",
		Labels:    map[string]string{"project": "1", "change": "305"},
		Workspace: console.Workspace{Path: "/tmp/change-305", Writable: true}, // borrowed: the opener keeps it
		Docker:    true,
		Steering:  "You are working change 305.", Actor: "bls",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := agent.Started(s.ID); !got.Workspace.Writable || !got.Docker {
		t.Fatalf("a writable workspace with Docker, as asked: %+v", got)
	}
	if err := chats.Annotate(ctx, s.ID, "started", "306"); err != nil {
		t.Fatal(err)
	}
	if got, _ := chats.Get(ctx, s.ID); got.Label("started") != "306" {
		t.Fatalf("labels: %+v", got.Labels)
	}
	if !chats.Busy(ctx, console.Match{Labels: map[string]string{"project": "1"}}, 10*time.Minute) || chats.Busy(ctx, console.Match{Labels: map[string]string{"project": "2"}}, 10*time.Minute) {
		t.Fatal("busy follows the labels")
	}
	if !chats.UsesWorkspace(ctx, "/tmp/change-305") || chats.UsesWorkspace(ctx, "/tmp/change-306") {
		t.Fatal("uses-workspace follows the checkout")
	}
	if err := chats.Send(ctx, s.ID, "make the button bigger"); err != nil {
		t.Fatal(err)
	}
	agent.Reply(s.ID, "Done: the button is bigger.")
	if _, err := chats.End(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("/tmp/change-305"); err == nil {
		t.Fatal("a borrowed workspace is not the console's to delete")
	}
	convs, _, err := chats.Transcript(ctx, "change:305")
	if err != nil || len(convs) != 1 || len(convs[0].Messages) != 2 || convs[0].Actor != "bls" {
		t.Fatalf("transcript after end: %+v %v", convs, err)
	}
}

// A project being deleted admits no chat and identifies nobody until released.
func TestDeletionBlocksAProject(t *testing.T) {
	ctx := context.Background()
	agent := consoletest.NewAgent()
	chats := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: t.TempDir()}})
	open := console.OpenRequest{Scope: "project:p", Labels: map[string]string{"project": "7"}, Workspace: console.Workspace{Path: "/tmp/p"}, Actor: "bls"}
	s, err := chats.Open(ctx, open)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chats.Block("project", "7"); err == nil {
		t.Fatal("a project with a fresh chat cannot be deleted")
	}
	old := time.Now().Add(-time.Hour)
	s.SetLastUsed(old)
	release, err := chats.Block("project", "7")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := chats.Identity(s.Token); ok {
		t.Fatal("a blocked project's chat identifies nobody")
	}
	if _, err := chats.Open(ctx, open); err == nil {
		t.Fatal("a blocked project admits no chat")
	}
	if err := chats.Remove(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := chats.Open(ctx, open); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// session.json is the session's identity across restarts: what Persist
// writes, Resume reads back verbatim, checkout and last activity included,
// and the file is private (it carries the token into a mounted dir).
func TestRecordRoundTrip(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	chats := console.New(console.Dependencies{Agent: consoletest.NewAgent(), Config: console.Config{DataDir: data}})
	used := time.Now().Add(-42 * time.Minute).Truncate(time.Second)
	s := chats.Restore(console.Meta{ID: "abc123", Scope: "project:p", Labels: map[string]string{"started": "7"}, Token: "tok", CreatedAt: time.Now(), Checkout: "/elsewhere/checkout"})
	s.SetLastUsed(used)
	if err := s.Persist(); err != nil {
		t.Fatal(err)
	}
	path := s.Dir + "/" + console.MetaFile
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("session.json holds the token; it must be 0600, got %o", fi.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	for _, k := range []string{`"checkout"`, `"lastUsed"`, `"token"`} {
		if !strings.Contains(string(raw), k) {
			t.Errorf("session.json must record %s: %s", k, raw)
		}
	}
	again := console.New(console.Dependencies{Agent: consoletest.NewAgent(), Config: console.Config{DataDir: data}})
	if err := again.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := again.Get(context.Background(), "abc123")
	if err != nil {
		t.Fatal("recovery must register a session whose record is present")
	}
	if got.Checkout != "/elsewhere/checkout" || !got.LastUsed().Equal(used) || got.Scope != "project:p" || got.Dir != s.Dir || got.Label("started") != "7" {
		t.Errorf("recovered session differs from the record: %+v lastUsed=%v", got.Meta, got.LastUsed())
	}
}

// Incomplete metadata is preserved for operator repair, transcripts
// included: recovery never invents a checkout or an activity timestamp.
func TestRecoveryPreservesIncompleteRecords(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"checkout", "lastUsed", "id"} {
		t.Run(name, func(t *testing.T) {
			data := t.TempDir()
			dir := data + "/console/chat"
			if err := os.MkdirAll(dir+"/sessions", 0o700); err != nil {
				t.Fatal(err)
			}
			meta := map[string]any{"id": "chat", "scope": "project:p", "checkout": "/recorded/checkout", "lastUsed": "2026-01-01T00:00:00Z"}
			if name == "id" {
				meta[name] = "another-chat"
			} else {
				delete(meta, name)
			}
			raw, _ := json.Marshal(meta)
			files := map[string][]byte{console.MetaFile: raw, "sessions/conversation.jsonl": []byte("conversation canary")}
			for path, body := range files {
				if err := os.WriteFile(dir+"/"+path, body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			chats := console.New(console.Dependencies{Agent: consoletest.NewAgent(), Config: console.Config{DataDir: data}})
			if err := chats.Resume(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := chats.Get(context.Background(), "chat"); err == nil {
				t.Fatal("incomplete record registered for automatic lifecycle management")
			}
			if _, err := chats.Get(context.Background(), "another-chat"); err == nil {
				t.Fatal("mismatched record registered")
			}
			for path, body := range files {
				got, err := os.ReadFile(dir + "/" + path)
				if err != nil || string(got) != string(body) {
					t.Fatalf("recovery changed %s: %v", path, err)
				}
			}
		})
	}
}

// A provision is a model, env, mounts, files and words; the console places
// the files, puts the words into the prompt after the authored steering,
// and replaces both at every wake. A resume gets a notice the agent reads
// at its next launch; the console is the prompt's only writer.
func TestProvisionAndThePrompt(t *testing.T) {
	ctx := context.Background()
	agent := consoletest.NewAgent()
	env := &countingEnvironment{p: console.Provision{
		Model:  "claude-sonnet-5",
		Env:    []string{"ANTHROPIC_API_KEY=sk-test"},
		Mounts: []string{"/tools/apptown:/opt/apptown/bin/apptown:ro"},
		Files:  map[string]console.Staged{"pi-home/.pi/agent/settings.json": {Data: []byte(`{"quietStartup":true}`)}, "pi-home/.pi/agent/npm": {Link: "/opt/pi-baked/.pi/agent/npm"}},
		Notes:  "The registry at http://proxy:8080 serves npm.",
	}}
	chats := console.New(console.Dependencies{Agent: agent, Environment: env, Config: console.Config{DataDir: t.TempDir()}})
	s, err := chats.Open(ctx, console.OpenRequest{Scope: "project:p", Workspace: console.Workspace{Path: "/tmp/p"}, Steering: "You help.", Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	if got := agent.Started(s.ID); got.Model != "claude-sonnet-5" || len(got.Env) != 1 || len(got.Mounts) != 1 {
		t.Fatalf("launch carries the provision: %+v", got)
	}
	if settings := readFile(t, s.Dir+"/pi-home/.pi/agent/settings.json"); settings != `{"quietStartup":true}` {
		t.Fatalf("staged file: %q", settings)
	}
	if target, err := os.Readlink(s.Dir + "/pi-home/.pi/agent/npm"); err != nil || target != "/opt/pi-baked/.pi/agent/npm" {
		t.Fatalf("staged link: %q %v", target, err)
	}
	prompt := s.Steering()
	if !strings.HasPrefix(prompt, "You help.") || !strings.Contains(prompt, "The registry at http://proxy:8080") {
		t.Fatalf("prompt: %q", prompt)
	}
	// The provision moves: the words and the files follow, the authored text stays.
	env.p.Notes = "The registry moved to http://proxy:9090."
	env.p.Files = map[string]console.Staged{"pi-home/.pi/agent/settings.json": {Data: []byte(`{"quietStartup":false}`)}}
	if err := chats.Send(ctx, s.ID, "hi"); err != nil {
		t.Fatal(err)
	}
	prompt = s.Steering()
	if strings.Contains(prompt, "8080") || !strings.Contains(prompt, "9090") || strings.Count(prompt, "You help.") != 1 {
		t.Fatalf("prompt after the provision moved: %q", prompt)
	}
	if _, err := os.Lstat(s.Dir + "/pi-home/.pi/agent/npm"); !os.IsNotExist(err) {
		t.Fatal("a file the provision no longer places was left behind")
	}
	// A stop and a message: the agent resumes with a notice in its prompt.
	if err := chats.Stop(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if err := chats.Send(ctx, s.ID, "back"); err != nil {
		t.Fatal(err)
	}
	if prompt = s.Steering(); !strings.Contains(prompt, "## Notice 1") || !strings.Contains(prompt, "resumed in a restarted container") {
		t.Fatalf("no resume notice: %q", prompt)
	}
	if env.calls < 3 {
		t.Fatalf("the environment is asked at every wake, got %d", env.calls)
	}
	// Static answers the same thing every time: the standalone console.
	standalone := console.New(console.Dependencies{Agent: consoletest.NewAgent(), Environment: console.Static{Provision: console.Provision{Model: "m", Env: []string{"KEY=v"}}}, Config: console.Config{DataDir: t.TempDir()}})
	if _, err := standalone.Open(ctx, console.OpenRequest{Scope: "dir:/tmp/p", Workspace: console.Workspace{Path: "/tmp/p"}, Actor: "me"}); err != nil {
		t.Fatal(err)
	}
}

type countingEnvironment struct {
	p     console.Provision
	calls int
}

func (e *countingEnvironment) Prepare(context.Context, *console.Session) (console.Provision, error) {
	e.calls++
	return e.p, nil
}

// endingAgent holds each chat's end until released: a chat still making its
// last save.
type endingAgent struct {
	*consoletest.Agent
	started, release chan struct{}
}

func (a endingAgent) End(ctx context.Context, id string) error {
	close(a.started)
	<-a.release
	return a.Agent.End(ctx, id)
}

// A project whose chat is ending cannot be deleted until the chat has
// ended: its last save needs the change the deletion would remove.
func TestDeletionWaitsForAnEndingChat(t *testing.T) {
	ctx := context.Background()
	agent := endingAgent{Agent: consoletest.NewAgent(), started: make(chan struct{}), release: make(chan struct{})}
	chats := console.New(console.Dependencies{Agent: agent, Config: console.Config{DataDir: t.TempDir()}})
	s, err := chats.Open(ctx, console.OpenRequest{Scope: "project:p", Labels: map[string]string{"project": "7"}, Workspace: console.Workspace{Path: "/tmp/p"}, Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	s.SetLastUsed(time.Now().Add(-time.Hour))
	ended := make(chan error, 1)
	go func() { _, err := chats.End(ctx, s.ID); ended <- err }()
	<-agent.started
	if _, err := chats.Block("project", "7"); err == nil || !strings.Contains(err.Error(), "still saving its work") {
		t.Fatalf("a project was admitted for deletion while its chat was ending: %v", err)
	}
	close(agent.release)
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
	release, err := chats.Block("project", "7")
	if err != nil {
		t.Fatalf("a project whose chat has ended: %v", err)
	}
	release()
}
