package worker_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/console/pi"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// fakeSessions stands in for the chat runtime on the worker's Docker.
type fakeSessions struct {
	mu       sync.Mutex
	status   map[string]console.Status
	launches []console.Launch
	calls    []string
	// runGate, when set, holds Run until it is closed.
	runGate chan struct{}
	running chan struct{}
	// keepDocker: Remove reports success but the private daemon stays.
	keepDocker bool
	// keepDockerRunning: Stop reports success but the private daemon runs.
	keepDockerRunning bool
	// keepScript: Stop and Remove report success but a save's container
	// is left.
	keepScript bool
	statusErr  error
	reaped     map[string]bool
}

func (f *fakeSessions) Deliver(context.Context, console.Launch, string) error { return nil }
func (f *fakeSessions) Interrupt(context.Context, string) error               { return nil }
func (f *fakeSessions) Attach(context.Context, string, console.Size) (console.Terminal, error) {
	return nil, errors.New("no terminal")
}
func (f *fakeSessions) End(ctx context.Context, id string) error { return f.Remove(ctx, id) }

func (f *fakeSessions) Start(_ context.Context, in console.Launch) error {
	f.mu.Lock()
	f.calls = append(f.calls, "start "+in.ID)
	gate, running := f.runGate, f.running
	f.runGate, f.running = nil, nil
	f.mu.Unlock()
	if running != nil {
		close(running)
	}
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.launches = append(f.launches, in)
	f.status[in.ID] = console.Status{Exists: true, Running: true, EverStarted: true, SidecarExists: in.Docker, SidecarRunning: in.Docker}
	return nil
}

func (f *fakeSessions) Status(_ context.Context, id string) (console.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusErr != nil {
		return console.Status{}, f.statusErr
	}
	return f.status[id], nil
}

func (f *fakeSessions) Stale(context.Context, console.Launch) (bool, error) { return false, nil }

func (f *fakeSessions) Stop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "stop "+id)
	st := f.status[id]
	st.Running = false
	st.SidecarRunning = st.SidecarRunning && f.keepDockerRunning
	st.ScriptExists = f.keepScript
	f.status[id] = st
	return nil
}

func (f *fakeSessions) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "remove "+id)
	st := console.Status{ScriptExists: f.keepScript}
	if f.keepDocker {
		st.SidecarExists = f.status[id].SidecarExists
	}
	f.status[id] = st
	return nil
}

func (f *fakeSessions) Reap(_ context.Context, alive map[string]bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reaped = alive
	return nil
}

// with changes the fake while the worker may be reading it.
func (f *fakeSessions) with(change func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change()
}

func (f *fakeSessions) launch(i int) console.Launch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.launches[i]
}

func (f *fakeSessions) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func sessionWorker(t *testing.T) (worker.SessionClient, *fakeSessions, string) {
	t.Helper()
	ca := workertest.NewCA(t)
	state := t.TempDir()
	fake := &fakeSessions{status: map[string]console.Status{}}
	srv := workertest.StartServer(t, ca, &worker.Server{State: state, Sessions: fake})
	return worker.SessionClient{URL: srv.URL, Client: workertest.Client(t, ca, workertest.Installation)}, fake, state
}

func chatSpec() worker.SessionSpec {
	return worker.SessionSpec{Image: "apptown-agent:test", Model: "m", AgentSession: "console", Docker: true, Env: []string{"APPTOWN_URL=https://forge.example"}}
}

