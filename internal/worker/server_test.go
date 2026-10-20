package worker_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/diskspace"
	"flokkit.io/forge/internal/execfile"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/execution/executionfake"
	"flokkit.io/forge/internal/execution/executiontest"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

func fakeSpec(key, output string, exit int, block bool) execution.TaskSpec {
	env := []string{"FAKE_OUTPUT=" + output, "FAKE_EXIT=" + strconv.Itoa(exit)}
	if block {
		env = append(env, "FAKE_BLOCK=1")
	}
	return execution.TaskSpec{Key: key, Image: "fake", Argv: []string{"run"}, Env: env,
		Admission: execution.Admission{Profile: "worker", Network: execution.NetworkNone, Expires: time.Now().Add(time.Hour)}}
}

// The worker path keeps the task contract: a fake behind a real worker
// server and client passes the lifecycle group. This establishes the wire
// protocol, not Docker support; the integration test runs the same suite
// against Docker.
func TestTheWorkerKeepsTheTaskContract(t *testing.T) {
	ca := workertest.NewCA(t)
	srv := workertest.Start(t, ca, t.TempDir(), &executionfake.Tasks{})
	driver := execution.WorkerTasks{URL: srv.URL, Client: workertest.Client(t, ca, "inst-1")}
	executiontest.Run(t, executiontest.Harness{Driver: driver, Spec: fakeSpec, Key: executiontest.UniqueKeys("worker")})
}

// A worker serves only the installation it is registered to: another
// installation's certificate is refused before anything is read, and a
// client without a certificate cannot connect.
func TestAWorkerServesOnlyItsInstallation(t *testing.T) {
	ca := workertest.NewCA(t)
	tasks := &executionfake.Tasks{}
	srv := workertest.Start(t, ca, t.TempDir(), tasks)
	other := execution.WorkerTasks{URL: srv.URL, Client: workertest.Client(t, ca, "inst-2")}
	if _, err := other.Ensure(context.Background(), fakeSpec("k-other", "", 0, false)); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("another installation's request: %v", err)
	}
	if obs, _ := tasks.Observe(context.Background(), execution.TaskRef{Key: "k-other"}); obs.State != execution.TaskAbsent {
		t.Fatalf("another installation created a task: %+v", obs)
	}
	anonymous := execution.WorkerTasks{URL: srv.URL, Client: workertest.Client(t, ca, "")}
	if _, err := anonymous.Observe(context.Background(), execution.TaskRef{Key: "k"}); !errors.Is(err, execution.ErrTaskUnobservable) {
		t.Fatalf("a client without a certificate: %v", err)
	}
}

