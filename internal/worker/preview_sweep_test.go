package worker_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"

	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// fileDocker is an in-process Docker whose state is files
// in dir: a preview is running while running-<name> exists, and every rm
// fails while rmfail does. It records every command. before, when set, runs
// first (a test holds a command there); reply, when set, may answer instead.
type fileDocker struct {
	dir    string
	mu     sync.Mutex
	calls  []string
	before func(args []string)
	reply  func(args []string) ([]byte, error, bool)
}

// errExit is how the docker CLI fails: its output says why.
var errExit = errors.New("exit status 1")

func sweepDocker(t *testing.T) (*fileDocker, string) {
	t.Helper()
	dir := t.TempDir()
	return &fileDocker{dir: dir}, dir
}

func (d *fileDocker) Run(ctx context.Context, host string, c docker.Command) ([]byte, error) {
	out, err := d.answer(c.Args)
	if c.Stdout != nil { // a command that asked for its standard output alone
		_, _ = c.Stdout.Write(out)
		return nil, err
	}
	return out, err
}

func (d *fileDocker) answer(args []string) ([]byte, error) {
	d.mu.Lock()
	d.calls = append(d.calls, strings.Join(args, " "))
	before, reply := d.before, d.reply
	d.mu.Unlock()
	if before != nil {
		before(args)
	}
	if out, ok := dockerfake.RoomyDisk(args); ok {
		return out, nil
	}
	if reply != nil {
		if out, err, ok := reply(args); ok {
			return out, err
		}
	}
	if len(args) == 0 {
		return nil, nil
	}
	name := args[len(args)-1]
	switch args[0] {
	case "inspect":
		if exists(filepath.Join(d.dir, "running-"+name)) {
			return []byte("true id-" + name + " inc\n"), nil
		}
		return []byte("Error: No such object: " + name + "\n"), errExit
	case "rm":
		if exists(filepath.Join(d.dir, "rmfail")) {
			return []byte("daemon busy\n"), errExit
		}
		_ = os.Remove(filepath.Join(d.dir, "running-"+name))
	}
	return nil, nil
}

// holdFirst holds the first command whose first word is verb until release
// is called; entered closes when that command arrives. release is safe to
// call more than once.
func (d *fileDocker) holdFirst(verb string) (entered <-chan struct{}, release func()) {
	in, out := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	var done sync.Once
	d.before = func(args []string) {
		// Not a sync.Once: a later command must pass while the first is held.
		if len(args) > 0 && args[0] == verb && first.CompareAndSwap(false, true) {
			close(in)
			<-out
		}
	}
	return in, func() { done.Do(func() { close(out) }) }
}

// commands are the commands run so far whose first word is verb; every
// command for "".
func (d *fileDocker) commands(verb string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, c := range d.calls {
		if verb == "" || c == verb || strings.HasPrefix(c, verb+" ") {
			out = append(out, c)
		}
	}
	return out
}

// unavailableDocker is a Docker that answers nothing.
func unavailableDocker() *fileDocker {
	return &fileDocker{reply: func([]string) ([]byte, error, bool) { return []byte("daemon unavailable\n"), errExit, true }}
}