// A session runs its container and private daemon from AppTown's spec,
// with its files in the worker's own state directory, and its status
// reports both.
func TestASessionRunsItsChatAndPrivateDockerOnTheWorker(t *testing.T) {
	ctx := context.Background()
	client, fake, state := sessionWorker(t)
	if _, err := client.Put(ctx, "abc123", chatSpec()); err != nil {
		t.Fatal(err)
	}
	st, err := client.Start(ctx, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Known || !st.Agent.Exists || !st.Agent.Running || !st.Docker.Exists || st.Gone() {
		t.Fatalf("a started session: %+v", st)
	}
	in := fake.launch(0)
	if in.Image != "apptown-agent:test" || in.AgentSession != "console" || !in.Docker || !slices.Equal(in.Env, chatSpec().Env) || len(in.Mounts) != 0 {
		t.Fatalf("launch: %+v", in)
	}
	for _, dir := range []string{in.Dir, in.Workspace.Path} {
		if !strings.HasPrefix(dir, state+string(filepath.Separator)) {
			t.Fatalf("%s is outside the worker's state", dir)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("session files: %v", err)
		}
	}
	list, err := client.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "abc123" || list[0].Removed {
		t.Fatalf("list: %+v %v", list, err)
	}
	unknown, err := client.Status(ctx, "other1")
	if err != nil || unknown.Known || !unknown.Gone() {
		t.Fatalf("a session the worker never had: %+v %v", unknown, err)
	}
}

// Removing a session is for good: the record says removed before anything
// is taken away, a later start or spec is refused, and so is one that was
// already on its way when the removal began.
func TestARemovedSessionIsNeverStartedAgain(t *testing.T) {
	ctx := context.Background()
	client, fake, _ := sessionWorker(t)
	if _, err := client.Put(ctx, "abc123", chatSpec()); err != nil {
		t.Fatal(err)
	}
	gate, running := make(chan struct{}), make(chan struct{})
	fake.with(func() { fake.runGate, fake.running = gate, running })
	started := make(chan error, 1)
	go func() { _, err := client.Start(ctx, "abc123"); started <- err }()
	<-running
	removed := make(chan error, 1)
	go func() { _, err := client.Remove(ctx, "abc123"); removed <- err }()
	select {
	case err := <-removed:
		close(gate) // let the start finish, so the server can close
		t.Fatalf("the removal overtook a start in progress: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(gate)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	if err := <-removed; err != nil {
		t.Fatal(err)
	}
	if _, err := client.Start(ctx, "abc123"); !errors.Is(err, worker.ErrSessionRemoved) {
		t.Fatalf("starting a removed session: %v", err)
	}
	if _, err := client.Put(ctx, "abc123", chatSpec()); !errors.Is(err, worker.ErrSessionRemoved) {
		t.Fatalf("respecifying a removed session: %v", err)
	}
	if calls := fake.callLog(); !slices.Equal(calls, []string{"start abc123", "remove abc123"}) {
		t.Fatalf("runtime calls: %v", calls)
	}
	st, err := client.Status(ctx, "abc123")
	if err != nil || !st.Known || !st.Removed || !st.Gone() {
		t.Fatalf("a removed session: %+v %v", st, err)
	}
	// A removal of a session the worker never had fences it too.
	if _, err := client.Remove(ctx, "never1"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(ctx, "never1", chatSpec()); !errors.Is(err, worker.ErrSessionRemoved) {
		t.Fatalf("a session removed before it was known: %v", err)
	}
}

// A removal is done only when the worker's Docker shows nothing of the
// session: a runtime that says it removed everything while the private
// daemon stays is not proof, the files stay, and asking again finishes it.
func TestARemovalIsDoneOnlyWhenNothingOfTheSessionRemains(t *testing.T) {
	ctx := context.Background()
	client, fake, _ := sessionWorker(t)
	if _, err := client.Put(ctx, "abc123", chatSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Start(ctx, "abc123"); err != nil {
		t.Fatal(err)
	}
	files := filepath.Dir(fake.launch(0).Dir)
	fake.with(func() { fake.keepDocker = true })
	if _, err := client.Remove(ctx, "abc123"); !errors.Is(err, execution.ErrWorkerUnavailable) || !strings.Contains(err.Error(), "still present") {
		t.Fatalf("a removal that left the private daemon: %v", err)
	}
	if _, err := os.Stat(files); err != nil {
		t.Fatalf("the files went before the runtime did: %v", err)
	}
	if _, err := client.Start(ctx, "abc123"); !errors.Is(err, worker.ErrSessionRemoved) {
		t.Fatalf("a half-removed session started: %v", err)
	}
	fake.with(func() { fake.keepDocker = false })
	st, err := client.Remove(ctx, "abc123")
	if err != nil || !st.Gone() {
		t.Fatalf("asking again: %+v %v", st, err)
	}
	if _, err := os.Stat(files); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("files after removal: %v", err)
	}
}

// What AppTown cannot find out is unknown, never absence: an unreachable
// worker, and a worker that cannot read its Docker.
func TestAnUnreachableWorkerIsUnknown(t *testing.T) {
	ctx := context.Background()
	client, fake, _ := sessionWorker(t)
	if _, err := client.Put(ctx, "abc123", chatSpec()); err != nil {
		t.Fatal(err)
	}
	fake.with(func() { fake.statusErr = errors.New("docker: cannot connect") })
	for name, call := range map[string]func() (worker.SessionStatus, error){
		"status": func() (worker.SessionStatus, error) { return client.Status(ctx, "abc123") },
		"remove": func() (worker.SessionStatus, error) { return client.Remove(ctx, "abc123") },
	} {
		if st, err := call(); !errors.Is(err, execution.ErrWorkerUnavailable) {
			t.Fatalf("%s with Docker unreadable: %+v %v", name, st, err)
		}
	}
	gone := worker.SessionClient{URL: "https://127.0.0.1:1", Client: client.Client}
	if st, err := gone.Status(ctx, "abc123"); !errors.Is(err, execution.ErrWorkerUnavailable) {
		t.Fatalf("an unreachable worker: %+v %v", st, err)
	}
}

// Stopping or rebuilding a session's runtime is not ending the session: its
// record and files stay, and a start runs it again from its spec.
func TestStoppingOrRebuildingKeepsTheSession(t *testing.T) {
	ctx := context.Background()
	client, fake, _ := sessionWorker(t)
	if _, err := client.Put(ctx, "abc123", chatSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Start(ctx, "abc123"); err != nil {
		t.Fatal(err)
	}
	files := fake.launch(0).Dir
	st, err := client.Stop(ctx, "abc123")
	if err != nil || !st.Known || !st.Agent.Exists || st.Agent.Running || st.Removed {
		t.Fatalf("stopped: %+v %v", st, err)
	}
	st, err = client.Rebuild(ctx, "abc123")
	if err != nil || !st.Gone() || st.Removed || !st.Known {
		t.Fatalf("rebuilt: %+v %v", st, err)
	}
	if _, err := os.Stat(files); err != nil {
		t.Fatalf("a rebuild took the session's files: %v", err)
	}
	spec := chatSpec()
	spec.Image = "apptown-agent:newer"
	if _, err := client.Put(ctx, "abc123", spec); err != nil {
		t.Fatal(err)
	}
	if st, err := client.Start(ctx, "abc123"); err != nil || !st.Agent.Running {
		t.Fatalf("started again: %+v %v", st, err)
	}
	if got := fake.launch(1); got.Image != "apptown-agent:newer" || got.Dir != files {
		t.Fatalf("the rebuilt launch: %+v", got)
	}
}

// A restarted worker keeps its sessions' records: a live session is still
// known and kept by the reaper, a removal it did not finish is finished,
// and a removed session is still refused.
func TestARestartedWorkerKeepsItsSessions(t *testing.T) {
	ctx := context.Background()
	ca := workertest.NewCA(t)
	state := t.TempDir()
	fake := &fakeSessions{status: map[string]console.Status{}}
	first := workertest.StartServer(t, ca, &worker.Server{State: state, Sessions: fake})
	client := worker.SessionClient{URL: first.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	for _, id := range []string{"live1", "ended1"} {
		if _, err := client.Put(ctx, id, chatSpec()); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Start(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	fake.with(func() { fake.keepDocker = true })
	if _, err := client.Remove(ctx, "ended1"); err == nil {
		t.Fatal("the removal finished with the private daemon present")
	}
	first.Close()

	fake.with(func() {
		fake.keepDocker = false
		fake.status["ended1"] = console.Status{SidecarExists: true}
	})
	restarted := &worker.Server{Installation: workertest.Installation, State: state, Sessions: fake}
	if err := restarted.ReapSessions(ctx); err != nil {
		t.Fatal(err)
	}
	var reaped map[string]bool
	fake.with(func() { reaped = fake.reaped })
	if !reaped["live1"] || reaped["ended1"] {
		t.Fatalf("live sessions given to the reaper: %v", reaped)
	}
	if _, err := os.Stat(filepath.Join(state, "chats", "ended1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the unfinished removal's files: %v", err)
	}
	second := workertest.StartServer(t, ca, restarted)
	client.URL = second.URL
	if st, err := client.Status(ctx, "live1"); err != nil || !st.Known || st.Removed {
		t.Fatalf("a live session after restart: %+v %v", st, err)
	}
	if _, err := client.Start(ctx, "ended1"); !errors.Is(err, worker.ErrSessionRemoved) {
		t.Fatalf("a removed session after restart: %v", err)
	}
}

// A spec carries no host paths or mounts, and ids and keys name only
// files of their own under the worker's state.
func TestSessionRequestsAreChecked(t *testing.T) {
	ctx := context.Background()
	client, fake, _ := sessionWorker(t)
	for name, spec := range map[string]worker.SessionSpec{
		"no image":             {AgentSession: "console"},
		"agent session quoted": {Image: "i", AgentSession: "x'; rm -rf /"},
		"bad environment":      {Image: "i", AgentSession: "console", Env: []string{"NOT AN ENTRY"}},
		"image as an option":   {Image: "--volume=/tmp:/outside", AgentSession: "console"},
		"image with a space":   {Image: "busybox -v", AgentSession: "console"},
		"mirror as an option":  {Image: "i", AgentSession: "console", Mirror: "--insecure-registry=0.0.0.0/0"},
		"mirror not a URL":     {Image: "i", AgentSession: "console", Mirror: "mirror.example"},
		"mirror with a space":  {Image: "i", AgentSession: "console", Mirror: "https://m.example --debug"},
	} {
		if _, err := client.Put(ctx, "abc123", spec); err == nil || !strings.Contains(err.Error(), "400") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, image := range []string{
		"apptown-agent",
		"registry.example/team/agent:stable",
		"registry.example:5000/team/agent:stable",
		"localhost:5000/apptown-agent",
		"apptown-agent@sha256:" + strings.Repeat("a", 64),
	} {
		if _, err := client.Put(ctx, "ok"+strconv.Itoa(len(image)), worker.SessionSpec{Image: image, AgentSession: "console", Mirror: "http://10.0.0.5:5000/v2-mirror"}); err != nil {
			t.Fatalf("a valid image reference %s: %v", image, err)
		}
	}
	for _, target := range []string{
		"/v1/sessions/..%2Fx",
		"/v1/sessions/a.b",
		"/v1/workspaces/..%2F..%2Fx?ref=refs/heads/work&commit=" + strings.Repeat("a", 40),
		"/v1/tasks/..%2Fx",
	} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, client.URL+target, nil)
		if strings.HasPrefix(target, "/v1/workspaces") {
			req.Method = http.MethodPut
		}
		resp, err := client.Client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %s", target, resp.Status)
		}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, client.URL+"/v1/sessions/abc123",
		strings.NewReader(`{"image":"i","agentSession":"console","mounts":["/:/host"]}`))
	resp, err := client.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a spec with mounts: %s", resp.Status)
	}
	if len(fake.callLog()) != 0 {
		t.Fatalf("a refused request reached the runtime: %v", fake.callLog())
	}
}

// With the production runtime, a private Docker daemon that could not be
// stopped or removed, or a network left behind, makes stop, rebuild and
// removal fail rather than report success; with nothing failing, each
// succeeds and Docker shows what it promised.
func TestPrivateDockerCleanupFailuresAreNotSuccess(t *testing.T) {
	for _, operation := range []string{"stop", "rebuild", "remove"} {
		for _, failing := range []bool{false, true} {
			name := operation
			if failing {
				name += " failing"
			}
			t.Run(name, func(t *testing.T) {
				const id = "abc123"
				var mu sync.Mutex
				// A rebuild is of a stopped chat: a running one with no record
				// may be mid-turn, and is refused.
				agent, running, daemon, daemonRunning, network := true, operation != "rebuild", true, true, true
				fail := ""
				runtime := pi.New(pi.Config{
					Image: func() string { return "agent:test" },
					Run: func(_ context.Context, args ...string) ([]byte, error) {
						mu.Lock()
						defer mu.Unlock()
						call := strings.Join(args, " ")
						if strings.Contains(call, "forge-console-script-") { // no script runs in this test
							if args[0] == "inspect" {
								return []byte("Error response from daemon: No such container: x"), errors.New("absent")
							}
							return nil, nil
						}
						if fail != "" && call == fail {
							return []byte("injected cleanup failure"), errors.New("cleanup failed")
						}
						missing := func() ([]byte, error) {
							return []byte("Error response from daemon: No such container: x"), errors.New("absent")
						}
						switch args[0] {
						case "inspect":
							if args[len(args)-1] == pi.ContainerName(id) {
								if !agent {
									return missing()
								}
								return []byte(fmt.Sprintf(`%t 2026-09-30T00:00:00Z img {"forge.console.dind":"1","forge.console.model":"m"}`, running)), nil
							}
							if !daemon {
								return missing()
							}
							if strings.Contains(call, "{{.State.Running}}") {
								return []byte(fmt.Sprintf("%s %t", id, daemonRunning)), nil
							}
							return []byte(id), nil
						case "image":
							return []byte("img"), nil
						case "stop":
							if args[len(args)-1] == pi.ContainerName(id) {
								running = false
							} else {
								daemonRunning = false
							}
						case "rm":
							if args[len(args)-1] == pi.ContainerName(id) {
								agent, running = false, false
							} else {
								daemon, daemonRunning = false, false
							}
						case "network":
							if args[1] == "inspect" {
								if !network {
									return missing()
								}
								return []byte(id), nil
							}
							if args[1] == "rm" {
								network = false
							}
						default:
							return nil, fmt.Errorf("unexpected Docker command: %s", call)
						}
						return nil, nil
					},
				})
				t.Cleanup(runtime.Close)
				ca := workertest.NewCA(t)
				srv := workertest.StartServer(t, ca, &worker.Server{State: t.TempDir(), Sessions: runtime})
				client := worker.SessionClient{URL: srv.URL, Client: workertest.Client(t, ca, workertest.Installation)}
				ctx := context.Background()
				if _, err := client.Put(ctx, id, worker.SessionSpec{Image: "agent:test", Model: "m", AgentSession: "console", Docker: true}); err != nil {
					t.Fatal(err)
				}
				if failing {
					mu.Lock()
					fail = map[string]string{
						"stop":    "stop --time 3 forge-dind-console-" + id,
						"rebuild": "rm -f -v forge-dind-console-" + id,
						"remove":  "network rm forge-dind-console-" + id,
					}[operation]
					mu.Unlock()
				}
				call := map[string]func(context.Context, string) (worker.SessionStatus, error){
					"stop": client.Stop, "rebuild": client.Rebuild, "remove": client.Remove,
				}[operation]
				st, err := call(ctx, id)
				if failing {
					if !errors.Is(err, execution.ErrWorkerUnavailable) {
						t.Fatalf("%s reported success despite failed private Docker cleanup: %+v %v", operation, st, err)
					}
					return
				}
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err != nil:
					t.Fatal(err)
				case operation == "stop" && (!st.Stopped() || daemonRunning || !daemon):
					t.Fatalf("stopped: %+v, daemon running %v", st, daemonRunning)
				case operation != "stop" && (!st.Gone() || daemon || agent):
					t.Fatalf("%s: %+v", operation, st)
				case operation == "remove" && network:
					t.Fatal("the removal left the private network")
				}
			})
		}
	}
}

// A stop is done only when Docker shows nothing of the session running: a
// runtime that reports success while the private daemon runs is not proof.
func TestAStopIsDoneOnlyWhenNothingRuns(t *testing.T) {
	ctx := context.Background()
	client, fake, _ := sessionWorker(t)
	if _, err := client.Put(ctx, "abc123", chatSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Start(ctx, "abc123"); err != nil {
		t.Fatal(err)
	}
	fake.with(func() { fake.keepDockerRunning = true })
	if st, err := client.Stop(ctx, "abc123"); !errors.Is(err, execution.ErrWorkerUnavailable) {
		t.Fatalf("a stop that left the private daemon running: %+v %v", st, err)
	}
	fake.with(func() { fake.keepDockerRunning = false })
	if st, err := client.Stop(ctx, "abc123"); err != nil || !st.Stopped() || !st.Docker.Exists {
		t.Fatalf("stopping again: %+v %v", st, err)
	}
}

// A save's container on the chat's work tree counts as something of the
// chat: a stop or a removal that leaves one is not done.
func TestAStopOrRemovalLeavingASaveContainerIsNotDone(t *testing.T) {
	ctx := context.Background()
	client, fake, _ := sessionWorker(t)
	if _, err := client.Put(ctx, "abc123", chatSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Start(ctx, "abc123"); err != nil {
		t.Fatal(err)
	}
	fake.with(func() { fake.keepScript = true })
	if _, err := client.Stop(ctx, "abc123"); !errors.Is(err, execution.ErrWorkerUnavailable) {
		t.Fatalf("a stop that left a save's container: %v", err)
	}
	if _, err := client.Remove(ctx, "abc123"); !errors.Is(err, execution.ErrWorkerUnavailable) {
		t.Fatalf("a removal that left a save's container: %v", err)
	}
	fake.with(func() { fake.keepScript = false })
	if st, err := client.Remove(ctx, "abc123"); err != nil || !st.Gone() {
		t.Fatalf("removing again: %+v %v", st, err)
	}
}
