package workflowexec

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/tasks"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/internal/worker/workertest"
)

// A script on a worker works from the change as published, and what it made
// comes back as commits published to the change's branch and brought into
// the forge's workspace. The attempt records the commit it was given.
func TestAScriptOnAWorkerPublishesWhatItMade(t *testing.T) {
	t.Parallel()
	// Step completion precedes task settlement. Pause the latter through the
	// existing clock seam so that both facts are tested deterministically.
	atSettlement, release := make(chan struct{}), make(chan struct{})
	var ready, released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	defer unblock() // release before the fixture waits for ExecuteStep, on failure too
	a, fake, step, run, remote := startOnWorker(t, func(a *taskForge, step *workflow.Step) {
		a.tasks.Records.Now = func() time.Time {
			s, err := workflow.ReadStepIn(context.Background(), a.db, step.ID)
			if err != nil {
				t.Errorf("reading the step before task settlement: %v", err)
			}
			if err == nil && s.Status == workflow.StatusPass {
				ready.Do(func() { close(atSettlement) })
				select {
				case <-release:
				case <-time.After(20 * time.Second):
					t.Error("task settlement barrier was not released")
				}
			}
			return time.Now()
		}
	})
	source := a.source(t, run)
	hub, ws := source.Repository(a.dir), source.Workspace(a.dir)
	given := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
	// The worker keeps each workspace's Git directory apart from its files.
	workerGit := filepath.Join(filepath.Dir(filepath.Dir(remote)), "git", filepath.Base(remote)+".git")
	if got := gittest.Run(t, "", "--git-dir", workerGit, "rev-parse", "HEAD"); got != given {
		t.Fatalf("the worker's workspace is at %s, want the published branch %s", got, given)
	}
	if err := os.WriteFile(filepath.Join(remote, "made.txt"), []byte("made on the worker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake.Exit(taskKeyOf(step), 0)
	awaitStep(t, a, step.ID, workflow.StatusPass)
	select {
	case <-atSettlement:
	case <-time.After(20 * time.Second):
		t.Fatal("passed step never reached task settlement")
	}
	tip := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
	if got := gittest.Run(t, hub, "show", tip+":made.txt"); got != "made on the worker" {
		t.Fatalf("the published branch holds %q", got)
	}
	if parent := gittest.Run(t, hub, "rev-parse", tip+"^"); parent != given {
		t.Fatalf("the task's commit builds on %s, want %s", parent, given)
	}
	if got := gittest.Run(t, ws, "rev-parse", "HEAD"); got != tip {
		t.Fatalf("AppTown's workspace is at %s, want the published %s", got, tip)
	}
	records, key := tasks.New(a.db), taskKeyOf(step)
	attempt, err := records.Get(context.Background(), key)
	if err != nil || attempt.Observed != tasks.Collecting {
		t.Fatalf("passed step before task settlement: %+v %v", attempt, err)
	}
	unblock()
	// The passed step does not witness this separately recorded fact.
	if !poll.Until(context.Background(), 20*time.Second, 10*time.Millisecond, func() bool {
		attempt, err = records.Get(context.Background(), key)
		return err != nil || attempt.Observed == tasks.Settled || attempt.Observed == tasks.Held || attempt.Observed == tasks.Refused || attempt.Observed == tasks.Unknown
	}) {
		t.Fatalf("the exact task attempt did not settle: %+v %v", attempt, err)
	}
	if err != nil || attempt.Source != given || !strings.HasPrefix(attempt.Backend, "worker at https://") || attempt.Observed != tasks.Settled {
		t.Fatalf("the attempt: %+v %v", attempt, err)
	}
}

// A result is published only under the authority and on the commit the task
// was given. A branch handed to another holder, or moved by someone else,
// while the task ran refuses it, and the step does not pass.
func TestAStaleWorkerResultIsRefused(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"handed over", "moved"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			a, fake, step, run, remote := startOnWorker(t)
			source := a.source(t, run)
			hub, ws := source.Repository(a.dir), source.Workspace(a.dir)
			given := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
			switch how {
			case "handed over":
				if _, err := database.Transact(context.Background(), a.db, func(tx *database.Tx) (scm.Authority, error) {
					return scm.AdvanceAuthorityIn(context.Background(), tx, run.ProjectID, source.Branch, scm.TicketHolder(idstest.N(1)))
				}); err != nil {
					t.Fatal(err)
				}
			case "moved":
				gittest.Run(t, ws, "commit", "-q", "--allow-empty", "-m", "published while the task ran")
				// The run itself publishes newer work (another step, or a
				// retry): the branch is no longer at the task's commit.
				if _, err := a.branches.Publish(context.Background(), scm.Publication{ProjectID: run.ProjectID, Repository: hub, Branch: source.Branch, From: ws,
					Grant: scm.Grant{Holder: scm.RunHolder(run.ID)}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(remote, "late.txt"), []byte("a stale result\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			before := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
			fake.Exit(taskKeyOf(step), 0)
			settled := awaitStep(t, a, step.ID, workflow.StatusError)
			why := map[string]string{"handed over": "no longer holds the branch", "moved": "moved: expected " + given[:12]}[how]
			if !strings.Contains(string(settled.Evidence), why) {
				t.Fatalf("the step's evidence: %s", settled.Evidence)
			}
			if after := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch); after != before {
				t.Fatalf("a stale result moved the branch from %s to %s", before, after)
			}
		})
	}
}

// An installation with a worker runs no project script on its own host: a
// script that asks to run locally is refused before anything runs.
func TestAnIsolatedForgeRunsNoScriptOnItsHost(t *testing.T) {
	t.Parallel()
	a, fake, step := scriptTaskFixture(t, map[string]any{"run": "touch ran-on-the-forge", "executor": "local"})
	a.onWorker(t, fake)
	run, err := workflow.ReadRunIn(context.Background(), a.db, step.RunID)
	if err != nil {
		t.Fatal(err)
	}
	ws := a.source(t, run).Workspace(a.dir)
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	settled := awaitStep(t, a, step.ID, workflow.StatusError)
	if !strings.Contains(string(settled.Evidence), "only on its worker") {
		t.Fatalf("the step's evidence: %s", settled.Evidence)
	}
	if _, err := os.Stat(filepath.Join(ws, "ran-on-the-forge")); !os.IsNotExist(err) {
		t.Fatalf("the script ran on AppTown's host: %v", err)
	}
	if fake.Starts(taskKeyOf(step)) != 0 {
		t.Fatal("a refused script started a task")
	}
}

// A worker that stops answering while its task runs has not said the task is
// gone: the forge keeps asking, and collects the task once the worker answers
// again, without starting it twice.
func TestAWorkerThatStopsAnsweringIsAskedAgain(t *testing.T) {
	t.Parallel()
	a, fake, step := fakeTaskFixture(t, true)
	var down atomic.Bool
	var refused atomic.Int64 // requests the worker could not answer
	ca := workertest.NewCA(t)
	srv := workertest.StartWith(t, ca, t.TempDir(), fake, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if down.Load() {
				refused.Add(1)
				http.Error(w, "the worker is restarting", http.StatusBadGateway)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	driver := workertest.Driver(t, ca, srv)
	a.tasks.Driver, a.tasks.Remote, a.tasks.Profile.Name, a.tasks.Backend = driver, driver, "worker", "worker at "+driver.URL
	executed := make(chan error, 1)
	go func() { executed <- a.work.ExecuteStep(a.ctx, step.ID) }()
	key := taskKeyOf(step)
	awaitStarted(t, fake, key, "the task never started")
	if starts := fake.Starts(key); starts != 1 {
		t.Fatalf("the task started %d times", starts)
	}
	down.Store(true)
	fake.Exit(key, 0)
	// The forge's decision on an unanswered request is to ask again. Witness
	// it deciding so twice (the second request answers what it made of the
	// first), or the step settling, which would be the wrong decision.
	if !poll.Until(context.Background(), 30*time.Second, 10*time.Millisecond, func() bool {
		current, err := workflow.ReadStepIn(context.Background(), a.db, step.ID)
		return refused.Load() >= 2 || err != nil || current.Status != workflow.StatusRunning
	}) {
		t.Fatal("AppTown neither asked the unanswering worker again nor settled the step")
	}
	if current, err := workflow.ReadStepIn(context.Background(), a.db, step.ID); err != nil || current.Status != workflow.StatusRunning {
		t.Fatalf("while the worker did not answer, the step became %v: %v", current.Status, err)
	}
	down.Store(false)
	awaitStep(t, a, step.ID, workflow.StatusPass)
	if err := <-executed; err != nil {
		t.Fatal(err)
	}
	if starts := fake.Starts(key); starts != 1 || fake.Present(key) {
		t.Fatalf("the task started %d times; still on the worker: %v", starts, fake.Present(key))
	}
	attempt, err := tasks.New(a.db).Get(context.Background(), key)
	if err != nil || attempt.Observed != tasks.Settled || attempt.ExitCode == nil || *attempt.ExitCode != 0 {
		t.Fatalf("the attempt: %+v %v", attempt, err)
	}
}

// Only source enters the change's branch from a worker: the forge checks
// what the result changes where it accepts it, not the worker. A result that
// changes .apptown beyond the native contract (a runtime file the project's
// rules re-include) is refused, the step does not pass and the branch does not
// move; the contract alone, and ordinary files, are published.
func TestAWorkerResultChangingNonSourceIsRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		files   map[string]string
		refused bool
	}{
		// The task's .gitignore re-includes its runtime file, over the
		// worker's own exclude, so the worker returns it.
		{"a runtime file", map[string]string{"made.txt": "made\n", ".gitignore": "!.apptown/runtime.json\n", ".apptown/runtime.json": "{}\n"}, true},
		{"the native contract", map[string]string{"made.txt": "made\n", ".apptown/app.yaml": "name: app\n"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fake, step, run, remote := startOnWorker(t)
			source := a.source(t, run)
			hub := source.Repository(a.dir)
			given := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
			for name, body := range tc.files {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(remote, name)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(remote, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			fake.Exit(taskKeyOf(step), 0)
			if !tc.refused {
				awaitStep(t, a, step.ID, workflow.StatusPass)
				if got := gittest.Run(t, hub, "show", scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)+":.apptown/app.yaml"); got != "name: app" {
					t.Fatalf("the published contract: %q", got)
				}
				return
			}
			got := awaitStep(t, a, step.ID, workflow.StatusError)
			if !strings.Contains(string(got.Evidence), ".apptown/runtime.json") {
				t.Fatalf("the refusal does not name the file: %s", got.Evidence)
			}
			if tip := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch); tip != given {
				t.Fatalf("the branch moved to %s", tip)
			}
		})
	}
}

// A forge without a worker still runs no project script as a process on its
// own host unless its operator allows it (-host-scripts): a script that asks
// for executor: local is refused before anything runs, naming the way out.
//
// Kills: the old default, which ran an executor-less or local script on the
// forge's host whenever no worker was configured.
func TestAForgeRunsNoScriptOnItsHostUnlessAllowed(t *testing.T) {
	t.Parallel()
	a, fake, step := scriptTaskFixture(t, map[string]any{"run": "touch ran-on-the-forge", "executor": "local"})
	run, err := workflow.ReadRunIn(context.Background(), a.db, step.RunID)
	if err != nil {
		t.Fatal(err)
	}
	ws := a.source(t, run).Workspace(a.dir)
	if err := a.work.ExecuteStep(a.ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	settled := awaitStep(t, a, step.ID, workflow.StatusError)
	if !strings.Contains(string(settled.Evidence), "-host-scripts") {
		t.Fatalf("the step's evidence: %s", settled.Evidence)
	}
	if _, err := os.Stat(filepath.Join(ws, "ran-on-the-forge")); !os.IsNotExist(err) {
		t.Fatalf("the script ran on AppTown's host: %v", err)
	}
	if fake.Starts(taskKeyOf(step)) != 0 {
		t.Fatal("a refused script started a task")
	}
}