// A restarted worker answers from its records and its backend: the same key
// finds the same task, and the key cannot be reused for a different request.
func TestARestartedWorkerFindsItsTasks(t *testing.T) {
	ca := workertest.NewCA(t)
	state, tasks := t.TempDir(), &executionfake.Tasks{}
	first := workertest.Start(t, ca, state, tasks)
	client := workertest.Client(t, ca, "inst-1")
	spec := fakeSpec("k-restart", "done", 0, true)
	created, err := (execution.WorkerTasks{URL: first.URL, Client: client}).Ensure(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := (execution.WorkerTasks{URL: first.URL, Client: client}).Start(context.Background(), execution.TaskRef{Key: spec.Key, ProviderID: created.ID}); err != nil {
		t.Fatal(err)
	}
	first.Close()

	second := execution.WorkerTasks{URL: workertest.Start(t, ca, state, tasks).URL, Client: client}
	running, err := second.Observe(context.Background(), execution.TaskRef{Key: spec.Key})
	if err != nil || running.ID != created.ID || running.State != execution.TaskRunning {
		t.Fatalf("after the restart: %+v %v", running, err)
	}
	again, err := second.Ensure(context.Background(), spec)
	if err != nil || again.ID != created.ID {
		t.Fatalf("a retried ensure after the restart made %+v: %v", again, err)
	}
	if tasks.Starts(spec.Key) != 1 {
		t.Fatalf("the task started %d times", tasks.Starts(spec.Key))
	}
	changed := spec
	changed.Argv = []string{"something", "else"}
	if _, err := second.Ensure(context.Background(), changed); err == nil || !strings.Contains(err.Error(), "different request") {
		t.Fatalf("a different request under the same key: %v", err)
	}
}

// A task's source reaches the worker only as a bundle, and its work comes
// back only as a bundle, which the forge imports into quarantine. A result
// larger than the forge accepts is refused.
func TestSourceAndResultsTravelAsBundles(t *testing.T) {
	ca := workertest.NewCA(t)
	state := t.TempDir()
	srv := workertest.Start(t, ca, state, &executionfake.Tasks{})
	driver := execution.WorkerTasks{URL: srv.URL, Client: workertest.Client(t, ca, "inst-1")}
	hub := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-q", "--allow-empty", "-m", "base"}} {
		if out, err := gitexec.Run(t.Context(), hub, args...); err != nil {
			t.Fatal(err, out)
		}
	}
	base, _ := gitexec.Output(t.Context(), hub, "rev-parse", "HEAD")
	spec := fakeSpec("k-bundle", "", 0, false)
	spec.Source = &execution.TaskSource{Repository: hub, Commit: base}
	if _, err := driver.Ensure(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	// The fake runs nothing; write what a task would have left behind.
	if err := os.WriteFile(filepath.Join(state, "workspaces", spec.Key, "made.txt"), []byte("made by the task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(t.TempDir(), "result.bundle")
	tip, err := driver.Collect(context.Background(), spec.Key, base, "apptown: the task's work", result)
	if err != nil || tip == "" {
		t.Fatalf("collecting: %q %v", tip, err)
	}
	dir, imported, cleanup, err := gitexec.Quarantine(context.Background(), hub, result, "work", "apptown/change-1", base)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if imported != tip {
		t.Fatalf("imported %s, the worker reported %s", imported, tip)
	}
	if got, _ := gitexec.Output(t.Context(), dir, "show", tip+":made.txt"); got != "made by the task" {
		t.Fatalf("the task's work: %q", got)
	}
	small := driver
	small.MaxResultBytes = 16
	if _, err := small.Collect(context.Background(), spec.Key, base, "apptown: again", result); !errors.Is(err, gitexec.ErrBundleRefused) {
		t.Fatalf("an oversized result: %v", err)
	}
	if err := driver.Release(context.Background(), execution.TaskRef{Key: spec.Key}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "workspaces", spec.Key)); !os.IsNotExist(err) {
		t.Fatalf("the released task's workspace is still there: %v", err)
	}
}

// A worker shares no files with the forge: a request naming a host path,
// a mount or a host route is refused before anything is sent.
func TestAWorkerRefusesHostPaths(t *testing.T) {
	driver := execution.WorkerTasks{URL: "https://127.0.0.1:1"}
	for _, change := range []func(*execution.TaskSpec){
		func(s *execution.TaskSpec) { s.Workspace = "/srv/forge/workspaces/change-1" },
		func(s *execution.TaskSpec) { s.Mounts = []string{"/var/run/docker.sock:/var/run/docker.sock"} },
		func(s *execution.TaskSpec) { s.ExtraHosts = []string{"host.docker.internal:host-gateway"} },
	} {
		spec := fakeSpec("k-host", "", 0, false)
		change(&spec)
		if _, err := driver.Ensure(context.Background(), spec); !errors.Is(err, execution.ErrUnsupported) {
			t.Fatalf("%+v: %v", spec, err)
		}
	}
}

// A task can write anything under /work, including a .git directory with
// configuration and hooks. The worker's Git directory is kept apart, so none
// of it runs or is committed when the worker collects the task's work.
func TestATaskCannotConfigureTheWorkersGit(t *testing.T) {
	ca := workertest.NewCA(t)
	state := t.TempDir()
	srv := workertest.Start(t, ca, state, &executionfake.Tasks{})
	driver := execution.WorkerTasks{URL: srv.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	hub := sourceRepo(t)
	base, _ := gitexec.Output(t.Context(), hub, "rev-parse", "HEAD")
	spec := fakeSpec("k-hostile", "", 0, false)
	spec.Source = &execution.TaskSource{Repository: hub, Commit: base}
	if _, err := driver.Ensure(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(state, "workspaces", spec.Key)
	if _, err := os.Stat(filepath.Join(work, ".git")); !os.IsNotExist(err) {
		t.Fatalf("the task's /work holds a .git: %v", err)
	}
	ran := filepath.Join(t.TempDir(), "ran")
	for path, body := range map[string]string{
		".git/config":           "[core]\n\tfsmonitor = touch " + ran + "\n\thooksPath = .git/hooks\n",
		".git/hooks/pre-commit": "#!/bin/sh\ntouch " + ran + "\n",
		"made.txt":              "the task's work\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(work, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := execfile.Write(filepath.Join(work, path), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	result := filepath.Join(t.TempDir(), "result.bundle")
	tip, err := driver.Collect(context.Background(), spec.Key, base, "apptown: the task's work", result)
	if err != nil || tip == "" {
		t.Fatalf("collecting: %q %v", tip, err)
	}
	if _, err := os.Stat(ran); !os.IsNotExist(err) {
		t.Fatal("the task's git configuration ran on the worker")
	}
	dir, imported, cleanup, err := gitexec.Quarantine(context.Background(), hub, result, "work", "work", base)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if files, _ := gitexec.Output(t.Context(), dir, "ls-tree", "-r", "--name-only", imported); files != "made.txt" {
		t.Fatalf("the task's commit holds %q", files)
	}
}

// A workspace is uploaded once per key: the same commit again is accepted,
// another commit under the key is refused.
func TestAWorkspaceKeepsTheCommitItWasUploadedWith(t *testing.T) {
	ca := workertest.NewCA(t)
	srv := workertest.Start(t, ca, t.TempDir(), &executionfake.Tasks{})
	driver := execution.WorkerTasks{URL: srv.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	hub := sourceRepo(t)
	first, _ := gitexec.Output(t.Context(), hub, "rev-parse", "HEAD")
	spec := fakeSpec("k-source", "", 0, false)
	spec.Source = &execution.TaskSource{Repository: hub, Commit: first}
	for range 2 {
		if _, err := driver.Ensure(context.Background(), spec); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := gitexec.Run(t.Context(), hub, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-q", "--allow-empty", "-m", "later"); err != nil {
		t.Fatal(err, out)
	}
	later, _ := gitexec.Output(t.Context(), hub, "rev-parse", "HEAD")
	spec.Source = &execution.TaskSource{Repository: hub, Commit: later}
	if _, err := driver.Ensure(context.Background(), spec); !errors.Is(err, execution.ErrTaskConflict) {
		t.Fatalf("another commit under the same key: %v", err)
	}
}

// A new task is refused, before its workspace or record is written, while
// the worker's state folder has less than its floor free or cannot be
// read; with room it is made, and a task already made is ensured again
// whatever the disk says. GET /v1/disks reports the reading with its time.
//
// Kills: the state admission removed from the workspace upload or from
// ensure; an unread state folder admitted; a repeated ensure refused.
func TestANewTaskIsRefusedWhileTheStateFolderIsShortOfSpace(t *testing.T) {
	ca := workertest.NewCA(t)
	state := t.TempDir()
	tasks := &executionfake.Tasks{}
	var reading atomic.Value // func() (diskspace.Space, error)
	set := func(s diskspace.Space, err error) { reading.Store(func() (diskspace.Space, error) { return s, err }) }
	srv := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: state, Tasks: tasks,
		StateDisk: func(path string) (diskspace.Space, error) {
			if path != state {
				return diskspace.Space{}, fmt.Errorf("read %s, not the state folder", path)
			}
			return reading.Load().(func() (diskspace.Space, error))()
		}})
	driver := execution.WorkerTasks{URL: srv.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	hub := sourceRepo(t)
	commit, _ := gitexec.Output(t.Context(), hub, "rev-parse", "HEAD")
	withSource := fakeSpec("k-built", "", 0, false)
	withSource.Source = &execution.TaskSource{Repository: hub, Commit: commit}
	const gib = int64(1) << 30

	set(diskspace.Space{TotalBytes: 100 * gib, FreeBytes: 5*gib - 1}, nil)
	for _, spec := range []execution.TaskSpec{fakeSpec("k-plain", "", 0, false), withSource} {
		if _, err := driver.Ensure(context.Background(), spec); !errors.Is(err, diskspace.ErrRefused) || !strings.Contains(err.Error(), "free of 100.0 GiB") {
			t.Errorf("%s on a state folder short of space: %v", spec.Key, err)
		}
	}
	set(diskspace.Space{}, errors.New("input/output error"))
	if _, err := driver.Ensure(context.Background(), fakeSpec("k-plain", "", 0, false)); !errors.Is(err, diskspace.ErrRefused) || !strings.Contains(err.Error(), "input/output error") {
		t.Errorf("a task on an unread state folder: %v", err)
	}
	for _, key := range []string{"k-plain", "k-built"} {
		if tasks.Present(key) {
			t.Errorf("refused %s was created", key)
		}
		for _, p := range []string{filepath.Join(state, "records", key+".json"), filepath.Join(state, "workspaces", key)} {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Errorf("refused %s left %s (%v)", key, p, err)
			}
		}
	}

	set(diskspace.Space{TotalBytes: 100 * gib, FreeBytes: 5 * gib}, nil)
	if _, err := driver.Ensure(context.Background(), withSource); err != nil {
		t.Fatalf("a task with its floor free: %v", err)
	}
	set(diskspace.Space{TotalBytes: 100 * gib, FreeBytes: gib}, nil)
	if _, err := driver.Ensure(context.Background(), withSource); err != nil {
		t.Errorf("a task already made, ensured again: %v", err)
	}
	disks, err := driver.Disks(context.Background())
	if err != nil || len(disks) != 1 || disks[0].Path != state || !disks[0].Refuses || disks[0].FreeBytes != gib || disks[0].FloorBytes != 5*gib || disks[0].ReadAt.IsZero() {
		t.Errorf("the worker's disks: %+v %v", disks, err)
	}
}

// dockerShortOfDisk is a task driver whose Docker data folder reads 1 GiB
// free of 100 GiB.
type dockerShortOfDisk struct{ *executionfake.Tasks }

func (dockerShortOfDisk) DataDisk(context.Context) (string, diskspace.Space, error) {
	return "/var/lib/docker", diskspace.Space{TotalBytes: 100 << 30, FreeBytes: 1 << 30}, nil
}

// The worker reads its tasks' Docker disk, as well as its state folder,
// before it writes anything of a new task: a source-backed task on a short
// Docker disk is refused at its upload, with which disk and what is free,
// leaving no workspace or record.
//
// Kills: the worker's Docker reading removed (DK-C02).
func TestATaskOnAShortDockerDiskIsRefusedBeforeTheWorkerWritesAnything(t *testing.T) {
	ca := workertest.NewCA(t)
	state := t.TempDir()
	tasks := dockerShortOfDisk{&executionfake.Tasks{}}
	srv := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: state, Tasks: tasks,
		StateDisk: func(string) (diskspace.Space, error) {
			return diskspace.Space{TotalBytes: 100 << 30, FreeBytes: 50 << 30}, nil
		}})
	driver := execution.WorkerTasks{URL: srv.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	hub := sourceRepo(t)
	commit, _ := gitexec.Output(t.Context(), hub, "rev-parse", "HEAD")
	spec := fakeSpec("k-docker-full", "", 0, false)
	spec.Source = &execution.TaskSource{Repository: hub, Commit: commit}
	_, err := driver.Ensure(context.Background(), spec)
	if !errors.Is(err, diskspace.ErrRefused) || !strings.Contains(err.Error(), "/var/lib/docker") || !strings.Contains(err.Error(), "1.0 GiB free of 100.0 GiB") {
		t.Fatalf("a task on a short Docker disk: %v", err)
	}
	for _, p := range []string{filepath.Join(state, "records", spec.Key+".json"), filepath.Join(state, "workspaces", spec.Key)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("the refusal left %s (%v)", p, err)
		}
	}
	if tasks.Present(spec.Key) {
		t.Error("the refused task was created")
	}
}

// dockerTurnsUnread reads its Docker disk with room the first time and
// cannot read it after, as in a Docker outage, counting its reads.
type dockerTurnsUnread struct {
	*executionfake.Tasks
	reads atomic.Int32
}

func (d *dockerTurnsUnread) DataDisk(context.Context) (string, diskspace.Space, error) {
	if d.reads.Add(1) > 1 {
		return "/var/lib/docker", diskspace.Space{}, errors.New("Docker is offline")
	}
	return "/var/lib/docker", diskspace.Space{TotalBytes: 100 << 30, FreeBytes: 50 << 30}, nil
}

// A new task is admitted once, before the first thing of it is written: a
// task in its own workspace when that is uploaded, and not again at its
// ensure (a disk that turns short or unreadable after is the accepted
// race); a task with no workspace at its ensure.
//
// Kills: a second admission at the ensure of a task in its own workspace;
// no admission for a task without one.
func TestANewTaskIsAdmittedOnceBeforeItsFirstWrite(t *testing.T) {
	ca := workertest.NewCA(t)
	tasks := &dockerTurnsUnread{Tasks: &executionfake.Tasks{}}
	srv := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Tasks: tasks,
		StateDisk: func(string) (diskspace.Space, error) {
			return diskspace.Space{TotalBytes: 100 << 30, FreeBytes: 50 << 30}, nil
		}})
	driver := execution.WorkerTasks{URL: srv.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	hub := sourceRepo(t)
	commit, _ := gitexec.Output(t.Context(), hub, "rev-parse", "HEAD")
	spec := fakeSpec("k-own-workspace", "", 0, false)
	spec.Source = &execution.TaskSource{Repository: hub, Commit: commit}
	if _, err := driver.Ensure(context.Background(), spec); err != nil {
		t.Fatalf("a task admitted at its upload: %v", err)
	}
	if n := tasks.reads.Load(); n != 1 {
		t.Errorf("the Docker disk was read %d times for one task", n)
	}
	// The disk now reads as unreadable: a task without a workspace is
	// admitted at its ensure, and refused.
	if _, err := driver.Ensure(context.Background(), fakeSpec("k-no-workspace", "", 0, false)); !errors.Is(err, diskspace.ErrRefused) || !strings.Contains(err.Error(), "Docker is offline") {
		t.Errorf("a task without a workspace on an unreadable disk: %v", err)
	}
}

// sourceTemplate is a hub with one empty commit on main.
var sourceTemplate = gittest.NewTemplate(func(t testing.TB, dir string) {
	gittest.Repository(t, dir, "main", gittest.Commit{Message: "base"})
})

func TestMain(m *testing.M) {
	code := m.Run()
	gittest.RemoveTemplates()
	os.Exit(code)
}

// sourceRepo is the test's own copy of sourceTemplate.
func sourceRepo(t *testing.T) string {
	t.Helper()
	return sourceTemplate.Copy(t, "hub")
}

// releaseBarrier pauses a backend's release right after it removed the
// task, until resume is closed.
type releaseBarrier struct {
	*executionfake.Tasks
	removed, resume chan struct{}
}

func (d *releaseBarrier) Release(ctx context.Context, ref execution.TaskRef) error {
	if err := d.Tasks.Release(ctx, ref); err != nil {
		return err
	}
	close(d.removed)
	<-d.resume
	return nil
}

// An ensure repeated while its task is being released waits for the
// release, and is then refused as stale: it never makes the task again
// behind a release that reports success, and a successful release leaves
// nothing under the key.
func TestAnEnsureNeverRecreatesATaskBeingReleased(t *testing.T) {
	ctx := context.Background()
	ca := workertest.NewCA(t)
	backend := &releaseBarrier{Tasks: &executionfake.Tasks{}, removed: make(chan struct{}), resume: make(chan struct{})}
	driver := workertest.Driver(t, ca, workertest.Start(t, ca, t.TempDir(), backend))
	spec := fakeSpec("release-race", "", 0, false)
	first, err := driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() { released <- driver.Release(ctx, execution.TaskRef{Key: spec.Key, ProviderID: first.ID}) }()
	<-backend.removed
	ensured := make(chan error, 1)
	go func() { _, err := driver.Ensure(ctx, spec); ensured <- err }()
	select {
	case err := <-ensured:
		close(backend.resume)
		t.Fatalf("an ensure was answered while the release removed the task: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	close(backend.resume)
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if err := <-ensured; err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("the ensure after the release: %v", err)
	}
	if obs, err := backend.Observe(ctx, execution.TaskRef{Key: spec.Key}); err != nil || obs.State != execution.TaskAbsent {
		t.Errorf("a successful release left a task behind: %+v, %v", obs, err)
	}
}

// A release is decided, durably, before anything is removed: a worker
// stopped between the decision and the removal refuses, once restarted,
// to make or start the task again, and the release asked again completes.
func TestAnInterruptedReleaseIsFinishedAndNeverUndone(t *testing.T) {
	ctx := context.Background()
	ca := workertest.NewCA(t)
	state, backend := t.TempDir(), &executionfake.Tasks{}
	first := workertest.Start(t, ca, state, backend)
	driver := workertest.Driver(t, ca, first)
	spec := fakeSpec("release-interrupted", "", 0, true)
	made, err := driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: spec.Key, ProviderID: made.ID}
	restore := worker.AbandonAt("releasing", spec.Key)
	if err := driver.Release(ctx, ref); err == nil {
		t.Fatal("an abandoned release was confirmed")
	}
	restore()
	first.Close()

	driver = workertest.Driver(t, ca, workertest.Start(t, ca, state, backend))
	if obs, err := driver.Observe(ctx, ref); err != nil || obs.ID != made.ID {
		t.Fatalf("before the release is finished, the task: %+v %v", obs, err)
	}
	if _, err := driver.Ensure(ctx, spec); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("an ensure after the release was decided: %v", err)
	}
	if err := driver.Start(ctx, ref); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("a start after the release was decided: %v", err)
	}
	if backend.Starts(spec.Key) != 0 {
		t.Errorf("the task was started %d times", backend.Starts(spec.Key))
	}
	if err := driver.Release(ctx, ref); err != nil {
		t.Fatalf("the release asked again: %v", err)
	}
	if obs, err := backend.Observe(ctx, execution.TaskRef{Key: spec.Key}); err != nil || obs.State != execution.TaskAbsent {
		t.Errorf("the finished release left %+v, %v", obs, err)
	}
	if _, err := os.Stat(filepath.Join(state, "records", spec.Key+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the finished release left the task's record: %v", err)
	}
}
