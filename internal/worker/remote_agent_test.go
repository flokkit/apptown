package worker_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execfile"

	"github.com/flokkit/apptown/internal/gitexec/gittest"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/console/consoletest"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/localtls"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// remoteChats is AppTown's chat service running its chats on a worker:
// console.Service with a RemoteAgent, and behind the worker's real server
// the fake chat runtime, which writes the chat's record as pi does.
type remoteChats struct {
	service *console.Service
	agent   *worker.RemoteAgent
	runtime *consoletest.Agent
	client  worker.SessionClient
	state   string // the worker's state directory
	data    string // AppTown's data directory
	srv     interface{ Close() }
}

func startRemoteChats(t *testing.T, env console.Environment) *remoteChats {
	t.Helper()
	ca := workertest.NewCA(t)
	rt := consoletest.NewAgent()
	state, data := t.TempDir(), t.TempDir()
	srv := workertest.StartServer(t, ca, &worker.Server{State: state, Sessions: rt})
	client := worker.SessionClient{URL: srv.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	agent := &worker.RemoteAgent{Sessions: client, Image: "apptown-agent", Root: filepath.Join(data, "console"), CopyEvery: 50 * time.Millisecond}
	t.Cleanup(agent.Close)
	svc := console.New(console.Dependencies{Agent: agent, Environment: env,
		Config: console.Config{DataDir: data, Mirror: func(context.Context) (string, error) { return "https://mirror.example", nil }}})
	return &remoteChats{service: svc, agent: agent, runtime: rt, client: client, state: state, data: data, srv: srv}
}

func (c *remoteChats) open(t *testing.T) *console.Session {
	t.Helper()
	s, err := c.service.Open(context.Background(), console.OpenRequest{Scope: "change:1", Workspace: console.Workspace{Path: t.TempDir(), Writable: true},
		Steering: "be helpful", Actor: "alice", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	if !poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, cond) {
		t.Fatalf("never: %s", what)
	}
}

// A chat on a worker is used as a chat on AppTown is: its files and
// mounts reach the worker without host paths, a message reaches it and its
// reply comes back into AppTown's copy of the record, an interrupt and
// the terminal reach it, and ending it removes it on the worker for good.
func TestAChatRunsOnAWorkerThroughTheChatService(t *testing.T) {
	ctx := context.Background()
	cli := filepath.Join(t.TempDir(), "forge")
	if err := execfile.Write(cli, []byte("#!/bin/sh\necho forge\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := consoletest.Static{Provision: console.Provision{Model: "m", Env: []string{"APPTOWN_URL=https://forge.example"},
		Mounts: []string{cli + ":/opt/apptown/bin/apptown:ro"},
		Files:  map[string]console.Staged{"pi-home/.pi/agent/models.json": {Data: []byte(`{"providers":{}}`)}}}}
	c := startRemoteChats(t, env)
	s := c.open(t)

	if err := c.service.Send(ctx, s.ID, "hello there"); err != nil {
		t.Fatal(err)
	}
	launch := c.runtime.Started(s.ID)
	if !strings.HasPrefix(launch.Dir, c.state) || !strings.HasPrefix(launch.Workspace.Path, c.state) {
		t.Fatalf("the worker's launch names paths outside its state: %+v", launch)
	}
	if len(launch.Mounts) != 1 || !strings.HasPrefix(launch.Mounts[0], c.state) || !strings.HasSuffix(launch.Mounts[0], ":/opt/apptown/bin/apptown:ro") {
		t.Fatalf("mounts on the worker: %v", launch.Mounts)
	}
	mounted := strings.SplitN(launch.Mounts[0], ":", 2)[0]
	if got, err := os.ReadFile(mounted); err != nil || string(got) != "#!/bin/sh\necho forge\n" {
		t.Fatalf("the agents' CLI binary on the worker: %q %v", got, err)
	}
	if fi, err := os.Stat(mounted); err != nil || fi.Mode()&0o111 == 0 {
		t.Fatalf("the agents' CLI binary is not executable on the worker: %v", err)
	}
	for _, name := range []string{"pi-home/.pi/agent/models.json", console.SteeringFile} {
		if _, err := os.Stat(filepath.Join(launch.Dir, name)); err != nil {
			t.Fatalf("%s did not reach the worker: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(launch.Dir, console.MetaFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the chat's record, with its credential, reached the worker: %v", err)
	}

	eventually(t, "the message in AppTown's copy", func() bool {
		for _, m := range s.Messages() {
			if strings.Contains(m.Text, "hello there") {
				return true
			}
		}
		return false
	})
	c.runtime.Reply(s.ID, "hi, how can I help")
	eventually(t, "the reply in AppTown's copy", func() bool {
		msgs := s.Messages()
		return len(msgs) > 0 && strings.Contains(msgs[len(msgs)-1].Text, "how can I help")
	})
	// The copy's time is written after its files, so it can trail the reply.
	eventually(t, "a time for AppTown's copy of the record", func() bool { return !recordAsOf(s.Dir).IsZero() })

	if err := c.service.Interrupt(ctx, s.ID); err != nil || !c.runtime.Interrupted(s.ID) {
		t.Fatalf("interrupt: %v", err)
	}

	c.runtime.Screen(s.ID, []byte("pi> "))
	term, err := c.service.Attach(ctx, s.ID, console.Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := readWithin(term, 4); err != nil || string(got) != "pi> " {
		t.Fatalf("the screen through the terminal: %q %v", got, err)
	}
	if _, err := term.Write([]byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	if err := term.Resize(console.Size{Cols: 100, Rows: 30}); err != nil {
		t.Fatal(err)
	}
	term.Close()
	second, err := c.service.Attach(ctx, s.ID, console.Size{})
	if err != nil {
		t.Fatal(err)
	}
	if screen, err := readWithin(second, 7); err != nil || string(screen) != "pi> ls\r" {
		t.Fatalf("keystrokes did not reach the chat: %q %v", screen, err)
	}
	second.Close()

	if _, err := c.service.End(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	st, err := c.client.Status(ctx, s.ID)
	if err != nil || !st.Removed || !st.Gone() {
		t.Fatalf("the ended chat on the worker: %+v %v", st, err)
	}
	if _, err := c.client.Start(ctx, s.ID); !errors.Is(err, worker.ErrSessionRemoved) {
		t.Fatalf("an ended chat started again: %v", err)
	}
}

// AppTown's Docker Hub mirror goes to the worker as data with a chat that
// has private Docker, and reaches the worker's runtime; a chat without
// private Docker is sent none.
func TestAWorkerChatGetsAppTownsMirror(t *testing.T) {
	ctx := context.Background()
	c := startRemoteChats(t, consoletest.Static{})
	for _, docker := range []bool{true, false} {
		s, err := c.service.Open(ctx, console.OpenRequest{Scope: "change:1", Workspace: console.Workspace{Path: t.TempDir(), Writable: true},
			Actor: "alice", Model: "m", Docker: docker})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.service.Warm(ctx, s.ID); err != nil {
			t.Fatal(err)
		}
		want := map[bool]string{true: "https://mirror.example", false: ""}[docker]
		if got := c.runtime.Started(s.ID).Mirror; got != want {
			t.Fatalf("docker %t: the worker's launch has the mirror %q, want %q", docker, got, want)
		}
	}
}

// An unreachable worker makes a chat unavailable, not dead; a message the
// AppTown cannot confirm is said to be not confirmed; and a chat whose worker
// cannot be reached does not end.
func TestAChatWhoseWorkerCannotBeReachedIsUnavailable(t *testing.T) {
	ctx := context.Background()
	c := startRemoteChats(t, consoletest.Static{})
	s := c.open(t)
	if err := c.service.Warm(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	c.srv.Close()
	if st := c.service.State(ctx, s); st != console.Unavailable {
		t.Fatalf("state with the worker unreachable: %s", st)
	}
	if err := c.agent.Deliver(ctx, console.Launch{ID: s.ID}, "are you there"); !errors.Is(err, worker.ErrNotConfirmed) {
		t.Fatalf("an unconfirmed message: %v", err)
	}
	if _, err := c.service.End(ctx, s.ID); err == nil {
		t.Fatal("a chat ended while its worker could not be reached")
	}
	if _, err := c.service.Get(ctx, s.ID); err != nil {
		t.Fatalf("the chat is gone from AppTown: %v", err)
	}
}

// A worker chat is rebuilt only while it is stopped, whatever its record
// says: a running chat is never stale to AppTown, and becomes so once
// stopped.
func TestAWorkerChatIsRebuiltOnlyWhileStopped(t *testing.T) {
	ctx := context.Background()
	c := startRemoteChats(t, consoletest.Static{})
	s := c.open(t)
	if err := c.service.Warm(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	launch := console.Launch{ID: s.ID, Dir: s.Dir, AgentSession: console.AgentSession}
	c.runtime.MarkStale(s.ID)
	for _, turn := range []func(string){c.runtime.Working, func(id string) { c.runtime.Reply(id, "done") }} {
		turn(s.ID)
		if stale, err := c.agent.Stale(ctx, launch); err != nil || stale {
			t.Fatalf("a running chat reported stale: %v %v", stale, err)
		}
	}
	if err := c.service.Stop(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	c.runtime.MarkStale(s.ID)
	if stale, err := c.agent.Stale(ctx, launch); err != nil || !stale {
		t.Fatalf("a stopped stale chat: %v %v", stale, err)
	}
}

// The chat's files are replaced as a whole and only when they change;
// the worker's own names cannot be written, and no entry is written through
// a symbolic link.
func TestAChatsFilesStayInsideItsDirectory(t *testing.T) {
	ctx := context.Background()
	c := startRemoteChats(t, consoletest.Static{})
	s := c.open(t)
	if err := c.service.Warm(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	dir := c.runtime.Started(s.ID).Dir
	outside := t.TempDir()
	for name, body := range map[string][]byte{
		"a worker name":        tarOf(t, entry{name: "sessions/x.jsonl", body: "{}"}),
		"an escaping name":     tarOf(t, entry{name: "../escape", body: "x"}),
		"through a link":       tarOf(t, entry{name: "pi-home", link: outside}, entry{name: "pi-home/planted", body: "x"}),
		"the worker's scratch": tarOf(t, entry{name: "scratch/notes", body: "x"}),
	} {
		if _, err := c.client.PutFiles(ctx, s.ID, bytes.NewReader(body)); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("a file was written outside the chat's directory: %v", entries)
	}
	if _, err := c.client.PutFiles(ctx, s.ID, bytes.NewReader(tarOf(t, entry{name: "pi-home/a", body: "1"}, entry{name: "pi-home/b", body: "2"}))); err != nil {
		t.Fatal(err)
	}
	if _, err := c.client.PutFiles(ctx, s.ID, bytes.NewReader(tarOf(t, entry{name: "pi-home/a", body: "1"}))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pi-home", "b")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a file AppTown no longer sends stayed: %v", err)
	}
}

// A blob is kept only under the digest of what was sent, holds one entry
// named "content", and carries no symbolic links.
func TestBlobsAreNamedByTheirContent(t *testing.T) {
	ctx := context.Background()
	c := startRemoteChats(t, consoletest.Static{})
	good := tarOf(t, entry{name: "content", body: "binary"})
	for name, body := range map[string][]byte{
		"another name": tarOf(t, entry{name: "other", body: "x"}),
		"a link":       tarOf(t, entry{name: "content", link: "/etc/passwd"}),
	} {
		if err := c.client.PutBlob(ctx, digest(body), bytes.NewReader(body)); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	if err := c.client.PutBlob(ctx, digest(good), bytes.NewReader(tarOf(t, entry{name: "content", body: "other"}))); err == nil {
		t.Fatal("a blob whose digest is not its name was accepted")
	}
	if has, err := c.client.HasBlob(ctx, digest(good)); err != nil || has {
		t.Fatalf("a refused blob is kept: %v %v", has, err)
	}
	if err := c.client.PutBlob(ctx, digest(good), bytes.NewReader(good)); err != nil {
		t.Fatal(err)
	}
	if has, err := c.client.HasBlob(ctx, digest(good)); err != nil || !has {
		t.Fatalf("a sent blob: %v %v", has, err)
	}
	if got, err := os.ReadFile(filepath.Join(c.state, "blobs", digest(good), "content")); err != nil || string(got) != "binary" {
		t.Fatalf("the blob's content: %q %v", got, err)
	}
}

// chatSourceRepo is a repository with a branch change/7 holding one commit.
func chatSourceRepo(t *testing.T) (repo, commit string) {
	t.Helper()
	repo = t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := gittest.Command(args...)
		cmd.Dir = repo
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "change/7")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("change 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "one")
	return repo, git("rev-parse", "HEAD")
}

// The chat's work tree is made once from a bundle of its change's branch,
// sent by AppTown before the chat first starts: it holds that commit on
// that branch, with no remote, and a start later does not send it again.
func TestAChatsWorkTreeIsMadeFromItsSource(t *testing.T) {
	ctx := context.Background()
	repo, commit := chatSourceRepo(t)
	c := startRemoteChats(t, consoletest.Static{})
	sent := 0
	c.agent.Source = func(ctx context.Context, in console.Launch, dest string) (string, string, error) {
		sent++
		return "change/7", commit, gitexec.BundleCommit(ctx, repo, commit, "change/7", dest)
	}
	s := c.open(t)
	for range 2 {
		if err := c.service.Warm(ctx, s.ID); err != nil {
			t.Fatal(err)
		}
	}
	work := c.runtime.Started(s.ID).Workspace.Path
	if got, err := os.ReadFile(filepath.Join(work, "README")); err != nil || string(got) != "change 7\n" {
		t.Fatalf("the work tree: %q %v", got, err)
	}
	head, _ := gittest.Command("-C", work, "rev-parse", "--abbrev-ref", "HEAD").Output()
	remotes, _ := gittest.Command("-C", work, "remote").Output()
	if strings.TrimSpace(string(head)) != "change/7" || strings.TrimSpace(string(remotes)) != "" {
		t.Fatalf("branch %q, remotes %q", head, remotes)
	}
	if sent != 1 {
		t.Fatalf("the source was sent %d times", sent)
	}
	// The chat can commit without setting an identity first, as a step's
	// agent can: no global configuration supplies one here.
	commit2 := gittest.Command("-C", work, "commit", "-q", "--allow-empty", "-m", "the chat's first commit")
	commit2.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	if out, err := commit2.CombinedOutput(); err != nil {
		t.Fatalf("a chat's commit needs no identity of its own: %v\n%s", err, out)
	}
	if author, _ := gittest.Command("-C", work, "log", "-1", "--format=%an <%ae>").Output(); strings.TrimSpace(string(author)) != "apptown <apptown@localhost>" {
		t.Fatalf("the chat's commit identity: %q", author)
	}
	var other bytes.Buffer
	if _, err := c.client.PutSource(ctx, s.ID, "change/7", strings.Repeat("a", 40), &other); err == nil {
		t.Fatal("a second source replaced the chat's work tree")
	}
}

// A save is the chat's commits since a base, bundled by the chat's own git
// and naming its tip; with nothing new it is nothing. The bundle passes the
// AppTown's checked import, which is what AppTown does with it.
func TestASaveIsTheChatsCommitsSinceItsBase(t *testing.T) {
	ctx := context.Background()
	repo, commit := chatSourceRepo(t)
	c := startRemoteChats(t, consoletest.Static{})
	c.agent.Source = func(ctx context.Context, in console.Launch, dest string) (string, string, error) {
		return "change/7", commit, gitexec.BundleCommit(ctx, repo, commit, "change/7", dest)
	}
	s := c.open(t)
	if err := c.service.Warm(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "save.bundle")
	if tip, err := c.agent.Save(ctx, s.ID, commit, dest); err != nil || tip != "" {
		t.Fatalf("nothing new: %q %v", tip, err)
	}
	work := c.runtime.Started(s.ID).Workspace.Path
	if err := os.WriteFile(filepath.Join(work, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "new.txt"}, {"-c", "user.name=c", "-c", "user.email=c@x", "commit", "-qm", "chat work"}} {
		if out, err := gittest.Command(append([]string{"-C", work}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	want, _ := gittest.Command("-C", work, "rev-parse", "HEAD").Output()
	tip, err := c.agent.Save(ctx, s.ID, commit, dest)
	if err != nil || tip != strings.TrimSpace(string(want)) {
		t.Fatalf("a save: %q %v", tip, err)
	}
	_, imported, cleanup, err := gitexec.Quarantine(ctx, repo, dest, worker.SaveRef, "change/7", commit)
	if err != nil || imported != tip {
		t.Fatalf("AppTown's import of the save: %q %v", imported, err)
	}
	cleanup()
	if _, err := c.agent.Save(ctx, s.ID, "not-a-commit", dest); err == nil {
		t.Fatal("a save from a base that is not a commit")
	}
}

// AppTown's copy of the record triggers a save when it shows a turn
// handed back that it had not shown before, once per turn.
func TestAHandedBackTurnTriggersASaveOnce(t *testing.T) {
	ctx := context.Background()
	c := startRemoteChats(t, consoletest.Static{})
	var mu sync.Mutex
	calls := 0
	c.agent.HandedBack = func(string) { mu.Lock(); calls++; mu.Unlock() }
	count := func() int { mu.Lock(); defer mu.Unlock(); return calls }
	// Each copy pass stamps the record's time from this clock, an hour on
	// from the last: the stamp says how many passes have completed.
	var at atomic.Int64
	at.Store(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).Unix())
	c.agent.Now = func() time.Time { return time.Unix(at.Add(3600), 0) }
	s := c.open(t)
	if err := c.service.Send(ctx, s.ID, "do it"); err != nil {
		t.Fatal(err)
	}
	c.runtime.Reply(s.ID, "done")
	eventually(t, "a save for the handed-back turn", func() bool { return count() == 1 })
	// Two more passes copy the same turn (the second's copy completes only
	// after the first decided): neither saves it again.
	saved := worker.RecordAsOf(filepath.Join(c.agent.Root, s.ID))
	eventually(t, "two more copies of the record", func() bool {
		return !worker.RecordAsOf(filepath.Join(c.agent.Root, s.ID)).Before(saved.Add(2 * time.Hour))
	})
	if n := count(); n != 1 {
		t.Fatalf("one handed-back turn triggered %d saves", n)
	}
	if err := c.service.Send(ctx, s.ID, "and again"); err != nil {
		t.Fatal(err)
	}
	c.runtime.Reply(s.ID, "done again")
	eventually(t, "a save for the next turn", func() bool { return count() == 2 })
}

type entry struct{ name, body, link string }

func tarOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(e.body))}
		if e.link != "" {
			hdr = &tar.Header{Name: e.name, Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: e.link}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.link == "" {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func listenLocal(t *testing.T) (net.Listener, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return l, "http://" + l.Addr().String()
}

type images map[string]bool

func (i images) Has(_ context.Context, image string) (bool, error) { return i[image], nil }

// A chat whose image the worker does not have is refused when it starts,
// saying so, and nothing of it runs.
func TestAChatIsRefusedAnImageTheWorkerDoesNotHave(t *testing.T) {
	ctx := context.Background()
	ca := workertest.NewCA(t)
	rt := consoletest.NewAgent()
	srv := workertest.StartServer(t, ca, &worker.Server{State: t.TempDir(), Sessions: rt, Images: images{"apptown-agent:aaaaaaaaaaaa": true}})
	client := worker.SessionClient{URL: srv.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	if _, err := client.Put(ctx, "c1", worker.SessionSpec{Image: "apptown-agent:bbbbbbbbbbbb", AgentSession: "console"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Start(ctx, "c1"); err == nil || !strings.Contains(err.Error(), "does not have the image apptown-agent:bbbbbbbbbbbb") {
		t.Fatalf("a missing image: %v", err)
	}
	if rt.Exists("c1") {
		t.Fatal("a chat without its image was started")
	}
	if _, err := client.Put(ctx, "c1", worker.SessionSpec{Image: "apptown-agent:aaaaaaaaaaaa", AgentSession: "console"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Start(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
}

// The agent image is named by its build context's content: the same
// context names the same image, and any change to it names another.
func TestTheAgentImageIsNamedByItsBuildContext(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := worker.AgentImage(dir)
	if err != nil || !strings.HasPrefix(first, "apptown-agent:") || len(first) != len("apptown-agent:")+12 {
		t.Fatalf("name: %q %v", first, err)
	}
	again, _ := worker.AgentImage(dir)
	if again != first {
		t.Fatalf("the same context named %s then %s", first, again)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nLABEL v=2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if moved, _ := worker.AgentImage(dir); moved == first {
		t.Fatal("a changed context kept the image's name")
	}
	if _, err := worker.AgentImage(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing context named an image")
	}
}

// The worker says which addresses it can reach: any answer counts, no
// answer does not, and only http and https addresses are tried.
func TestTheWorkerSaysWhichRegistriesItCanReach(t *testing.T) {
	ctx := context.Background()
	c := startRemoteChats(t, consoletest.Static{})
	listener, up := listenLocal(t)
	registry := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })}
	go registry.Serve(listener)
	t.Cleanup(func() { registry.Close() })
	closed, down := listenLocal(t)
	closed.Close()
	got, err := c.client.Reach(ctx, []string{up, down, "file:///etc/passwd"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !got[0].Reachable || got[1].Reachable || got[1].Error == "" || got[2].Reachable {
		t.Fatalf("reach: %+v", got)
	}
}

// A registry behind a company's TLS-inspecting proxy answers with a
// certificate its CA signed: the worker's reachability check trusts the
// deployments bundle it was started with, so the registry counts as
// reachable, and without that bundle it does not.
//
// Not parallel: it installs the process's trust.
func TestTheWorkerReachesARegistryItsDeploymentsBundleTrusts(t *testing.T) {
	ctx := context.Background()
	c := startRemoteChats(t, consoletest.Static{})
	ca, err := localtls.Load(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	registry := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })}
	go registry.Serve(tls.NewListener(listener, ca.TLSConfig()))
	t.Cleanup(func() { registry.Close() })
	url := "https://localhost:" + strings.TrimPrefix(listener.Addr().String(), "127.0.0.1:") + "/v2/"

	if got, err := c.client.Reach(ctx, []string{url}); err != nil || len(got) != 1 || got[0].Reachable {
		t.Fatalf("without the bundle the registry's certificate was trusted: %+v %v", got, err)
	}
	b, err := trust.Parse(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Certificate().Raw}))
	if err != nil {
		t.Fatal(err)
	}
	set, err := trust.New(t.TempDir(), map[trust.Use]trust.Bundle{trust.Deployments: b}, trust.Bundle{})
	if err != nil {
		t.Fatal(err)
	}
	trust.Install(set)
	t.Cleanup(func() { trust.Install(nil) })
	if got, err := c.client.Reach(ctx, []string{url}); err != nil || len(got) != 1 || !got[0].Reachable {
		t.Fatalf("with the deployments bundle: %+v %v", got, err)
	}
}

// readWithin reads n bytes from a terminal, or fails after five seconds
// (closing it) rather than waiting for bytes that will not come.
func readWithin(term console.Terminal, n int) ([]byte, error) {
	buf := make([]byte, n)
	done := make(chan error, 1)
	go func() { _, err := io.ReadFull(term, buf); done <- err }()
	select {
	case err := <-done:
		return buf, err
	case <-time.After(5 * time.Second):
		term.Close()
		<-done
		return buf, errors.New("nothing to read within five seconds")
	}
}

// recordAsOf reads the time of AppTown's last good copy of a chat's
// record from its session directory; zero when there is none.
func recordAsOf(dir string) time.Time {
	raw, err := os.ReadFile(filepath.Join(dir, worker.RecordAsOfFile))
	if err != nil {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, strings.TrimSpace(string(raw)))
	return t
}

// Once AppTown has copied a chat's record, the copy's time never reads as
// "never copied" while later copies are written: a page listing the chat
// between a copy's truncation and its write would otherwise show the chat
// as never copied.
func TestTheRecordsCopyTimeNeverReadsAsNeverCopiedOnceItHasBeen(t *testing.T) {
	c := startRemoteChats(t, consoletest.Static{})
	c.agent.CopyEvery = time.Millisecond
	s := c.open(t)
	dir := filepath.Join(c.agent.Root, s.ID)
	eventually(t, "a first copy of the record", func() bool { return !worker.RecordAsOf(dir).IsZero() })
	reads, start := 0, time.Now()
	for time.Since(start) < 500*time.Millisecond {
		if worker.RecordAsOf(dir).IsZero() {
			t.Fatalf("read %d: the record's copy time read as never copied while copies were being written", reads)
		}
		reads++
	}
}
