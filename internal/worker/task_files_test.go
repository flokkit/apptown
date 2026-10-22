package worker_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execfile"

	"github.com/flokkit/apptown/internal/console/consoletest"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// mountsSeen is the worker's task driver, recording the mounts each task
// was created with.
type mountsSeen struct {
	*executionfake.Tasks
	mu     sync.Mutex
	mounts map[string][]string
}

func (m *mountsSeen) Ensure(ctx context.Context, spec execution.TaskSpec) (execution.TaskObservation, error) {
	m.mu.Lock()
	if m.mounts == nil {
		m.mounts = map[string][]string{}
	}
	m.mounts[spec.Key] = spec.Mounts
	m.mu.Unlock()
	return m.Tasks.Ensure(ctx, spec)
}

func (m *mountsSeen) of(key string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mounts[key]
}

var miseConfig = execution.File{Path: "/etc/mise/config.toml", Content: []byte("[settings]\nnode.mirror_url = \"https://mirror.invalid/\"\n"), Mode: 0o644}

func taskWithFiles(key string, files ...execution.File) execution.TaskSpec {
	return execution.TaskSpec{Key: key, Image: "alpine:3", Argv: []string{"true"},
		Admission: execution.Admission{Profile: "worker", Network: execution.NetworkDefault}, Files: files}
}

// readMount reads the host side of the mount that presents path read-only.
func readMount(t *testing.T, mounts []string, path string) (string, string) {
	t.Helper()
	for _, m := range mounts {
		if strings.HasSuffix(m, ":"+path+":ro") {
			src := strings.TrimSuffix(m, ":"+path+":ro")
			body, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			return src, string(body)
		}
	}
	t.Fatalf("nothing presents %s: %v", path, mounts)
	return "", ""
}