// previewFolder seeds a preview's folder on a healthy worker: one whose
// fence exists (a worker with previews and no fence has lost their order).
func previewFolder(t *testing.T, state, name string) string {
	t.Helper()
	fence := filepath.Join(state, "previews", "fence.json")
	if _, err := os.Stat(fence); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(fence), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fence, []byte(`{"last":{}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(state, "previews", name)
	if err := os.MkdirAll(filepath.Join(dir, "tree"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec.json"), []byte(`{"name":"`+name+`","stackId":"pv1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

// bootedMark records that a preview's boot completed, as the worker does.
func bootedMark(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "booted"), []byte("a+b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A removal that fails says so and is tried again by the sweep until it
// succeeds. The minute's sweep also removes a preview whose boot never
// completed (no request holds it), and keeps a completed one; starting, the
// worker also removes the previews that are not running, and keeps running
// ones without the boot configurations the stop left.
func TestThePreviewSweepFinishesRemovalsAndInterruptedBoots(t *testing.T) {
	t.Parallel()
	binary, dk := sweepDocker(t)
	ca := workertest.NewCA(t)
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Previews: binary}
	hs := workertest.StartServer(t, ca, srv)
	previews := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}

	removed := previewFolder(t, srv.State, "apptown-preview-change-00000000-0001-7000-8000-000000000000")
	bootedMark(t, removed)
	_ = os.WriteFile(filepath.Join(dk, "running-apptown-preview-change-00000000-0001-7000-8000-000000000000"), nil, 0o600)
	_ = os.WriteFile(filepath.Join(dk, "rmfail"), nil, 0o600)
	if err := previews.Remove(context.Background(), "apptown-preview-change-00000000-0001-7000-8000-000000000000"); err == nil {
		t.Fatal("a removal that failed answered success")
	}
	srv.SweepPreviews(context.Background(), false)
	if !exists(removed) {
		t.Fatal("the sweep forgot a removal that failed again")
	}
	_ = os.Remove(filepath.Join(dk, "rmfail"))
	srv.SweepPreviews(context.Background(), false)
	if exists(removed) || exists(filepath.Join(dk, "running-apptown-preview-change-00000000-0001-7000-8000-000000000000")) {
		t.Fatal("the sweep did not finish a failed removal")
	}

	interrupted := previewFolder(t, srv.State, "apptown-preview-change-00000000-0002-7000-8000-000000000000")
	_ = os.WriteFile(filepath.Join(dk, "running-apptown-preview-change-00000000-0002-7000-8000-000000000000"), nil, 0o600)
	running := previewFolder(t, srv.State, "apptown-preview-change-00000000-0003-7000-8000-000000000000")
	bootedMark(t, running)
	_ = os.WriteFile(filepath.Join(dk, "running-apptown-preview-change-00000000-0003-7000-8000-000000000000"), nil, 0o600)
	stopped := previewFolder(t, srv.State, "apptown-preview-change-00000000-0005-7000-8000-000000000000")
	bootedMark(t, stopped)
	leftConfig := filepath.Join(running, "docker-config-left")
	if err := os.MkdirAll(leftConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	srv.SweepPreviews(context.Background(), false)
	if exists(interrupted) || exists(filepath.Join(dk, "running-apptown-preview-change-00000000-0002-7000-8000-000000000000")) {
		t.Fatal("the minute's sweep kept a preview whose boot never completed")
	}
	if !exists(running) || !exists(stopped) || !exists(leftConfig) {
		t.Fatal("the minute's sweep removed a completed preview, or a configuration that may be in use")
	}
	srv.SweepPreviews(context.Background(), true)
	if exists(stopped) {
		t.Fatal("starting, the worker kept a preview that is not running")
	}
	if !exists(running) || exists(leftConfig) {
		t.Fatal("starting, the worker removed a running preview, or kept a configuration its stop left")
	}
	st, err := previews.State(context.Background(), "apptown-preview-change-00000000-0003-7000-8000-000000000000")
	if err != nil || !st.Running {
		t.Fatalf("%+v %v", st, err)
	}
}

// A prepared value without the contract it was prepared for is refused
// before anything of the preview is touched.
func TestAWorkerRefusesAPreparedBootWithoutItsContract(t *testing.T) {
	t.Parallel()
	binary, dk := sweepDocker(t)
	ca := workertest.NewCA(t)
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Previews: binary}
	hs := workertest.StartServer(t, ca, srv)
	previews := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	kept := previewFolder(t, srv.State, "apptown-preview-change-00000000-0004-7000-8000-000000000000")
	_ = os.WriteFile(filepath.Join(dk, "running-apptown-preview-change-00000000-0004-7000-8000-000000000000"), nil, 0o600)
	_, err := previews.Boot(context.Background(), worker.PreviewSpec{Name: "apptown-preview-change-00000000-0004-7000-8000-000000000000", StackID: "pv4", Slug: "app", Incarnation: "a+b", ProbeSeconds: 10},
		docker.Prepared{Version: docker.PreparedVersion, BrowserEnv: "{}"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "contract") {
		t.Fatalf("a boot without its contract: %v", err)
	}
	if !exists(kept) || !exists(filepath.Join(dk, "running-apptown-preview-change-00000000-0004-7000-8000-000000000000")) {
		t.Fatal("a refused boot touched the running preview")
	}
}

// The worker lists its previews by their folders and app containers; a
// Docker that cannot list them is an error, never an empty list.
func TestAWorkerListsItsPreviews(t *testing.T) {
	t.Parallel()
	binary, _ := sweepDocker(t)
	ca := workertest.NewCA(t)
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Previews: binary}
	hs := workertest.StartServer(t, ca, srv)
	previews := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	previewFolder(t, srv.State, "apptown-preview-change-00000000-0002-7000-8000-000000000000")
	previewFolder(t, srv.State, "apptown-preview-run-00000000-0001-7000-8000-000000000000")
	names, err := previews.List(context.Background())
	if err != nil || strings.Join(names, ",") != "apptown-preview-change-00000000-0002-7000-8000-000000000000,apptown-preview-run-00000000-0001-7000-8000-000000000000" {
		t.Fatalf("listed %q %v", names, err)
	}

	srv2 := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Previews: unavailableDocker()}
	hs2 := workertest.StartServer(t, ca, srv2)
	if names, err := (worker.Previews{URL: hs2.URL, Client: workertest.Client(t, ca, workertest.Installation)}).List(context.Background()); err == nil {
		t.Fatalf("a Docker that cannot list answered %q", names)
	}
}

// Requests that change a preview are fenced by their token, an AppTown session
// number then a request number: one older than the last the worker acted
// on for that preview is refused; an AppTown that starts again, with a greater
// session, claims the worker, removing every preview, whatever numbers the
// old session reached, and every request of the old session is refused
// from then on.
func TestAnOlderRequestNeverUndoesALaterOne(t *testing.T) {
	t.Parallel()
	binary, dk := sweepDocker(t)
	ca := workertest.NewCA(t)
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Previews: binary}
	hs := workertest.StartServer(t, ca, srv)
	client := workertest.Client(t, ca, workertest.Installation)
	send := func(path, session string, n int64) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodDelete, hs.URL+path, nil)
		req.Header.Set(worker.PreviewSessionHeader, session)
		req.Header.Set(worker.PreviewSeqHeader, strconv.FormatInt(n, 10))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := send("/v1/previews/apptown-preview-change-00000000-0001-7000-8000-000000000000", "1", 1_000_000); code != http.StatusNoContent {
		t.Fatalf("a removal: %d", code)
	}
	if code := send("/v1/previews/apptown-preview-change-00000000-0001-7000-8000-000000000000", "1", 7); code != http.StatusConflict {
		t.Fatalf("an older removal after a later one: %d", code)
	}
	// The old session's last removal of this preview failed; its preview is
	// still there, far ahead in numbers.
	name := "apptown-preview-change-00000000-0002-7000-8000-000000000000"
	left := previewFolder(t, srv.State, name)
	bootedMark(t, left)
	_ = os.WriteFile(filepath.Join(dk, "running-"+name), nil, 0o600)
	_ = os.WriteFile(filepath.Join(dk, "rmfail"), nil, 0o600)
	if code := send("/v1/previews/"+name, "1", 9_000_000); code != http.StatusInternalServerError {
		t.Fatalf("fixture: a failed removal: %d", code)
	}
	_ = os.Remove(filepath.Join(dk, "rmfail"))
	// A new AppTown session, with small numbers, claims the worker.
	if code := send("/v1/previews", "2", 1); code != http.StatusNoContent || exists(left) {
		t.Fatalf("the new session's claim: %d, left behind: %v", code, exists(left))
	}
	if code := send("/v1/previews/"+name, "2", 2); code != http.StatusNoContent {
		t.Fatalf("the new session's request after its claim: %d", code)
	}
	if code := send("/v1/previews/"+name, "1", 10_000_000); code != http.StatusConflict {
		t.Fatalf("a request of the old session after the claim: %d", code)
	}
}

