package workflowexec

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execfile"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// A worker task's result is collected once, as a candidate in the project's
// repository, and published from there by compare-and-swap; a refused one is
// kept under its refused ref (design/operations/workers.md, "How work
// travels"). That it can then be downloaded through the run's
// route is the composition root's test (TestARefusedResultIsKeptForItsDownload).

// inRepo runs git in dir, which must be named: an empty dir would run git
// wherever the test happens to be.
func inRepo(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if dir == "" {
		t.Fatal("inRepo needs a directory")
	}
	return gittest.Run(t, dir, args...)
}

// branchOrDefault is the run's published branch, or the default branch's
// tip before the branch begins: the commit a task is given.
func branchOrDefault(t *testing.T, hub string, source workflow.Source) string {
	t.Helper()
	if tip := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch); tip != "" {
		return tip
	}
	return scm.Ref(t.Context(), hub, "refs/heads/"+source.DefaultBranch)
}

// writeRemote writes files into a task's workspace on the worker.
func writeRemote(t *testing.T, remote string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(remote, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// runEvents are the run's events of kind, through the run's detail.
func runEvents(t *testing.T, a *taskAppTown, run *workflow.Run, kind string) []map[string]any {
	t.Helper()
	snap, err := a.work.Snapshot(a.ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, e := range snap.Events {
		if e.Kind == kind {
			var payload map[string]any
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			out = append(out, payload)
		}
	}
	return out
}

// awaitReleased waits until the attempt's task is released on the worker.
func awaitReleased(t *testing.T, a *taskAppTown, fake *executionfake.Tasks, key string) {
	t.Helper()
	var attempt tasks.Attempt
	var err error
	if !poll.Until(context.Background(), 20*time.Second, 10*time.Millisecond, func() bool {
		attempt, err = tasks.New(a.db).Get(context.Background(), key)
		return err == nil && attempt.Released && !fake.Present(key)
	}) {
		t.Fatalf("the task was not released: %+v %v; present %v", attempt, err, fake.Present(key))
	}
}

// handOver gives the run's branch to another holder, as a desktop push
// ticket taking the change would.
func handOver(t *testing.T, a *taskAppTown, run *workflow.Run, branch string) {
	t.Helper()
	if _, err := database.Transact(context.Background(), a.db, func(tx *database.Tx) (scm.Authority, error) {
		return scm.AdvanceAuthorityIn(context.Background(), tx, run.ProjectID, branch, scm.TicketHolder(idstest.N(1)))
	}); err != nil {
		t.Fatal(err)
	}
}

// resultWorker serves the worker's result through alter, which gets the
// real reply and the call's count.
func resultWorker(t *testing.T, a *taskAppTown, fake *executionfake.Tasks, alter func(n int, w http.ResponseWriter, reply *httptest.ResponseRecorder)) string {
	t.Helper()
	ca, state := workertest.NewCA(t), t.TempDir()
	var calls atomic.Int32
	srv := workertest.StartWith(t, ca, state, fake, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/result") {
				next.ServeHTTP(w, r)
				return
			}
			reply := httptest.NewRecorder()
			next.ServeHTTP(reply, r)
			alter(int(calls.Add(1)), w, reply)
		})
	})
	driver := workertest.Driver(t, ca, srv)
	a.tasks.Driver, a.tasks.Remote, a.tasks.Profile.Name, a.tasks.Backend = driver, driver, "worker", "worker at "+driver.URL
	return state
}