// A task on the worker is given its generated files read-only at their
// paths, from the task's own folder, never its tree; an ensure repeated
// after a lost reply finds the same files, one with other files is
// refused, and the files are kept through the worker's start-up sweeps
// until the task is released.
func TestAWorkerTaskKeepsItsFilesUntilItIsReleased(t *testing.T) {
	t.Parallel()
	ca := workertest.NewCA(t)
	tasks := &mountsSeen{Tasks: &executionfake.Tasks{}}
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Tasks: tasks, Sessions: consoletest.NewAgent()}
	hs := workertest.StartServer(t, ca, srv)
	driver := execution.WorkerTasks{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	ctx := context.Background()

	spec := taskWithFiles("script-7", miseConfig)
	if _, err := driver.Ensure(ctx, spec); err != nil {
		t.Fatal(err)
	}
	src, body := readMount(t, tasks.of(spec.Key), miseConfig.Path)
	if body != string(miseConfig.Content) || !strings.HasPrefix(src, filepath.Join(srv.State, "task-files")+string(filepath.Separator)) {
		t.Fatalf("the task's file %s holds %q", src, body)
	}
	if _, err := driver.Ensure(ctx, spec); err != nil {
		t.Fatalf("an ensure repeated after a lost reply: %v", err)
	}
	if again, _ := readMount(t, tasks.of(spec.Key), miseConfig.Path); again != src {
		t.Fatalf("an ensure repeated wrote other files: %s, then %s", src, again)
	}
	// The same request identity with other files (or another mode) at the
	// same paths is refused, and the files stay as they are.
	for _, changed := range []execution.File{
		{Path: miseConfig.Path, Content: []byte("[other]\n"), Mode: 0o644},
		{Path: miseConfig.Path, Content: miseConfig.Content, Mode: 0o600},
	} {
		other := taskWithFiles(spec.Key, changed)
		other.Request = spec.Digest()
		if _, err := driver.Ensure(ctx, other); !errors.Is(err, execution.ErrTaskConflict) {
			t.Fatalf("an ensure with other files: %v", err)
		}
	}
	if b, _ := os.ReadFile(src); string(b) != string(miseConfig.Content) {
		t.Fatalf("a task's file was rewritten: %q", b)
	}

	// The worker's start-up sweeps leave a task's files alone, whatever
	// state the task is in: created, running, or exited and not collected.
	sweep := func(state string) {
		t.Helper()
		srv.SweepChecks(ctx, unavailableDocker(), true)
		srv.SweepPreviews(ctx, true)
		if err := srv.ReapSessions(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(src); err != nil {
			t.Fatalf("a start-up sweep removed a %s task's files: %v", state, err)
		}
	}
	sweep("created")
	if err := driver.Start(ctx, execution.TaskRef{Key: spec.Key}); err != nil {
		t.Fatal(err)
	}
	sweep("running")
	tasks.Exit(spec.Key, 0)
	sweep("exited")

	if err := driver.Release(ctx, execution.TaskRef{Key: spec.Key}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(src)); !os.IsNotExist(err) {
		t.Fatalf("a released task's files are kept: %v", err)
	}
}

// A task's files are never in its tree: the commits collected from it hold
// what the task made, and none of the generated files.
func TestACollectedCommitHoldsNoGeneratedFiles(t *testing.T) {
	t.Parallel()
	ca := workertest.NewCA(t)
	tasks := &mountsSeen{Tasks: &executionfake.Tasks{}}
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Tasks: tasks}
	hs := workertest.StartServer(t, ca, srv)
	driver := execution.WorkerTasks{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	hub := sourceRepo(t)
	base, _ := gitexec.Output(t.Context(), hub, "rev-parse", "HEAD")
	spec := taskWithFiles("script-8", miseConfig)
	spec.Source = &execution.TaskSource{Repository: hub, Commit: base}
	if _, err := driver.Ensure(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	// The fake runs nothing; write what a task would have left behind.
	if err := os.WriteFile(filepath.Join(srv.State, "workspaces", spec.Key, "made.txt"), []byte("made by the task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(t.TempDir(), "result.bundle")
	tip, err := driver.Collect(context.Background(), spec.Key, base, "apptown: the task's work", result)
	if err != nil || tip == "" {
		t.Fatalf("collecting: %q %v", tip, err)
	}
	if out, err := gitexec.Run(t.Context(), hub, "fetch", "-q", result, "refs/heads/work:refs/test/result"); err != nil {
		t.Fatal(err, out)
	}
	files, err := gitexec.Output(t.Context(), hub, "ls-tree", "-r", "--name-only", "refs/test/result")
	if err != nil || strings.TrimSpace(files) != "made.txt" {
		t.Fatalf("the collected commit holds %q (%v)", files, err)
	}
}

// A check on the worker is given its generated files read-only at their
// paths, written in the check's own folder beside its tree, which goes with
// the check.
func TestAWorkerCheckIsGivenItsFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	binary := filepath.Join(dir, "docker")
	script := `#!/bin/sh
echo "$@" >> "` + calls + `"
case "$*" in inspect*OOMKilled*) echo false; exit 0;; esac
[ "$1" = run ] || exit 0
prev=""
for arg in "$@"; do
  if [ "$prev" = "-v" ]; then
    case "$arg" in *:/etc/mise/config.toml:ro) cat "${arg%%:*}";; esac
  fi
  prev="$arg"
done
exit 0
`
	if err := execfile.Write(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	checks, srv := checkWorker(t, binary)
	out := checks.Run(context.Background(), execution.Request{Workspace: t.TempDir(), Image: "alpine:3", Argv: []string{"true"},
		Network: "none", Timeout: time.Minute, Name: "files", Files: []execution.File{miseConfig}})
	if !out.Exited0() || !strings.Contains(string(out.Output), "node.mirror_url") {
		t.Fatalf("the check was not given its file: %+v %s", out, out.Output)
	}
	if entries, _ := os.ReadDir(filepath.Join(srv.State, "checks")); len(entries) != 0 {
		t.Fatalf("the check's folder outlived it: %v", entries)
	}
	bad := checks.Run(context.Background(), execution.Request{Workspace: t.TempDir(), Image: "alpine:3", Argv: []string{"true"},
		Network: "none", Timeout: time.Minute, Name: "files", Files: []execution.File{{Path: "/work/.npmrc", Content: []byte("x"), Mode: 0o644}}})
	if bad.Termination != execution.LaunchFailed {
		t.Fatalf("a file placed in the tree was accepted: %+v", bad)
	}
}