// A change's preview keeps the images its boot built when it is removed:
// a reopen builds from them, and AppTown removes them when their keep is
// over (DELETE .../images). A run's preview's images go with it: it is
// never booted again. Another preview's images stay.
func TestAChangesPreviewKeepsItsBuildImagesUntilAppTownRemovesThem(t *testing.T) {
	t.Parallel()
	binary, _ := sweepDocker(t)
	binary.reply = func(args []string) ([]byte, error, bool) {
		call := strings.Join(args, " ")
		if len(args) > 1 && args[0] == "image" && args[1] == "ls" {
			for _, name := range []string{"apptown-preview-change-00000000-0001-7000-8000-000000000000", "apptown-preview-run-00000000-0002-7000-8000-000000000000"} {
				if strings.Contains(call, "reference=apptown-preview-build-"+name+" ") {
					return []byte("apptown-preview-build-" + name + ":latest\napptown-preview-build-" + name + "-web:latest\n"), nil, true
				}
			}
		}
		return nil, nil, false
	}
	ca := workertest.NewCA(t)
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Previews: binary}
	hs := workertest.StartServer(t, ca, srv)
	previews := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	removed := func() string { return strings.Join(binary.commands("rmi"), "\n") + "\n" }
	for _, name := range []string{"apptown-preview-change-00000000-0001-7000-8000-000000000000", "apptown-preview-run-00000000-0002-7000-8000-000000000000"} {
		bootedMark(t, previewFolder(t, srv.State, name))
		if err := previews.Remove(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	if calls := removed(); strings.Contains(calls, "change-00000000-0001-7000-8000-000000000000") || !strings.Contains(calls, "rmi apptown-preview-build-apptown-preview-run-00000000-0002-7000-8000-000000000000:latest\n") ||
		!strings.Contains(calls, "rmi apptown-preview-build-apptown-preview-run-00000000-0002-7000-8000-000000000000-web:latest\n") {
		t.Fatalf("removing the previews: a change's images must stay and a run's go:\n%s", calls)
	}
	if err := previews.RemoveImages(context.Background(), "apptown-preview-change-00000000-0001-7000-8000-000000000000"); err != nil {
		t.Fatal(err)
	}
	calls := removed()
	for _, want := range []string{"rmi apptown-preview-build-apptown-preview-change-00000000-0001-7000-8000-000000000000:latest", "rmi apptown-preview-build-apptown-preview-change-00000000-0001-7000-8000-000000000000-web:latest"} {
		if !strings.Contains(calls, want+"\n") {
			t.Errorf("not removed: %s\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "rmi -f") || strings.Count(calls, "rmi ") != 4 {
		t.Errorf("removals beyond the previews' own images:\n%s", calls)
	}
}

// The worker keeps its build cache within AppTown's budget, as AppTown
// keeps its own: previews and release builds fill it, nothing else trims it.
func TestTheWorkerTrimsItsBuildCacheToTheBudget(t *testing.T) {
	t.Parallel()
	binary, _ := sweepDocker(t)
	(&worker.Server{State: t.TempDir(), Docker: binary}).TrimBuildCache(context.Background())
	if got := binary.commands("builder"); len(got) != 1 || got[0] != "builder prune -f --keep-storage "+docker.BuildCacheBudget {
		t.Fatalf("trimmed with %v", got)
	}
}