// pass writes the reply as it came.
func pass(w http.ResponseWriter, reply *httptest.ResponseRecorder) {
	for name, values := range reply.Header() {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(reply.Code)
	_, _ = w.Write(reply.Body.Bytes())
}

// runOnResultWorker runs the fixture's script to its exit on a worker whose
// result replies alter changes, with files made in its workspace.
func runOnResultWorker(t *testing.T, files map[string]string, alter func(n int, w http.ResponseWriter, reply *httptest.ResponseRecorder)) (*taskAppTown, *executionfake.Tasks, *workflow.Step, *workflow.Run) {
	t.Helper()
	return runOnResultWorkerWith(t, files, alter, nil)
}

// runOnResultWorkerWith is runOnResultWorker with arm called on AppTown
// before the step runs (a fault, a stage to stop at), when it is given.
func runOnResultWorkerWith(t *testing.T, files map[string]string, alter func(n int, w http.ResponseWriter, reply *httptest.ResponseRecorder), arm func(*taskAppTown)) (*taskAppTown, *executionfake.Tasks, *workflow.Step, *workflow.Run) {
	t.Helper()
	a, fake, step := fakeTaskFixture(t, true)
	state := resultWorker(t, a, fake, alter)
	if arm != nil {
		arm(a)
	}
	run, err := workflow.ReadRunIn(context.Background(), a.db, step.RunID)
	if err != nil {
		t.Fatal(err)
	}
	executed := make(chan error, 1)
	go func() { executed <- a.work.ExecuteStep(a.ctx, step.ID) }()
	key := taskKeyOf(step)
	awaitStarted(t, fake, key, "the task never started on the worker")
	writeRemote(t, filepath.Join(state, "workspaces", key), files)
	fake.Exit(key, 0)
	if err := <-executed; err != nil {
		t.Fatal(err)
	}
	return a, fake, step, run
}

// A result that fails the import's checks (a malformed bundle, or a bundle
// holding another commit than the worker named) is kept nowhere: no
// candidate and no refused ref, the branch unchanged. AppTown has no copy
// of the work, so the attempt is an error and its workspace on the worker
// is held, also by a restarted AppTown.
func TestAMalformedResultIsKeptNowhere(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"malformed bundle", "another tip"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			a, fake, step, run := runOnResultWorker(t, map[string]string{"made.txt": "made\n"}, func(_ int, w http.ResponseWriter, reply *httptest.ResponseRecorder) {
				switch how {
				case "malformed bundle":
					reply.Body.Reset()
					reply.Body.WriteString("# v2 git bundle\nnot a bundle at all\n")
				case "another tip":
					reply.Header().Set("AppTown-Head", strings.Repeat("ab", 20))
				}
				pass(w, reply)
			})
			source := a.source(t, run)
			hub := source.Repository(a.dir)
			given := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
			settled := awaitStep(t, a, step.ID, workflow.StatusError)
			if !strings.Contains(string(settled.Evidence), "result not kept") {
				t.Fatalf("the step's evidence: %s", settled.Evidence)
			}
			for _, ref := range []string{repositoryedit.CandidateRef(step.ID, 1), repositoryedit.RefusedStepRef(step.ID, 1)} {
				if kept := scm.Ref(t.Context(), hub, ref); kept != "" {
					t.Fatalf("a malformed result is kept under %s: %s", ref, kept)
				}
			}
			if tip := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch); tip != given {
				t.Fatalf("the branch moved to %s", tip)
			}
			key := taskKeyOf(step)
			held := func(who string) {
				t.Helper()
				attempt, err := tasks.New(a.db).Get(context.Background(), key)
				if err != nil || attempt.Observed != tasks.Held || attempt.Released || !fake.Present(key) {
					t.Fatalf("%s: the attempt's workspace is not held: %+v %v; present %v", who, attempt, err, fake.Present(key))
				}
			}
			held("after the step")
			b := restarted(t, a)
			if _, err := b.tasks.ReleaseSettled(context.Background()); err != nil {
				t.Fatal(err)
			}
			held("after a restart")
		})
	}
}

// A refusal whose work cannot be kept (its refused ref is locked) is not
// settled: the attempt is an error and its workspace on the worker is held.
func TestARefusedResultThatCannotBeKeptHoldsTheWorkspace(t *testing.T) {
	t.Parallel()
	a, fake, step, run, remote := startOnWorker(t)
	source := a.source(t, run)
	hub := source.Repository(a.dir)
	handOver(t, a, run, source.Branch)
	lock := filepath.Join(hub, ".git", repositoryedit.RefusedStepRef(step.ID, 1)+".lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, []byte("busy"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRemote(t, remote, map[string]string{"made.txt": "work to keep\n"})
	fake.Exit(taskKeyOf(step), 0)
	settled := awaitStep(t, a, step.ID, workflow.StatusError)
	if !strings.Contains(string(settled.Evidence), "not kept") {
		t.Fatalf("the step's evidence: %s", settled.Evidence)
	}
	// What could release the workspace: the step's own execution, which
	// has settled it, and the release pass. Witness the execution's end,
	// then make a pass; the workspace is held through both.
	select {
	case <-a.executed:
	case <-time.After(30 * time.Second):
		t.Fatal("the step's execution never returned")
	}
	if _, err := a.tasks.ReleaseSettled(context.Background()); err != nil {
		t.Fatal(err)
	}
	attempt, err := tasks.New(a.db).Get(context.Background(), taskKeyOf(step))
	if err != nil || attempt.Observed != tasks.Held || attempt.Released || !fake.Present(taskKeyOf(step)) {
		t.Fatalf("the attempt's workspace is not held: %+v %v", attempt, err)
	}
}

// An attempt that changed nothing still has a candidate: the commit it was
// given. Nothing is published and the step passes.
func TestANoChangeAttemptsCandidateIsItsSource(t *testing.T) {
	t.Parallel()
	a, fake, step, run, _ := startOnWorker(t)
	source := a.source(t, run)
	hub := source.Repository(a.dir)
	given := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
	fake.Exit(taskKeyOf(step), 0)
	settled := awaitStep(t, a, step.ID, workflow.StatusPass)
	if c := scm.Ref(t.Context(), hub, repositoryedit.CandidateRef(step.ID, 1)); c != given {
		t.Fatalf("the candidate is %q, want the source %s", c, given)
	}
	if tip := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch); tip != given {
		t.Fatalf("the branch moved to %s", tip)
	}
	var evidence struct {
		Result struct {
			Commit string `json:"commit"`
		} `json:"result"`
	}
	if err := json.Unmarshal(settled.Evidence, &evidence); err != nil || evidence.Result.Commit != given {
		t.Fatalf("the step's evidence: %s %v", settled.Evidence, err)
	}
	if n := len(runEvents(t, a, run, "commit.created")); n != 0 {
		t.Fatalf("%d commits published for no change", n)
	}
}

