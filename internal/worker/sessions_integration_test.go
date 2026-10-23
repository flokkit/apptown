//go:build integration

package worker_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/gitexec"

	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// The chat tests run apptown-worker as a process on this machine, driving its
// Docker directly (workertest.StartOnHost): the chat's container and its
// daemon one level down, what the daemon runs two, the supported topology.
// A chat's container and its daemon each get these limits.
var chatLimits = []string{"-chat-cpus", "1", "-chat-memory-mib", "1024", "-chat-pids", "512"}

// removeSessionLeftovers removes, when the test ends, whatever a failed
// test left of a session on this machine's Docker: only what carries its id.
func removeSessionLeftovers(t *testing.T, id func() string) {
	t.Cleanup(func() {
		if id() == "" {
			return
		}
		for _, list := range [][]string{{"ps", "-aq", "--filter", "label=apptown.owner=" + id()}, {"network", "ls", "-q", "--filter", "label=apptown.console=" + id()},
			{"volume", "ls", "-q", "--filter", "label=apptown.console=" + id()}} {
			out, _ := exec.Command("docker", list...).Output()
			for _, obj := range strings.Fields(string(out)) {
				rm := map[string][]string{"ps": {"rm", "-f", "-v"}, "network": {"network", "rm"}, "volume": {"volume", "rm"}}[list[0]]
				_ = exec.Command("docker", append(rm, obj)...).Run()
			}
		}
	})
}