// A collection whose reply is lost after the worker committed is asked for
// again; the result is collected, kept and published once.
func TestALostCollectionReplyIsAskedAgainAndPublishedOnce(t *testing.T) {
	t.Parallel()
	a, _, step, run := runOnResultWorker(t, map[string]string{"made.txt": "made once\n"}, func(n int, w http.ResponseWriter, reply *httptest.ResponseRecorder) {
		if n == 1 {
			http.Error(w, "the reply was lost", http.StatusBadGateway)
			return
		}
		pass(w, reply)
	})
	source := a.source(t, run)
	hub := source.Repository(a.dir)
	awaitStep(t, a, step.ID, workflow.StatusPass)
	tip := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
	if body := inRepo(t, hub, "show", tip+":made.txt"); body != "made once" {
		t.Fatalf("the published branch holds %q", body)
	}
	if c := scm.Ref(t.Context(), hub, repositoryedit.CandidateRef(step.ID, 1)); c != tip {
		t.Fatalf("the candidate %s is not the published %s", c, tip)
	}
	if created := runEvents(t, a, run, "commit.created"); len(created) != 1 || created[0]["sha"] != tip {
		t.Fatalf("published: %+v", created)
	}
	if n := inRepo(t, hub, "rev-list", "--count", tip, "^"+tip+"^"); n != "1" {
		t.Fatalf("the result is %s commits", n)
	}
}

// An AppTown that stops after publishing a result and before settling its step
// (its publication's answer lost) publishes it once: the restarted AppTown
// finds the candidate, finds the branch already at it, and settles.
func TestARestartAfterPublicationPublishesOnce(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	state := a.onWorker(t, fake)
	run, err := workflow.ReadRunIn(context.Background(), a.db, step.RunID)
	if err != nil {
		t.Fatal(err)
	}
	source := a.source(t, run)
	hub := source.Repository(a.dir)
	given := branchOrDefault(t, hub, source)
	a.tasks.AbandonAt = func(s string) bool { return s == "published" }
	executed := make(chan error, 1)
	go func() { executed <- a.work.ExecuteStep(a.ctx, step.ID) }()
	key := taskKeyOf(step)
	awaitStarted(t, fake, key, "the task never started on the worker")
	writeRemote(t, filepath.Join(state, "workspaces", key), map[string]string{"made.txt": "made once\n"})
	fake.Exit(key, 0)
	if err := <-executed; err != nil {
		t.Fatal(err)
	}
	a.tasks.AbandonAt = nil
	published := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
	if published == given {
		t.Fatal("the result was not published before the stop")
	}
	if current, err := workflow.ReadStepIn(context.Background(), a.db, step.ID); err != nil || current.Status != workflow.StatusRunning {
		t.Fatalf("the step settled before the stop: %v %v", current.Status, err)
	}
	b := restarted(t, a)
	awaitStep(t, b, step.ID, workflow.StatusPass)
	if tip := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch); tip != published {
		t.Fatalf("the branch moved again, to %s", tip)
	}
	if created := runEvents(t, b, run, "commit.created"); len(created) != 1 {
		t.Fatalf("published %d times: %+v", len(created), created)
	}
	if n := inRepo(t, hub, "rev-list", "--count", published, "^"+given); n != "1" {
		t.Fatalf("%s commits on the source", n)
	}
	awaitReleased(t, b, fake, key)
}