// A chat's session on apptown-worker, with real Docker: starting it runs the
// chat's container and its private Docker daemon, each with its limits, both
// on the session's own cache volume, the daemon with the worker's resolvers
// and AppTown's mirror; stopping keeps both, running neither; a restarted
// worker still knows the session; and removing it leaves nothing on the
// worker's Docker, its cache included, and fences it for good.
func TestIntegrationASessionOnARealWorker(t *testing.T) {
	if exec.Command("docker", "image", "inspect", "apptown-agent").Run() != nil {
		t.Skip("needs AppTown-agent image on this machine's Docker")
	}
	ctx := context.Background()
	ca := workertest.NewCA(t)
	w := workertest.StartOnHost(t, ca, chatLimits...)
	client := worker.SessionClient{URL: w.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	id := fmt.Sprintf("c%d", time.Now().UnixNano()%1_000_000_000)
	removeSessionLeftovers(t, func() string { return id })
	const mirror = "http://mirror.invalid:5000"
	if _, err := client.Put(ctx, id, worker.SessionSpec{Image: "apptown-agent", AgentSession: "console", Docker: true, Mirror: mirror}); err != nil {
		t.Fatal(err)
	}
	st, err := client.Start(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Agent.Running || !st.Docker.Exists {
		t.Fatalf("started: %+v", st)
	}
	chat, daemon, cache := "apptown-console-"+id, "apptown-dind-console-"+id, "apptown-cache-console-"+id
	running := w.Docker("ps", "--format", "{{.Names}}")
	for _, name := range []string{chat, daemon} {
		if !strings.Contains(running, name) {
			t.Fatalf("%s is not running on the worker: %s", name, running)
		}
	}
	if owner := w.Docker("volume", "inspect", "--format", `{{index .Labels "apptown.console"}}`, cache); owner != id {
		t.Fatalf("the session's cache volume is labelled %q", owner)
	}
	for _, name := range []string{chat, daemon} {
		// Configured: Docker's own record of the container.
		if mounts := w.Docker("inspect", "--format", `{{range .Mounts}}{{.Name}}:{{.Destination}} {{end}}`, name); !strings.Contains(mounts, cache+":/apptown-cache") ||
			strings.Contains(mounts, "forge-cache:") {
			t.Fatalf("%s mounts %s, not the session's own cache", name, mounts)
		}
		if got := w.Docker("inspect", "--format", "{{.HostConfig.NanoCpus}} {{.HostConfig.Memory}} {{.HostConfig.MemorySwap}} {{.HostConfig.PidsLimit}}", name); got != "1000000000 1073741824 1073741824 512" {
			t.Fatalf("%s's configured limits: %s", name, got)
		}
		// Observed: what the kernel holds the container to.
		if got := w.Docker("exec", name, "cat", "/sys/fs/cgroup/cpu.max", "/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/pids.max"); strings.Fields(got)[0] != "100000" ||
			strings.Join(strings.Fields(got)[1:], " ") != "100000 1073741824 512" {
			t.Fatalf("%s's cgroup limits: %s", name, got)
		}
	}
	// The daemon's own flags: this machine's resolvers, AppTown's mirror;
	// its builder the same; and a nested container's resolvers are this
	// machine's too.
	args := w.Docker("inspect", "--format", "{{json .Args}}", daemon)
	if !strings.Contains(args, "--registry-mirror="+mirror) || !strings.Contains(args, "--insecure-registry=mirror.invalid:5000") {
		t.Fatalf("the daemon does not pull through the mirror: %s", args)
	}
	for _, ns := range resolvers(t, "nameserver") {
		if !strings.Contains(args, "--dns="+ns) {
			t.Fatalf("the daemon lacks this machine's resolver %s: %s", ns, args)
		}
	}
	if info := w.Docker("exec", "-e", "DOCKER_HOST=tcp://localhost:2375", daemon, "docker", "info", "--format", "{{json .RegistryConfig.Mirrors}}"); !strings.Contains(info, "mirror.invalid:5000") {
		t.Fatalf("the daemon's mirrors: %s", info)
	}
	if toml := w.Docker("exec", daemon, "cat", "/forge-dind/buildkitd.toml"); !strings.Contains(toml, `mirrors = ["mirror.invalid:5000"]`) {
		t.Fatalf("the builder's configuration: %s", toml)
	}
	// A nested container: this machine's resolvers, and the daemon's PID
	// limit holds what it starts.
	loadNested(t, daemon, diskspace.DFImage, workertest.LoadedBusybox)
	nested := w.Docker("exec", "-e", "DOCKER_HOST=tcp://localhost:2375", daemon, "docker", "run", "--rm", workertest.LoadedBusybox, "sh", "-c",
		"cat /etc/resolv.conf; i=0; while [ $i -lt 700 ]; do sleep 30 & i=$((i+1)); done 2>&1 | tail -1; echo started")
	for _, ns := range resolvers(t, "nameserver") {
		if !strings.Contains(nested, "nameserver "+ns) {
			t.Fatalf("a nested container's resolvers: %s", nested)
		}
	}
	if !strings.Contains(nested, "can't fork") && !strings.Contains(nested, "Resource temporarily unavailable") {
		t.Fatalf("700 nested processes were not refused under the daemon's 512: %s", nested)
	}

	st, err = client.Stop(ctx, id)
	if err != nil || !st.Stopped() || !st.Agent.Exists || !st.Docker.Exists {
		t.Fatalf("stopped: %+v %v", st, err)
	}

	if still := w.Docker("ps", "--filter", "label=apptown.owner="+id, "--format", "{{.Names}}"); still != "" {
		t.Fatalf("running after the stop: %s", still)
	}

	w.Restart()
	st, err = client.Status(ctx, id)
	if err != nil || !st.Known || !st.Agent.Exists {
		t.Fatalf("after a worker restart: %+v %v", st, err)
	}
	if st, err = client.Start(ctx, id); err != nil || !st.Agent.Running {
		t.Fatalf("started again after the restart: %+v %v", st, err)
	}

	st, err = client.Remove(ctx, id)
	if err != nil || !st.Gone() {
		t.Fatalf("removed: %+v %v", st, err)
	}
	if left := w.Docker("volume", "ls", "--filter", "label=apptown.console="+id, "--format", "{{.Name}}"); left != "" {
		t.Fatalf("the session's cache outlived it: %s", left)
	}
	if left := w.Docker("ps", "-a", "--filter", "label=apptown.owner="+id, "--format", "{{.Names}}"); left != "" {
		t.Fatalf("left on the worker's Docker: %s", left)
	}
	if left := w.Docker("network", "ls", "--filter", "label=apptown.console="+id, "--format", "{{.Name}}"); left != "" {
		t.Fatalf("network left on the worker's Docker: %s", left)
	}
	if _, err := client.Start(ctx, id); !errors.Is(err, worker.ErrSessionRemoved) {
		t.Fatalf("starting a removed session: %v", err)
	}
}

// A real chat on a real worker, through AppTown's chat service: the
// agent image under the name AppTown's build context gives it, pi under
// tmux in its container, a message typed in and confirmed against pi's own
// record, that record copied back to AppTown, the terminal showing pi's
// screen, and the end removing it on the worker.
func TestIntegrationARealChatOnAWorker(t *testing.T) {
	if exec.Command("docker", "image", "inspect", "apptown-agent").Run() != nil {
		t.Skip("needs AppTown-agent image on this machine's Docker")
	}
	ctx := context.Background()
	ca := workertest.NewCA(t)
	w := workertest.StartOnHost(t, ca, chatLimits...)
	image, err := worker.AgentImage("../../deploy/apptown-agent")
	if err != nil {
		t.Fatal(err)
	}
	w.Docker("tag", "apptown-agent", image)
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", image).Run() })
	client := worker.SessionClient{URL: w.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	data := t.TempDir()
	agent := &worker.RemoteAgent{Sessions: client, Image: image, Root: filepath.Join(data, "console"), CopyEvery: 200 * time.Millisecond}
	repo, source := chatSourceRepo(t)
	agent.Source = func(ctx context.Context, in console.Launch, dest string) (string, string, error) {
		return "change/7", source, gitexec.BundleCommit(ctx, repo, source, "change/7", dest)
	}
	t.Cleanup(agent.Close)
	// A model pi accepts, at an address that answers nothing: pi records the
	// message, then fails only on asking the model.
	models := `{"providers":{"fake":{"baseUrl":"http://127.0.0.1:9/v1","api":"openai-completions","apiKey":"$APPTOWN_SESSION_TOKEN",` +
		`"authHeader":true,"models":[{"id":"m1","contextWindow":32768,"maxTokens":1024}]}}}`
	env := consoletest.Static{Provision: console.Provision{Model: "fake/m1", Env: []string{"APPTOWN_SESSION_TOKEN=unused"},
		Files: map[string]console.Staged{"pi-home/.pi/agent/models.json": {Data: []byte(models)}}}}
	svc := console.New(console.Dependencies{Agent: agent, Environment: env, Config: console.Config{DataDir: data}})
	s, err := svc.Open(ctx, console.OpenRequest{Scope: "change:1", Workspace: console.Workspace{Path: t.TempDir(), Writable: true},
		Steering: "Answer briefly.", Actor: "alice", Model: "fake/m1", Docker: true})
	if err != nil {
		t.Fatal(err)
	}
	removeSessionLeftovers(t, func() string { return s.ID })
	if err := svc.Warm(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	term, err := svc.Attach(ctx, s.ID, console.Size{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	screen := make([]byte, 1)
	if _, err := io.ReadAtLeast(term, screen, 1); err != nil {
		t.Fatalf("nothing on the chat's screen: %v", err)
	}
	term.Close()

	if err := svc.Send(ctx, s.ID, "hello from AppTown"); err != nil {
		t.Fatalf("a message to a real chat on the worker: %v\nscreen:\n%s", err, screenOf(t, svc, s.ID))
	}
	if !poll.Until(context.Background(), 30*time.Second, 100*time.Millisecond, func() bool {
		for _, m := range s.Messages() {
			if strings.Contains(m.Text, "hello from AppTown") {
				return true
			}
		}
		return false
	}) {
		t.Fatal("the message never appeared in AppTown's copy of the record")
	}

	// A commit in the chat's container is saved: the worker bundles it
	// with the chat's own git, and it passes AppTown's checked import.
	w.Docker("exec", "apptown-console-"+s.ID, "sh", "-c",
		"cd /work && echo saved > saved.txt && git add saved.txt && git -c user.name=chat -c user.email=chat@x commit -qm 'chat work'")
	dest := filepath.Join(t.TempDir(), "save.bundle")
	tip, err := agent.Save(ctx, s.ID, source, dest)
	if err != nil || tip == "" {
		t.Fatalf("a save from the real chat: %q %v", tip, err)
	}
	_, imported, cleanup, err := gitexec.Quarantine(ctx, repo, dest, worker.SaveRef, "change/7", source)
	if err != nil || imported != tip {
		t.Fatalf("AppTown's import of the save: %q %v", imported, err)
	}
	cleanup()

	// Ending while something in the chat's container writes its work tree:
	// the chat stops first, so the last save (with the leftovers committed)
	// is made after the writer has stopped, then the chat is removed.
	w.Docker("exec", "-d", "apptown-console-"+s.ID, "sh", "-c", "while true; do date >> /work/writer.log; sleep 0.05; done")
	// And the chat's private Docker daemon writes it too.
	w.Docker("exec", "-d", "apptown-dind-console-"+s.ID, "sh", "-c", "while true; do date >> /work/dind.log; sleep 0.05; done")
	// Both writers are writing (each has written its log) when the chat is ended.
	if !poll.Until(context.Background(), 30*time.Second, 50*time.Millisecond, func() bool {
		return w.Docker("exec", "apptown-console-"+s.ID, "sh", "-c", "test -s /work/writer.log && test -s /work/dind.log && echo writing || true") == "writing"
	}) {
		t.Fatal("the chat's writers never wrote")
	}
	var last string
	agent.Ending = func(ctx context.Context, id string) error {
		for _, class := range []string{"console", "console-dind"} {
			if running := w.Docker("ps", "--filter", "label=apptown.owner="+id, "--filter", "label=apptown.class="+class, "--format", "{{.Names}}"); running != "" {
				t.Errorf("the last save began while the chat's %s ran: %s", class, running)
			}
		}
		dest := filepath.Join(t.TempDir(), "last.bundle")
		// The test's repository has only the source: the last save builds
		// on it (AppTown's builds on its branch, with the earlier save).
		tip, err := agent.SaveLast(ctx, id, source, dest)
		if err != nil {
			return err
		}
		quarantine, imported, cleanup, err := gitexec.Quarantine(ctx, repo, dest, worker.SaveRef, "change/7", source)
		if err != nil || imported != tip {
			t.Errorf("AppTown's import of the last save: %q %v", imported, err)
			return err
		}
		files, _ := gitexec.Output(ctx, quarantine, "ls-tree", "--name-only", tip)
		if !strings.Contains(files, "writer.log") || !strings.Contains(files, "dind.log") {
			t.Errorf("the leftovers are not in the last save: %s", files)
		}
		cleanup()
		last = tip
		return nil
	}
	if _, err := svc.End(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if last == "" || last == tip {
		t.Fatalf("no last save with the leftovers: %q", last)
	}
	if left := w.Docker("ps", "-a", "--filter", "label=apptown.owner="+s.ID, "--format", "{{.Names}}"); left != "" {
		t.Fatalf("left on the worker's Docker: %s", left)
	}
	if left := w.Docker("volume", "ls", "--filter", "label=apptown.console="+s.ID, "--format", "{{.Name}}"); left != "" {
		t.Fatalf("the chat's cache outlived it: %s", left)
	}
}

// resolvers are this machine's non-loopback entries of a resolv.conf field.
func resolvers(t *testing.T, field string) []string {
	body, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == field && !strings.HasPrefix(f[1], "127.") {
			out = append(out, f[1])
		}
	}
	if len(out) == 0 {
		t.Skip("this machine has no non-loopback resolver to look for")
	}
	return out
}

// loadNested copies an image from this machine's Docker into a private
// daemon, as name (an image named by digest loads untagged).
func loadNested(t *testing.T, daemon, image, name string) {
	t.Helper()
	save := exec.Command("docker", "save", image)
	load := exec.Command("docker", "exec", "-i", "-e", "DOCKER_HOST=tcp://localhost:2375", daemon, "docker", "load")
	pipe, err := save.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	load.Stdin = pipe
	if err := save.Start(); err != nil {
		t.Fatal(err)
	}
	out, err := load.CombinedOutput()
	if err != nil {
		t.Fatalf("loading %s into %s: %v\n%s", image, daemon, err, out)
	}
	if err := save.Wait(); err != nil {
		t.Fatal(err)
	}
	loaded := workertest.LoadedImage(string(out))
	if loaded == "" {
		t.Fatalf("loading %s into %s named no image:\n%s", image, daemon, out)
	}
	if out, err := exec.Command("docker", "exec", "-e", "DOCKER_HOST=tcp://localhost:2375", daemon, "docker", "tag", loaded, name).CombinedOutput(); err != nil {
		t.Fatalf("naming %s in %s: %v\n%s", image, daemon, err, out)
	}
}

// screenOf is what a chat's terminal shows within two seconds, for a
// failure's evidence.
func screenOf(t *testing.T, svc *console.Service, id string) string {
	term, err := svc.Attach(context.Background(), id, console.Size{Cols: 100, Rows: 30})
	if err != nil {
		return err.Error()
	}
	defer term.Close()
	var out strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := term.Read(buf)
			out.Write(buf[:n])
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		term.Close()
		<-done
	}
	return out.String()
}