// A landing only reads: a script in a landing run on a worker has its result
// kept as its candidate, and nothing is published.
func TestALandingRunPublishesNothing(t *testing.T) {
	t.Parallel()
	env := map[string]any{"FAKE_OUTPUT": "ran", "FAKE_EXIT": "0", "FAKE_BLOCK": "1"}
	spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{
		{Name: "check", Uses: "script", With: map[string]any{"run": "true", "executor": "docker", "image": "fake", "env": env}},
	}}}}
	a, fake := newTaskAppTown(t, "land", spec)
	state := a.onWorker(t, fake)
	ch := a.beginChange(t)
	// A landing starts from the change's published branch. AppTown
	// publishes the change's committed work when it admits a landing; this
	// change has none beyond main, so its branch is main's tip.
	gittest.Run(t, a.hub, "branch", ch.Branch, "main")
	// The fixture's administrator may land: it starts the landing.
	run := a.admit(t, ch, "land", a.ctx)
	snap, err := a.work.Snapshot(a.ctx, run.ID, workflow.DetailView)
	if err != nil || len(snap.Steps) != 1 {
		t.Fatalf("the landing's steps: %+v %v", snap, err)
	}
	step := snap.Steps[0]
	source := a.source(t, run)
	if source.RootRole != "landing" {
		t.Fatalf("the run's role is %q", source.RootRole)
	}
	hub := source.Repository(a.dir)
	before := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
	executed := make(chan error, 1)
	go func() { executed <- a.work.ExecuteStep(a.ctx, step.ID) }()
	key := taskKeyOf(step)
	awaitStarted(t, fake, key, "the task never started on the worker")
	writeRemote(t, filepath.Join(state, "workspaces", key), map[string]string{"made.txt": "made in a landing\n"})
	fake.Exit(key, 0)
	if err := <-executed; err != nil {
		t.Fatal(err)
	}
	awaitStep(t, a, step.ID, workflow.StatusPass)
	if after := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch); after != before {
		t.Fatalf("a landing published: the branch moved from %s to %s", before, after)
	}
	candidate := scm.Ref(t.Context(), hub, repositoryedit.CandidateRef(step.ID, 1))
	if candidate == "" || inRepo(t, hub, "show", candidate+":made.txt") != "made in a landing" {
		t.Fatalf("the landing's result is not kept as its candidate: %q", candidate)
	}
	if n := len(runEvents(t, a, run, "commit.created")); n != 0 {
		t.Fatalf("a landing published %d commits", n)
	}
}

// A folder ahead of its branch whose Git metadata AppTown refuses is never
// fetched from: the task is given the published tip, the folder's commit
// never reaches the project's repository, and nothing the folder's config
// names runs.
func TestAHostileFolderAheadIsNeverFetchedFrom(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	a.onWorker(t, fake)
	run, err := workflow.ReadRunIn(context.Background(), a.db, step.RunID)
	if err != nil {
		t.Fatal(err)
	}
	source := a.source(t, run)
	hub, ws := source.Repository(a.dir), source.Workspace(a.dir)
	given, before := branchOrDefault(t, hub, source), scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
	inRepo(t, ws, "commit", "-q", "--allow-empty", "-m", "the folder's own commit")
	ahead := inRepo(t, ws, "rev-parse", "HEAD")
	sentinel := filepath.Join(t.TempDir(), "fired")
	hook := filepath.Join(t.TempDir(), "hook")
	if err := execfile.Write(hook, []byte("#!/bin/sh\ntouch "+sentinel+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	inRepo(t, ws, "config", "core.fsmonitor", hook)
	inRepo(t, ws, "config", "uploadpack.packObjectsHook", hook)
	if err := gitexec.CheckFolder(t.Context(), ws); !errors.Is(err, gitexec.ErrFolderRefused) {
		t.Fatalf("the fixture's folder is not refused: %v", err)
	}
	executed := make(chan error, 1)
	go func() { executed <- a.work.ExecuteStep(a.ctx, step.ID) }()
	key := taskKeyOf(step)
	t.Cleanup(func() {
		fake.Exit(key, 137)
		if err := <-executed; err != nil {
			t.Error(err)
		}
	})
	awaitStarted(t, fake, key, "the task never started on the worker")
	attempt, err := tasks.New(a.db).Get(context.Background(), key)
	if err != nil || attempt.Source != given {
		t.Fatalf("the task was given %q, want the published %s: %v", attempt.Source, given, err)
	}
	if _, err := gitexec.HubRun(context.Background(), hub, "cat-file", "-e", ahead+"^{commit}"); err == nil {
		t.Fatal("the refused folder's commit was fetched into the project's repository")
	}
	if tip := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch); tip != before {
		t.Fatalf("the branch moved from %q to %q", before, tip)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("the folder's configured command ran: %v", err)
	}
}
